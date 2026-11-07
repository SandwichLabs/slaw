//go:build unix && integration

package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/lsp"
	"github.com/SandwichLabs/slaw/internal/repl"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
	"github.com/SandwichLabs/slaw/internal/testsupport/miniotest"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// L3 acceptance: a person reads an Iceberg REST catalog's table end to end (the
// hub, the editor's expansion, the console), the listings are names only, and a
// signed-out storage connection is named.

var loadTableReq = regexp.MustCompile(`^GET /v1/(?:[^/]+/)?namespaces/[^/]+/tables/[^/]+$`)

func tableLoads(reqs []string) int {
	n := 0
	for _, r := range reqs {
		if loadTableReq.MatchString(r) {
			n++
		}
	}
	return n
}

type catalogWorld struct {
	root  string
	f     *icebergtest.Fixture
	proxy *icebergtest.Proxy
}

// newCatalogWorld seeds the fixture and writes a workspace the way a person
// adds it: an s3 connection for the warehouse bucket (env sign-in), a REST
// catalog "cat" through the request-logging proxy, a saved query and a metric
// over cat.ns.orders.
func newCatalogWorld(t *testing.T) *catalogWorld {
	t.Helper()
	f := icebergtest.Start(t)
	f.Seed(t)
	proxy := f.Proxy(t, icebergtest.ProxyOptions{})
	miniotest.UseExtensionCache(t)
	t.Setenv("ICE_KEY_ID", f.KeyID)
	t.Setenv("ICE_SECRET", f.Secret)

	cfg := t.TempDir()
	t.Setenv("SLAW_CONFIG_DIR", cfg)
	res, err := workspace.Init(filepath.Join(t.TempDir(), "acme"), workspace.WithInitLogger(quietLogger()))
	require.NoError(t, err)
	require.NoError(t, res.Workspace.Close())
	root := res.Workspace.Root()
	ws, err := workspace.Open(root, workspace.WithLogger(quietLogger()), workspace.WithConfigDir(cfg),
		workspace.WithUserConfig(workspace.UserConfig{Version: workspace.ConfigVersion}))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "minio", Type: "s3", Scope: "s3://" + icebergtest.Warehouse + "/", Region: "us-east-1",
		Options: map[string]string{"endpoint": f.S3Endpoint, "url_style": "path", "use_ssl": "false"}}))
	require.NoError(t, ws.SetSignIn("minio", "env:ICE"))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "cat", Type: "iceberg_catalog", ReadOnly: workspace.Bool(true),
		Options: map[string]string{"endpoint_type": "rest", "uri": proxy.URL, "warehouse": icebergtest.Warehouse, "auth": "none"}}))

	for path, body := range map[string]string{
		"queries/order-rows.sql":             "-- slaw: {\"title\":\"Order rows\"}\nSELECT id, amount, region FROM cat.ns.orders\n",
		"semantics/metrics/order_total.yaml": "version: 1\nname: order_total\ntitle: Order total\ndescription: Sum of order amounts.\nquery: order-rows\nmeasure: sum(amount)\ndimensions: [region]\n",
	} {
		p := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return &catalogWorld{root: root, f: f, proxy: proxy}
}

func scalarOf(t *testing.T, rs *engine.ResultSet) any {
	t.Helper()
	require.NotNil(t, rs)
	require.Equal(t, 1, rs.RowCount())
	return rs.Rows[0][0]
}

