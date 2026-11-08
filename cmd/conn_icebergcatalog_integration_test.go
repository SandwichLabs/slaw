//go:build integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
	"github.com/SandwichLabs/slaw/internal/testsupport/miniotest"
)

// L3 acceptance through the CLI, exactly as a person types it: the storage
// connection signs in from the environment (nothing is stored), the REST
// catalog is added with --option, and a column of its table is read.
func TestIcebergCatalogThroughTheCLI(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	proxy := f.Proxy(t, icebergtest.ProxyOptions{})
	miniotest.UseExtensionCache(t)

	root := newWS(t)
	t.Setenv("ICE_KEY_ID", f.KeyID)
	t.Setenv("ICE_SECRET", f.Secret)
	var all string
	run := func(args ...string) string {
		t.Helper()
		out, stderr, err := runSlaw(append(args, "--workspace", root)...)
		all += out + stderr
		require.NoError(t, err, stderr)
		return out
	}
	run("conn", "add", "minio", "--type", "s3", "--prefix", "s3://"+icebergtest.Warehouse+"/", "--region", "us-east-1", "--sign-in", "env:ICE",
		"--option", "endpoint="+f.S3Endpoint, "--option", "url_style=path", "--option", "use_ssl=false")
	run("conn", "add", "cat", "--type", "iceberg_catalog", "--option", "endpoint_type=rest", "--option", "auth=none",
		"--option", "uri="+proxy.URL, "--option", "warehouse="+icebergtest.Warehouse)

	list := run("conn", "list", "--json")
	assert.Regexp(t, `"name": "cat",[^}]*"status": "not-connected"`, list)

	out := run("query", "SELECT max(id) AS top FROM cat.ns.orders", "--format", "csv")
	assert.Contains(t, out, "299")

	// The catalog is read-only: a write is refused.
	_, stderr, err := runSlaw("query", "INSERT INTO cat.ns.orders VALUES (1, 1, 'x')", "--workspace", root)
	require.Error(t, err)
	assert.NotContains(t, err.Error()+stderr, f.Secret)

	// An entity over the catalog's table passes `slaw semantics check`: the agent's
	// read-only policy reads the catalog on the main hub like any other.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "semantics", "entities"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "semantics", "entities", "orders.yaml"), []byte(
		"version: 1\nname: orders\ndescription: Orders in the lakehouse.\ngrain: one row per order\ntable: cat.ns.orders\nconnection: cat\nid_column: id\n"+
			"assertions:\n  - {kind: unique, column: id}\n  - {kind: row_count, min_rows: 100}\n"), 0o644))
	cOut := run("semantics", "check", "--compile")
	assert.Contains(t, cOut, "compiles")
	assert.Contains(t, cOut, "1 passed, 0 failed, 0 could not run")
	rOut := run("semantics", "check")
	assert.Contains(t, rOut, "3 passed, 0 failed, 0 could not run", "compile, unique and row_count: %s", rOut)

	assert.NotContains(t, all+stderr, f.Secret)
}
