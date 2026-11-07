//go:build unix && integration

package hub

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
	"github.com/SandwichLabs/slaw/internal/testsupport/miniotest"
	"github.com/SandwichLabs/slaw/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// icebergMCPWorkspace writes the workspace a person would: an s3 connection to
// the fixture's MinIO (signed in from the environment, nothing stored), a REST
// Iceberg catalog "cat", a saved query over it and the metric order_total.
func icebergMCPWorkspace(t *testing.T, f *icebergtest.Fixture) (root, cfg string) {
	t.Helper()
	cfg = t.TempDir()
	t.Setenv("SLAW_CONFIG_DIR", cfg)
	res, err := workspace.Init(filepath.Join(t.TempDir(), "acme"), workspace.WithInitLogger(quietLogger()))
	require.NoError(t, err)
	require.NoError(t, res.Workspace.Close())
	ws, err := workspace.Open(res.Workspace.Root(), workspace.WithLogger(quietLogger()), workspace.WithConfigDir(cfg),
		workspace.WithUserConfig(workspace.UserConfig{Version: workspace.ConfigVersion}))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "minio", Type: "s3", Scope: "s3://" + icebergtest.Warehouse + "/", Region: "us-east-1",
		Options: map[string]string{"endpoint": f.S3Endpoint, "url_style": "path", "use_ssl": "false"}}))
	require.NoError(t, ws.SetSignIn("minio", "env:MINIO"))
	require.NoError(t, ws.AddConnection(workspace.ScopeShared, workspace.ConnectionDef{
		Name: "cat", Type: "iceberg_catalog", Options: map[string]string{
			"endpoint_type": "rest", "uri": f.RESTURI, "warehouse": icebergtest.Warehouse, "auth": "none"}}))
	ctx := context.Background()
	// Three-part names on the default connection: run_query has no connection
	// argument, and a metric runs only where its saved query lives.
	require.NoError(t, ws.Queries().Create(ctx, &queries.Query{ID: "orders_base", Title: "orders",
		SQL: "SELECT region, amount FROM cat.ns.orders"}))
	m, err := semantics.DecodeMetric("order_total", []byte("version: 1\nname: order_total\nquery: orders_base\nmeasure: sum(amount)\ndimensions: [region]\n"))
	require.NoError(t, err)
	require.NoError(t, ws.Semantics().PutMetric(ctx, m))
	return res.Workspace.Root(), cfg
}

// `slaw mcp serve` over an Iceberg catalog: in the read-only tier an external
// agent reads it with run_query, describe_table, introspect_schema and
// metric(), gets the rows the person's path gets, and cannot write.
func TestIcebergCatalogOverMCPServe(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	miniotest.UseExtensionCache(t)
	root, cfg := icebergMCPWorkspace(t, f)
	t.Setenv("MINIO_KEY_ID", f.KeyID)
	t.Setenv("MINIO_SECRET", f.Secret)
	ctx := context.Background()

	// The person's path: the same hub the CLI opens, then released for serve.
	o, err := Open(ctx, root, OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	personRows := func(q string) string {
		exp, err := workspace.ExpandForPerson(ctx, o.WS, o.Sess, q, "", nil, workspace.PersonOptions{})
		require.NoError(t, err, q)
		rs, err := o.Eng.Execute(ctx, exp.SQL, exp.Params)
		require.NoError(t, err, q)
		var cells []string
		for _, r := range rs.Rows {
			for _, c := range r {
				cells = append(cells, fmt.Sprint(c))
			}
		}
		return strings.Join(cells, "|")
	}
	maxQ := "SELECT max(id) AS m FROM cat.ns.orders"
	metricQ := "SELECT * FROM metric('order_total', by => 'region') ORDER BY region"
	wantMax := personRows(maxQ)
	wantMetric := personRows(metricQ)
	o.Close()
	require.Equal(t, "299", wantMax)
	require.Len(t, strings.Split(wantMetric, "|"), 6, "three regions, two columns")

	run := startServe(t, ServeOptions{Workspace: root})
	run.handshake()
	var all []string

	text, isErr := run.tool("run_query", map[string]any{"sql": maxQ})
	require.False(t, isErr, text)
	assert.Contains(t, text, wantMax)
	all = append(all, text)

	text, isErr = run.tool("run_query", map[string]any{"sql": metricQ})
	require.False(t, isErr, text)
	for _, cell := range strings.Split(wantMetric, "|") {
		assert.Contains(t, text, cell, "the metric's rows are the person's rows")
	}
	all = append(all, text)

	text, isErr = run.tool("describe_table", map[string]any{"table": "cat.ns.orders"})
	require.False(t, isErr, text)
	for _, col := range []string{"id", "amount", "region"} {
		assert.Contains(t, text, col)
	}
	all = append(all, text)

	text, isErr = run.tool("introspect_schema", map[string]any{"connection": "cat"})
	require.False(t, isErr, text)
	assert.Contains(t, text, "orders")
	all = append(all, text)

	for _, q := range []string{
		"INSERT INTO cat.ns.events VALUES (100, 'x')",
		"DELETE FROM cat.ns.orders",
		"CREATE TABLE cat.ns.t AS SELECT 1",
	} {
		text, isErr = run.tool("run_query", map[string]any{"sql": q})
		assert.True(t, isErr, q)
		all = append(all, text)
	}
	text, isErr = run.tool("run_query", map[string]any{"sql": "SELECT count(*) AS n FROM cat.ns.events"})
	require.False(t, isErr, text)
	assert.Contains(t, text, "10", "the refused writes did not run")

	for _, s := range append(all, run.stderr.String()) {
		assert.False(t, strings.Contains(s, f.Secret), "the storage secret reached the MCP client or stderr")
	}
	noNeedle(t, root, cfg)
}
