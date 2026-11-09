package repl

import (
	"context"
	"fmt"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/usersql"
)

// fetchCap is how many rows a statement keeps; the true total is still
// counted. It does not depend on .maxrows (which is only what is drawn).
const fetchCap = 10000

// plainTypes are statements named by their first word whose type DuckDB's
// parser need not be asked: none of them can carry a setting or a credential
// into history, and preparing a SELECT would bind its remote tables a second
// time. Everything else is prepared, so its type is DuckDB's own.
var plainTypes = map[string]duckdb.StmtType{
	"SELECT": duckdb.STATEMENT_TYPE_SELECT, "WITH": duckdb.STATEMENT_TYPE_SELECT,
	"FROM": duckdb.STATEMENT_TYPE_SELECT, "VALUES": duckdb.STATEMENT_TYPE_SELECT,
	"TABLE": duckdb.STATEMENT_TYPE_SELECT, "INSERT": duckdb.STATEMENT_TYPE_INSERT,
	"UPDATE": duckdb.STATEMENT_TYPE_UPDATE, "DELETE": duckdb.STATEMENT_TYPE_DELETE,
}

// noResultTypes are statements whose only output is a "Count" column the
// DuckDB CLI does not print.
var noResultTypes = map[duckdb.StmtType]bool{
	duckdb.STATEMENT_TYPE_CREATE: true, duckdb.STATEMENT_TYPE_CREATE_FUNC: true,
	duckdb.STATEMENT_TYPE_DROP: true, duckdb.STATEMENT_TYPE_ALTER: true,
	duckdb.STATEMENT_TYPE_INSERT: true, duckdb.STATEMENT_TYPE_UPDATE: true,
	duckdb.STATEMENT_TYPE_DELETE: true, duckdb.STATEMENT_TYPE_TRANSACTION: true,
	duckdb.STATEMENT_TYPE_VACUUM: true, duckdb.STATEMENT_TYPE_SET: true,
	duckdb.STATEMENT_TYPE_LOAD: true, duckdb.STATEMENT_TYPE_ATTACH: true,
	duckdb.STATEMENT_TYPE_DETACH: true,
}

// classify is the statement's type: from its first word for the plain ones,
// else DuckDB's own parse (prepared, never run). err says DuckDB could not
// prepare it, and then the type means nothing.
func (s *Session) classify(text string) (duckdb.StmtType, error) {
	if stmts := usersql.Tokens(text); len(stmts) > 0 && len(stmts[0]) > 0 && stmts[0][0].Kind == usersql.TokWord {
		if typ, ok := plainTypes[stmts[0][0].Text]; ok {
			return typ, nil
		}
	}
	return usersql.ParsedType(s.conn.SQLConn(), text)
}

// beginRun makes the statement cancellable by Ctrl+C from its first step.
func (s *Session) beginRun() (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(s.ctx)
	s.interrupted.Store(false)
	s.cancel.Store(&cancel)
	s.running.Store(true)
	return runCtx, cancel
}

// endRun ends the statement's cancellable span. A Ctrl+C that cancelled it
// also discards what was typed ahead: the half-typed statement here, the
// queued input in Input.
func (s *Session) endRun(cancel context.CancelFunc) {
	s.running.Store(false)
	s.cancel.Store(nil)
	cancel()
	if s.dropRest.Swap(false) {
		s.buf = ""
		s.aborted = true
	}
}

// runStatement runs one statement (as typed, no ';') through the editor's
// gates, the engine's one run path and the AfterRun hook, then draws it. The
// whole of it is cancelled by Ctrl+C.
func (s *Session) runStatement(text string) {
	runCtx, cancel := s.beginRun()
	defer s.endRun(cancel)

	runText, params, err := s.expand(runCtx, text)
	if err != nil {
		s.errorLine("Error:", err.Error()) // refused: no history, no hook
		return
	}
	if err := usersql.CheckInteractive(runCtx, s.o.Sess, runText); err != nil {
		s.errorLine("Error:", err.Error()) // refused: no history, no hook
		return
	}
	typ, prepErr := s.classify(runText)
	before := session.StatusSnapshot(s.o.Sess)
	start := s.clock()

	rs, total, err := s.o.Engine.ExecuteOn(runCtx, s.conn, s.current, runText, params, fetchCap)
	if s.o.Hooks.AfterRun != nil {
		s.o.Hooks.AfterRun(before, text) // always after a run, as RunQuery does
	}
	s.show(runCtx, runText, rs, total, err, typ, prepErr)
	elapsed := s.clock().Sub(start)

	if s.st.timer {
		s.printf("%s\n", s.pal().dim(fmt.Sprintf("Run Time (s): real %.3f", elapsed.Seconds())))
	}
	if typ == duckdb.STATEMENT_TYPE_SET || prepErr != nil {
		s.refreshCurrent()
	}
	entry := foldEntry(text)
	if eligible(text, typ, prepErr) && engine.Recordable(entry) {
		s.hist.record(text)
		if err == nil {
			s.last = text // what .save offers: never an ATTACH, a SET or a credential
		}
	}
}

// expand is the statement as it will run: through Options.Expand when the
// embedding surface set one (metric() calls), otherwise as typed.
func (s *Session) expand(ctx context.Context, text string) (string, map[string]any, error) {
	if s.o.Expand == nil {
		return text, nil, nil
	}
	return s.o.Expand(ctx, text, s.current)
}

// show prints a statement's outcome: ^C, the error, or the result (nothing for
// a statement whose only output is a row count).
func (s *Session) show(ctx context.Context, text string, rs *engine.ResultSet, total int, err error, typ duckdb.StmtType, prepErr error) {
	switch {
	case err != nil && (s.interrupted.Load() || ctx.Err() != nil):
		s.printf("^C\n")
	case err != nil:
		s.errorLine("Error:", err.Error())
	case prepErr == nil && noResultTypes[typ] && countOnly(rs) && !hasReturning(text):
	default:
		if rerr := render(ctx, s.out, rs, total, s.renderOpts()); rerr != nil {
			s.printf("^C\n")
		}
	}
}

// countOnly reports whether rs is the bare result DuckDB gives a statement
// that returns no data: a row count, or the empty "Success" column of DDL.
func countOnly(rs *engine.ResultSet) bool {
	if rs == nil || len(rs.Columns) != 1 {
		return false
	}
	name := rs.Columns[0].Name
	return strings.EqualFold(name, "Count") || strings.EqualFold(name, "Success") && len(rs.Rows) == 0
}

// refreshCurrent re-reads which catalog the pinned connection is on, after a
// statement that may have moved it (USE parses as SET), and gives the
// connection that catalog's timeout and row cap as .conn does. It takes no
// gate, no hook and no history: it is the console's own bookkeeping.
func (s *Session) refreshCurrent() {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	rows, err := s.conn.Query(ctx, "SELECT current_database()")
	if err != nil {
		return
	}
	defer rows.Close() //nolint:errcheck
	var cat string
	if rows.Next() && rows.Scan(&cat) == nil && cat != "" && cat != s.current {
		s.setCurrent(cat)
		s.conn.Follow(cat)
	}
}

// hasReturning reports a RETURNING clause, whose columns are data.
func hasReturning(text string) bool {
	for _, st := range usersql.Tokens(text) {
		for _, t := range st {
			if t.Kind == usersql.TokWord && t.Text == "RETURNING" {
				return true
			}
		}
	}
	return false
}
