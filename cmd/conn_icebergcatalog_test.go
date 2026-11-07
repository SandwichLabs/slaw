package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The kinds of catalog are added from the command line, and `credentials set`
// asks only for what the kind signs in with.
func TestConnAdd_IcebergCatalogKinds(t *testing.T) {
	root := newWS(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_, stderr, err := runSlaw("conn", "add", "aws_lake", "--type", "s3", "--prefix", "s3://acme-lake/", "--region", "us-east-1", "--workspace", root)
	require.NoError(t, err, stderr)

	out, stderr, err := runSlaw("conn", "add", "s3t", "--type", "iceberg_catalog",
		"--option", "endpoint_type=s3_tables", "--option", "warehouse=arn:aws:s3tables:us-east-1:111122223333:bucket/sales",
		"--option", "storage=aws_lake", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Added iceberg_catalog connection s3t")

	// s3_tables signs with its storage connection: nothing to set.
	_, _, err = runSlaw("conn", "credentials", "set", "s3t", "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials")

	_, stderr, err = runSlaw("conn", "add", "nessie", "--type", "iceberg_catalog", "--option", "endpoint_type=rest", "--option", "auth=bearer",
		"--option", "uri=https://nessie.example.com/iceberg", "--option", "warehouse=main", "--workspace", root)
	require.NoError(t, err, stderr)
	_, stderr, err = runSlaw("conn", "add", "polaris", "--type", "iceberg_catalog", "--option", "endpoint_type=rest",
		"--option", "uri=https://polaris.example.com/api/catalog", "--option", "warehouse=analytics", "--workspace", root)
	require.NoError(t, err, stderr)
	_, stderr, err = runSlaw("conn", "add", "open", "--type", "iceberg_catalog", "--option", "endpoint_type=rest", "--option", "auth=none",
		"--option", "uri=http://localhost:8181", "--option", "warehouse=w", "--workspace", root)
	require.NoError(t, err, stderr)
	_, _, err = runSlaw("conn", "credentials", "set", "open", "--workspace", root)
	require.Error(t, err)

	// A bearer catalog takes the token only: a second line is not a field.
	_, stderr, err = runSlawIn(strings.NewReader("tok-needle\nextra\n"), "conn", "credentials", "set", "nessie", "--sign-in", "prompt", "--workspace", root)
	require.NoError(t, err, stderr)
	st, _, err := runSlaw("conn", "credentials", "status", "nessie", "--workspace", root)
	require.NoError(t, err)
	assert.NotContains(t, st, "key id:")
	assert.NotContains(t, st+stderr, "tok-needle")

	// An OAuth2 catalog takes the client id and secret, in that order.
	_, stderr, err = runSlawIn(strings.NewReader("cid\nsecret-needle\n"), "conn", "credentials", "set", "polaris", "--workspace", root)
	require.NoError(t, err, stderr)
	st, _, err = runSlaw("conn", "credentials", "status", "polaris", "--workspace", root)
	require.NoError(t, err)
	assert.Contains(t, st, "key id:        cid")
	assert.NotContains(t, st+stderr, "secret-needle")

	// Glue is refused.
	_, _, err = runSlaw("conn", "add", "g", "--type", "iceberg_catalog", "--option", "endpoint_type=glue", "--workspace", root)
	require.Error(t, err)

	help, _, err := runSlaw("conn", "add", "--help")
	require.NoError(t, err)
	for _, want := range []string{"endpoint_type=s3_tables", "endpoint_type=rest --option auth=bearer", "auth=none", "OAuth2"} {
		assert.Contains(t, help, want)
	}
}
