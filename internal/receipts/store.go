package receipts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SandwichLabs/slaw/internal/filelock"
	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/jsonenc"
)

// MaxFileBytes caps one receipt file, on write and before decode.
const MaxFileBytes = 256 << 10

// PruneEvery is how many Puts a FileStore makes between prunes.
const PruneEvery = 500

const (
	lockName     = "prune.lock"
	lockTimeout  = 5 * time.Second
	staleTempAge = time.Hour
	fileExt      = ".json"
)

// ErrNotFound is wrapped by the error Get returns for an id with no receipt
// (or an id that is not a receipt id: the two are indistinguishable on purpose).
var ErrNotFound = errors.New("no receipt with that id")

// Store keeps receipts. Put never rewrites a receipt's content: a receipt is
// its own content address.
type Store interface {
	// Put validates and keeps a sealed receipt; the same receipt twice is one.
	Put(r Receipt) error
	// Get returns the receipt with this id, or an error wrapping ErrNotFound.
	Get(id string) (Receipt, error)
	// Prune removes the oldest receipts until the policy (clamped) holds.
	Prune(p Policy) error
	// Usage counts the receipts kept, their total size and the oldest one's time
	// (zero when there is none), without decoding any of them.
	Usage() (count int, bytes int64, oldest time.Time)
	// Clear removes every receipt and nothing else.
	Clear() error
	// List returns the newest receipts as one line each, newest first, narrowed
	// by the filter and bounded (see ListFilter).
	List(f ListFilter) ([]Summary, error)
}

// Encode is the one file encoding of a receipt: compact canonical JSON.
func Encode(r Receipt) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	b, err := jsonenc.Encode(r, "")
	if err != nil {
		return nil, fmt.Errorf("failed to encode the receipt: %w", err)
	}
	if len(b) > MaxFileBytes {
		return nil, invalid("the receipt is %d bytes, over the %d byte limit", len(b), MaxFileBytes)
	}
	return b, nil
}

