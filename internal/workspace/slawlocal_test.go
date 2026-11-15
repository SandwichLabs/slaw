package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SaveState and the folder nonce write under .slaw-local/ too; a workspace that
// lost its .gitignore gets it back on the next write, and one that has its own
// keeps it.
func TestSaveState_MakesTheLocalFolderIgnoreItself(t *testing.T) {
	ws, root := newWorkspace(t)
	ignore := filepath.Join(root, ".slaw-local", ".gitignore")
	require.NoError(t, os.Remove(ignore))
	require.NoError(t, ws.SaveState(State{LastNotebook: "a"}))
	got, err := os.ReadFile(ignore)
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
}

func TestSaveState_LeavesAnExistingLocalGitignore(t *testing.T) {
	ws, root := newWorkspace(t)
	ignore := filepath.Join(root, ".slaw-local", ".gitignore")
	require.NoError(t, os.WriteFile(ignore, []byte("mine\n"), 0o600))
	require.NoError(t, ws.SaveState(State{LastNotebook: "a"}))
	got, err := os.ReadFile(ignore)
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(got))
}
