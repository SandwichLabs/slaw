package receipts

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A hand-made repo has no root .gitignore lines from `slaw workspace init`, so
// the receipts folder makes .slaw-local ignore itself.
func TestDir_WritesALocalGitignoreWhenMissing(t *testing.T) {
	root := t.TempDir()
	_, err := Dir(root)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(root, ".slaw-local", ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
}

func TestDir_NeverOverwritesAnExistingLocalGitignore(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, ".slaw-local")
	require.NoError(t, os.Mkdir(local, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(local, ".gitignore"), []byte("mine\n"), 0o600))
	_, err := Dir(root)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(got))
}

// If the .gitignore can't be written, receipts stay off rather than keep a key
// and statements where git could pick them up.
func TestDir_RefusesWhenTheLocalGitignoreCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix directory permissions as a normal user")
	}
	root := t.TempDir()
	local := filepath.Join(root, ".slaw-local")
	require.NoError(t, os.MkdirAll(filepath.Join(local, "receipts"), 0o700))
	require.NoError(t, os.Chmod(local, 0o500))
	t.Cleanup(func() { _ = os.Chmod(local, 0o700) })
	_, err := Dir(root)
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(local, ".gitignore"))
	assert.True(t, os.IsNotExist(statErr))
}
