package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SandwichLabs/slaw/internal/filelock"
	"github.com/SandwichLabs/slaw/internal/slawlocal"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// LockFile is the hub lock's name inside the workspace's .slaw-local/.
const LockFile = "hub.lock"

// ControlSigusr1 is the Holder.Control value of a holder that releases the hub
// when it receives SIGUSR1.
const ControlSigusr1 = "sigusr1"

var (
	// ErrHubHeld is wrapped by the error AcquireHub returns when another
	// process owns the hub.
	ErrHubHeld = errors.New("workspace is already open in another Slaw process")
	// ErrNotHeld means nobody holds the hub lock right now.
	ErrNotHeld = errors.New("nobody holds this workspace's hub")
	// ErrUntrusted means a hub.lock exists and is held, but its content cannot
	// be trusted (wrong owner, loose permissions, not a regular file,
	// non-loopback endpoint).
	ErrUntrusted = errors.New("the hub lock file cannot be trusted")
)

// MCPEndpoint is where a holder serves MCP: a loopback SSE URL and the bearer
// token for it. The token is a secret; never log or print it.
type MCPEndpoint struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Caps lists optional protocol features the server supports, e.g.
	// CapSandboxHeader. A proxy that needs one refuses an owner without it.
	Caps []string `json:"caps,omitempty"`
	// Pending means the holder is starting up and will publish its endpoint
	// shortly; URL and Token are empty. A second client waits instead of
	// failing.
	Pending bool `json:"pending,omitempty"`
}

// CapSandboxHeader: the server honours an X-Slaw-Sandbox header on the SSE
// connection and applies it tighten-only for that session.
const CapSandboxHeader = "sandbox-header"

// Has reports whether the endpoint advertises capability c.
func (e MCPEndpoint) Has(c string) bool {
	for _, x := range e.Caps {
		if x == c {
			return true
		}
	}
	return false
}

// String redacts the token so an endpoint is safe to log by accident.
func (e MCPEndpoint) String() string { return e.URL }

// Holder is what the owning process writes into hub.lock.
type Holder struct {
	App  string `json:"app"`
	PID  int    `json:"pid"`
	Host string `json:"host,omitempty"`
	// MCP is set while the holder serves MCP.
	MCP *MCPEndpoint `json:"mcp,omitempty"`
	// Control is how to ask the holder to release: ControlSigusr1, or "".
	Control string `json:"control,omitempty"`
}

// String renders the holder for messages, e.g. "Slaw (pid 4242)".
func (h Holder) String() string {
	s := fmt.Sprintf("%s (pid %d)", h.App, h.PID)
	if h.Host != "" {
		if name, err := os.Hostname(); err != nil || h.Host != name {
			s += " on " + h.Host
		}
	}
	return s
}

// HeldError is returned by AcquireHub when another process owns the hub.
// Holder is nil when its lock file could not be read or trusted.
type HeldError struct {
	Root   string
	Holder *Holder
}

func (e *HeldError) Error() string {
	who := "another Slaw process"
	if e.Holder != nil {
		who = e.Holder.String()
	}
	return fmt.Sprintf("workspace %s is already open in %s", filepath.Base(e.Root), who)
}

func (e *HeldError) Unwrap() error { return ErrHubHeld }

// LockPath is where the hub lock for the workspace rooted at root lives.
func LockPath(root string) string {
	return filepath.Join(root, workspace.LocalDir, LockFile)
}

// Option configures AcquireHub.
type Option func(*options)

type options struct {
	onRelease func()
	pending   bool
	noRetry   bool // TakeOver polls itself: no wait for a probe to clear
}

// withoutRetry makes an acquire report a held lock at once.
func withoutRetry() Option { return func(o *options) { o.noRetry = true } }

// WithPendingMCP publishes mcp {pending:true} in the very first write of
// hub.lock, so a second `slaw mcp serve` that races the owner's startup waits
// for the endpoint instead of failing.
func WithPendingMCP() Option { return func(o *options) { o.pending = true } }

// WithReleaseHandler makes the holder releasable: it advertises
// ControlSigusr1 and calls fn (once, on its own goroutine) when another
// process asks it to let go via RequestRelease. fn must close whatever uses
// the hub and then call Hold.Release; the lock is not released for it.
func WithReleaseHandler(fn func()) Option { return func(o *options) { o.onRelease = fn } }

// Hold is the held hub lock.
type Hold struct {
	mu     sync.Mutex
	lock   *filelock.Lock
	dir    *os.File // .slaw-local, held open: the lock is created and removed relative to it
	holder Holder
	stop   func() // stops the release-signal listener
}

// AcquireHub takes the workspace's hub lock, non-blocking. app names the
// holder ("Slaw", "slaw mcp serve"). When another process owns it the
// error is a *HeldError (errors.Is ErrHubHeld) naming the holder.
func AcquireHub(ws workspace.Workspace, app string, opts ...Option) (*Hold, error) {
	return acquireAt(ws.Root(), app, opts...)
}

