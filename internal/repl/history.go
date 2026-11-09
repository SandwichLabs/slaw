package repl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/usersql"
)

const (
	historyMax       = 1000
	historyEntryMax  = 64 << 10
	historyReadMax   = 16 << 20
	historyDirMode   = 0o700
	historyFileMode  = 0o600
	historyDirParts  = ".slaw-local"
	historyDirParts2 = "repl"
	historyFileName  = "history"
)

// history is the console's recall list, kept in .slaw-local/repl/history: one
// entry per line, machine-local, never in the workspace's tracked files. It
// only ever holds what engine.Recordable and eligible allow. The file is a cache the
// repository does not control: a symbolic link anywhere on its path, or a file
// that is not a regular file, switches history off for the session (with one
// notice) rather than following it. Up to four consoles of a workspace share
// the file without a lock; appends are single writes, the rewrite at open is
// atomic (temp file and rename), and the worst case is a lost entry.
type history struct {
	mu      sync.Mutex
	path    string // "" = nothing is written
	entries []string
}

// foldEntry is a statement as one history line.
func foldEntry(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

// safeDir returns the history directory under root, creating it (0700) and
// tightening it, and refusing a symbolic link or a non-directory at any step.
func safeDir(root string) (string, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("the workspace folder cannot be resolved")
	}
	cur := real
	for _, part := range []string{historyDirParts, historyDirParts2} {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, historyDirMode); err != nil {
				return "", fmt.Errorf("the history folder cannot be created: %w", err)
			}
		case err != nil:
			return "", fmt.Errorf("the history folder cannot be read: %w", err)
		case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
			return "", errors.New("the history folder is a symbolic link or not a folder")
		}
	}
	if resolved, err := filepath.EvalSymlinks(cur); err != nil || resolved != cur {
		return "", errors.New("the history folder is not inside the workspace")
	}
	if err := os.Chmod(cur, historyDirMode); err != nil {
		return "", fmt.Errorf("the history folder's permissions cannot be tightened: %w", err)
	}
	return cur, nil
}

// openHistory loads the history file under root (root "" keeps history in
// memory only). When the path is unsafe it returns an in-memory history that
// writes nothing, and a notice saying why.
func openHistory(root string) (h *history, notice string) {
	h = &history{}
	if root == "" {
		return h, ""
	}
	dir, err := safeDir(root)
	if err != nil {
		return h, err.Error()
	}
	path := filepath.Join(dir, historyFileName)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return h, "the history file is a symbolic link or not a regular file"
	}
	entries, dirty, err := loadHistory(path)
	if err != nil {
		return h, err.Error()
	}
	h.path = path
	h.entries = entries
	if len(h.entries) > historyMax {
		h.entries = h.entries[len(h.entries)-historyMax:]
		dirty = true
	}
	if dirty {
		if err := h.rewrite(dir); err != nil {
			h.path = ""
			return h, err.Error()
		}
	}
	return h, ""
}

// loadHistory reads the file without following a link, tightens its mode and
// keeps the entries engine.Recordable allows. dirty says the file should be rewritten.
func loadHistory(path string) (entries []string, dirty bool, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("the history file cannot be opened: %w", err)
	}
	defer f.Close() //nolint:errcheck
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false, errors.New("the history file is not a regular file")
	}
	if err := f.Chmod(historyFileMode); err != nil {
		return nil, false, fmt.Errorf("the history file's permissions cannot be tightened: %w", err)
	}
	if fi.Size() > historyReadMax {
		if _, err := f.Seek(fi.Size()-historyReadMax, io.SeekStart); err != nil {
			return nil, false, fmt.Errorf("the history file cannot be read: %w", err)
		}
		dirty = true
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), historyEntryMax+1)
	for sc.Scan() {
		line := sc.Text()
		if !engine.Recordable(line) {
			dirty = dirty || line != ""
			continue
		}
		entries = append(entries, line)
	}
	if sc.Err() != nil {
		dirty = true // an oversized line: keep what was read
	}
	return entries, dirty, nil
}

