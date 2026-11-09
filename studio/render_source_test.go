package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/a2ui"
	"github.com/SandwichLabs/slaw/internal/artifacts"
	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

// notebookWithCells stores a notebook with one SQL cell and one A2UI cell (a
// stat bound to a query), and returns it.
func notebookWithCells(t *testing.T, app *App) notebooks.Notebook {
	t.Helper()
	nb := notebooks.Empty("Receipts")
	nb.Cells = []notebooks.Cell{
		{ID: "cell_sql", Type: notebooks.CellSQL, Source: "SELECT 1 AS n", View: "table"},
		{ID: "cell_ui", Type: notebooks.CellA2UI, Page: &a2ui.Page{Title: "Totals", Components: []a2ui.Component{
			{ID: "total", Type: "stat", Fields: map[string]any{"label": "Total", "sql": "SELECT 7 AS n"}},
		}}},
	}
	saved, _, err := app.ws.Notebooks().Insert(app.ctxOrBackground(), nb)
	require.NoError(t, err)
	return saved
}

func receiptOf(t *testing.T, app *App, id string) receipts.Receipt {
	t.Helper()
	require.NotEmpty(t, id)
	r, err := app.GetReceipt(id)
	require.NoError(t, err)
	return r
}

// A source the open notebook confirms gives the receipt its surface and ref; a
// source it does not is dropped and the run goes ahead as a plain render.
func TestRunA2UIQueryIn_SourceBecomesTheReceiptRef(t *testing.T) {
	app := newOpenApp(t)
	nb := notebookWithCells(t, app)
	base := "notebooks/" + nb.ID + ".slnb#"

	res, err := app.RunA2UIQueryIn("SELECT 1 AS n", "", RenderSource{Kind: "cell", Notebook: nb.ID, Cell: "cell_sql"})
	require.NoError(t, err)
	r := receiptOf(t, app, res.ReceiptID)
	assert.Equal(t, receipts.SurfaceNotebookCell, r.Surface)
	assert.Equal(t, receipts.Source{Kind: receipts.SourceCell, Ref: base + "cell_sql"}, r.Source)

	res, err = app.RunA2UIQueryIn("SELECT 7 AS n", "", RenderSource{Kind: "a2ui", Notebook: nb.ID, Cell: "cell_ui", Component: "total"})
	require.NoError(t, err)
	r = receiptOf(t, app, res.ReceiptID)
	assert.Equal(t, receipts.SurfaceA2UI, r.Surface)
	assert.Equal(t, receipts.Source{Kind: receipts.SourceComponent, Ref: base + "cell_ui/total"}, r.Source)
}

func TestRunA2UIQueryIn_AWrongSourceIsDroppedNotRefused(t *testing.T) {
	app := newOpenApp(t)
	nb := notebookWithCells(t, app)
	for name, src := range map[string]RenderSource{
		"no such notebook":          {Kind: "cell", Notebook: "nb_missing", Cell: "cell_sql"},
		"no such cell":              {Kind: "cell", Notebook: nb.ID, Cell: "cell_gone"},
		"a cell of the wrong type":  {Kind: "cell", Notebook: nb.ID, Cell: "cell_ui"},
		"no such component":         {Kind: "a2ui", Notebook: nb.ID, Cell: "cell_ui", Component: "nope"},
		"a component of a sql cell": {Kind: "a2ui", Notebook: nb.ID, Cell: "cell_sql", Component: "c"},
		"a path, not an id":         {Kind: "cell", Notebook: "../../etc/passwd", Cell: "x"},
		"an unknown kind":           {Kind: "page", Notebook: nb.ID, Cell: "cell_sql"},
		"no artifact":               {Kind: "a2ui", Artifact: "ghost", Component: "t"},
		"a kind with no fields":     {Kind: "cell"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := app.RunA2UIQueryIn("SELECT 1 AS n", "", src)
			require.NoError(t, err, "the run is never refused for its source")
			r := receiptOf(t, app, res.ReceiptID)
			assert.Equal(t, receipts.SurfaceRender, r.Surface)
			assert.Equal(t, receipts.Source{}, r.Source)
		})
	}
}

func TestRunA2UIQueryIn_ArtifactComponentSource(t *testing.T) {
	app := newOpenApp(t)
	view := []byte(`{"title":"T","components":[{"id":"t","type":"table","query":"rows"}]}`)
	manifest := []byte("version: 1\ntitle: Sales\nqueries:\n  - name: rows\n    sql: SELECT 1 AS id\n")
	require.NoError(t, app.ws.Artifacts().Put(app.ctxOrBackground(), &artifacts.Artifact{Name: "sales", ManifestText: manifest, View: view}, artifacts.WithRev("")))

	res, err := app.RunA2UIQueryIn("SELECT 1 AS id", "", RenderSource{Kind: "a2ui", Artifact: "sales", Component: "t"})
	require.NoError(t, err)
	r := receiptOf(t, app, res.ReceiptID)
	assert.Equal(t, receipts.SurfaceA2UI, r.Surface)
	assert.Equal(t, receipts.Source{Kind: receipts.SourceComponent, Ref: "artifacts/sales/view.a2ui.json#t"}, r.Source)

	res, err = app.RunA2UIQueryIn("SELECT 1 AS id", "", RenderSource{Kind: "a2ui", Artifact: "sales", Component: "other"})
	require.NoError(t, err)
	assert.Equal(t, receipts.SurfaceRender, receiptOf(t, app, res.ReceiptID).Surface)
}

