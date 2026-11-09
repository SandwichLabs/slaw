package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
)

// The exit criterion of metric(): a hand-written metric (a saved query and a
// yaml file, no agent) runs from the Query Explorer, a notebook cell, an
// artifact query and the console.

func appWithMetric(t *testing.T) *App {
	t.Helper()
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec(`CREATE TABLE sales (region VARCHAR, month INTEGER, amount INTEGER);
		INSERT INTO sales VALUES ('east', 1, 10), ('east', 2, 20), ('west', 1, 5), ('west', 2, 1)`)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, app.ws.Queries().Create(ctx, &queries.Query{ID: "net_revenue_base", Title: "Net revenue base",
		SQL:        "SELECT region, month, amount FROM sales WHERE amount > $min;",
		Parameters: []queries.Param{{Name: "min", Type: "number", Default: "2"}}}))
	m, err := semantics.DecodeMetric("net_revenue", []byte("version: 1\nname: net_revenue\nquery: net_revenue_base\nmeasure: sum(amount)\ndimensions: [region, month]\n"))
	require.NoError(t, err)
	require.NoError(t, app.ws.Semantics().PutMetric(ctx, m))
	return app
}

func TestMetric_RunsInTheQueryExplorer(t *testing.T) {
	app := appWithMetric(t)
	res, err := app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('net_revenue', by => 'region') ORDER BY region"})
	require.NoError(t, err)
	assert.Equal(t, []ColumnInfo{{Name: "region", Type: "VARCHAR"}, {Name: "net_revenue", Type: "HUGEINT"}}, res.Columns)
	require.Len(t, res.Rows, 2)
	assert.EqualValues(t, 30, res.Rows[0][1])
	assert.EqualValues(t, 5, res.Rows[1][1])

	// The editor's own saved-query inputs still bind next to a metric.
	require.NoError(t, app.ws.Queries().Create(context.Background(), &queries.Query{ID: "mine", Title: "Mine",
		SQL: "SELECT * FROM metric('net_revenue') WHERE net_revenue > $floor", Parameters: []queries.Param{{Name: "floor", Type: "number", Default: "100"}}}))
	res, err = app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('net_revenue') WHERE net_revenue > $floor", QueryID: "mine", Params: map[string]any{"floor": float64(10)}})
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	res, err = app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('net_revenue') WHERE net_revenue > $floor", QueryID: "mine"})
	require.NoError(t, err)
	assert.Empty(t, res.Rows)

	for _, q := range []string{"SELECT * FROM metric('nope')", "SELECT * FROM metric('net_revenue', by => 'colour')"} {
		_, err = app.RunQuery(RunQueryRequest{SQL: q})
		require.Error(t, err, q)
	}
}

func TestMetric_PersonSurfaceHoldsTheSavedQueryToASelect(t *testing.T) {
	app := appWithMetric(t)
	ctx := context.Background()
	require.NoError(t, app.ws.Queries().Create(ctx, &queries.Query{ID: "sneaky", Title: "Sneaky",
		SQL: "WITH x AS (SELECT 1 AS amount) INSERT INTO sales SELECT 'z', 9, amount FROM x"}))
	m, err := semantics.DecodeMetric("sneaky", []byte("version: 1\nname: sneaky\nquery: sneaky\nmeasure: count(*)\n"))
	require.NoError(t, err)
	require.NoError(t, app.ws.Semantics().PutMetric(ctx, m))
	_, err = app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('sneaky')"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a plain read")
	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT count(*) FROM sales").Scan(&n))
	assert.Equal(t, 4, n)
}

