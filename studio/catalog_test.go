package main

// #108: the Studio bindings address tables by catalog-qualified name, so a
// second attached catalog is browsable, editable, importable and queryable
// while every unqualified call keeps meaning "the open file".

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/importer"
	"github.com/SandwichLabs/slaw/internal/session"
)

// appWithSecondCatalog opens an App and attaches "other" (a writable .duckdb
// file holding items(id PK, name)) next to the open file's own catalog.
func appWithSecondCatalog(t *testing.T) *App {
	t.Helper()
	app := newOpenApp(t)

	path := filepath.Join(t.TempDir(), "other.duckdb")
	f, err := sql.Open("duckdb", path)
	require.NoError(t, err)
	_, err = f.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name VARCHAR);
		INSERT INTO items VALUES (1, 'alpha'), (2, 'beta');`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, app.sess.Attach(app.ctx, session.Spec{Name: "other", Path: path}))
	// A same-named table with a different shape in the default catalog.
	_, err = app.sess.DB().Exec(`CREATE TABLE items (sku VARCHAR); INSERT INTO items VALUES ('only-here')`)
	require.NoError(t, err)
	return app
}

func TestStudio_SecondCatalog_BrowseAndDescribe(t *testing.T) {
	app := appWithSecondCatalog(t)

	conns, err := app.ListConnections()
	require.NoError(t, err)
	byName := map[string]ConnectionInfo{}
	for _, c := range conns.Connections {
		byName[c.Name] = c
	}
	assert.True(t, byName["local"].Default, "the workspace's own database is the default")
	assert.Equal(t, "ok", byName["local"].Status)

	tabs, err := catalogTables(app, "other")
	require.NoError(t, err)
	assert.Equal(t, []string{"other.main.items"}, tabs)
	def, err := catalogTables(app, "")
	require.NoError(t, err)
	assert.Equal(t, []string{app.sess.DefaultCatalog() + ".main.items"}, def)

	// Unqualified ListTables / DescribeTable still mean the open file.
	names, err := app.ListTables()
	require.NoError(t, err)
	assert.Equal(t, []string{"items"}, names)
	d, err := app.DescribeTable("items")
	require.NoError(t, err)
	require.Len(t, d.Columns, 1)
	assert.Equal(t, "sku", d.Columns[0].Name)

	q, err := app.DescribeTable("other.main.items")
	require.NoError(t, err)
	require.Len(t, q.Columns, 2)
	assert.Equal(t, "id", q.Columns[0].Name)
	assert.True(t, q.Columns[0].PK, "primary key is read from the right catalog")

	data, err := app.GetTableData("other.main.items", 1, 10, "name", "DESC")
	require.NoError(t, err)
	assert.Equal(t, 2, data.TotalRows)
	assert.Equal(t, "beta", data.Rows[0][1])

	fd, err := app.GetTableDataFiltered("other.main.items", FilterSpec{Conditions: []FilterCondition{{Column: "name", Op: "eq", Value: "alpha"}}}, 1, 10, "", "")
	require.NoError(t, err)
	assert.Equal(t, 1, fd.TotalRows)

	_, err = app.DescribeTable(`other.main.items"; DROP TABLE x; --`)
	assert.Error(t, err)
}