func TestGetReceiptAndListReceipts(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.RunA2UIQuery("SELECT 1 AS a")
	require.NoError(t, err)
	q, err := app.RunQuery(RunQueryRequest{SQL: "SELECT 2 AS b", TabID: "t"})
	require.NoError(t, err)

	got, err := app.GetReceipt(" " + q.ReceiptID + " ")
	require.NoError(t, err)
	assert.Equal(t, q.ReceiptID, got.ID)
	assert.Equal(t, "SELECT 2 AS b", got.SQL)

	_, err = app.GetReceipt("r_00000000000000000000")
	require.Error(t, err, "an id with no receipt")
	_, err = app.GetReceipt("../../etc/passwd")
	require.Error(t, err)

	all, err := app.ListReceipts(ReceiptFilter{})
	require.NoError(t, err)
	require.Len(t, all.Receipts, 2)
	assert.Equal(t, q.ReceiptID, all.Receipts[0].ID, "newest first")

	editor, err := app.ListReceipts(ReceiptFilter{Surface: "query_editor", Limit: 5})
	require.NoError(t, err)
	require.Len(t, editor.Receipts, 1)
	assert.Equal(t, "SELECT 2 AS b", editor.Receipts[0].SQL)

	_, err = app.ListReceipts(ReceiptFilter{Surface: "nowhere"})
	require.Error(t, err)
}

// A store that cannot be written never fails a run: the result goes out with a
// reason class and no id, which the drawer words as "No receipt was kept".
func TestReceiptNote_TravelsOnTheWire(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only folder does not stop root")
	}
	app := newOpenApp(t)
	fileStore, ok := app.ws.Receipts().Store().(*receipts.FileStore)
	require.True(t, ok)
	dir := fileStore.Dir()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries { // no shard folder is left to write into
		if e.IsDir() {
			require.NoError(t, os.RemoveAll(filepath.Join(dir, e.Name())))
		}
	}
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	res, err := app.RunA2UIQuery("SELECT 1 AS a")
	require.NoError(t, err)
	assert.Empty(t, res.ReceiptID)
	assert.Equal(t, "not_kept", res.ReceiptNote)
	assert.NotContains(t, res.ReceiptNote, dir, "a reason class, never a path")
}

// The source check is cached per notebook by the file's size and mtime, follows a
// change on disk, and never reads through the notebook store: its Get would record
// the revision as seen and hide a later conflict from the editor.
func TestRenderOrigin_CachesByFileStampAndLeavesTheStoreAlone(t *testing.T) {
	app := newOpenApp(t)
	nb := notebookWithCells(t, app)
	ctx := app.ctxOrBackground()
	src := RenderSource{Kind: "cell", Notebook: nb.ID, Cell: "cell_sql"}

	// The editor loaded the notebook: the store has seen this revision.
	loaded, _, err := app.ws.Notebooks().Get(ctx, nb.ID)
	require.NoError(t, err)

	o := renderOrigin(ctx, app.ws, &app.renderIndex, src)
	assert.Equal(t, receipts.SurfaceNotebookCell, o.Surface)
	require.Len(t, app.renderIndex.m, 1)
	first := app.renderIndex.m[app.ws.Root()+"\x00"+nb.ID]
	renderOrigin(ctx, app.ws, &app.renderIndex, src)
	assert.Equal(t, first.cells["cell_sql"].typ, app.renderIndex.m[app.ws.Root()+"\x00"+nb.ID].cells["cell_sql"].typ, "a second render reuses the entry")

	// Another process edits the file (the cell is gone). The next check sees it.
	path := filepath.Join(app.ws.Root(), "notebooks", nb.ID+".slnb")
	edited := strings.Replace(readFile(t, path), "cell_sql", "cell_other", 1)
	require.NoError(t, os.WriteFile(path, []byte(edited), 0o600))
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
	assert.Equal(t, receipts.SurfaceRender, renderOrigin(ctx, app.ws, &app.renderIndex, src).Surface, "the cell no longer exists")

	// The check did not tell the store it had seen the new revision: saving from
	// the old copy is still a conflict.
	_, _, err = app.ws.Notebooks().Save(ctx, loaded)
	require.ErrorIs(t, err, notebooks.ErrConflict)
}

func TestNotebookIndexCache_IsBounded(t *testing.T) {
	app := newOpenApp(t)
	for i := 0; i < notebookIndexMax+5; i++ {
		nb := notebooks.Empty("N")
		nb.ID = "nb_" + strings.Repeat("a", 8) + "-" + strings.Repeat("0", 4) + "-" + strings.Repeat("0", 4) + "-" + strings.Repeat("0", 4) + "-" + fmt.Sprintf("%012d", i)
		saved, _, err := app.ws.Notebooks().Insert(app.ctxOrBackground(), nb)
		require.NoError(t, err)
		_, ok := app.renderIndex.cells(app.ws.Root(), saved.ID)
		require.True(t, ok)
	}
	assert.LessOrEqual(t, len(app.renderIndex.m), notebookIndexMax)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}
