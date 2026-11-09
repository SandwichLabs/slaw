package receipts

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keyPath(root string) string { return filepath.Join(recDir(root), KeyFile) }

func TestLoadKey_CreatesOnceWith0600(t *testing.T) {
	root := t.TempDir()
	k1, err := LoadKey(root)
	require.NoError(t, err)
	assert.Len(t, k1, KeySize)
	fi, err := os.Stat(keyPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(recDir(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	k2, err := LoadKey(root)
	require.NoError(t, err)
	assert.Equal(t, k1, k2)

	other, err := LoadKey(t.TempDir())
	require.NoError(t, err)
	assert.NotEqual(t, k1, other, "each workspace has its own key")
}

func TestLoadKey_ConcurrentFirstUseAgrees(t *testing.T) {
	root := t.TempDir()
	keys := make([][]byte, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := LoadKey(root)
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
	entries, err := os.ReadDir(recDir(root))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file is left behind")
}

func TestLoadKey_RefusesAWideModeAndAWrongSize(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o700} {
		root := t.TempDir()
		_, err := LoadKey(root)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(keyPath(root), mode))
		_, err = LoadKey(root)
		require.Error(t, err, mode.String())
		assert.Contains(t, err.Error(), "wider than 0600")
	}
	root := t.TempDir()
	_, err := LoadKey(root)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(keyPath(root), 0o400))
	_, err = LoadKey(root)
	require.NoError(t, err, "0400 is not wider than 0600")

	for name, content := range map[string][]byte{"short": make([]byte, 16), "long": make([]byte, 64), "empty": nil} {
		r := t.TempDir()
		_, err := Dir(r)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(keyPath(r), content, 0o600))
		_, err = LoadKey(r)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "32 bytes")
	}
}

func TestLoadKey_RefusesASymlinkedKeyAndANonFile(t *testing.T) {
	root := t.TempDir()
	_, err := Dir(root)
	require.NoError(t, err)
	target := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(target, make([]byte, KeySize), 0o600))
	require.NoError(t, os.Symlink(target, keyPath(root)))
	_, err = LoadKey(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbolic link")

	r := t.TempDir()
	_, err = Dir(r)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(keyPath(r), 0o700))
	_, err = LoadKey(r)
	require.Error(t, err)
}

// The workspace may be a clone: a committed link at .slaw-local or at receipts
// must not carry the key or the receipts out of the machine-local folder.
func TestDir_RefusesSymlinkedFolders(t *testing.T) {
	outside := t.TempDir()

	root := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, ".slaw-local")))
	_, err := LoadKey(root)
	require.Error(t, err)
	_, err = NewFileStore(root, DefaultPolicy())
	require.Error(t, err)

	root = t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".slaw-local"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, ".slaw-local", "receipts")))
	_, err = LoadKey(root)
	require.Error(t, err)
	_, err = NewFileStore(root, DefaultPolicy())
	require.Error(t, err)

	root = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".slaw-local"), nil, 0o600))
	_, err = Dir(root)
	require.Error(t, err, "a file where the folder should be")

	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written through a link")
}

func TestDir_TightensAnExistingFolder(t *testing.T) {
	root := t.TempDir()
	d := recDir(root)
	require.NoError(t, os.MkdirAll(d, 0o755))
	_, err := Dir(root)
	require.NoError(t, err)
	fi, err := os.Stat(d)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}

func TestFileStore_PutRefusesALinkedShardFolder(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root, DefaultPolicy())
	r := sealed(t, 1)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(recDir(root), r.ID[2:4])))
	require.Error(t, s.Put(r))
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
