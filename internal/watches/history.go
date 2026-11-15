package watches

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SandwichLabs/slaw/internal/connname"
	"github.com/SandwichLabs/slaw/internal/filelock"
	"github.com/SandwichLabs/slaw/internal/slawlocal"
)

// Status is the outcome of one check.
type Status string

// The outcomes. ok: the condition does not hold. alert: it holds. error: the
// watch did not run, or its result had the wrong shape; never an alert.
const (
	StatusOK    Status = "ok"
	StatusAlert Status = "alert"
	StatusError Status = "error"
)

// Valid reports whether s is one of the three outcomes.
func (s Status) Valid() bool { return s == StatusOK || s == StatusAlert || s == StatusError }

// Entry is one line of the history: what a check found, never the rows it
// read. A watch over a rows condition can name customers, so only the count
// and the reason are kept.
type Entry struct {
	Watch  string    `json:"watch"`
	Rev    string    `json:"rev,omitempty"`
	At     time.Time `json:"at"`
	Status Status    `json:"status"`
	// Value is the number a threshold check read, exactly as a JSON number: an
	// integer or decimal in full however large, a double in its shortest form.
	// Empty when the check read none.
	Value json.Number `json:"value,omitempty"`
	// Rows is the row count a rows check read (at most 1,001).
	Rows *int `json:"rows,omitempty"`
	// RowsCapped says the count stopped at the read limit.
	RowsCapped bool `json:"rows_capped,omitempty"`
	// Reason is one line saying why: "revenue is 1,240, above 1,000".
	Reason     string `json:"reason"`
	DurationMS int64  `json:"duration_ms"`
}

// MaxReasonRunes caps a reason, so an error message that quotes a value cannot
// grow the history.
const MaxReasonRunes = 300

// MaxValueLen caps the text of an entry's Value.
const MaxValueLen = 100

// History limits.
const (
	// DefaultPerWatch is how many entries of each watch the history keeps.
	DefaultPerWatch = 200
	// DefaultMaxBytes is the history file's size cap; the oldest entries go
	// first.
	DefaultMaxBytes = 5 << 20

	maxLineBytes    = 8 << 10
	historyDirMode  = 0o700
	historyFileMode = 0o600
	// HistoryDir and HistoryFile are where the history lives under the
	// workspace root. LocalDir is the machine-local folder, git-ignored.
	localDir    = ".slaw-local"
	historyDir  = "watches"
	historyFile = "history.jsonl"
	lockName    = "history.lock"
	lockTimeout = 5 * time.Second
)

// HistoryPath is the history file's path relative to the workspace root.
const HistoryPath = localDir + "/" + historyDir + "/" + historyFile

// HistoryReader is the read half of History: what a surface that must never
// record or change a check (an agent's list tool) is given.
type HistoryReader interface {
	// Entries returns the entries of one watch ("" is every watch), oldest
	// first; never nil. A missing history is empty.
	Entries(ctx context.Context, watch string) ([]Entry, error)
}

// History is the record of checks. It is machine-local: the file history keeps
// it under .slaw-local/ and nothing bundles or commits it.
type History interface {
	// Append records one check and prunes the history to its caps.
	Append(ctx context.Context, e Entry) error
	// Entries returns the entries of one watch ("" is every watch), oldest
	// first; never nil. A missing history is empty.
	Entries(ctx context.Context, watch string) ([]Entry, error)
}

// Summary is what a list shows about a watch from its history.
type Summary struct {
	Checks      int
	LastChecked *time.Time
	LastFired   *time.Time
	LastStatus  Status
}

// Summarize folds entries (any order) into one Summary per watch.
func Summarize(entries []Entry) map[string]Summary {
	out := map[string]Summary{}
	for _, e := range entries {
		s := out[e.Watch]
		s.Checks++
		at := e.At
		if s.LastChecked == nil || !at.Before(*s.LastChecked) {
			s.LastChecked, s.LastStatus = &at, e.Status
		}
		if e.Status == StatusAlert && (s.LastFired == nil || !at.Before(*s.LastFired)) {
			s.LastFired = &at
		}
		out[e.Watch] = s
	}
	return out
}