// Decode is the strict reader of a receipt file: a byte cap before decoding,
// unknown fields refused, exactly one JSON value, then Validate (which also
// checks that the id is the content's address).
func Decode(data []byte) (Receipt, error) {
	if len(data) > MaxFileBytes {
		return Receipt{}, invalid("the file is over the %d byte limit", MaxFileBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Receipt
	if err := dec.Decode(&r); err != nil {
		return Receipt{}, invalid("not a receipt: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Receipt{}, invalid("data after the receipt")
	}
	if err := r.Validate(); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// FileStore keeps one file per receipt under a folder (.slaw-local/receipts/):
// <dir>/<aa>/<id>.json, aa being the two hex digits after "r_". Writes are
// atomic, so Studio, `slaw mcp serve` and the CLI read one another's receipts
// without a shared index. Prune runs under a cross-process file lock
// (prune.lock in the folder) and an in-process mutex.
//
// Pruning never blocks a Put. The open-time and periodic prunes run in one
// background goroutine (single-flight) that only try-locks prune.lock and skips
// the round when it is busy; an explicit Prune waits for the lock, on its own
// mutex, which a Put never takes. Wait and Close wait for the goroutine.
type FileStore struct {
	dir string

	mu     sync.Mutex // guards puts, policy and source; never held across I/O
	puts   int
	policy Policy
	source PolicySource

	pruneMu sync.Mutex // one prune at a time in this process
	running atomic.Bool
	pending atomic.Bool
	wg      sync.WaitGroup

	// closeMu guards closed and the wg.Add that starts a prune, so a Close never
	// races an Add: after Close no prune starts.
	closeMu sync.Mutex
	closed  bool
}

var _ Store = (*FileStore)(nil)

// NewFileStore opens the store of the workspace at root (its receipts folder
// is made and checked by Dir: no symbolic link) and starts a background prune by
// policy, so a limit lowered since the last run takes effect soon after the open
// without delaying the first Put. Later Puts prune by the same policy every
// PruneEvery writes, also in the background.
func NewFileStore(root string, p Policy) (*FileStore, error) {
	dir, err := Dir(root)
	if err != nil {
		return nil, err
	}
	s := &FileStore{dir: dir, policy: p.Clamp()}
	s.pruneAsync()
	return s, nil
}

// Wait returns once the background prune, if one is running, has finished.
func (s *FileStore) Wait() { s.wg.Wait() }

// Close waits for the background prune; the store holds nothing else open.
func (s *FileStore) Close() error {
	s.closeMu.Lock()
	s.closed = true
	s.closeMu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *FileStore) isClosed() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closed
}

// pruneAsync asks for a background prune by the current policy. At most one runs
// at a time; a request made while one runs makes it go round once more.
func (s *FileStore) pruneAsync() {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.pending.Store(true)
	if !s.running.CompareAndSwap(false, true) {
		s.closeMu.Unlock()
		return
	}
	s.wg.Add(1)
	s.closeMu.Unlock()
	go func() {
		defer s.wg.Done()
		for !s.isClosed() && s.pending.Swap(false) {
			_ = s.prune(s.policyNow(), false)
		}
		s.running.Store(false)
		if s.pending.Load() { // asked for again as it finished
			s.pruneAsync()
		}
	}()
}

// PolicySource gives a running process the person's current policy: the
// workspace reads the per-person settings file, so a change made by another
// process (the CLI, say) is seen here. ok false (an unreadable, untrusted or
// missing file) means keep the policy in force; it never falls back to defaults.
type PolicySource func() (Policy, bool)

// SetPolicySource makes every prune round ask src for the policy first.
func (s *FileStore) SetPolicySource(src PolicySource) {
	s.mu.Lock()
	s.source = src
	s.mu.Unlock()
}

// policyNow is the policy a prune round uses: the source's, when it answers.
func (s *FileStore) policyNow() Policy {
	s.mu.Lock()
	src := s.source
	s.mu.Unlock()
	if src != nil {
		if p, ok := src(); ok {
			s.mu.Lock()
			s.policy = p.Clamp()
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy
}

// Dir is the store's folder.
func (s *FileStore) Dir() string { return s.dir }

// SetPolicy changes the policy the prunes use, and prunes by it in the
// background, so a lowered limit takes effect without waiting for the next open.
func (s *FileStore) SetPolicy(p Policy) {
	s.mu.Lock()
	s.policy = p.Clamp()
	s.mu.Unlock()
	s.pruneAsync()
}

func (s *FileStore) path(id string) string {
	return filepath.Join(s.dir, id[2:4], id+fileExt)
}

// Put writes r atomically (0600, folder 0700). Every PruneEvery writes it
// also starts a background prune; that housekeeping never fails or delays the Put.
func (s *FileStore) Put(r Receipt) error {
	data, err := Encode(r)
	if err != nil {
		return err
	}
	path := s.path(r.ID)
	if err := ensureShard(filepath.Dir(path)); err != nil {
		return err
	}
	if err := filestore.WriteFileAtomicExact(path, data, fileMode); err != nil {
		return fmt.Errorf("failed to write the receipt: %w", err)
	}
	s.mu.Lock()
	s.puts++
	due := s.puts%PruneEvery == 0
	s.mu.Unlock()
	if due {
		s.pruneAsync()
	}
	return nil
}

// Get reads and strictly decodes one receipt. A symbolic link or any other
// file that is not a regular file is refused.
func (s *FileStore) Get(id string) (Receipt, error) {
	if !ValidID(id) {
		return Receipt{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	path := s.path(id)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Receipt{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return Receipt{}, fmt.Errorf("failed to read the receipt: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxFileBytes {
		return Receipt{}, invalid("the receipt file is not a regular file under the size limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return Receipt{}, fmt.Errorf("failed to read the receipt: %w", err)
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Receipt{}, fmt.Errorf("failed to read the receipt: %w", err)
	}
	r, err := Decode(data)
	if err != nil {
		return Receipt{}, err
	}
	if r.ID != id {
		return Receipt{}, invalid("the file holds another receipt")
	}
	return r, nil
}

type entry struct {
	rel  string // <aa>/<id>.json, relative to the receipts folder
	size int64
	mod  time.Time
}

// Prune removes receipts, oldest first, until none is older than MaxAge and
// the count and the total size are within MaxCount and MaxBytes. The policy is
// clamped first. A receipt's age is its file's modification time. Files that
// are not receipt files are left alone, except stale temp files.
func (s *FileStore) Prune(p Policy) error { return s.prune(p.Clamp(), true) }

// prune is Prune under a clamped policy. With wait false it only try-locks, and
// a busy lock skips the round (nil).
func (s *FileStore) prune(p Policy, wait bool) error {
	unlock, err := s.lock(wait)
	if err != nil || unlock == nil {
		return err
	}
	defer unlock()
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return fmt.Errorf("failed to open the receipts folder: %w", err)
	}
	defer root.Close() //nolint:errcheck
	entries, err := list(root, true)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].mod.Equal(entries[j].mod) {
			return entries[i].mod.Before(entries[j].mod)
		}
		return entries[i].rel < entries[j].rel
	})
	cutoff := time.Now().Add(-p.MaxAge)
	var total int64
	for _, e := range entries {
		total += e.size
	}
	count := len(entries)
	for _, e := range entries {
		if e.mod.After(cutoff) && count <= p.MaxCount && total <= p.MaxBytes {
			break
		}
		if err := root.Remove(e.rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to prune a receipt: %w", err)
		}
		count--
		total -= e.size
	}
	return nil
}

// lock takes the in-process mutex and the cross-process prune lock. With wait
// it waits up to lockTimeout; without, a lock that is busy returns a nil unlock
// and no error. It never takes s.mu, so a Put is never held up by a prune.
func (s *FileStore) lock(wait bool) (func(), error) {
	if wait {
		s.pruneMu.Lock()
	} else if !s.pruneMu.TryLock() {
		return nil, nil
	}
	d, err := os.Open(s.dir)
	if err != nil {
		s.pruneMu.Unlock()
		return nil, fmt.Errorf("failed to open the receipts folder: %w", err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		l, err := filelock.AcquirePersistentAt(d, lockName)
		if err == nil {
			return func() { _ = l.Release(); _ = d.Close(); s.pruneMu.Unlock() }, nil
		}
		if !wait {
			_ = d.Close()
			s.pruneMu.Unlock()
			return nil, nil
		}
		if errors.Is(err, filelock.ErrUnsafe) || time.Now().After(deadline) {
			_ = d.Close()
			s.pruneMu.Unlock()
			return nil, fmt.Errorf("failed to lock the receipts folder: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Usage walks the shard folders and adds up the receipt files by name and
// size: no file is opened or decoded, and nothing is swept or removed. A folder
// that cannot be read counts as empty.
func (s *FileStore) Usage() (int, int64, time.Time) {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return 0, 0, time.Time{}
	}
	defer root.Close() //nolint:errcheck
	entries, err := list(root, false)
	if err != nil {
		return 0, 0, time.Time{}
	}
	var total int64
	var oldest time.Time
	for _, e := range entries {
		total += e.size
		if oldest.IsZero() || e.mod.Before(oldest) {
			oldest = e.mod
		}
	}
	return len(entries), total, oldest
}

// Clear removes every receipt file, under the prune lock so it does not run
// beside a prune. It keeps the key (every process holding the workspace open
// has it in memory, so rotating it would split new receipts into two families
// that cannot be compared) and the folders. Only regular files named like a
// receipt, inside a real <aa> folder, are removed: a symbolic link, at either
// level, is never followed, and any other file (the key, the lock, a stray
// note, a temp file another process is still writing) is left alone.
func (s *FileStore) Clear() error {
	unlock, err := s.lock(true)
	if err != nil {
		return err
	}
	defer unlock()
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return fmt.Errorf("failed to open the receipts folder: %w", err)
	}
	defer root.Close() //nolint:errcheck
	entries, err := list(root, false)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := root.Remove(e.rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to delete a receipt: %w", err)
		}
	}
	return nil
}

// list lists the receipt files under root (the receipts folder, opened with
// os.OpenRoot, so no path can leave it): regular files named <id>.json in a real
// <aa> folder (a link is neither a folder nor a regular file here). With sweep
// it also removes stale temp files. Every access goes through root, so a shard
// swapped for a link while the walk runs cannot reach a file outside the folder.
func list(root *os.Root, sweep bool) ([]entry, error) {
	d, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("failed to read the receipts folder: %w", err)
	}
	tops, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read the receipts folder: %w", err)
	}
	var out []entry
	for _, top := range tops {
		if !top.IsDir() || !isHex(top.Name(), 2) {
			continue
		}
		files, err := readShard(root, top.Name())
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			fi, err := f.Info()
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			name := f.Name()
			rel := top.Name() + "/" + name
			switch {
			case strings.HasPrefix(name, filestore.TempPrefix):
				if sweep && time.Since(fi.ModTime()) > staleTempAge {
					_ = root.Remove(rel)
				}
			case strings.HasSuffix(name, fileExt) && ValidID(strings.TrimSuffix(name, fileExt)):
				out = append(out, entry{rel: rel, size: fi.Size(), mod: fi.ModTime()})
			}
		}
	}
	return out, nil
}

// readShard lists one <aa> folder through root, refusing it when it is no longer
// a real folder (swapped for a link since the folder list was read).
func readShard(root *os.Root, name string) ([]fs.DirEntry, error) {
	if fi, err := root.Lstat(name); err != nil || !fi.IsDir() {
		return nil, nil
	}
	d, err := root.Open(name)
	if err != nil {
		return nil, nil // gone or refused: nothing to list
	}
	defer d.Close() //nolint:errcheck
	files, err := d.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("failed to read the receipts folder: %w", err)
	}
	return files, nil
}

// ensureShard makes the <aa> folder, refusing a link or a non-folder there.
func ensureShard(dir string) error {
	if err := os.Mkdir(dir, dirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("failed to create the receipt folder: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("failed to read the receipt folder: %w", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return errors.New("a receipt folder is a symbolic link or not a folder")
	}
	return nil
}
