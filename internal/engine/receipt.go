package engine

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/storageglob"
	"github.com/SandwichLabs/slaw/internal/usersql"
)

// This file is where a run's receipt facts are collected (docs/plans/run-receipts.md).
// A caller that wants a receipt puts a receipts.Intent on the context of its final
// run; the engine fills in what only it knows (the text it executed, the catalogs
// that text resolves to, the columns, the timing and the statement type) and the
// caller finishes the receipt with receipts.Finish after trimming its rows. The
// engine never makes or keeps a receipt itself, and a run whose context has no
// intent costs one context lookup.
//
// The glue between the leaf receipts package and the text rules lives here
// because receipts cannot import engine: OpenRecorder hands the leaf the one
// Recordable and RecordableStatement.

// OpenRecorder opens the receipts of the workspace at root: its key and its
// file store under .slaw-local/receipts/, under the default Policy (the
// per-person settings come with the Settings slice). It never fails a run: a
// store that cannot be opened is logged at warn level and the result is nil, and
// a nil *receipts.Recorder records nothing.
func OpenRecorder(root string, logger *slog.Logger) *receipts.Recorder {
	if root == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	policy := receipts.DefaultPolicy()
	key, err := receipts.LoadKey(root)
	if err != nil {
		logger.Warn("run receipts are off: the receipt key cannot be used", "error", err)
		return nil
	}
	store, err := receipts.NewFileStore(root, policy)
	if err != nil {
		logger.Warn("run receipts are off: the receipts folder cannot be used", "error", err)
		return nil
	}
	return receipts.NewRecorder(store, key, policy, receipts.Checks{Statement: RecordableStatement, Value: Recordable}, logger)
}

// recordExecuted notes the text DuckDB ran and its columns on the run. query is
// the statement as the caller handed it to the engine (after the caller's own
// metric() expansion and cap wrapper, $name placeholders intact), expanded the
// bound text after wildcard expansion. A wildcard this engine expanded itself is
// only in the bound text, so that is what ran; one the agent policy expanded
// before it vetted the statement is already in query.
func recordExecuted(ctx context.Context, query, expanded string, rw *storageglob.Rewrite, rs *ResultSet) {
	run := receipts.RunFrom(ctx)
	if run == nil {
		return
	}
	executed := query
	if _, vetted := storageglob.FromContext(ctx); !vetted && rw.Changed() {
		executed = expanded
	}
	summary, _ := rw.Summarize(executed)
	cols := make([]receipts.Column, len(rs.Columns))
	for i, c := range rs.Columns {
		cols[i] = receipts.Column{Name: c.Name, Type: c.Type}
	}
	run.Record(func(f *receipts.Facts) {
		f.Executed, f.Summary, f.Columns = executed, summary, cols
	})
}

// recordRunFacts completes the facts after a successful run: when it started,
// how long it took, which statement kind it is (the caller's word for it, or a
// parse of the text) and which connections its names resolve to.
func (m *Manager) recordRunFacts(ctx context.Context, connection string, start time.Time) {
	run := receipts.RunFrom(ctx)
	if run == nil {
		return
	}
	dur := time.Since(start)
	executed := run.Facts().Executed
	if executed == "" {
		return
	}
	kind := run.Kind()
	if kind == "" {
		kind = m.statementKind(ctx, executed)
	}
	// The snapshot facts are asked only for a statement that will be a receipt,
	// and never fail or hold the run past their own budget (session.SnapshotConnections).
	var conns []receipts.Connection
	if kind == receipts.StmtSelect || kind == receipts.StmtExplain {
		conns = receiptConnections(session.SnapshotConnections(ctx, m.sess, connection, executed))
	}
	fromData := columnNamesFromData(executed)
	run.Record(func(f *receipts.Facts) {
		f.At, f.Duration, f.Stmt, f.Connections, f.NamesFromData = start, dur, kind, conns, fromData
	})
}

// ReadKind is the kind of a statement a caller knows is a single read (the agent
// policy's verdict, or a console's own parse): an explain when it says EXPLAIN,
// otherwise a select. A caller sets it as Intent.Kind to spare the engine a parse.
func ReadKind(text string) receipts.StmtKind {
	if stmts := usersql.Tokens(text); len(stmts) == 1 && len(stmts[0]) > 0 && stmts[0][0].Kind == usersql.TokWord {
		switch stmts[0][0].Text {
		case "EXPLAIN":
			return explainKind(stmts[0])
		case "PRAGMA": // a read-style PRAGMA is a read, but never a receipt
			return receipts.StmtOther
		}
	}
	return receipts.StmtSelect
}

