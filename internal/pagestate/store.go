// Package pagestate is the `state` capability of a scripted HTML page
// (docs/plans/html-artifacts.md, H5): a small JSON document store for the
// page's own state, kept in .slaw-local/artifacts/<name>/state.json (git-ignored
// and machine-local, never in the folder, a bundle or a commit).
//
// The store is keyed by the artifact's name, which the caller (Studio) supplies:
// a page never names a store, so one artifact's state is not reachable from
// another's. The contents are untrusted input to the page, because another
// version of the page wrote them. Writes are debounced in memory and land with
// a compare-and-write through filestore; a write that finds the file changed
// by another store merges its own changes onto the new content.
package pagestate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/jsonenc"
	"github.com/SandwichLabs/slaw/internal/slawlocal"
)

// Limits (H5).
const (
	MaxTotalBytes = 1 << 20  // keys and values together
	MaxValueBytes = 64 << 10 // one value, compacted
	MaxKeyBytes   = 200
	MaxKeys       = 2000
	MaxListed     = 1000
)

// LocalDir is the machine-local folder the files live under (workspace.LocalDir).
const LocalDir = ".slaw-local"

// Errors. None carries a path or a value.
var (
	ErrBadKey   = errors.New("pagestate: bad key")
	ErrBadValue = errors.New("pagestate: the value is not JSON")
	ErrTooLarge = errors.New("pagestate: over a size limit")
	ErrCorrupt  = errors.New("pagestate: the state file is not usable")
	ErrBadName  = errors.New("pagestate: bad artifact name")
	ErrUnsafe   = errors.New("pagestate: the state folder is a link")
)

const fileVersion = 1

type fileDoc struct {
	Version int                        `json:"version"`
	Values  map[string]json.RawMessage `json:"values"`
}

// Change is what a subscriber is told after a set or delete.
type Change struct {
	Key     string
	Value   json.RawMessage // nil for a delete
	Deleted bool
}

type pendingOp struct {
	value   json.RawMessage
	deleted bool
}

type subscriber struct {
	origin any
	fn     func(Change)
}

// Store is one artifact's state.
type Store struct {
	root, rel string
	delay     time.Duration

	mu      sync.Mutex
	loaded  bool
	doc     map[string]json.RawMessage
	pending map[string]pendingOp
	size    int
	timer   *time.Timer
	subs    map[int]subscriber
	nextSub int
	// onError hears a failed background flush (a path never leaves this package
	// in an error, but the caller may log the kind).
	onError func(error)
}

// Option configures a Store.
type Option func(*Store)

// WithDelay sets the debounce before pending writes are flushed (default 300 ms).
func WithDelay(d time.Duration) Option { return func(s *Store) { s.delay = d } }

// WithErrorHook sets the function told about a failed background flush.
func WithErrorHook(f func(error)) Option { return func(s *Store) { s.onError = f } }

// Rel is the workspace-relative path of an artifact's state file.
func Rel(name string) string { return LocalDir + "/artifacts/" + name + "/state.json" }

// Open returns the store of the named artifact in the workspace at root. The
// name must be an artifact id; nothing is read until first use.
func Open(root, name string, opts ...Option) (*Store, error) {
	if !filestore.ValidID(name) {
		return nil, ErrBadName
	}
	s := &Store{root: root, rel: Rel(name), delay: 300 * time.Millisecond, subs: map[int]subscriber{}, pending: map[string]pendingOp{}}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// ValidKey is the rule for a key: 1 to MaxKeyBytes bytes of valid UTF-8 with no
// control or format characters.
func ValidKey(k string) bool {
	if k == "" || len(k) > MaxKeyBytes || !utf8.ValidString(k) {
		return false
	}
	return !strings.ContainsFunc(k, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || isFormat(r) })
}

func isFormat(r rune) bool {
	return (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2060 && r <= 0x2064) || (r >= 0x2066 && r <= 0x2069) || r == 0xfeff
}

