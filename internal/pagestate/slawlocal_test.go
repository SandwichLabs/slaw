package pagestate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func flushOne(t *testing.T, root string) error {
	t.Helper()
	s, err := Open(root, "pg", WithDelay(time.Hour))
	require.NoError(t, err)
	if err := s.Set("a", raw(`1`), nil); err != nil {
		return err // a linked folder is already refused on the first read
	}
	return s.Flush()
}

func TestStore_FlushMakesTheLocalFolderIgnoreItself(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, flushOne(t, root))
	got, err := os.ReadFile(filepath.Join(root, ".slaw-local", ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
}

func TestStore_FlushLeavesAnExistingLocalGitignore(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, ".slaw-local")
	require.NoError(t, os.Mkdir(local, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(local, ".gitignore"), []byte("mine\n"), 0o600))
	require.NoError(t, flushOne(t, root))
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(got))
}

func TestStore_FlushRefusesASymlinkedLocalFolder(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(out, filepath.Join(root, ".slaw-local")))
	require.Error(t, flushOne(t, root))
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
