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
type FileStore struct {
	dir    string
	mu     sync.Mutex
	puts   int
	policy Policy
}

var _ Store = (*FileStore)(nil)

// NewFileStore opens the store of the workspace at root (its receipts folder
// is made and checked by Dir: no symbolic link) and prunes it by policy, so a
// limit lowered since the last run takes effect on the next open. Later Puts
// prune by the same policy every PruneEvery writes.
func NewFileStore(root string, p Policy) (*FileStore, error) {
	dir, err := Dir(root)
	if err != nil {
		return nil, err
	}
	s := &FileStore{dir: dir, policy: p.Clamp()}
	if err := s.Prune(s.policy); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir is the store's folder.
func (s *FileStore) Dir() string { return s.dir }

// SetPolicy changes the policy the periodic prune uses; it does not prune.
func (s *FileStore) SetPolicy(p Policy) {
	s.mu.Lock()
	s.policy = p.Clamp()
	s.mu.Unlock()
}

func (s *FileStore) path(id string) string {
	return filepath.Join(s.dir, id[2:4], id+fileExt)
}

// Put writes r atomically (0600, folder 0700). Every PruneEvery writes it
// also prunes; that housekeeping never fails the Put.
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
	p := s.policy
	s.mu.Unlock()
	if due {
		_ = s.Prune(p)
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
	path string
	size int64
	mod  time.Time
}

// Prune removes receipts, oldest first, until none is older than MaxAge and
// the count and the total size are within MaxCount and MaxBytes. The policy is
// clamped first. A receipt's age is its file's modification time. Files that
// are not receipt files are left alone, except stale temp files.
func (s *FileStore) Prune(p Policy) error {
	p = p.Clamp()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := s.scan()
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].mod.Equal(entries[j].mod) {
			return entries[i].mod.Before(entries[j].mod)
		}
		return entries[i].path < entries[j].path
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
		if err := os.Remove(e.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to prune a receipt: %w", err)
		}
		count--
		total -= e.size
	}
	return nil
}

// lock takes the cross-process prune lock, waiting up to lockTimeout.
func (s *FileStore) lock() (func(), error) {
	s.mu.Lock()
	d, err := os.Open(s.dir)
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("failed to open the receipts folder: %w", err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		l, err := filelock.AcquirePersistentAt(d, lockName)
		if err == nil {
			return func() { _ = l.Release(); _ = d.Close(); s.mu.Unlock() }, nil
		}
		if errors.Is(err, filelock.ErrUnsafe) || time.Now().After(deadline) {
			_ = d.Close()
			s.mu.Unlock()
			return nil, fmt.Errorf("failed to lock the receipts folder: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// scan lists the receipt files, sweeping stale temp files as it goes.
func (s *FileStore) scan() ([]entry, error) {
	tops, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read the receipts folder: %w", err)
	}
	var out []entry
	for _, top := range tops {
		if !top.IsDir() || !isHex(top.Name(), 2) {
			continue
		}
		dir := filepath.Join(s.dir, top.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to read the receipts folder: %w", err)
		}
		for _, f := range files {
			fi, err := f.Info()
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			name := f.Name()
			switch {
			case strings.HasPrefix(name, filestore.TempPrefix):
				if time.Since(fi.ModTime()) > staleTempAge {
					_ = os.Remove(filepath.Join(dir, name))
				}
			case strings.HasSuffix(name, fileExt) && ValidID(strings.TrimSuffix(name, fileExt)):
				out = append(out, entry{path: filepath.Join(dir, name), size: fi.Size(), mod: fi.ModTime()})
			}
		}
	}
	return out, nil
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
