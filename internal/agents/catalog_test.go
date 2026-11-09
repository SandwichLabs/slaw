package agents

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/session"
)

// sessionAccess is a WorkspaceAccess whose Session is a real hub with a
// second catalog attached.
type sessionAccess struct {
	*mockAccess // the in-memory notebooks/queries/skills stores
	sess        session.Session
}

func (c *sessionAccess) DuckDBDB() *sql.DB        { return c.sess.DB() }
func (c *sessionAccess) Session() session.Session { return c.sess }

// newCatalogHandler builds a full-sandbox Handler over a hub whose default
// catalog holds users(id, name) and whose attached catalog "other" holds
// users(uid PK, email) plus orders(id, total).
func newCatalogHandler(t *testing.T) (*Handler, session.Session) {
	t.Helper()
	dir := t.TempDir()
	sess, err := session.New(
		session.WithTempDirectory(filepath.Join(dir, "tmp")),
		session.WithExtensionDirectory(filepath.Join(dir, "ext")),
		session.WithSecretDirectory(filepath.Join(dir, "sec")),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	path := filepath.Join(dir, "other.duckdb")
	f, err := sql.Open("duckdb", path)
	require.NoError(t, err)
	_, err = f.Exec(`CREATE TABLE users (uid INTEGER PRIMARY KEY, email VARCHAR NOT NULL);
		INSERT INTO users VALUES (1, 'a@x.io');
		CREATE TABLE orders (id INTEGER, total INTEGER); INSERT INTO orders VALUES (1, 10), (2, 20);`)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, sess.Attach(context.Background(), session.Spec{Name: "other", Path: path}))
	_, err = sess.DB().Exec("CREATE TABLE users (id INTEGER, name VARCHAR); INSERT INTO users VALUES (7, 'seven')")
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	eng := engine.NewManager(sess, engine.WithLogger(logger))
	h := NewHandler(&sessionAccess{mockAccess: &mockAccess{}, sess: sess}, eng, &SandboxConfig{Mode: ModeFull, MaxQueryRows: 100}, logger)
	return h, sess
}

// resultMap is a successful response's result object.
func resultMap(t *testing.T, r *Response) map[string]any {
	t.Helper()
	require.Nil(t, r.Error)
	m, ok := r.Result.(map[string]any)
	require.True(t, ok, "result is %T", r.Result)
	return m
}

func call(h *Handler, method string, params map[string]any) *Response {
	return h.Handle(context.Background(), &Request{JSONRPC: "2.0", ID: "1", Method: method, Params: params})
}

func TestIntrospectSchema_ByConnection(t *testing.T) {
	h, _ := newCatalogHandler(t)

	def := resultMap(t, call(h, "introspect_schema", map[string]any{}))
	assert.Equal(t, []string{"users"}, def["tables"], "default catalog, bare names (legacy shape)")

	other := resultMap(t, call(h, "introspect_schema", map[string]any{"connection": "other"}))
	assert.Equal(t, []string{"other.main.orders", "other.main.users"}, other["tables"], "other catalog, qualified")

	missing := call(h, "introspect_schema", map[string]any{"connection": "ghost"})
	assert.NotNil(t, missing.Error, "an unknown connection is an error, not an empty list")
}

func TestDescribeTable_Qualified(t *testing.T) {
	h, _ := newCatalogHandler(t)
	cols := func(table string) []string {
		m := resultMap(t, call(h, "describe_table", map[string]any{"table": table}))
		cols, ok := m["columns"].([]map[string]any)
		require.True(t, ok)
		var names []string
		for _, c := range cols {
			name, _ := c["name"].(string)
			names = append(names, name)
		}
		return names
	}
	assert.Equal(t, []string{"id", "name"}, cols("users"))
	assert.Equal(t, []string{"uid", "email"}, cols("other.main.users"))
	assert.Equal(t, []string{"uid", "email"}, cols("other.users"), "catalog.table uses the catalog's default schema")
	missing := call(h, "describe_table", map[string]any{"table": "other.main.nope"})
	require.NotNil(t, missing.Error, "a missing table is an error, not an empty column list")
}

