// Package editor opens SQL in the person's own editor ($VISUAL, then $EDITOR)
// for the TUI REPL and `slaw query --edit`, the way git and psql's \e do.
//
// A scratch buffer lives under the workspace's .slaw-local/edit/ rather than
// the system temp directory, so an editor that finds its language-server root
// by walking up to slaw.yaml (Neovim's root_markers, Helix's roots) attaches
// `slaw lsp` to it. The editor also gets SLAW_WORKSPACE in its environment.
package editor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/slawlocal"
)

// EnvWorkspace names the workspace root for the editor and the `slaw lsp` it
// starts.
const EnvWorkspace = "SLAW_WORKSPACE"

// Fallback is the editor used when neither $VISUAL nor $EDITOR is set.
const Fallback = "vi"

// ErrUnbalancedQuote is returned for an editor setting with an open quote.
var ErrUnbalancedQuote = errors.New("unbalanced quote in editor command")

// Resolve returns the editor command line as argv: $VISUAL, then $EDITOR, then
// Fallback. The value may carry arguments ("code --wait", "emacsclient -t"),
// split the way a shell would for plain words and single or double quotes.
func Resolve(getenv func(string) string) ([]string, error) {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			argv, err := splitWords(v)
			if err != nil {
				return nil, fmt.Errorf("failed to read $%s: %w", key, err)
			}
			return argv, nil
		}
	}
	return []string{Fallback}, nil
}

// splitWords splits s on unquoted whitespace. It understands '...' and "..."
// (with \" and \\ inside double quotes); it does not expand variables.
func splitWords(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, ErrUnbalancedQuote
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// Check fails clearly when the editor program cannot be found, naming the
// program and the variables to set, before a scratch file is made.
func Check(argv []string) error {
	if len(argv) == 0 {
		return errors.New("no editor configured; set $VISUAL or $EDITOR")
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return fmt.Errorf("editor %q was not found; set $VISUAL or $EDITOR to an editor on your PATH", argv[0])
	}
	return nil
}

// scratchDir makes sure root's scratch folder exists as a real directory that
// belongs to the current user and is private (0700). A symbolic link anywhere
// on the way out of the workspace (a cloned repository can ship one under an
// ignored folder) is refused: another user could otherwise swap the buffer
// while the editor is open and have the edited query run as the person.
func scratchDir(root string) (string, error) {
	if _, err := slawlocal.Ensure(root); err != nil {
		return "", fmt.Errorf("refusing to use the scratch directory: %w", err)
	}
	dir, err := filestore.Resolve(root, filepath.ToSlash(filepath.Join(".slaw-local", "edit")))
	if err != nil {
		return "", fmt.Errorf("refusing to use the scratch directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create the scratch directory: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("failed to check the scratch directory: %w", err)
	}
	if !fi.IsDir() || !ownedByMe(fi) {
		return "", fmt.Errorf("refusing to use %s: not a directory of yours", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to secure the scratch directory: %w", err)
	}
	return dir, nil
}

// NewScratch writes initial to a new .sql file for one edit and returns its
// path. With root == "" the file goes to the system temp directory.
func NewScratch(root, initial string) (string, error) {
	dir := os.TempDir()
	if root != "" {
		var err error
		if dir, err = scratchDir(root); err != nil {
			return "", err
		}
	}
	f, err := os.CreateTemp(dir, "query-*.sql")
	if err != nil {
		return "", fmt.Errorf("failed to create a scratch file: %w", err)
	}
	if _, err := f.WriteString(initial); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to write the scratch file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to write the scratch file: %w", err)
	}
	return f.Name(), nil
}

// Command builds the editor process for path. root, when set, is passed to the
// editor (and so to any `slaw lsp` it starts) as SLAW_WORKSPACE.
func Command(argv []string, path, root string) *exec.Cmd {
	args := append(append([]string{}, argv[1:]...), path)
	c := exec.Command(argv[0], args...) //nolint:gosec // the person's own $EDITOR
	if root != "" {
		c.Env = append(c.Environ(), EnvWorkspace+"="+root)
	}
	return c
}

// Finish reads the edited buffer back and removes the scratch file. An editor
// that exited non-zero (":cq" in Vim) cancels the edit: ok is false.
func Finish(path string, runErr error) (text string, ok bool, err error) {
	defer os.Remove(path) //nolint:errcheck
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to run the editor: %w", runErr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("failed to read the edited query: %w", err)
	}
	return string(b), true, nil
}

// Edit runs the editor in the foreground on a scratch buffer holding initial
// and returns what was saved. ok is false when the editor was cancelled.
func Edit(argv []string, root, initial string, stdin io.Reader, stdout, stderr io.Writer) (text string, ok bool, err error) {
	if err := Check(argv); err != nil {
		return "", false, err
	}
	path, err := NewScratch(root, initial)
	if err != nil {
		return "", false, err
	}
	c := Command(argv, path, root)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	return Finish(path, c.Run())
}
