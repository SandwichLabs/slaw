package slawlocal

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsure_CreatesTheFolderWithAnIgnoreFile(t *testing.T) {
	root := t.TempDir()
	local, err := Ensure(root)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
	_, err = Ensure(root) // again: no error
	require.NoError(t, err)
}

func TestEnsure_NeverOverwritesAnExistingIgnoreFile(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, Dir)
	require.NoError(t, os.Mkdir(local, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(local, ".gitignore"), []byte("mine\n"), 0o600))
	_, err := Ensure(root)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "mine\n", string(got))
}

func TestEnsure_RefusesASymlinkedFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root, out := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(out, filepath.Join(root, Dir)))
	_, err := Ensure(root)
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(out, ".gitignore"))
	assert.True(t, os.IsNotExist(statErr), "nothing is written through the link")
}

func TestEnsure_RefusesAFileInThePlaceOfTheFolder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, Dir), []byte("x"), 0o600))
	_, err := Ensure(root)
	require.Error(t, err)
}

func TestEnsure_ErrorsWhenTheIgnoreFileCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix directory permissions as a normal user")
	}
	root := t.TempDir()
	local := filepath.Join(root, Dir)
	require.NoError(t, os.Mkdir(local, 0o500))
	t.Cleanup(func() { _ = os.Chmod(local, 0o700) })
	_, err := Ensure(root)
	require.Error(t, err)
}

// A write that fails halfway must leave neither a .gitignore nor a temp file:
// an empty .gitignore would be trusted from then on.
func TestEnsure_AFailedWriteLeavesNoIgnoreFile(t *testing.T) {
	old := fillIgnore
	fillIgnore = func(*os.File) error { return errors.New("disk full") }
	t.Cleanup(func() { fillIgnore = old })
	root := t.TempDir()
	_, err := Ensure(root)
	require.Error(t, err)
	entries, rerr := os.ReadDir(filepath.Join(root, Dir))
	require.NoError(t, rerr)
	assert.Empty(t, entries)

	fillIgnore = old
	local, err := Ensure(root) // the next call recovers
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
	entries, _ = os.ReadDir(local)
	assert.Len(t, entries, 1, "no temp file is left behind")
}

// A filesystem without hard links still gets its .gitignore, whole.
func TestEnsure_FallsBackWhenHardLinksAreUnsupported(t *testing.T) {
	old := linkIgnore
	linkIgnore = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} }
	t.Cleanup(func() { linkIgnore = old })
	root := t.TempDir()
	local, err := Ensure(root)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(local, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(got))
	entries, err := os.ReadDir(local)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file is left behind")
}

func TestEnsure_FallbackFailureLeavesNoEmptyIgnoreFile(t *testing.T) {
	oldLink, oldFill := linkIgnore, fillIgnore
	linkIgnore = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} }
	calls := 0
	fillIgnore = func(f *os.File) error {
		calls++
		if calls == 1 {
			return oldFill(f) // the temp file
		}
		return errors.New("disk full")
	}
	t.Cleanup(func() { linkIgnore, fillIgnore = oldLink, oldFill })
	root := t.TempDir()
	_, err := Ensure(root)
	require.Error(t, err)
	entries, rerr := os.ReadDir(filepath.Join(root, Dir))
	require.NoError(t, rerr)
	assert.Empty(t, entries)
}