// load reads the file into memory once; mu is held.
func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	doc, _, err := s.readDisk()
	if err != nil {
		return err
	}
	s.doc, s.loaded = doc, true
	s.size = docSize(doc)
	return nil
}

// readDisk reads and strictly decodes the state file ("" revision and an empty
// document when there is none).
func (s *Store) readDisk() (map[string]json.RawMessage, string, error) {
	if err := s.checkPath(); err != nil {
		return nil, "", err
	}
	data, st, err := filestore.Read(s.root, s.rel)
	if errors.Is(err, filestore.ErrNotFound) {
		return map[string]json.RawMessage{}, "", nil
	}
	if err != nil {
		return nil, "", ErrCorrupt
	}
	if len(data) > MaxTotalBytes+(256<<10) {
		return nil, "", ErrCorrupt
	}
	var f fileDoc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil || f.Version != fileVersion {
		return nil, "", ErrCorrupt
	}
	if f.Values == nil {
		f.Values = map[string]json.RawMessage{}
	}
	if len(f.Values) > MaxKeys || docSize(f.Values) > MaxTotalBytes {
		return nil, "", ErrCorrupt
	}
	for k, v := range f.Values {
		if !ValidKey(k) || len(v) > MaxValueBytes || !json.Valid(v) {
			return nil, "", ErrCorrupt
		}
	}
	return f.Values, st.Hash, nil
}

// checkPath refuses a state path that goes through a symbolic link. A link
// committed under .slaw-local/ (say artifacts/a -> ../../notes) would make the
// state land in a tracked folder, which is the way out through git that the
// local scope exists to close. Every component below the root is looked at.
func (s *Store) checkPath() error {
	dir := s.root
	for _, part := range strings.Split(s.rel, "/") {
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafe
		}
	}
	return nil
}

func docSize(d map[string]json.RawMessage) int {
	n := 0
	for k, v := range d {
		n += len(k) + len(v)
	}
	return n
}

// Get returns the value at key, and whether there is one.
func (s *Store) Get(key string) (json.RawMessage, bool, error) {
	if !ValidKey(key) {
		return nil, false, ErrBadKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return nil, false, err
	}
	v, ok := s.doc[key]
	return v, ok, nil
}

// List returns the keys that start with prefix, sorted, at most MaxListed.
func (s *Store) List(prefix string) ([]string, error) {
	if len(prefix) > MaxKeyBytes || !utf8.ValidString(prefix) {
		return nil, ErrBadKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return nil, err
	}
	out := []string{}
	for k := range s.doc {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) > MaxListed {
		out = out[:MaxListed]
	}
	return out, nil
}

// Set stores value (any JSON) at key. The value is compacted; over MaxValueBytes
// or over the store's total it is refused. origin identifies the writer so its
// own subscription is not told (nil tells everyone).
func (s *Store) Set(key string, value json.RawMessage, origin any) error {
	if !ValidKey(key) {
		return ErrBadKey
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, value); err != nil || buf.Len() == 0 {
		return ErrBadValue
	}
	// Keep the value in the form every later encoder emits (jsonenc still writes
	// U+2028 and U+2029 as 6-byte escapes), so the size the caps measure is the
	// size in memory, on disk and in a push.
	enc, err := jsonenc.Encode(json.RawMessage(buf.Bytes()), "")
	if err != nil {
		return ErrBadValue
	}
	v := json.RawMessage(bytes.TrimRight(enc, "\n"))
	if len(v) > MaxValueBytes {
		return ErrTooLarge
	}
	s.mu.Lock()
	if err := s.load(); err != nil {
		s.mu.Unlock()
		return err
	}
	old, had := s.doc[key]
	next := s.size - len(old) + len(v)
	if !had {
		next += len(key)
		if len(s.doc) >= MaxKeys {
			s.mu.Unlock()
			return ErrTooLarge
		}
	}
	if next > MaxTotalBytes {
		s.mu.Unlock()
		return ErrTooLarge
	}
	s.doc[key], s.size = v, next
	s.pending[key] = pendingOp{value: v}
	s.scheduleLocked()
	subs := s.subsLocked(origin)
	s.mu.Unlock()
	notify(subs, Change{Key: key, Value: v})
	return nil
}

