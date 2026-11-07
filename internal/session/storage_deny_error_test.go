package session

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDenyErrorText(t *testing.T) {
	const want = "table data at s3://other-bucket/ is outside every storage connection's scope: add one whose scope contains it"
	for name, in := range map[string]string{
		"virtual host": "IO Error: Could not resolve hostname error for HTTP GET to 'https://other-bucket.deny.invalid/data/00000-a.parquet' (HTTP 0)",
		"path style":   "IO Error: Could not resolve hostname error for HTTP GET to 'https://deny.invalid/other-bucket/data/00000-a.parquet'",
		"dotted":       "HTTP GET to 'https://other-bucket.deny.invalid/k?x=1'",
	} {
		got, ok := DenyErrorText(in)
		require.True(t, ok, name)
		assert.Equal(t, want, got, name)
	}
	got, ok := DenyErrorText("HTTP GET to 'https://my.dotted.bucket.deny.invalid/k'")
	require.True(t, ok)
	assert.Contains(t, got, "s3://my.dotted.bucket/")

	_, ok = DenyErrorText("IO Error: Could not resolve hostname error for HTTP GET to 'https://s3.example.com/b/k'")
	assert.False(t, ok)
	_, ok = DenyErrorText("Catalog Error: Table with name x does not exist")
	assert.False(t, ok)
}

func TestMapStorageDenyErrorNeverShowsTheRawError(t *testing.T) {
	raw := errors.New("failed: HTTP GET to 'https://b.deny.invalid/secret/key.parquet' SELECT max(secret_col)")
	err := MapStorageDenyError(raw)
	var de *StorageDenyError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "b", de.Bucket)
	assert.NotContains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "key.parquet")
	assert.ErrorIs(t, err, raw)
	other := errors.New("boom")
	assert.Same(t, other, MapStorageDenyError(other))
	assert.NoError(t, MapStorageDenyError(nil))
}
