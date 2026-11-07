//go:build integration

package agents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// icebergWorkspace opens a workspace over the fixture: an s3 connection that
// reads the warehouse bucket, a read-only catalog "cat" and a writable one "catw"
// (read_only: false) on the same REST catalog.
func icebergWorkspace(t *testing.T, f *icebergtest.Fixture, catOpts ...map[string]string) (workspace.Workspace, session.Session) {
	t.Helper()
	ctx := context.Background()
	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeShared, workspace.ConnectionDef{
		Name: "minio", Type: "s3", Scope: "s3://" + icebergtest.Warehouse + "/", Region: "us-east-1",
		Options: map[string]string{"endpoint": f.S3Endpoint, "url_style": "path", "use_ssl": "false"}}))
	require.NoError(t, ws.SetCredentials(ctx, "minio", secrets.Credential{KeyID: f.KeyID, Secret: f.Secret}, false))
	for _, name := range []string{"cat", "catw"} {
		d := workspace.ConnectionDef{Name: name, Type: "iceberg_catalog", Options: map[string]string{
			"endpoint_type": "rest", "uri": f.RESTURI, "warehouse": icebergtest.Warehouse, "auth": "none"}}
		if name == "catw" {
			d.ReadOnly = ptr(false)
		}
		for _, o := range catOpts {
			for k, v := range o {
				d.Options[k] = v
			}
		}
		// A committed definition: iceberg_catalog is not own-scope-only.
		require.NoError(t, ws.AddConnection(workspace.ScopeShared, d))
	}
	sess, issues, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.Empty(t, issues)
	t.Cleanup(func() { _ = sess.Close() })
	for _, ext := range []string{"httpfs", "avro", "iceberg"} {
		require.NoError(t, sess.InstallExtension(ctx, ext))
	}
	sess.Preflight(ctx, "minio")
	require.NoError(t, sess.Connect(ctx, "minio"))
	return ws, sess
}