// describe_table reports the primary key DuckDB's catalog holds, per column.
func TestDescribeTable_ReportsPrimaryKeys(t *testing.T) {
	h, _ := newCatalogHandler(t)
	pk := func(table string) map[string]bool {
		m := resultMap(t, call(h, "describe_table", map[string]any{"table": table}))
		out := map[string]bool{}
		cols, _ := m["columns"].([]map[string]any)
		for _, c := range cols {
			name, _ := c["name"].(string)
			isPK, _ := c["pk"].(bool)
			out[name] = isPK
		}
		return out
	}
	assert.Equal(t, map[string]bool{"uid": true, "email": false}, pk("other.main.users"))
	assert.Equal(t, map[string]bool{"id": false, "name": false}, pk("users"), "a table with no key has no pk column")
}

// A file path or URL is not a table: the error says how to describe a file.
func TestDescribeTable_PathShapedArgumentPointsAtDescribeSelect(t *testing.T) {
	h, _ := newCatalogHandler(t)
	for _, arg := range []string{"/home/zac/", "/home/zac/sales.csv", "data\\sales.csv", "s3://bucket/x.parquet", "~/x.csv"} {
		r := call(h, "describe_table", map[string]any{"table": arg})
		require.NotNil(t, r.Error, arg)
		assert.Equal(t, ErrInvalidParams, r.Error.Code, arg)
		assert.Contains(t, r.Error.Message, "DESCRIBE SELECT * FROM read_csv(", arg)
		assert.NotContains(t, r.Error.Message, "unknown connection", arg)
	}
}

// A quoted table name may hold a slash: it is a table, not a path.
func TestDescribeTable_QuotedSlashNameIsNotAPath(t *testing.T) {
	h, sess := newCatalogHandler(t)
	_, err := sess.DB().Exec(`CREATE TABLE "a/b" (x INTEGER)`)
	require.NoError(t, err)
	for _, arg := range []string{`"a/b"`, `memory.main."a/b"`} {
		m := resultMap(t, call(h, "describe_table", map[string]any{"table": arg}))
		cols, _ := m["columns"].([]map[string]any)
		require.Len(t, cols, 1, arg)
	}
}

// list_connections is the workspace's places data lives, not DuckDB's own
// scratch catalogs; one that is the default stays, since SQL resolves there.
func TestListConnections_HidesBuiltinCatalogs(t *testing.T) {
	h, sess := newCatalogHandler(t)
	names := func() []string {
		m := resultMap(t, call(h, "list_connections", map[string]any{}))
		var out []string
		conns, _ := m["connections"].([]map[string]any)
		for _, c := range conns {
			name, _ := c["name"].(string)
			out = append(out, name)
		}
		return out
	}
	assert.Contains(t, names(), "memory", "memory is this fixture's default catalog")

	require.NoError(t, sess.SetDefaultCatalog(context.Background(), "other"))
	got := names()
	assert.Contains(t, got, "other")
	for _, builtin := range []string{"memory", "system", "temp"} {
		assert.NotContains(t, got, builtin)
	}
}

func TestWriteTools_OnSecondCatalog(t *testing.T) {
	h, sess := newCatalogHandler(t)
	count := func(q string) int {
		var n int
		require.NoError(t, sess.DB().QueryRow(q).Scan(&n))
		return n
	}

	r := call(h, "create_record", map[string]any{"table": "other.main.users", "values": map[string]any{"uid": 2, "email": "b@x.io"}})
	require.Nil(t, r.Error, "%+v", r.Error)
	assert.Equal(t, 2, count("SELECT COUNT(*) FROM other.main.users"))
	assert.Equal(t, 1, count("SELECT COUNT(*) FROM users"), "default catalog untouched")

	r = call(h, "update_record", map[string]any{"table": "other.main.users", "values": map[string]any{"email": "c@x.io"}, "where": "uid = 2"})
	require.Nil(t, r.Error, "%+v", r.Error)
	r = call(h, "delete_record", map[string]any{"table": "other.main.users", "where": "uid = 2"})
	require.Nil(t, r.Error, "%+v", r.Error)
	assert.Equal(t, 1, count("SELECT COUNT(*) FROM other.main.users"))

	r = call(h, "bulk_insert", map[string]any{"table": "other.main.orders", "columns": []any{"id", "total"}, "rows": []any{[]any{3, 30}, []any{4, 40}}})
	require.Nil(t, r.Error, "%+v", r.Error)
	assert.Equal(t, 4, count("SELECT COUNT(*) FROM other.main.orders"))

	r = call(h, "create_table", map[string]any{"name": "other.main.fresh", "columns": []any{map[string]any{"name": "a", "type": "INTEGER"}}})
	require.Nil(t, r.Error, "%+v", r.Error)
	r = call(h, "add_column", map[string]any{"table": "other.main.fresh", "name": "b", "type": "VARCHAR"})
	require.Nil(t, r.Error, "%+v", r.Error)
	assert.Equal(t, 2, count("SELECT COUNT(*) FROM information_schema.columns WHERE table_catalog='other' AND table_name='fresh'"))
	r = call(h, "drop_table", map[string]any{"name": "other.main.fresh"})
	require.Nil(t, r.Error, "%+v", r.Error)
	assert.Equal(t, 0, count("SELECT COUNT(*) FROM information_schema.tables WHERE table_name='fresh'"))
}

