package proposals

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/slawlocal"
)

// FileStore keeps proposals as .slaw-local/proposals/<id>.yaml under a
// workspace root. Writes are filestore compare-and-write; nothing is cached.
type FileStore struct {
	*core
}

// Option configures NewFileStore.
type Option func(*fileBackend, *core)

// WithObserver reports this store's own writes so a watcher can ignore them.
func WithObserver(o filestore.Observer) Option {
	return func(b *fileBackend, _ *core) { b.obs = o }
}

// WithLogger sets where the store reports files it lists with an error.
func WithLogger(l *slog.Logger) Option {
	return func(_ *fileBackend, c *core) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithClock sets the time a decision is stamped with (tests).
func WithClock(now func() time.Time) Option {
	return func(_ *fileBackend, c *core) { c.now = now }
}

// NewFileStore returns the file-backed Store rooted at a workspace folder.
func NewFileStore(root string, opts ...Option) *FileStore {
	b := &fileBackend{root: root}
	c := &core{b: b, logger: slog.Default(), now: time.Now}
	for _, o := range opts {
		o(b, c)
	}
	return &FileStore{core: c}
}

type fileBackend struct {
	root string
	obs  filestore.Observer
}

func rel(id string) string { return Dir + "/" + id + Extension }

func (f *fileBackend) read(id string) ([]byte, string, error) {
	data, st, err := filestore.Read(f.root, rel(id))
	return data, st.Rev(), err
}

func (f *fileBackend) write(id string, data []byte, expect string) error {
	if _, err := slawlocal.Ensure(f.root); err != nil {
		return fmt.Errorf("the proposals folder cannot be prepared: %w", err)
	}
	_, err := filestore.WriteAtomic(f.root, filestore.Write{Rel: rel(id), Data: data, Expect: expect}, f.obs)
	return err
}

func (f *fileBackend) remove(id string) error {
	return filestore.Remove(f.root, rel(id), "", true, f.obs)
}

func (f *fileBackend) ids() ([]string, error) {
	abs, err := filestore.Resolve(f.root, Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if filestore.IsTemp(n) || !strings.HasSuffix(n, Extension) || !e.Type().IsRegular() {
			continue
		}
		out = append(out, strings.TrimSuffix(n, Extension))
		if len(out) > 4*MaxFiles {
			break
		}
	}
	return out, nil
}