// Delete removes key (a missing key is not an error).
func (s *Store) Delete(key string, origin any) error {
	if !ValidKey(key) {
		return ErrBadKey
	}
	s.mu.Lock()
	if err := s.load(); err != nil {
		s.mu.Unlock()
		return err
	}
	if old, ok := s.doc[key]; ok {
		delete(s.doc, key)
		s.size -= len(old) + len(key)
	}
	s.pending[key] = pendingOp{deleted: true}
	s.scheduleLocked()
	subs := s.subsLocked(origin)
	s.mu.Unlock()
	notify(subs, Change{Key: key, Deleted: true})
	return nil
}

// Subscribe registers fn for changes made through this store by any origin but
// the one given; the returned function cancels it. fn runs on the writer's
// goroutine and must not call back into the store.
func (s *Store) Subscribe(origin any, fn func(Change)) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = subscriber{origin: origin, fn: fn}
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

func (s *Store) subsLocked(except any) []func(Change) {
	var out []func(Change)
	for _, sub := range s.subs {
		if except != nil && sub.origin == except {
			continue
		}
		out = append(out, sub.fn)
	}
	return out
}

func notify(fns []func(Change), c Change) {
	for _, f := range fns {
		f(c)
	}
}

func (s *Store) scheduleLocked() {
	if s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(s.delay, func() {
		if err := s.Flush(); err != nil && s.onError != nil {
			s.onError(err)
		}
	})
}

// Flush writes pending changes now. It re-reads the file, applies the pending
// changes on top of what is there, and writes with the revision it read, so a
// change another store made in between is kept, not overwritten; a write that
// loses the race tries again.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if len(s.pending) == 0 {
		return nil
	}
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		disk, rev, err := s.readDisk()
		if err != nil {
			return err
		}
		doc := make(map[string]json.RawMessage, len(disk)+len(s.pending))
		for k, v := range disk {
			doc[k] = v
		}
		for k, op := range s.pending {
			if op.deleted {
				delete(doc, k)
			} else {
				doc[k] = op.value
			}
		}
		if docSize(doc) > MaxTotalBytes || len(doc) > MaxKeys {
			// The other store filled the file: these changes cannot all fit. Drop
			// them rather than grow past the cap, and show what is on disk.
			s.reset(disk)
			return ErrTooLarge
		}
		// HTML escaping off: a "<" in a value stays one byte on disk, so the file
		// is the size the caps measured.
		data, err := jsonenc.Encode(fileDoc{Version: fileVersion, Values: doc}, "")
		if err != nil {
			return ErrCorrupt
		}
		if _, lerr := slawlocal.Ensure(s.root); lerr != nil {
			return fmt.Errorf("pagestate: failed to prepare the local folder: %w", lerr)
		}
		_, err = filestore.WriteAtomic(s.root, filestore.Write{Rel: s.rel, Data: data, Expect: rev}, nil)
		if err == nil {
			s.doc, s.size, s.loaded = doc, docSize(doc), true
			s.pending = map[string]pendingOp{}
			return nil
		}
		if !errors.Is(err, filestore.ErrConflict) {
			return fmt.Errorf("pagestate: failed to write the state file: %w", err)
		}
		last = err
	}
	return fmt.Errorf("pagestate: the state file kept changing: %w", last)
}

func (s *Store) reset(doc map[string]json.RawMessage) {
	s.doc, s.size, s.loaded = doc, docSize(doc), true
	s.pending = map[string]pendingOp{}
}
