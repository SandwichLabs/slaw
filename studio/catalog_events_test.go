package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/importer"
	"github.com/SandwichLabs/slaw/internal/session"
)

// catalogConns lists the conn of every catalog:changed event seen, in order.
func catalogConns(ev *eventLog) []string {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	var out []string
	for _, d := range ev.data["catalog:changed"] {
		if m, ok := d.(map[string]any); ok {
			conn, _ := m["conn"].(string)
			out = append(out, conn)
		}
	}
	return out
}

// otherDuckDB registers a second DuckDB connection called "other" with a table.
func otherDuckDB(t *testing.T, app *App) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "other.duckdb")
	db, err := sql.Open("duckdb", path)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t(x INT)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, app.sess.Register(session.Spec{Name: "other", Type: "duckdb", Path: path}))
}

// #71: the connection rail reloads on catalog:changed, so an import that
// creates a table must raise it, for the connection it wrote to.
func TestImportFile_RefreshesTheRailForCSVAndXLSX(t *testing.T) {
	app := newOpenApp(t)
	var ev eventLog
	app.emit = ev.sink
	dflt := app.sess.DefaultCatalog()

	csvPath := filepath.Join(t.TempDir(), "calls.csv")
	require.NoError(t, os.WriteFile(csvPath, []byte("id,name\n1,a\n2,b\n"), 0o644))
	dry, err := app.ImportFile(importer.Options{Source: importer.Source{Path: csvPath, HasHeader: true}, Table: "calls", DryRun: true})
	require.NoError(t, err)
	require.True(t, dry.OK, "%+v", dry)
	assert.Empty(t, catalogConns(&ev), "a dry run changes nothing")

	sum, err := app.ImportFile(importer.Options{Source: importer.Source{Path: csvPath, HasHeader: true}, Table: "calls", Key: importer.KeyNone})
	require.NoError(t, err)
	require.True(t, sum.OK, "%+v", sum)
	assert.Equal(t, []string{dflt}, catalogConns(&ev), "csv: the default connection's rail reloads")

	xlsx := filepath.Join(t.TempDir(), "fixture.xlsx")
	writeTwoSheetXLSXFixture(t, xlsx)
	sum, err = app.ImportFile(importer.Options{Source: importer.Source{Path: xlsx, Sheet: "People", HasHeader: true}, Table: "people", Key: importer.KeyNone})
	require.NoError(t, err)
	require.True(t, sum.OK, "%+v", sum)
	assert.Equal(t, []string{dflt, dflt}, catalogConns(&ev), "xlsx: reloads too")
}

func TestImportFile_RefreshesOnlyTheConnectionItWroteTo(t *testing.T) {
	app := newOpenApp(t)
	otherDuckDB(t, app)
	require.NoError(t, app.sess.Connect(context.Background(), "other"))
	var ev eventLog
	app.emit = ev.sink

	csvPath := filepath.Join(t.TempDir(), "c.csv")
	require.NoError(t, os.WriteFile(csvPath, []byte("id\n1\n"), 0o644))
	sum, err := app.ImportFile(importer.Options{Source: importer.Source{Path: csvPath, HasHeader: true}, Table: "other.main.fresh", Key: importer.KeyNone})
	require.NoError(t, err)
	require.True(t, sum.OK, "%+v", sum)
	assert.Equal(t, []string{"other"}, catalogConns(&ev))
}

// #267: a write announces (and so rebuilds the completion catalog of) only the
// connection it ran on, "" meaning the default; an unknown connection (the
// console) is the one case that announces every connection.
func TestAfterRun_AnnouncesOnlyTheRunsConnection(t *testing.T) {
	app := newOpenApp(t)
	otherDuckDB(t, app)
	var ev eventLog
	app.emit = ev.sink
	before := session.StatusSnapshot(app.sess)

	app.afterRun(app.sess, before, "CREATE TABLE z(a INT)", "other")
	assert.Equal(t, []string{"other"}, catalogConns(&ev))

	app.afterRun(app.sess, before, "CREATE TABLE z2(a INT)", "")
	assert.Equal(t, []string{"other", app.sess.DefaultCatalog()}, catalogConns(&ev), "the default connection, not every one")

	app.afterRun(app.sess, before, "SELECT 1", "other")
	assert.Len(t, catalogConns(&ev), 2, "a plain read announces nothing")

	app.afterRun(app.sess, before, "CREATE TABLE z3(a INT)", unknownConn)
	assert.Equal(t, []string{"other", app.sess.DefaultCatalog(), ""}, catalogConns(&ev), "unknown connection: all")
}