func TestRunReadOnlySQLIn_ConnectionIsDefaultCatalog(t *testing.T) {
	h, _ := newCatalogHandler(t)
	ctx := context.Background()

	rs, _, err := h.RunReadOnlySQLIn(ctx, "other", "SELECT email FROM users", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, "a@x.io", rs.Rows[0][0], "unqualified users is the other catalog's")

	rs, _, err = h.RunReadOnlySQL(ctx, "SELECT name FROM users", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, "seven", rs.Rows[0][0])

	// The classifier resolves names on the pinned connection: a table only
	// the other catalog has is fine there and unresolved without it.
	_, _, err = h.RunReadOnlySQLIn(ctx, "other", "SELECT total FROM orders", nil, 10)
	require.NoError(t, err)
	_, _, err = h.RunReadOnlySQL(ctx, "SELECT total FROM orders", nil, 10)
	assert.Error(t, err)

	// Writes and file reads stay denied through a connection.
	_, _, err = h.RunReadOnlySQLIn(ctx, "other", "DELETE FROM users", nil, 10)
	assert.Error(t, err)
	_, _, err = h.RunReadOnlySQLIn(ctx, "other", "SELECT * FROM read_csv('/etc/passwd')", nil, 10)
	assert.Error(t, err)

	_, _, err = h.RunReadOnlySQLIn(ctx, "ghost", "SELECT 1", nil, 10)
	assert.Error(t, err)
}

func TestWriteTools_ResolvedNamesAndLegacyDottedTable(t *testing.T) {
	h, sess := newCatalogHandler(t)
	_, err := sess.DB().Exec(`CREATE TABLE "legacy.dotted" (a INTEGER)`)
	require.NoError(t, err)

	// A bare dotted name that exists as a table is that table, not a path.
	r := call(h, "create_record", map[string]any{"table": "legacy.dotted", "values": map[string]any{"a": 1}})
	require.Nil(t, r.Error, "%+v", r.Error)
	var n int
	require.NoError(t, sess.DB().QueryRow(`SELECT count(*) FROM "legacy.dotted"`).Scan(&n))
	assert.Equal(t, 1, n)

	// Internal catalogs and unknown connections are refused, not passed to DuckDB.
	for _, tbl := range []string{"system.main.users", "temp.main.users", "ghost.main.users"} {
		r = call(h, "create_record", map[string]any{"table": tbl, "values": map[string]any{"a": 1}})
		assert.NotNil(t, r.Error, tbl)
	}

	// A TEMP table shadows the bare name for plain SQL, not for the tools.
	_, err = sess.DB().Exec("CREATE TEMP TABLE users (id INTEGER, name VARCHAR)")
	require.NoError(t, err)
	r = call(h, "create_record", map[string]any{"table": "users", "values": map[string]any{"id": 8, "name": "eight"}})
	require.Nil(t, r.Error, "%+v", r.Error)
	require.NoError(t, sess.DB().QueryRow(`SELECT count(*) FROM memory.main.users WHERE id = 8`).Scan(&n))
	assert.Equal(t, 1, n)
}

// A table that is not there is an error that names it and points at
// introspect_schema (found by the typed session.ErrNoSuchTable, not by message
// text), not an empty column list (#221); any other failure (an unusable name)
// is an error too.
func TestDescribeTable_MissingIsAnErrorNamingTheTable(t *testing.T) {
	h, _ := newCatalogHandler(t)

	r := call(h, "describe_table", map[string]any{"table": "nope"})
	require.NotNil(t, r.Error)
	assert.Contains(t, r.Error.Message, `"nope"`)
	assert.Contains(t, r.Error.Message, "introspect_schema")

	bad := call(h, "describe_table", map[string]any{"table": "a.b.c.d"})
	require.NotNil(t, bad.Error, "an unparseable table name is an error, not an empty list")
}
