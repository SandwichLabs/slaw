package receipts

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadKey_CreatesOnceWith0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	k1, err := LoadKey(dir)
	require.NoError(t, err)
	assert.Len(t, k1, KeySize)
	fi, err := os.Stat(filepath.Join(dir, KeyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	k2, err := LoadKey(dir)
	require.NoError(t, err)
	assert.Equal(t, k1, k2)

	other, err := LoadKey(filepath.Join(t.TempDir(), "receipts"))
	require.NoError(t, err)
	assert.NotEqual(t, k1, other, "each workspace has its own key")
}

func TestLoadKey_ConcurrentFirstUseAgrees(t *testing.T) {
	dir := t.TempDir()
	keys := make([][]byte, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := LoadKey(dir)
			if err == nil {
				keys[i] = k
			}
		}()
	}
	wg.Wait()
	for _, k := range keys {
		require.Len(t, k, KeySize)
		assert.Equal(t, keys[0], k)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file is left behind")
}

func TestLoadKey_RefusesAWideModeAndAWrongSize(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o700} {
		dir := t.TempDir()
		_, err := LoadKey(dir)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(filepath.Join(dir, KeyFile), mode))
		_, err = LoadKey(dir)
		require.Error(t, err, mode.String())
		assert.Contains(t, err.Error(), "wider than 0600")
	}
	dir := t.TempDir()
	_, err := LoadKey(dir)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Join(dir, KeyFile), 0o400))
	_, err = LoadKey(dir)
	require.NoError(t, err, "0400 is not wider than 0600")

	for name, content := range map[string][]byte{"short": make([]byte, 16), "long": make([]byte, 64), "empty": nil} {
		d := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(d, KeyFile), content, 0o600))
		_, err := LoadKey(d)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "32 bytes")
	}
}

func TestLoadKey_RefusesALinkAndANonFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(target, make([]byte, KeySize), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, KeyFile)))
	_, err := LoadKey(dir)
	require.Error(t, err)

	d := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(d, KeyFile), 0o700))
	_, err = LoadKey(d)
	require.Error(t, err)
}