// #267: a write that names another registered connection, or that attaches,
// detaches or switches one, is announced for those too.
func TestAfterRun_AnnouncesWhatTheStatementNames(t *testing.T) {
	app := newOpenApp(t)
	otherDuckDB(t, app)
	var ev eventLog
	app.emit = ev.sink
	before := session.StatusSnapshot(app.sess)
	dflt := app.sess.DefaultCatalog()

	app.afterRun(app.sess, before, "CREATE TABLE other.main.fresh(a INT)", "")
	assert.Equal(t, []string{dflt, "other"}, catalogConns(&ev), "the run's connection and the one it names, any case")

	app.afterRun(app.sess, before, `create table "OTHER".main.fresh2 as select 1`, "")
	assert.Equal(t, []string{dflt, "other", dflt, "other"}, catalogConns(&ev))

	for _, s := range []string{"DETACH other", "USE other", "ATTACH 'x.duckdb' AS y"} {
		n := len(catalogConns(&ev))
		app.afterRun(app.sess, before, s, "")
		got := catalogConns(&ev)
		require.Len(t, got, n+1, s)
		assert.Equal(t, "", got[n], "%s announces every connection", s)
	}
}

// #168: a connection that attached during a run gets its status published and
// its own catalog announced, even when the run was a plain read.
func TestAfterRun_StatusChangePublishesAndAnnouncesThatConnection(t *testing.T) {
	app := newOpenApp(t)
	otherDuckDB(t, app)
	var ev eventLog
	app.emit = ev.sink
	before := session.StatusSnapshot(app.sess)

	require.NoError(t, app.sess.Connect(context.Background(), "other"))
	app.afterRun(app.sess, before, "SELECT 1", "")

	assert.Equal(t, []string{"other"}, catalogConns(&ev))
	require.Equal(t, 1, ev.count("connection:status"))
	status, _ := ev.data["connection:status"][0].(map[string]any)
	assert.Equal(t, "other", status["name"])

	// Nothing moved: nothing is published.
	app.afterRun(app.sess, session.StatusSnapshot(app.sess), "SELECT 1", "")
	assert.Equal(t, 1, ev.count("connection:status"))
	assert.Len(t, catalogConns(&ev), 1)
}

// #168 / #138: after every run the deny secrets and the S3 URL setting are
// back in place, whatever the run did to them.
func TestSettleRun_RestoresTheStorageDenySecrets(t *testing.T) {
	app := newOpenApp(t)
	db := app.sess.DB()
	count := func() int {
		var n int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM duckdb_secrets() WHERE name LIKE 'slaw-deny-%'").Scan(&n))
		return n
	}
	full := count()
	if full == 0 {
		if os.Getenv("CI") == "true" {
			t.Fatal("httpfs is not installed; CI installs it before the tests")
		}
		t.Skip("httpfs is not installed for this DuckDB version; the deny step is a no-op without it")
	}

	_, err := db.Exec(`DROP TEMPORARY SECRET "slaw-deny-s3"`)
	require.NoError(t, err)
	require.Equal(t, full-1, count(), "the secret is gone")

	app.settleRun(app.sess, session.StatusSnapshot(app.sess))
	assert.Equal(t, full, count(), "settleRun put the deny secret back")
}

// A two-part name is a schema of the current catalog before it is an attached
// catalog (the importer's rule), so the connection is not known: announce all.
func TestImportCatalog_TwoPartNameAnnouncesAll(t *testing.T) {
	app := newOpenApp(t)
	assert.Equal(t, app.sess.DefaultCatalog(), app.importCatalog("calls"))
	assert.Equal(t, "other", app.importCatalog("other.main.calls"))
	assert.Equal(t, "", app.importCatalog("other.calls"))
	assert.Equal(t, "", app.importCatalog("bad name;"))
}