// Prune keeps the newest perWatch entries of each watch, then drops the oldest
// entries overall until the encoded lines fit maxBytes. The order of the rest
// is kept.
func Prune(entries []Entry, perWatch int, maxBytes int64) []Entry {
	keep := make([]bool, len(entries))
	seen := map[string]int{}
	for i := len(entries) - 1; i >= 0; i-- {
		seen[entries[i].Watch]++
		keep[i] = perWatch <= 0 || seen[entries[i].Watch] <= perWatch
	}
	sizes := make([]int64, len(entries))
	var total int64
	for i := range entries {
		if !keep[i] {
			continue
		}
		sizes[i] = int64(len(encodeLine(entries[i])))
		total += sizes[i]
	}
	for i := 0; i < len(entries) && maxBytes > 0 && total > maxBytes; i++ {
		if keep[i] {
			keep[i] = false
			total -= sizes[i]
		}
	}
	out := make([]Entry, 0, len(entries))
	for i, e := range entries {
		if keep[i] {
			out = append(out, e)
		}
	}
	return out
}

func encodeLine(e Entry) []byte {
	b, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// clean makes an entry safe to store: a known outcome, a valid watch name and
// a bounded one-line reason.
func (e Entry) clean() (Entry, error) {
	if !connname.Valid(e.Watch) {
		return e, fmt.Errorf("%q is not a watch name", e.Watch)
	}
	if !e.Status.Valid() {
		return e, fmt.Errorf("%q is not an outcome", e.Status)
	}
	if e.Value != "" && (len(e.Value) > MaxValueLen || !json.Valid([]byte(e.Value))) {
		return e, fmt.Errorf("%q is not a number a check could have read", e.Value)
	}
	e.Reason = oneLine(e.Reason, MaxReasonRunes)
	return e, nil
}

func oneLine(s string, max int) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			out = append(out, ' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			out = append(out, '?')
		default:
			out = append(out, r)
		}
	}
	if len(out) > max {
		out = append(out[:max-3], '.', '.', '.')
	}
	return string(out)
}

// parseHistory reads lines into entries, skipping a line that is not a
// well-formed entry; dirty says some were skipped.
func parseHistory(data []byte) (entries []Entry, dirty bool) {
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Entry
		if len(line) > maxLineBytes || json.Unmarshal(line, &e) != nil {
			dirty = true
			continue
		}
		c, err := e.clean()
		if err != nil {
			dirty = true
			continue
		}
		entries = append(entries, c)
	}
	return entries, dirty
}

// FileHistory keeps the history in <root>/.slaw-local/watches/history.jsonl:
// one JSON line per check. Appends and compaction run under a cross-process
// file lock (history.lock beside it) and an in-process mutex, so Studio and a
// CLI check cannot interleave a prune with an append. A symbolic link anywhere
// on the path, or a file that is not a regular file, is refused rather than
// followed: the folder may come from a clone.
type FileHistory struct {
	root     string
	perWatch int
	maxBytes int64
	mu       sync.Mutex
}

// HistoryOption configures NewFileHistory.
type HistoryOption func(*FileHistory)

// WithLimits sets the per-watch entry count and the file size cap (tests; the
// defaults are DefaultPerWatch and DefaultMaxBytes).
func WithLimits(perWatch int, maxBytes int64) HistoryOption {
	return func(h *FileHistory) { h.perWatch, h.maxBytes = perWatch, maxBytes }
}

// NewFileHistory returns the file-backed History of a workspace root.
func NewFileHistory(root string, opts ...HistoryOption) *FileHistory {
	h := &FileHistory{root: root, perWatch: DefaultPerWatch, maxBytes: DefaultMaxBytes}
	for _, o := range opts {
		o(h)
	}
	return h
}

var _ History = (*FileHistory)(nil)