func TestMetric_RunsInANotebookCellAndAnArtifactQuery(t *testing.T) {
	app := appWithMetric(t)
	for _, mode := range []string{"read-only", "read-write", "full"} {
		setAgentSandbox(t, app, mode)
		res, err := app.RunA2UIQueryIn("SELECT region, net_revenue FROM metric('net_revenue', by => 'region') ORDER BY region", "", RenderSource{})
		require.NoError(t, err, mode)
		require.Len(t, res.Rows, 2, mode)
		assert.EqualValues(t, 30, res.Rows[0][1])
	}
	// The artifact's own $min (outside the call) is a value the caller supplies, so
	// it also feeds the metric's saved query, and min=0 is told apart from the default 2.
	writeStudioArtifact(t, app, "revenue", `version: 1
title: Revenue
parameters:
  - {name: min, type: number, default: "2"}
queries:
  - name: by_region
    sql: SELECT region, net_revenue FROM metric('net_revenue', by => 'region') WHERE $min >= 0 ORDER BY region
`, `{"title":"Revenue","components":[{"id":"t","type":"table","query":"by_region"}]}`)
	res, err := app.RunArtifactQuery("revenue", "by_region", nil)
	require.NoError(t, err)
	require.Len(t, res.Rows, 2)
	assert.EqualValues(t, 5, res.Rows[1][1], "the default $min = 2 dropped the 1")
	res, err = app.RunArtifactQuery("revenue", "by_region", map[string]any{"min": float64(0)})
	require.NoError(t, err)
	require.Len(t, res.Rows, 2)
	assert.EqualValues(t, 6, res.Rows[1][1], "min = 0 keeps it")
}

// A cloned repository's metric whose saved query reads a file outside the
// workspace is refused at run, on the Query Explorer and the console, with the
// metric's name; the person's own SQL is not held to the policy.
func TestMetric_HostileClonedMetricIsRefusedOnThePersonSurfaces(t *testing.T) {
	app := appWithMetric(t)
	outside := filepath.Join(t.TempDir(), "secret.csv")
	require.NoError(t, os.WriteFile(outside, []byte("region,month,amount\nx,1,99\n"), 0o644))
	ctx := context.Background()
	require.NoError(t, app.ws.Queries().Create(ctx, &queries.Query{ID: "pv_base", Title: "pv", SQL: "SELECT region, month, amount FROM read_csv('" + outside + "')"}))
	m, err := semantics.DecodeMetric("pv", []byte("version: 1\nname: pv\nquery: pv_base\nmeasure: sum(amount)\n"))
	require.NoError(t, err)
	require.NoError(t, app.ws.Semantics().PutMetric(ctx, m))

	_, err = app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('pv')"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metric 'pv' is refused")
	// ... while the person's own SQL outside any metric keeps the editor's rules.
	res, err := app.RunQuery(RunQueryRequest{SQL: "SELECT count(*) FROM read_csv('" + outside + "')"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Rows[0][0])

	r := newConsoleRigWith(t, app)
	id := r.open(t, "")
	r.waitText(t, id, "▸")
	require.NoError(t, r.app.ReplInput(id, "SELECT * FROM metric('pv');\r"))
	r.waitText(t, id, "metric 'pv' is refused")
}

func TestMetric_OuterPlaceholderOfAMetricParameterIsRefused(t *testing.T) {
	app := appWithMetric(t)
	_, err := app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('net_revenue') WHERE net_revenue > $min"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "$min belongs to metric net_revenue's saved query; it can't be used outside metric()")

	// A saved query open in the editor that declares $min with another type is a clash.
	ctx := context.Background()
	require.NoError(t, app.ws.Queries().Create(ctx, &queries.Query{ID: "texty", Title: "Texty",
		SQL: "SELECT * FROM metric('net_revenue') WHERE 'a' = $min", Parameters: []queries.Param{{Name: "min", Type: "text", Default: "a"}}}))
	_, err = app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('net_revenue') WHERE 'a' = $min", QueryID: "texty"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declared as text by the open query and as number")

	// With the same type, the editor's input is the supplied value, and it feeds the metric.
	require.NoError(t, app.ws.Queries().Create(ctx, &queries.Query{ID: "numy", Title: "Numy",
		SQL: "SELECT * FROM metric('net_revenue') WHERE $min >= 0", Parameters: []queries.Param{{Name: "min", Type: "number", Default: "2"}}}))
	res, err := app.RunQuery(RunQueryRequest{SQL: "SELECT * FROM metric('net_revenue') WHERE $min >= 0", QueryID: "numy", Params: map[string]any{"min": float64(0)}})
	require.NoError(t, err)
	assert.EqualValues(t, 36, res.Rows[0][0])
}

