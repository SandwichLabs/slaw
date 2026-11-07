//go:build integration

package agents_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// A catalog that stalls on LoadTable: a context cancel does not interrupt the
// request (S0 item 11), so the hub's http_timeout is the only bound. The read
// returns an error within it, and so does a watch check that is canceled.
func TestAHungCatalogFailsWithinTheHTTPTimeout(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	proxy := f.Proxy(t, icebergtest.ProxyOptions{HangLoadTable: true})
	ctx := context.Background()
	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "minio", Type: "s3", Scope: "s3://" + icebergtest.Warehouse + "/", Region: "us-east-1",
		Options: map[string]string{"endpoint": f.S3Endpoint, "url_style": "path", "use_ssl": "false"}}))
	require.NoError(t, ws.SetCredentials(ctx, "minio", secrets.Credential{KeyID: f.KeyID, Secret: f.Secret}, false))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "cat", Type: "iceberg_catalog", Options: map[string]string{
		"endpoint_type": "rest", "uri": proxy.URL, "warehouse": icebergtest.Warehouse, "auth": "none"}}))
	// f.Seed installed httpfs into the shared extension directory, so it is
	// loaded (and the limits set, read back and locked) while the session opens.
	ext := extDir(t)
	sess, _, err := workspace.OpenSession(ctx, ws, nil,
		workspace.WithSessionOptions(session.WithExtensionDirectory(ext), session.WithHTTPLimits(1, 1)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	for _, e := range []string{"httpfs", "avro", "iceberg"} {
		require.NoError(t, sess.InstallExtension(ctx, e))
	}
	sess.Preflight(ctx, "minio")
	require.NoError(t, sess.Connect(ctx, "minio"))
	h := agentOn(t, ws, sess, agents.ModeReadOnly, true)

	// Names alone do not need the catalog's table: the attach worked.
	start := time.Now()
	resp := sqlCall(h, "SELECT max(id) FROM cat.ns.orders")
	require.NotNil(t, resp.Error, "the hung LoadTable is an error, not a hang")
	assert.Less(t, time.Since(start), 30*time.Second)
	t.Logf("hung read failed after %s: %s", time.Since(start).Round(time.Millisecond), resp.Error.Message)

	// A watch check canceled while the catalog hangs returns once the timeout fires.
	require.NoError(t, ws.Queries().Create(ctx, &queries.Query{ID: "mx", Title: "max", SQL: "SELECT max(id) AS m FROM cat.ns.orders"}))
	_, err = ws.Watches().PutSource(ctx, "w", []byte("question: {query: mx}\nwhen: {above: 1}\n"))
	require.NoError(t, err)
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(300*time.Millisecond, cancel)
	start = time.Now()
	res, err := workspace.CheckWatches(cctx, ws, nil, h, time.Now())
	assert.Less(t, time.Since(start), 30*time.Second, "bounded by http_timeout")
	require.Error(t, err, "the canceled check says so")
	require.Len(t, res, 1)
	assert.True(t, res[0].Interrupted)
}