// dir returns the history folder, creating it (0700) when create is set and
// refusing a symbolic link or a non-directory at any step. A missing folder
// without create is ("", nil).
func (h *FileHistory) dir(create bool) (string, error) {
	if create {
		if _, err := slawlocal.Ensure(h.root); err != nil {
			return "", fmt.Errorf("the watch history folder cannot be prepared: %w", err)
		}
	}
	real, err := filepath.EvalSymlinks(h.root)
	if err != nil {
		return "", errors.New("the workspace folder cannot be resolved")
	}
	cur := real
	for _, part := range []string{localDir, historyDir} {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return "", nil
			}
			// Another process may make it first; what is there is checked below.
			if merr := os.Mkdir(cur, historyDirMode); merr != nil && !errors.Is(merr, fs.ErrExist) {
				return "", fmt.Errorf("the watch history folder cannot be created: %w", merr)
			}
			fi, err = os.Lstat(cur)
		}
		switch {
		case err != nil:
			return "", fmt.Errorf("the watch history folder cannot be read: %w", err)
		case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
			return "", errors.New("the watch history folder is a symbolic link or not a folder")
		}
	}
	return cur, nil
}

// lock takes the cross-process lock, waiting up to lockTimeout for another
// process's write. The lock file is persistent, so every waiter locks the
// same inode.
func lock(dir string) (func(), error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to open the watch history folder: %w", err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		l, err := filelock.AcquirePersistentAt(d, lockName)
		if err == nil {
			return func() { _ = l.Release(); _ = d.Close() }, nil
		}
		if errors.Is(err, filelock.ErrUnsafe) || time.Now().After(deadline) {
			_ = d.Close()
			return nil, fmt.Errorf("failed to lock the watch history: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// readFile reads the history without following a link.
func readFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the watch history cannot be opened: %w", err)
	}
	defer f.Close() //nolint:errcheck
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("the watch history is not a regular file")
	}
	// A file far over the cap is read from its tail; Prune then rewrites it.
	limit := int64(DefaultMaxBytes) * 4
	if fi.Size() > limit {
		if _, err := f.Seek(fi.Size()-limit, io.SeekStart); err != nil {
			return nil, fmt.Errorf("the watch history cannot be read: %w", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, fmt.Errorf("the watch history cannot be read: %w", err)
	}
	return data, nil
}

// Entries implements History.
func (h *FileHistory) Entries(ctx context.Context, watch string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := h.dir(false)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return []Entry{}, nil
	}
	data, err := readFile(filepath.Join(dir, historyFile))
	if err != nil {
		return nil, err
	}
	all, _ := parseHistory(data)
	if watch == "" {
		if all == nil {
			all = []Entry{}
		}
		return all, nil
	}
	out := []Entry{}
	for _, e := range all {
		if e.Watch == watch {
			out = append(out, e)
		}
	}
	return out, nil
}

// Append implements History.
func (h *FileHistory) Append(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e, err := e.clean()
	if err != nil {
		return fmt.Errorf("failed to record the check: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	dir, err := h.dir(true)
	if err != nil {
		return err
	}
	release, err := lock(dir)
	if err != nil {
		return err
	}
	defer release()

	path := filepath.Join(dir, historyFile)
	data, err := readFile(path)
	if err != nil {
		return err
	}
	cur, dirty := parseHistory(data)
	cur = append(cur, e)
	kept := Prune(cur, h.perWatch, h.maxBytes)
	if !dirty && len(kept) == len(cur) && int64(len(data)) <= h.maxBytes {
		return appendLine(path, encodeLine(e))
	}
	return rewrite(dir, path, kept)
}

// appendLine adds one line to the file with a single write.
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|oNoFollow, historyFileMode)
	if err != nil {
		return fmt.Errorf("the watch history cannot be written: %w", err)
	}
	if _, werr := f.Write(line); werr != nil {
		_ = f.Close()
		return fmt.Errorf("the watch history cannot be written: %w", werr)
	}
	if err := errors.Join(f.Chmod(historyFileMode), f.Close()); err != nil {
		return fmt.Errorf("the watch history cannot be written: %w", err)
	}
	return nil
}

// rewrite replaces the file with entries: temp file, then rename (which
// replaces a link rather than writing through it).
func rewrite(dir, path string, entries []Entry) error {
	var buf bytes.Buffer
	for _, e := range entries {
		buf.Write(encodeLine(e))
	}
	tmp, err := os.CreateTemp(dir, "history-*")
	if err != nil {
		return fmt.Errorf("the watch history cannot be rewritten: %w", err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(buf.Bytes())
	cerr := tmp.Chmod(historyFileMode)
	if err := errors.Join(werr, cerr, tmp.Close()); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("the watch history cannot be rewritten: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("the watch history cannot be rewritten: %w", err)
	}
	return nil
}
