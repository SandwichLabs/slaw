package engine

import (
	"context"
	"log/slog"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

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
	summary, _, _ := rw.Summarize(executed)
	cols := make([]receipts.Column, len(rs.Columns))
	for i, c := range rs.Columns {
		cols[i] = receipts.Column{Name: c.Name, Type: c.Type}
	}
	run.Record(func(f *receipts.Facts) {
		f.Executed, f.Summary, f.Columns = executed, summary, cols
	})
}

// recordRunFacts completes the facts after a successful run: when it started,
// how long it took, which statement type DuckDB reports and which connections
// its names resolve to. pin is the connection a console holds (nil otherwise).
func (m *Manager) recordRunFacts(ctx context.Context, connection string, pin *session.Conn, start time.Time) {
	run := receipts.RunFrom(ctx)
	if run == nil {
		return
	}
	dur := time.Since(start)
	executed := run.Facts().Executed
	if executed == "" {
		return
	}
	kind := m.statementKind(ctx, connection, pin, executed)
	conns := receiptConnections(session.ReferencedConnections(m.sess, connection, executed))
	run.Record(func(f *receipts.Facts) {
		f.At, f.Duration, f.Stmt, f.Connections = start, dur, kind, conns
	})
}

// receiptConnections is what a receipt keeps of a connection: its name, type and
// read-only flag, and a status that is an enum, never the status message (which
// can name a host).
func receiptConnections(infos []session.ConnectionInfo) []receipts.Connection {
	out := make([]receipts.Connection, 0, len(infos))
	for _, c := range infos {
		st := receipts.StatusError
		switch c.Kind {
		case session.StatusOK:
			st = receipts.StatusConnected
		case session.StatusNotConnected:
			st = receipts.StatusNotConnected
		}
		out = append(out, receipts.Connection{Name: c.Name, Type: c.Type, ReadOnly: c.ReadOnly, Status: st})
	}
	return out
}

// statementKind says whether text is a single select or explain, which are the
// only statements a receipt is made for. A script is other, whatever its first
// statement is. The plain forms are read from the first word, as the console
// does, without preparing them (which would bind their remote tables a second
// time); WITH is prepared (a WITH can lead an INSERT or a DELETE), and so is
// everything else, and DuckDB's own type decides. EXPLAIN ANALYZE runs the
// statement it explains, so it is other.
func (m *Manager) statementKind(ctx context.Context, connection string, pin *session.Conn, text string) receipts.StmtKind {
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
	typ, err := m.parsedType(ctx, connection, pin, text)
	switch {
	case err != nil:
		return receipts.StmtOther
	case typ == duckdb.STATEMENT_TYPE_SELECT:
		return receipts.StmtSelect
	case typ == duckdb.STATEMENT_TYPE_EXPLAIN:
		return explainKind(stmts[0])
	}
	return receipts.StmtOther
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

// parsedType prepares text (never runs it) on the connection the run used, or on
// a pooled one pinned to connection's catalog, and returns DuckDB's own type.
func (m *Manager) parsedType(ctx context.Context, connection string, pin *session.Conn, text string) (duckdb.StmtType, error) {
	if pin != nil {
		return usersql.ParsedType(pin.SQLConn(), text)
	}
	var opts []session.QueryOption
	if connection != "" {
		opts = append(opts, session.WithCatalog(connection))
	}
	c, err := m.sess.Conn(ctx, opts...)
	if err != nil {
		return 0, err
	}
	defer c.Close() //nolint:errcheck
	return usersql.ParsedType(c.SQLConn(), text)
}
