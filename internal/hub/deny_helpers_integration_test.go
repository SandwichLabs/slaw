//go:build unix && integration

package hub

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

// requireDenied asserts that err is a read refused by a deny secret for bucket.
// An error that already went through the engine is a *session.StorageDenyError;
// one read straight from DuckDB is mapped here, which also proves the mapping
// matches DuckDB's real message.
func requireDenied(t *testing.T, err error, bucket string) {
	t.Helper()
	var de *session.StorageDenyError
	if !errors.As(err, &de) {
		require.ErrorAs(t, session.MapStorageDenyError(err), &de, "not a read refused by a deny secret: %v", err)
	}
	assert.Equal(t, bucket, de.Bucket)
	assert.NotContains(t, de.Error(), "deny.invalid")
}