func acquireAt(root, app string, opts ...Option) (*Hold, error) {
	var o options
	for _, f := range opts {
		f(&o)
	}
	dir, err := openLocalDir(root, true)
	if err != nil {
		return nil, err
	}
	if _, err := slawlocal.Ensure(root); err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("failed to prepare %s: %w", workspace.LocalDir, err)
	}
	h := Holder{App: app, PID: os.Getpid()}
	if name, err := os.Hostname(); err == nil {
		h.Host = name
	}
	if o.pending {
		h.MCP = &MCPEndpoint{Pending: true}
	}
	if o.onRelease != nil && releaseSupported {
		h.Control = ControlSigusr1
	}
	payload, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("failed to encode the hub holder: %w", err)
	}
	// Listen before the lock (and its "control" advertisement) becomes
	// visible: a release request that arrives early must find a handler, not
	// Go's default SIGUSR1 action (terminate).
	stop := func() {}
	if h.Control != "" {
		stop, err = listenForRelease(o.onRelease)
		if err != nil {
			closeDir(dir)
			return nil, err
		}
	}
	budget := acquireRetryBudget
	if o.noRetry {
		budget = 0
	}
	l, err := acquireWithRetry(dir, payload, budget)
	if err != nil {
		stop()
		closeDir(dir)
		if errors.Is(err, filelock.ErrHeld) {
			holder, _ := ReadHolder(root) // best effort: nil when untrusted
			return nil, &HeldError{Root: root, Holder: holder}
		}
		return nil, fmt.Errorf("failed to take the workspace's hub lock: %w", err)
	}
	return &Hold{lock: l, dir: dir, holder: h, stop: stop}, nil
}

// Holder is a copy of what this hold has published.
func (h *Hold) Holder() Holder {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.holder
}

// SetMCP publishes the MCP endpoint in hub.lock (a nil endpoint clears it).
func (h *Hold) SetMCP(ep *MCPEndpoint) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lock == nil {
		return errors.New("the hub lock is released")
	}
	next := h.holder
	next.MCP = ep
	payload, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("failed to encode the hub holder: %w", err)
	}
	if err := h.lock.Write(payload); err != nil {
		return err
	}
	h.holder = next
	return nil
}

// Release gives up the hub and removes hub.lock. Safe to call more than once.
func (h *Hold) Release() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lock == nil {
		return nil
	}
	h.stop()
	err := h.lock.Release()
	h.lock = nil
	closeDir(h.dir)
	return err
}

// ReadHolder learns who owns the workspace's hub, and how to reach them,
// without holding the lock. It returns ErrNotHeld when nobody holds the lock
// (whatever a leftover file says), and an error wrapping ErrUntrusted when the
// file fails the trust checks in the package doc.
func ReadHolder(root string) (*Holder, error) {
	dir, err := openLocalDir(root, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotHeld
		}
		return nil, err
	}
	defer closeDir(dir)
	// Everything below is relative to the directory we just checked, so
	// swapping .slaw-local for a symlink now changes nothing.
	f, err := filelock.OpenAt(dir, LockFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotHeld
		}
		if isSymlinkErr(err) {
			return nil, fmt.Errorf("%w: %s is a symbolic link", ErrUntrusted, LockFile)
		}
		return nil, fmt.Errorf("failed to read the hub lock: %w", err)
	}
	defer f.Close() //nolint:errcheck
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to read the hub lock: %w", err)
	}
	if err := checkTrusted(fi); err != nil {
		return nil, err
	}
	held, err := filelock.ProbeAt(dir, LockFile)
	if err != nil {
		if errors.Is(err, filelock.ErrUnsafe) {
			return nil, fmt.Errorf("%w: %w", ErrUntrusted, err)
		}
		return nil, err
	}
	if !held {
		return nil, ErrNotHeld
	}
	var h Holder
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		var data []byte
		data, lastErr = io.ReadAll(io.NewSectionReader(f, 0, 64<<10))
		if lastErr == nil {
			if lastErr = json.Unmarshal(data, &h); lastErr == nil {
				break
			}
		}
		// The holder rewrites the file in place; a torn read parses as junk.
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: unreadable content", ErrUntrusted)
	}
	if h.MCP != nil && !h.MCP.Pending {
		if err := validateEndpoint(*h.MCP); err != nil {
			return nil, err
		}
	}
	return &h, nil
}

// validateEndpoint accepts only a plain-http loopback URL: a lock file must
// never be able to send the bridge (and its token) anywhere else.
func validateEndpoint(ep MCPEndpoint) error {
	u, err := url.Parse(ep.URL)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return fmt.Errorf("%w: the MCP address is not a local http address", ErrUntrusted)
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("%w: the MCP address is not a loopback address", ErrUntrusted)
		}
	}
	if ep.Token == "" {
		return fmt.Errorf("%w: the MCP endpoint has no token", ErrUntrusted)
	}
	return nil
}

// acquireRetryBudget and acquireRetryStep bound how long Open waits to tell a
// concurrent ReadHolder probe from a real holder. ProbeAt holds a shared flock
// for one LOCK_SH/LOCK_UN pair (ReadHolder's torn-read retries run unlocked),
// so the window is tiny, but a probing process that is descheduled inside it
// can stretch it; the budget leaves generous room for that. A real holder
// costs one budget of waiting, so callers that poll (TakeOver) pass zero.
const (
	acquireRetryBudget = 250 * time.Millisecond
	acquireRetryStep   = 25 * time.Millisecond
)

// acquireWithRetry takes the hub lock, retrying ErrHeld for the retry budget;
// a holder still there after it is a real one.
func acquireWithRetry(dir *os.File, payload []byte, budget time.Duration) (*filelock.Lock, error) {
	deadline := time.Now().Add(budget)
	for {
		l, err := filelock.AcquireAt(dir, LockFile, payload)
		if !errors.Is(err, filelock.ErrHeld) || !time.Now().Before(deadline) {
			return l, err
		}
		time.Sleep(acquireRetryStep)
	}
}
