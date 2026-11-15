package lsp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteMerged_MakesTheLocalFolderIgnoreItself(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, writeMerged(root, &Catalog{}))
	got, err := os.ReadFile(filepath.Join(root, ".slaw-local", ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
}

func TestWriteMerged_LeavesAnExistingLocalGitignore(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, ".slaw-local")
	require.NoError(t, os.Mkdir(local, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(local, ".gitignore"), []byte("mine\n"), 0o600))
	require.NoError(t, writeMerged(root, &Catalog{}))
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(got))
}

func TestWriteMerged_RefusesASymlinkedLocalFolder(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(out, filepath.Join(root, ".slaw-local")))
	require.Error(t, writeMerged(root, &Catalog{}))
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