// The agent reads an Iceberg catalog like any other catalog, in the read-only
// tier: run_query, describe_table, introspect_schema and metric() all work, and
// a join with a local table is allowed.
func TestAgentReadsAnIcebergCatalog(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	ws, sess := icebergWorkspace(t, f)
	ctx := context.Background()
	h := agentOn(t, ws, sess, agents.ModeReadOnly, true)

	resp := sqlCall(h, "SELECT count(*) AS n, max(id) AS m FROM cat.ns.orders")
	require.Nil(t, resp.Error, errText(resp))
	rows, ok := resp.Result.(map[string]any)["rows"].([][]any)
	require.True(t, ok)
	assert.EqualValues(t, 300, rows[0][0])
	assert.EqualValues(t, 299, rows[0][1])

	// A scan that reads a column (not only metadata), a join, a CTE.
	for _, q := range []string{
		"SELECT region, sum(amount) FROM cat.ns.orders GROUP BY region ORDER BY region",
		"SELECT o.id FROM cat.ns.orders o JOIN cat.ns.events e ON e.id = o.id ORDER BY o.id",
		"WITH t AS (SELECT * FROM cat.ns.orders WHERE id < 5) SELECT max(amount) FROM t",
		"SELECT (SELECT max(id) FROM cat.ns.events) AS m",
	} {
		resp = sqlCall(h, q)
		require.Nil(t, resp.Error, "%s: %s", q, errText(resp))
	}
	// Every reference is one vetted ICEBERG_SCAN: a self join, and a person's
	// view or macro over the catalog (its body is screened and counted too).
	_, err := sess.DB().ExecContext(ctx, "CREATE VIEW orders_v AS SELECT * FROM cat.ns.orders")
	require.NoError(t, err)
	_, err = sess.DB().ExecContext(ctx, "CREATE MACRO orders_m() AS TABLE SELECT * FROM cat.ns.orders")
	require.NoError(t, err)
	for _, q := range []string{
		"SELECT count(*) FROM cat.ns.orders a JOIN cat.ns.orders b USING (id)",
		"SELECT count(*) FROM orders_v",
		"SELECT count(*) FROM orders_v a JOIN cat.ns.events e ON e.id = a.id",
		"SELECT count(*) FROM orders_m()",
		// Pruned scans (fewer ICEBERG_SCANs than references) still pass.
		"WITH unused AS (SELECT * FROM cat.ns.orders) SELECT 1",
		"SELECT * FROM cat.ns.orders LIMIT 0",
		"SELECT id FROM cat.ns.orders WHERE false",
		"SELECT count(*) FROM cat.ns.orders",
		"SELECT 1 WHERE EXISTS (SELECT * FROM cat.ns.orders WHERE false)",
		"SELECT id FROM cat.ns.orders UNION ALL SELECT id FROM cat.ns.events WHERE 1 = 0",
	} {
		resp = sqlCall(h, q)
		require.Nil(t, resp.Error, "%s: %s", q, errText(resp))
	}
	// A raw reader the statement does not account for is still refused: the
	// catalog reference does not pay for it (the plans of the two look alike).
	resp = sqlCall(h, "SELECT * FROM cat.ns.orders, iceberg_scan('/etc/passwd')")
	require.NotNil(t, resp.Error)
	_, err = sess.DB().ExecContext(ctx, "CREATE TABLE local_ids AS SELECT i AS id FROM range(10) t(i)")
	require.NoError(t, err)
	resp = sqlCall(h, "SELECT count(*) FROM local_ids l JOIN cat.ns.orders o ON o.id = l.id")
	require.Nil(t, resp.Error, errText(resp))

	desc := call(h, "describe_table", map[string]any{"table": "cat.ns.orders"})
	require.Nil(t, desc.Error, errText(desc))
	cols, ok := desc.Result.(map[string]any)["columns"].([]map[string]any)
	require.True(t, ok)
	var names []string
	for _, c := range cols {
		name, ok := c["name"].(string)
		require.True(t, ok)
		names = append(names, name)
	}
	assert.Equal(t, []string{"id", "amount", "region"}, names)

	intro := call(h, "introspect_schema", map[string]any{"connection": "cat"})
	require.Nil(t, intro.Error, errText(intro))

	// metric(): a saved query over the catalog, grouped by a dimension.
	acc := workspace.NewAgentAccess(ws, sess)
	require.NoError(t, acc.Queries().Create(ctx, &queries.Query{ID: "orders_base", Title: "orders", SQL: "SELECT region, amount FROM cat.ns.orders", Connection: "cat"}))
	m, err := semantics.DecodeMetric("order_amount", []byte("version: 1\nname: order_amount\nquery: orders_base\nmeasure: sum(amount)\ndimensions: [region]\n"))
	require.NoError(t, err)
	require.NoError(t, ws.Semantics().PutMetric(ctx, m))
	// run_query has no connection argument, so a metric defined on "cat" runs the
	// way a notebook cell, a watch or `slaw semantics check` runs it: on the catalog.
	mres, _, err := h.RunReadOnlySQLIn(ctx, "cat", "SELECT * FROM metric('order_amount', by => 'region') ORDER BY region", nil, 100)
	require.NoError(t, err)
	assert.Len(t, mres.Rows, 3)

	// A write is refused in the read-only tier.
	resp = sqlCall(h, "INSERT INTO catw.ns.events VALUES (100, 'x')")
	require.NotNil(t, resp.Error)
}

// The attach never asks the catalog for credentials: even a catalog that vends
// them in every LoadTable response creates no vended secret.
func TestAgentReadThroughAVendingCatalogCreatesNoVendedSecret(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	proxy := f.Proxy(t, icebergtest.ProxyOptions{Vended: f.VendedConfig()})
	ctx := context.Background()
	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "minio", Type: "s3", Scope: "s3://" + icebergtest.Warehouse + "/", Region: "us-east-1",
		Options: map[string]string{"endpoint": f.S3Endpoint, "url_style": "path", "use_ssl": "false"}}))
	require.NoError(t, ws.SetCredentials(ctx, "minio", secrets.Credential{KeyID: f.KeyID, Secret: f.Secret}, false))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "cat", Type: "iceberg_catalog", Options: map[string]string{
		"endpoint_type": "rest", "uri": proxy.URL, "warehouse": icebergtest.Warehouse, "auth": "none"}}))
	sess, _, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	for _, ext := range []string{"httpfs", "avro", "iceberg"} {
		require.NoError(t, sess.InstallExtension(ctx, ext))
	}
	sess.Preflight(ctx, "minio")
	require.NoError(t, sess.Connect(ctx, "minio"))
	h := agentOn(t, ws, sess, agents.ModeReadOnly, true)
	resp := sqlCall(h, "SELECT max(id) FROM cat.ns.orders")
	require.Nil(t, resp.Error, errText(resp))
	var n int
	require.NoError(t, sess.DB().QueryRowContext(ctx, "SELECT count(*) FROM duckdb_secrets() WHERE provider = 'iceberg'").Scan(&n))
	assert.Zero(t, n)
}
