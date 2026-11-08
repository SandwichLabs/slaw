package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An env sign-in hint names only the variables the catalog's kind reads.
func TestConnAdd_IcebergCatalogEnvHintNamesOnlyItsVariables(t *testing.T) {
	root := newWS(t)
	out, stderr, err := runSlaw("conn", "add", "nessie", "--type", "iceberg_catalog", "--option", "endpoint_type=rest", "--option", "auth=bearer",
		"--option", "uri=https://nessie.example.com/iceberg", "--option", "warehouse=main", "--sign-in", "env:BEAR", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "BEAR_TOKEN")
	assert.NotContains(t, out, "BEAR_USER")
	assert.NotContains(t, out, "BEAR_PASSWORD")
	assert.NotContains(t, out, "BEAR_KEY_ID")

	out, stderr, err = runSlaw("conn", "credentials", "set", "nessie", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "BEAR_TOKEN")
	assert.NotContains(t, out, "BEAR_USER")

	out, stderr, err = runSlaw("conn", "add", "polaris", "--type", "iceberg_catalog", "--option", "endpoint_type=rest",
		"--option", "uri=https://polaris.example.com/api/catalog", "--option", "warehouse=a", "--sign-in", "env:POL", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "POL_KEY_ID")
	assert.Contains(t, out, "POL_SECRET")
	assert.NotContains(t, out, "POL_TOKEN")
	assert.NotContains(t, out, "POL_USER")

	// The status detail for unset variables says the same.
	st, _, err := runSlaw("conn", "credentials", "status", "nessie", "--workspace", root)
	require.NoError(t, err)
	assert.Contains(t, st, "BEAR_TOKEN")
	assert.NotContains(t, st, "BEAR_USER")
}