// rewrite replaces the file with the in-memory entries: temp file, then rename
// (which replaces a link rather than writing through it).
func (h *history) rewrite(dir string) error {
	tmp, err := os.CreateTemp(dir, "history-*")
	if err != nil {
		return fmt.Errorf("the history file cannot be rewritten: %w", err)
	}
	name := tmp.Name()
	var b strings.Builder
	for _, e := range h.entries {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	_, werr := tmp.WriteString(b.String())
	cerr := tmp.Chmod(historyFileMode)
	if err := errors.Join(werr, cerr, tmp.Close()); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("the history file cannot be rewritten: %w", err)
	}
	if err := os.Rename(name, h.path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("the history file cannot be rewritten: %w", err)
	}
	return nil
}

// record adds an entry (statement newlines become spaces), skipping one
// engine.Recordable refuses and a repeat of the last.
func (h *history) record(text string) {
	entry := foldEntry(text)
	if !engine.Recordable(entry) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.entries); n > 0 && h.entries[n-1] == entry {
		return
	}
	h.entries = append(h.entries, entry)
	if len(h.entries) > historyMax {
		h.entries = h.entries[len(h.entries)-historyMax:]
	}
	if h.path == "" {
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|oNoFollow, historyFileMode)
	if err != nil {
		h.path = "" // something changed under us: stop writing
		return
	}
	defer f.Close() //nolint:errcheck
	_ = f.Chmod(historyFileMode)
	_, _ = f.WriteString(entry + "\n")
}

// termHistory is the line editor's view of the list: read-only. The editor
// calls Add for every line (half statements, dot commands, before any gate
// ran), so Add does nothing; the session records what is eligible.
type termHistory struct{ h *history }

func (termHistory) Add(string) {}

func (t termHistory) Len() int {
	t.h.mu.Lock()
	defer t.h.mu.Unlock()
	return len(t.h.entries)
}

// At is the entry idx steps back (0 is the most recent).
func (t termHistory) At(idx int) string {
	t.h.mu.Lock()
	defer t.h.mu.Unlock()
	if idx < 0 || idx >= len(t.h.entries) {
		return ""
	}
	return safe(t.h.entries[len(t.h.entries)-1-idx]) // the last guard: never replay a control character
}

// neverRecordedTypes are the statement types DuckDB parses ATTACH, DETACH,
// SET (SET, RESET, PRAGMA x = y and USE all parse as SET), PRAGMA, LOAD and
// extension statements to: they can carry a credential or a setting.
var neverRecordedTypes = map[duckdb.StmtType]bool{
	duckdb.STATEMENT_TYPE_ATTACH:    true,
	duckdb.STATEMENT_TYPE_DETACH:    true,
	duckdb.STATEMENT_TYPE_SET:       true,
	duckdb.STATEMENT_TYPE_PRAGMA:    true,
	duckdb.STATEMENT_TYPE_LOAD:      true,
	duckdb.STATEMENT_TYPE_EXTENSION: true,
	duckdb.STATEMENT_TYPE_MULTI:     true,
}

// neverRecordedFirstWords decide a statement DuckDB could not prepare (a typo,
// a table that does not exist yet): its type is unknown, so its first token
// stands in. CREATE and DROP are here only so a hand-typed CREATE SECRET that
// does not parse cannot slip in; a well-formed CREATE TABLE prepares and is
// recorded.
var neverRecordedFirstWords = map[string]bool{
	"ATTACH": true, "DETACH": true, "SET": true, "RESET": true, "PRAGMA": true,
	"USE": true, "INSTALL": true, "LOAD": true, "FORCE": true, "CREATE": true, "DROP": true,
}

// eligible reports whether a statement that passed the gates may be written to
// the history. typ is DuckDB's parsed statement type; prepErr is why it could
// not be prepared (then typ means nothing).
func eligible(text string, typ duckdb.StmtType, prepErr error) bool {
	if prepErr == nil {
		return !neverRecordedTypes[typ]
	}
	stmts := usersql.Tokens(text)
	if len(stmts) == 0 || len(stmts[0]) == 0 {
		return false
	}
	first := stmts[0][0]
	if first.Kind != usersql.TokWord {
		return false
	}
	return !neverRecordedFirstWords[first.Text]
}
