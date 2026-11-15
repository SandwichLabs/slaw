package watches

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileHistory_AppendMakesTheLocalFolderIgnoreItself(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, NewFileHistory(root).Append(context.Background(), entry("w", 0, StatusOK)))
	got, err := os.ReadFile(filepath.Join(root, ".slaw-local", ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
}

func TestFileHistory_AppendLeavesAnExistingLocalGitignore(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, ".slaw-local")
	require.NoError(t, os.Mkdir(local, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(local, ".gitignore"), []byte("mine\n"), 0o600))
	require.NoError(t, NewFileHistory(root).Append(context.Background(), entry("w", 0, StatusOK)))
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(got))
}

func TestFileHistory_AppendRefusesASymlinkedLocalFolder(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(out, filepath.Join(root, ".slaw-local")))
	require.Error(t, NewFileHistory(root).Append(context.Background(), entry("w", 0, StatusOK)))
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
