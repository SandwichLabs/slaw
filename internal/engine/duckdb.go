package engine

import (
	"context"
	"fmt"

	"github.com/SandwichLabs/slaw/internal/session"
)

// DuckDBEngine executes queries against a DuckDB session.
type DuckDBEngine struct {
	sess session.Session
}

// NewDuckDBEngine creates a DuckDB engine over the given session.
func NewDuckDBEngine(sess session.Session) *DuckDBEngine {
	return &DuckDBEngine{
		sess: sess,
	}
}

// Execute runs a SQL query against the DuckDB database and returns a ResultSet.
func (e *DuckDBEngine) Execute(ctx context.Context, query string, params map[string]any) (*ResultSet, error) {
	rs, _, err := e.execute(ctx, query, params, 0)
	return rs, err
}

// ExecuteCapped runs a SQL query but only keeps the first maxRows rows in
// the returned ResultSet (maxRows <= 0 means unlimited), also returning the
// true total row count. #91: bounds the Go-side/webview copy for a query
// like `SELECT * FROM range(100000)` without a crash there — it does NOT
// bound DuckDB's own memory, since go-duckdb still materializes the full
// result natively before this ever starts scanning; a true streaming cap
// and cancel are #84's.
func (e *DuckDBEngine) ExecuteCapped(ctx context.Context, query string, params map[string]any, maxRows int) (*ResultSet, int, error) {
	return e.execute(ctx, query, params, maxRows)
}

// execute is the shared implementation behind Execute and ExecuteCapped.
func (e *DuckDBEngine) execute(ctx context.Context, query string, params map[string]any, maxRows int) (*ResultSet, int, error) {
	return e.executeIn(ctx, "", query, params, maxRows)
}

// rowsFunc is the one thing that differs between running on a connection
// pinned per call and on one the caller holds: where the bound SQL goes.
type rowsFunc func(boundSQL string, args []any) (*session.Rows, error)

// executeIn is execute with connection (a catalog name) as the default for
// unqualified names; "" is the session default.
func (e *DuckDBEngine) executeIn(ctx context.Context, connection, query string, params map[string]any, maxRows int) (*ResultSet, int, error) {
	var opts []session.QueryOption
	if connection != "" {
		opts = append(opts, session.WithCatalog(connection))
	}
	return e.executeWith(ctx, query, params, maxRows, func(boundSQL string, args []any) (*session.Rows, error) {
		return e.sess.QueryWith(ctx, boundSQL, args, opts...)
	})
}

// executeOn is executeIn on a connection the caller holds across statements.
func (e *DuckDBEngine) executeOn(ctx context.Context, conn *session.Conn, query string, params map[string]any, maxRows int) (*ResultSet, int, error) {
	return e.executeWith(ctx, query, params, maxRows, func(boundSQL string, args []any) (*session.Rows, error) {
		return conn.Query(ctx, boundSQL, args...)
	})
}

// executeWith binds parameters, expands storage wildcards, runs the SQL
// through run and scans the rows: the part every entry point shares.
func (e *DuckDBEngine) executeWith(ctx context.Context, query string, params map[string]any, maxRows int, run rowsFunc) (*ResultSet, int, error) {
	boundSQL, args, err := BindParams(query, params)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to bind parameters: %w", err)
	}

	boundSQL, rw, err := e.expandWildcards(ctx, boundSQL)
	if err != nil {
		return nil, 0, err
	}

	rows, err := run(boundSQL, args)
	if err != nil {
		return nil, 0, shorten(rw, fmt.Errorf("failed to execute duckdb query: %w", err))
	}
	defer rows.Close() //nolint:errcheck

	rs, total, err := ScanRows(rows.Rows, maxRows, maxResultBytes(ctx))
	if err == nil {
		recordExecuted(ctx, query, boundSQL, rw, rs)
	}
	return rs, total, shorten(rw, err)
}

// Close is a no-op for DuckDBEngine since the db is managed externally.
func (e *DuckDBEngine) Close() error {
	return nil
}
