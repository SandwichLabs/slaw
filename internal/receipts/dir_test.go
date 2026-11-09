package receipts

import (
	"os"
	"path/filepath"
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