func TestPersonReadsAnIcebergRESTCatalogEndToEnd(t *testing.T) {
	w := newCatalogWorld(t)
	ctx := context.Background()
	o, err := Open(ctx, w.root, OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	defer o.Close()
	require.Empty(t, o.Issues)

	// The hub: a column is read (not count(*), which the manifest answers).
	rs, err := o.Eng.Execute(ctx, "SELECT max(id) FROM cat.ns.orders", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 299, scalarOf(t, rs))

	// The editor's expansion first, as the Query Explorer and `slaw query` do:
	// plain SQL is untouched and a metric over the catalog's table expands and runs.
	exp, err := workspace.ExpandForPerson(ctx, o.WS, o.Sess, "SELECT max(id) FROM cat.ns.orders", "", nil, workspace.PersonOptions{})
	require.NoError(t, err)
	assert.Equal(t, "SELECT max(id) FROM cat.ns.orders", exp.SQL)
	exp, err = workspace.ExpandForPerson(ctx, o.WS, o.Sess, "SELECT sum(order_total) AS t FROM metric('order_total', by => 'region')", "", nil, workspace.PersonOptions{})
	require.NoError(t, err)
	require.NotEqual(t, "SELECT sum(order_total) AS t FROM metric('order_total', by => 'region')", exp.SQL, "the metric was expanded")
	rs, err = o.Eng.Execute(ctx, exp.SQL, exp.Params)
	require.NoError(t, err)
	assert.EqualValues(t, 14850, scalarOf(t, rs))

	// The console engine.
	var out strings.Builder
	sw := &syncWriter{w: &out}
	cs, err := repl.New(ctx, sw, repl.Options{Engine: o.Eng, Sess: o.Sess, Workspace: "acme", Cols: 100, Rows: 24})
	require.NoError(t, err)
	defer cs.Close() //nolint:errcheck
	require.NoError(t, cs.Input([]byte("SELECT max(id) AS top FROM cat.ns.orders;\r")))
	require.Eventually(t, func() bool { return strings.Contains(sw.String(), "299") }, 30*time.Second, 50*time.Millisecond, sw.String())
}

// Listing is names only: the rail, completion and the snapshot make no
// LoadTable per table; a table's columns load when a person opens it.
func TestIcebergCatalogListingsMakeNoPerTableFetch(t *testing.T) {
	w := newCatalogWorld(t)
	ctx := context.Background()
	o, err := Open(ctx, w.root, OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	defer o.Close()
	require.NoError(t, o.Sess.Connect(ctx, "cat"))
	before := tableLoads(w.proxy.Requests())

	schemas, err := engine.ListCatalog(ctx, o.Sess, engine.CatalogRequest{Connection: "cat"})
	require.NoError(t, err)
	require.Equal(t, "schemas", schemas.Kind)
	require.Zero(t, schemas.More)
	var names []string
	for _, n := range schemas.Nodes {
		names = append(names, n.Name)
	}
	require.Contains(t, names, icebergtest.Namespace)
	tabs, err := engine.ListCatalog(ctx, o.Sess, engine.CatalogRequest{Connection: "cat", Schema: icebergtest.Namespace})
	require.NoError(t, err)
	var tnames []string
	for _, n := range tabs.Nodes {
		tnames = append(tnames, n.Name)
		assert.EqualValues(t, -1, n.Rows, "no statistics are read for a catalog's tables")
	}
	assert.ElementsMatch(t, []string{"orders", "events"}, tnames)

	cat, err := lsp.Build(ctx, o.Sess)
	require.NoError(t, err)
	conn := cat.Conn("cat")
	require.NotNil(t, conn)
	sch := conn.Schema(icebergtest.Namespace)
	require.NotNil(t, sch)
	require.Len(t, sch.Tables, 2)
	for _, r := range sch.Tables {
		assert.Empty(t, r.Columns, "completion knows the table, not its columns, until it is opened")
	}
	assert.Equal(t, before, tableLoads(w.proxy.Requests()), "no LoadTable during the listings: %v", w.proxy.Requests())

	// Opening one table loads it.
	cols, err := engine.ListCatalog(ctx, o.Sess, engine.CatalogRequest{Connection: "cat", Schema: icebergtest.Namespace, Table: "orders"})
	require.NoError(t, err)
	var cn []string
	for _, c := range cols.Nodes {
		cn = append(cn, c.Name)
	}
	assert.Equal(t, []string{"id", "amount", "region"}, cn)
	// DESCRIBE binds the table (DuckDB makes two GETs for it); the other is never loaded.
	var orders, events int
	for _, r := range w.proxy.Requests() {
		if !loadTableReq.MatchString(r) {
			continue
		}
		switch {
		case strings.HasSuffix(r, "/tables/orders"):
			orders++
		case strings.HasSuffix(r, "/tables/events"):
			events++
		}
	}
	assert.Positive(t, orders, "the table opened is loaded")
	assert.Zero(t, events, "a table nobody opened is never loaded: %v", w.proxy.Requests())
}

// A signed-out storage connection is named, not hidden: an s3_tables catalog
// that signs with it reports "needs the ... connection" before anything is dialled.
func TestS3TablesCatalogNamesItsSignedOutStorageConnection(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("SLAW_CONFIG_DIR", cfg)
	t.Setenv("NOPE_KEY_ID", "")
	t.Setenv("NOPE_SECRET", "")
	res, err := workspace.Init(filepath.Join(t.TempDir(), "acme"), workspace.WithInitLogger(quietLogger()))
	require.NoError(t, err)
	require.NoError(t, res.Workspace.Close())
	ws, err := workspace.Open(res.Workspace.Root(), workspace.WithLogger(quietLogger()), workspace.WithConfigDir(cfg),
		workspace.WithUserConfig(workspace.UserConfig{Version: workspace.ConfigVersion}))
	require.NoError(t, err)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "aws_lake", Type: "s3", Scope: "s3://lake/", Region: "us-east-1"}))
	require.NoError(t, ws.SetSignIn("aws_lake", "env:NOPE"))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "s3t", Type: "iceberg_catalog", ReadOnly: workspace.Bool(true),
		Options: map[string]string{"endpoint_type": "s3_tables", "warehouse": "arn:aws:s3tables:us-east-1:111122223333:bucket/sales", "storage": "aws_lake"}}))
	require.NoError(t, ws.Close())

	ctx := context.Background()
	o, err := Open(ctx, res.Workspace.Root(), OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	defer o.Close()
	require.Empty(t, o.Issues)
	err = o.Sess.Connect(ctx, "s3t")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `needs the "aws_lake" connection`)
	st := statusLine(o.Sess, "s3t")
	assert.Equal(t, session.StatusError, st.Kind)
	assert.Contains(t, st.Message, `needs the "aws_lake" connection`)
	assert.NotContains(t, fmt.Sprint(st), "111122223333", "the status names the requirement, not the catalog's ARN")
}

// syncWriter is a goroutine-safe io.Writer over a strings.Builder.
type syncWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}