// receiptConnections is what a receipt keeps of a connection: its name, type and
// read-only flag, a status that is an enum, never the status message (which can
// name a host), and its snapshot fact (nil for a connection that was not
// attached, so not asked).
func receiptConnections(snaps []session.ConnectionSnapshot) []receipts.Connection {
	out := make([]receipts.Connection, 0, len(snaps))
	for _, sc := range snaps {
		c := sc.Info
		st := receipts.StatusError
		switch c.Kind {
		case session.StatusOK:
			st = receipts.StatusConnected
		case session.StatusNotConnected:
			st = receipts.StatusNotConnected
		}
		rc := receipts.Connection{Name: c.Name, Type: c.Type, ReadOnly: c.ReadOnly, Status: st}
		if sn := sc.Snapshot; sn != nil {
			rc.Snapshot = &receipts.Snapshot{Fact: sn.Fact, Value: sn.Value, Label: receipts.SnapshotLabel(sn.Label)}
		}
		out = append(out, rc)
	}
	return out
}

// statementKind says whether text is a single select or explain, which are the
// only statements a receipt is made for, by parsing alone: nothing is prepared
// or bound, so no remote table is bound a second time. A script is other,
// whatever its first statement is. SELECT, FROM, VALUES, TABLE and EXPLAIN are
// read from the first word; anything else (WITH, a parenthesised query) must be
// a single SELECT by DuckDB's own parser (json_serialize_sql), so a WITH that
// leads a DELETE is other. EXPLAIN ANALYZE runs what it explains, so it is other.
func (m *Manager) statementKind(ctx context.Context, text string) receipts.StmtKind {
	stmts := usersql.Tokens(text)
	if len(stmts) != 1 || len(stmts[0]) == 0 {
		return receipts.StmtOther
	}
	first := stmts[0][0]
	if first.Kind == usersql.TokWord {
		switch first.Text {
		case "SELECT", "FROM", "VALUES", "TABLE":
			return receipts.StmtSelect
		case "EXPLAIN":
			return explainKind(stmts[0])
		}
	}
	if m.sess == nil || m.sess.DB() == nil || usersql.RequireSelect(ctx, m.sess.DB(), text) != nil {
		return receipts.StmtOther
	}
	return receipts.StmtSelect
}

// explainKind is an explain unless it analyzes, which runs what it explains.
func explainKind(toks []usersql.Token) receipts.StmtKind {
	for _, t := range toks[:min(len(toks), 6)] {
		if t.Kind == usersql.TokWord && (t.Text == "ANALYZE" || t.Text == "ANALYSE") {
			return receipts.StmtOther
		}
	}
	return receipts.StmtExplain
}

// columnNamesFromData reports a statement whose result columns are named by data,
// not by the statement: PIVOT, PIVOT_WIDER and UNPIVOT (names from row values),
// a file reader (a header row names the columns: read_csv, read_json, ...,
// *_scan) and a table read by file name (FROM 'x.csv'). Keywords are found by
// the lexer, never by substring. Such names are never kept in a receipt.
func columnNamesFromData(text string) bool {
	for _, stmt := range usersql.Tokens(text) {
		for i, t := range stmt {
			if t.Kind != usersql.TokWord {
				continue
			}
			switch {
			case t.Text == "PIVOT" || t.Text == "PIVOT_WIDER" || t.Text == "UNPIVOT":
				return true
			case strings.HasPrefix(t.Text, "READ_") || strings.HasSuffix(t.Text, "_SCAN") || t.Text == "SNIFF_CSV":
				return true
			case (t.Text == "FROM" || t.Text == "JOIN") && fileInFromClause(stmt[i+1:]):
				return true
			}
		}
	}
	return false
}

// fromClauseEnds are the words that end a FROM clause's table list (and ON and
// USING, whose strings are conditions and column names, and JOIN, which starts
// its own scan).
var fromClauseEnds = map[string]bool{
	"WHERE": true, "GROUP": true, "ORDER": true, "HAVING": true, "LIMIT": true, "OFFSET": true, "QUALIFY": true,
	"UNION": true, "INTERSECT": true, "EXCEPT": true, "WINDOW": true, "ON": true, "USING": true, "JOIN": true,
}

// fileInFromClause reports a table named by a file in the clause that starts
// right after a FROM or JOIN: a string literal (FROM 'x.csv', FROM t, 'x.csv',
// JOIN 'x.csv'), or a quoted name that looks like a path or has an extension
// (FROM "x.csv": the replacement scan takes it). Only the clause's own level is
// read, up to a word that ends it or the paren that closes it.
func fileInFromClause(toks []usersql.Token) bool {
	depth := 0
	for _, t := range toks {
		switch {
		case t.Kind == usersql.TokParen && t.Text == "(":
			depth++
		case t.Kind == usersql.TokParen:
			if depth--; depth < 0 {
				return false
			}
		case depth > 0:
		case t.Kind == usersql.TokWord && fromClauseEnds[t.Text]:
			return false
		case t.Kind == usersql.TokString:
			return true
		case t.Kind == usersql.TokIdent && strings.ContainsAny(t.Text, "/."):
			return true
		}
	}
	return false
}
