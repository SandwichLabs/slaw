package hub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireHub_MakesTheLocalFolderIgnoreItself(t *testing.T) {
	ws := newWorkspace(t)
	ignore := filepath.Join(ws.Root(), ".slaw-local", ".gitignore")
	require.NoError(t, os.Remove(ignore))
	hold, err := AcquireHub(ws, "Slaw")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hold.Release() })
	got, err := os.ReadFile(ignore)
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
}
