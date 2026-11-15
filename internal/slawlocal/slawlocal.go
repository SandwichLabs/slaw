// Package slawlocal makes sure a workspace's machine-local folder,
// <root>/.slaw-local/, exists and ignores itself. It is a leaf (standard
// library only) so every writer under that folder can call it before its first
// write: receipts, watch history, proposals, page state, console history, the
// catalog snapshot, editor scratch buffers, the workspace state and nonce and
// the hub lock.
//
// `slaw workspace init` adds .slaw-local/ to the root .gitignore, but a repo
// made any other way has no such line, and the folder can hold a receipt key
// and statement text. A "*" .gitignore inside the folder closes that gap.
package slawlocal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Dir is the folder's name under a workspace root.
const Dir = ".slaw-local"

const (
	dirMode    = 0o700
	ignoreName = ".gitignore"
)

// Ensure returns the machine-local folder of the workspace at root
// (<root>/.slaw-local), creating it at 0700 when missing and writing its
// .gitignore ("*") when that is missing. The folder may come from a clone, so a
// symbolic link or a non-directory at the folder is refused, never followed. An
// existing .gitignore, whatever it holds, is left alone (O_EXCL never follows a
// link). root is trusted (the workspace the person opened). When the ignore
// file cannot be written the error is returned and the caller must not write
// into the folder.
func Ensure(root string) (string, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("the workspace folder cannot be resolved")
	}
	local := filepath.Join(real, Dir)
	fi, err := os.Lstat(local)
	if errors.Is(err, fs.ErrNotExist) {
		// Another process may make it first; what is there is checked below.
		if merr := os.Mkdir(local, dirMode); merr != nil && !errors.Is(merr, fs.ErrExist) {
			return "", fmt.Errorf("the local folder cannot be created: %w", merr)
		}
		fi, err = os.Lstat(local)
	}
	switch {
	case err != nil:
		return "", fmt.Errorf("the local folder cannot be read: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
		return "", errors.New("the local folder is a symbolic link or not a folder")
	}
	if err := writeIgnore(local); err != nil {
		return "", err
	}
	return local, nil
}

// fillIgnore writes the ignore file's content; a seam for the failure test.
var fillIgnore = func(f *os.File) error {
	_, err := f.WriteString("*\n")
	return err
}

// writeIgnore publishes .gitignore whole or not at all: the content goes to a
// temp file in the folder, which is hard-linked to .gitignore (EEXIST means
// another writer, or the person, got there first). A failed write can never
// leave an empty .gitignore that is trusted from then on.
func writeIgnore(local string) error {
	final := filepath.Join(local, ignoreName)
	if _, err := os.Lstat(final); err == nil {
		return nil
	}
	fail := func(err error) error {
		return fmt.Errorf("the local folder's .gitignore cannot be written: %w", err)
	}
	f, err := os.CreateTemp(local, ".gitignore-tmp-*")
	if err != nil {
		return fail(err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) //nolint:errcheck
	werr := fillIgnore(f)
	if werr == nil {
		werr = f.Chmod(0o644)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fail(werr)
	}
	err = linkIgnore(tmp, final)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return nil
	}
	// No hard links here (vfat, exFAT, some network mounts): create it directly,
	// and never leave a partial one.
	return createIgnore(final, fail)
}

// linkIgnore publishes the temp file as .gitignore; a seam for the tests.
var linkIgnore = os.Link

func createIgnore(final string, fail func(error) error) error {
	f, err := os.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fail(err)
	}
	werr := fillIgnore(f)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(final)
		return fail(werr)
	}
	return nil
}