func TestMetric_RunsInTheConsole(t *testing.T) {
	r := newConsoleRigWith(t, appWithMetric(t))
	id := r.open(t, "")
	r.waitText(t, id, "▸")
	require.NoError(t, r.app.ReplInput(id, "SELECT region, net_revenue FROM metric('net_revenue', by => 'region') ORDER BY region;\r"))
	r.waitText(t, id, "east")
	r.waitText(t, id, "30")
	require.NoError(t, r.app.ReplInput(id, "SELECT * FROM metric('nope');\r"))
	r.waitText(t, id, "there is no metric named 'nope'")
}

// An exported Query Explorer buffer is prepared like a run: a metric() call
// expands (its saved query's $min bound inside COPY) and the editor's guard
// refuses INSTALL.
func TestMetric_ExportQueryResultsExpandsAndIsGuarded(t *testing.T) {
	app := appWithMetric(t)
	dst := filepath.Join(t.TempDir(), "out.csv")
	got, err := app.ExportQueryResults("SELECT * FROM metric('net_revenue', by => 'region') ORDER BY region;", "csv", dst)
	require.NoError(t, err)
	assert.Equal(t, dst, got)
	b, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "region,net_revenue\neast,30\nwest,5\n", string(b))

	dst2 := filepath.Join(t.TempDir(), "x.csv")
	_, err = app.ExportQueryResults("INSTALL httpfs", "csv", dst2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INSTALL is not available here")
	_, statErr := os.Stat(dst2)
	assert.True(t, os.IsNotExist(statErr))
	_, err = app.ExportQueryResults("SELECT * FROM metric('nope')", "csv", dst2)
	require.Error(t, err)
}

func TestMetric_ExportRefusesBreakoutAndHostileMetric(t *testing.T) {
	app := appWithMetric(t)
	dst := filepath.Join(t.TempDir(), "e.csv")
	_, err := app.ExportQueryResults("SELECT 1) TO '"+dst+".x'; DELETE FROM sales; COPY (SELECT 1", "csv", dst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one SELECT")
	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT count(*) FROM sales").Scan(&n))
	assert.Equal(t, 4, n)
	assert.NoFileExists(t, dst)
	assert.NoFileExists(t, dst+".x")

	outside := filepath.Join(t.TempDir(), "secret.csv")
	require.NoError(t, os.WriteFile(outside, []byte("region,month,amount\nx,1,99\n"), 0o644))
	ctx := context.Background()
	require.NoError(t, app.ws.Queries().Create(ctx, &queries.Query{ID: "pv_base", Title: "pv", SQL: "SELECT region, month, amount FROM read_csv('" + outside + "')"}))
	m, err := semantics.DecodeMetric("pv", []byte("version: 1\nname: pv\nquery: pv_base\nmeasure: sum(amount)\n"))
	require.NoError(t, err)
	require.NoError(t, app.ws.Semantics().PutMetric(ctx, m))
	dst2 := filepath.Join(t.TempDir(), "pv.csv")
	_, err = app.ExportQueryResults("SELECT metric('pv')", "csv", dst2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metric 'pv' is refused")
	assert.NoFileExists(t, dst2)

	dollar := filepath.Join(t.TempDir(), "a$b.csv")
	_, err = app.ExportQueryResults("SELECT metric('net_revenue') AS total", "csv", dollar)
	require.NoError(t, err)
	assert.FileExists(t, dollar)
}
