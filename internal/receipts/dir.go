package receipts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/SandwichLabs/slaw/internal/slawlocal"
)

// Where the receipts live under a workspace root.
const receiptsDir = "receipts"

// Dir returns the receipts folder of the workspace at root
// (<root>/.slaw-local/receipts), creating it at 0700 and tightening an existing
// one to 0700. slawlocal.Ensure makes .slaw-local and its .gitignore first (and
// refuses a link there); a hand-made repo never offers the key and the
// statement text to a commit, and if the ignore file cannot be written receipts
// stay off. The folder may come from a clone, so a symbolic link or a
// non-directory at the receipts step is refused, never followed: a link would
// carry the key and the receipts out of the machine-local folder. root is
// trusted (the workspace the person opened); every constructor here takes the
// root, not the folder, so this check cannot be skipped.
func Dir(root string) (string, error) {
	local, err := slawlocal.Ensure(root)
	if err != nil {
		return "", err
	}
	cur := filepath.Join(local, receiptsDir)
	fi, err := os.Lstat(cur)
	if errors.Is(err, fs.ErrNotExist) {
		// Another process may make it first; what is there is checked below.
		if merr := os.Mkdir(cur, dirMode); merr != nil && !errors.Is(merr, fs.ErrExist) {
			return "", fmt.Errorf("the receipts folder cannot be created: %w", merr)
		}
		fi, err = os.Lstat(cur)
	}
	switch {
	case err != nil:
		return "", fmt.Errorf("the receipts folder cannot be read: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
		return "", errors.New("the receipts folder is a symbolic link or not a folder")
	}
	if err := os.Chmod(cur, dirMode); err != nil {
		return "", fmt.Errorf("the receipts folder's permissions cannot be tightened: %w", err)
	}
	return cur, nil
}
