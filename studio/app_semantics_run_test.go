package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
)

func putDef(t *testing.T, app *App, kind semantics.Kind, name, text string) {
	t.Helper()
	_, err := app.ws.Semantics().PutSource(context.Background(), kind, name, []byte(text), semantics.WithOverwrite())
	require.NoError(t, err)
}

func appWithDefinitions(t *testing.T) *App {
	t.Helper()
	app := appWithMetric(t) // sales + net_revenue (dimensions region, month)
	_, err := app.sess.DB().Exec(`CREATE TABLE customers (customer_id INTEGER, status VARCHAR);
		INSERT INTO customers VALUES (1, 'active'), (2, 'churned');
		CREATE TABLE orders (order_id INTEGER, customer_id INTEGER);
		INSERT INTO orders VALUES (10, 1), (11, 1), (12, 2)`)
	require.NoError(t, err)
	putDef(t, app, semantics.KindEntity, "customer", "version: 1\nname: customer\ngrain: one row per customer\ntable: customers\nid_column: customer_id\nassertions:\n  - {kind: unique, column: customer_id}\n  - {kind: accepted_values, column: status, values: [active, churned]}\n")
	putDef(t, app, semantics.KindEntity, "orders", "version: 1\nname: orders\ngrain: one row per order\ntable: orders\nid_column: order_id\n")
	putDef(t, app, semantics.KindJoin, "orders_customers", "version: 1\nname: orders_customers\nleft: orders\nright: customer\non: [{left: customer_id, right: customer_id}]\ncardinality: many_to_one\n")
	putDef(t, app, semantics.KindMetric, "net_revenue", "version: 1\nname: net_revenue\nquery: net_revenue_base\nmeasure: sum(amount)\ndimensions: [region, month]\nassertions:\n  - {kind: non_negative}\n  - {kind: not_null}\n")
	return app
}

func TestRunAssertions_Binding(t *testing.T) {
	app := appWithDefinitions(t)
	rows, err := app.RunAssertions("", "")
	require.NoError(t, err)
	require.Len(t, rows, 7, "a compile row per entity and metric, then its assertions")
	for _, r := range rows {
		assert.Equal(t, "pass", r.Status, "%+v", r)
	}
	assert.Equal(t, "compile", rows[0].Assertion)
	_, err = app.sess.DB().Exec(`INSERT INTO customers VALUES (2, 'paused')`)
	require.NoError(t, err)
	rows, err = app.RunAssertions("entity", "customer")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "compile", rows[0].Assertion)
	assert.Equal(t, "pass", rows[0].Status)
	assert.Equal(t, "fail", rows[1].Status)
	assert.EqualValues(t, 1, rows[1].Violations)
	assert.Equal(t, "fail", rows[2].Status)

	_, err = app.RunAssertions("metric", "nope")
	require.Error(t, err)
}

func TestProfileJoin_Binding(t *testing.T) {
	app := appWithDefinitions(t)
	_, err := app.sess.DB().Exec(`INSERT INTO customers VALUES (1, 'active')`)
	require.NoError(t, err)
	p, err := app.ProfileJoin("orders_customers")
	require.NoError(t, err)
	assert.Equal(t, semantics.VerdictFanOut, p.Measured.Verdict)
	assert.EqualValues(t, 2, p.Measured.MaxFanout)
	j, err := app.ws.Semantics().Join(context.Background(), "orders_customers")
	require.NoError(t, err)
	require.NotNil(t, j.Measured)
	assert.Equal(t, semantics.VerdictFanOut, j.Measured.Verdict)
	_, err = app.ProfileJoin("nope")
	require.Error(t, err)
}

func TestRunMetric_Binding(t *testing.T) {
	app := appWithDefinitions(t)
	res, err := app.RunMetric("net_revenue", []string{"region"})
	require.NoError(t, err)
	assert.Equal(t, []ColumnInfo{{Name: "region", Type: "VARCHAR"}, {Name: "net_revenue", Type: "HUGEINT"}}, res.Columns)
	require.Len(t, res.Rows, 2)
	assert.Equal(t, "east", res.Rows[0][0], "ordered by the dimensions")
	assert.EqualValues(t, 30, res.Rows[0][1])
	assert.Equal(t, res.RowCount, res.TotalRows)

	res, err = app.RunMetric("net_revenue", nil)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.EqualValues(t, 35, res.Rows[0][0])

	for _, bad := range [][]string{{"colour"}, {"region'); DROP TABLE sales; --"}, {"region", "region"}} {
		_, err = app.RunMetric("net_revenue", bad)
		require.Error(t, err, bad)
	}
	_, err = app.RunMetric("no such", nil)
	require.Error(t, err)
}

// A cloned metric that reads outside the workspace is refused by the policy
// both when checked and when tried.
func TestRunMetric_HostileMetricIsRefused(t *testing.T) {
	app := appWithDefinitions(t)
	outside := filepath.Join(t.TempDir(), "secret.csv")
	require.NoError(t, os.WriteFile(outside, []byte("region,amount\nLEAKED_REGION,99\n"), 0o644))
	require.NoError(t, app.ws.Queries().Create(context.Background(), &queries.Query{ID: "pv_base", Title: "pv", SQL: "SELECT region, amount FROM read_csv('" + outside + "')"}))
	putDef(t, app, semantics.KindMetric, "pv", "version: 1\nname: pv\nquery: pv_base\nmeasure: sum(amount)\nassertions:\n  - {kind: not_null}\n")
	_, err := app.RunMetric("pv", nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, agents.ErrReadOnlyBindingDenied.Error(), "the policy refused it")
	rows, err := app.RunAssertions("metric", "pv")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "compile", rows[0].Assertion)
	assert.Equal(t, "fail", rows[0].Status, "a definition the policy refuses does not compile")
	assert.Contains(t, rows[0].Error, "outside the workspace", "the policy refused it")
	assert.Equal(t, "error", rows[1].Status, "its assertion was not run")
	assert.False(t, strings.Contains(rows[0].Error+rows[1].Error, "LEAKED_REGION"), "no row value in the errors (a number could match the random temp path)")
}

// Try it stops at the grid's cap and says so, without inventing a total.
func TestRunMetric_TruncatesAtTheCap(t *testing.T) {
	app := appWithDefinitions(t)
	require.NoError(t, app.ws.Queries().Create(context.Background(), &queries.Query{ID: "many_base", Title: "many", SQL: "SELECT i AS k, 1 AS amount FROM range(6000) t(i)"}))
	putDef(t, app, semantics.KindMetric, "many", "version: 1\nname: many\nquery: many_base\nmeasure: sum(amount)\ndimensions: [k]\n")
	res, err := app.RunMetric("many", []string{"k"})
	require.NoError(t, err)
	assert.True(t, res.Truncated)
	assert.Equal(t, tryItRowCap, res.RowCount)
	assert.Len(t, res.Rows, tryItRowCap)
	assert.Equal(t, res.RowCount, res.TotalRows, "the true total is unknown, not invented")

	res, err = app.RunMetric("net_revenue", []string{"region"})
	require.NoError(t, err)
	assert.False(t, res.Truncated)
}
