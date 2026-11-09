package receipts

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// KeyFile is the key's name inside the receipts folder.
const KeyFile = "key"

const (
	dirMode  = 0o700
	fileMode = 0o600
)

// LoadKey returns the HMAC key of the workspace at root, creating it on first
// use: KeySize random bytes in .slaw-local/receipts/key, mode 0600. The folder
// is checked by Dir (no symbolic link). The key file is opened without
// following a link and checked on the open descriptor: a file that is not
// regular, has a mode wider than 0600 or is not KeySize bytes is refused, never
// repaired: a key someone else could have read or replaced fingerprints
// nothing. Creation is atomic (a temp file linked into place), so two
// processes starting together end up with one key.
func LoadKey(root string) ([]byte, error) {
	dir, err := Dir(root)
	if err != nil {
		return nil, err
	}
	return loadKeyIn(dir)
}

func loadKeyIn(dir string) ([]byte, error) {
	path := filepath.Join(dir, KeyFile)
	key, err := readKey(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	if err := createKey(dir, path); err != nil {
		return nil, err
	}
	return readKey(path)
}

func createKey(dir, path string) error {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("failed to make a receipt key: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".key.*") // mode 0600
	if err != nil {
		return fmt.Errorf("failed to create the receipt key: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write the receipt key: %w", err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to set the receipt key's mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to write the receipt key: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("failed to create the receipt key: %w", err)
	}
	return nil
}

func readKey(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("the receipt key cannot be opened (a symbolic link is refused): %w", err)
	}
	defer f.Close() //nolint:errcheck
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to read the receipt key: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("the receipt key is not a regular file")
	}
	if fi.Mode().Perm()&^fileMode != 0 {
		return nil, fmt.Errorf("the receipt key's mode %#o is wider than 0600", fi.Mode().Perm())
	}
	if fi.Size() != KeySize {
		return nil, errors.New("the receipt key is not 32 bytes")
	}
	key, err := io.ReadAll(io.LimitReader(f, KeySize+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read the receipt key: %w", err)
	}
	if len(key) != KeySize {
		return nil, errors.New("the receipt key is not 32 bytes")
	}
	return key, nil
}