func TestStudio_SecondCatalog_CRUD(t *testing.T) {
	app := appWithSecondCatalog(t)

	res, err := app.InsertRow("other.main.items", map[string]any{"id": "3", "name": "gamma"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Affected)
	_, err = app.UpdateRow("other.main.items", "id", "3", map[string]any{"name": "GAMMA"})
	require.NoError(t, err)

	var name string
	require.NoError(t, app.sess.DB().QueryRow("SELECT name FROM other.main.items WHERE id = 3").Scan(&name))
	assert.Equal(t, "GAMMA", name)

	_, err = app.DeleteRow("other.main.items", "id", "3")
	require.NoError(t, err)
	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM other.main.items").Scan(&n))
	assert.Equal(t, 2, n)
	require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM items").Scan(&n))
	assert.Equal(t, 1, n, "default catalog's items untouched")

	_, err = app.UpdateRow("other.main.items", "id", "99", map[string]any{"name": "x"})
	assert.Error(t, err, "PK mismatch is an error")
}

func TestStudio_SecondCatalog_SchemaChanges(t *testing.T) {
	app := appWithSecondCatalog(t)

	require.NoError(t, app.CreateTable("other.main.made", []ColumnInfo{
		{Name: "id", Type: "INTEGER", PK: true}, {Name: "label", Type: "VARCHAR"},
	}))
	_, err := app.InsertRow("other.main.made", map[string]any{"label": "auto"})
	require.NoError(t, err, "auto-number default works in the other catalog")
	var id int
	require.NoError(t, app.sess.DB().QueryRow("SELECT id FROM other.main.made").Scan(&id))
	assert.Equal(t, 1, id)
	var seqs int
	require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM duckdb_sequences() WHERE database_name='other' AND sequence_name='_slaw_seq_made_id'").Scan(&seqs))
	assert.Equal(t, 1, seqs)

	d, err := app.DescribeTable("other.main.items")
	require.NoError(t, err)
	assert.Len(t, d.Columns, 2)

	require.NoError(t, app.DropTable("other.main.made"))
	tabs, err := catalogTables(app, "other")
	require.NoError(t, err)
	assert.Equal(t, []string{"other.main.items"}, tabs)
}

func TestStudio_SecondCatalog_Import(t *testing.T) {
	app := appWithSecondCatalog(t)
	csv := filepath.Join(t.TempDir(), "p.csv")
	require.NoError(t, os.WriteFile(csv, []byte("pid,label\n1,x\n2,y\n"), 0o644))

	sum, err := app.ImportFile(importer.Options{
		Source: importer.Source{Path: csv, HasHeader: true}, Table: "other.main.imported", Key: "pid",
	})
	require.NoError(t, err)
	require.True(t, sum.OK, "%+v", sum)
	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM other.main.imported").Scan(&n))
	assert.Equal(t, 2, n)
	names, err := app.ListTables()
	require.NoError(t, err)
	assert.NotContains(t, names, "imported")
}

func TestStudio_RunA2UIQueryIn_UsesConnectionAsDefaultCatalog(t *testing.T) {
	app := appWithSecondCatalog(t)

	// Unset connection: the open file's items (one column).
	def, err := app.RunA2UIQuery("SELECT * FROM items")
	require.NoError(t, err)
	assert.Equal(t, []string{"sku"}, def.Columns)

	// connection = other: unqualified `items` is the OTHER catalog's table.
	in, err := app.RunA2UIQueryIn("SELECT * FROM items ORDER BY id", "other", RenderSource{})
	require.NoError(t, err)
	assert.Equal(t, []string{"id", "name"}, in.Columns)
	require.Len(t, in.Rows, 2)
	assert.Equal(t, "alpha", in.Rows[0][1])

	// Qualified names keep working with or without a connection.
	q, err := app.RunA2UIQueryIn("SELECT count(*) FROM other.main.items", "", RenderSource{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, q.Rows[0][0])

	// The pinned connection never leaks USE into the pool.
	again, err := app.RunA2UIQuery("SELECT * FROM items")
	require.NoError(t, err)
	assert.Equal(t, []string{"sku"}, again.Columns)

	// Same read-only gate on a bound statement.
	_, err = app.RunA2UIQueryIn("DELETE FROM items", "other", RenderSource{})
	require.Error(t, err)
	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM other.main.items").Scan(&n))
	assert.Equal(t, 2, n)

	// Unknown / malformed connection names are refused, not interpolated.
	_, err = app.RunA2UIQueryIn("SELECT 1", "nope", RenderSource{})
	assert.Error(t, err)
	_, err = app.RunA2UIQueryIn("SELECT 1", `x"; DROP TABLE items; --`, RenderSource{})
	assert.Error(t, err)
}

// The reviewer's scenario: a USE issued through the query editor must not
// redirect later unqualified datasheet writes, and a TEMP table must not
// shadow the user's table.
func TestStudio_UseAndTempTableDoNotRedirectDatasheetWrites(t *testing.T) {
	app := appWithSecondCatalog(t)
	app.sess.DB().SetMaxOpenConns(1)
	_, err := app.sess.DB().Exec(`CREATE TABLE people (id INTEGER PRIMARY KEY, name VARCHAR); INSERT INTO people VALUES (1, 'orig')`)
	require.NoError(t, err)
	_, err = app.sess.DB().Exec(`CREATE TABLE other.main.people (id INTEGER PRIMARY KEY, name VARCHAR); INSERT INTO other.main.people VALUES (1, 'other')`)
	require.NoError(t, err)

	_, err = app.ExecuteQuery("USE other")
	require.NoError(t, err)
	_, err = app.UpdateRow("people", "id", "1", map[string]any{"name": "edited"})
	require.NoError(t, err)
	var def, oth string
	require.NoError(t, app.sess.DB().QueryRow("SELECT name FROM people").Scan(&def))
	require.NoError(t, app.sess.DB().QueryRow("SELECT name FROM other.main.people").Scan(&oth))
	assert.Equal(t, "edited", def, "the edit landed in the open file's table")
	assert.Equal(t, "other", oth)

	_, err = app.ExecuteQuery("CREATE TEMP TABLE people (id INTEGER PRIMARY KEY, name VARCHAR)")
	require.NoError(t, err)
	_, err = app.InsertRow("people", map[string]any{"id": "2", "name": "real"})
	require.NoError(t, err)
	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT count(*) FROM "+app.sess.DefaultCatalog()+".main.people").Scan(&n))
	assert.Equal(t, 2, n, "the insert went to the real table, not the temp shadow")
}

func TestStudio_UnknownAndInternalConnectionsRefused(t *testing.T) {
	app := appWithSecondCatalog(t)
	for _, tbl := range []string{"system.main.duckdb_tables", "temp.main.x", "ghost.main.items", "ghost.items"} {
		if tbl != "ghost.items" { // a missing schema in the open file just describes as empty, as a missing table always did
			_, err := app.DescribeTable(tbl)
			assert.Error(t, err, tbl)
		}
		_, err := app.InsertRow(tbl, map[string]any{"a": 1})
		assert.Error(t, err, tbl)
	}
	_, err := app.RunA2UIQueryIn("SELECT 1", "Prod DB", RenderSource{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown connection")
	_, err = app.RunA2UIQueryIn("SELECT 1", "ghost", RenderSource{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown connection")
}

func TestStudio_SchemaChangeOnCatalogDotTable(t *testing.T) {
	app := appWithSecondCatalog(t)
	// "other.items": a catalog qualifier with no schema.
	d, err := app.DescribeTable("other.items")
	require.NoError(t, err)
	assert.Len(t, d.Columns, 2)
}

// Live repro: a BEGIN left open by the query editor must not capture later
// schema changes; Save must still work and the change must persist.
func TestStudio_OpenTransactionFromEditorDoesNotLoseWork(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.ExecuteQuery("BEGIN TRANSACTION")
	require.NoError(t, err)

	require.NoError(t, app.CreateTable("after_begin", []ColumnInfo{{Name: "id", Type: "INTEGER", PK: true}}))
	_, err = app.ExecuteQuery("ALTER TABLE after_begin ADD COLUMN note VARCHAR")
	require.NoError(t, err)
	require.NoError(t, app.sess.DB().Ping(), "the pool is not blocked by a leaked transaction")

	reopenApp(t, app)
	d, err := app.DescribeTable("after_begin")
	require.NoError(t, err)
	assert.Len(t, d.Columns, 2, "the table and column persisted across reopen")
}

// catalogTables lists one connection's default-schema tables, qualified, the
// way the (removed) ListCatalogTables binding did; "" is the default catalog.
func catalogTables(app *App, connection string) ([]string, error) {
	tabs, err := app.sess.ListTables(app.ctxOrBackground(), connection, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, t.Ref().String())
	}
	return out, nil
}

func TestStudio_RunA2UIQueryIn_ByteCap(t *testing.T) {
	app := appWithSecondCatalog(t)
	_, err := app.RunA2UIQueryIn("SELECT repeat('x', 1000000) FROM range(100)", "", RenderSource{})
	require.ErrorIs(t, err, engine.ErrResultTooLarge)
}
