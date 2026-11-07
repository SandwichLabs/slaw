package session

import (
	"github.com/SandwichLabs/slaw/internal/secrets"
	"log/slog"
	"sync/atomic"
	"time"
)

// Option configures New, Wrap and OpenDB.
type Option func(*config)

type config struct {
	logger        *slog.Logger
	redactor      *secrets.Redactor
	tempDir       string        // DuckDB temp_directory (spill files)
	extensionDir  string        // app-private extension_directory
	secretDir     string        // secret_directory (SET; DSN ignores it)
	defaultTO     time.Duration // default statement timeout (0 = none)
	defaultRowCap int           // default row cap (0 = none)
	skipLock      bool          // tests only: leave lock_configuration off
	extraTypes    []ConnectionType
	clock         func() time.Time // nil: time.Now
	onStatus      func(name string)
	mdToken       string // MotherDuck token to SET before the lock ("" = none)
	mdSet         bool
	md            *mdOpen // set by applyHardening

	// listing marks the private listing session (ListObjects): it keeps S3
	// globbing on and must only ever list one vetted scope (#138).
	listing bool
	// lazyStorage is set by applyHardening when httpfs was not loaded at open:
	// the lock then lets the URL-parameter setting through (allowed_configs).
	lazyStorage bool
	// httpTimeout and httpRetries override the httpfs limits (0 = the defaults).
	httpTimeout, httpRetries int

	// initCatalog, once set by SetDefaultCatalog, is USEd on every new pooled
	// connection of an owned hub so the pool's default catalog follows it.
	initCatalog atomic.Pointer[string]
}

func newConfig(opts []Option) *config {
	c := &config{logger: slog.Default(), redactor: secrets.NewRedactor()}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *config) httpLimits() httpLimits {
	l := defaultHTTPLimits()
	if c == nil {
		return l
	}
	l.atMost = false // this session knows its own limits: set them exactly
	if c.httpTimeout > 0 {
		l.timeout = c.httpTimeout
	}
	if c.httpRetries > 0 {
		l.retries = c.httpRetries
	}
	return l
}

// WithHTTPLimits sets httpfs's http_timeout (seconds) and http_retries for a
// session whose httpfs is loaded while it opens (0 keeps a default). A session
// whose httpfs loads later uses the defaults.
func WithHTTPLimits(timeoutSeconds, retries int) Option {
	return func(c *config) { c.httpTimeout, c.httpRetries = timeoutSeconds, retries }
}

// withListing marks the private listing session (see ObjectLister).
func withListing() Option { return func(c *config) { c.listing = true } }

// WithLogger sets the logger. The session never logs SQL text or attach
// parameters, only connection names and statuses.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithTempDirectory pins DuckDB's temp_directory (spill files). The caller
// owns the directory. Without it New creates a per-pid directory and removes
// it on Close.
func WithTempDirectory(dir string) Option { return func(c *config) { c.tempDir = dir } }

// WithExtensionDirectory pins the app-private extension_directory so
// extensions in the user's ~/.duckdb are never loadable (spike finding G).
func WithExtensionDirectory(dir string) Option { return func(c *config) { c.extensionDir = dir } }

// WithSecretDirectory pins secret_directory. Persistent secrets are disabled
// anyway; this is defence in depth, and it is SET (not DSN) because the DSN
// silently ignores it (spike finding J).
func WithSecretDirectory(dir string) Option { return func(c *config) { c.secretDir = dir } }

// WithDefaultRowCap sets the default Rows cap (0 = none).
func WithDefaultRowCap(n int) Option { return func(c *config) { c.defaultRowCap = n } }

// QueryOption tunes one Query/Exec/Conn call.
type QueryOption func(*callConfig)

type callConfig struct {
	timeout    time.Duration
	hasTimeout bool
	rowCap     int
	hasRowCap  bool
	catalog    string
}

// WithTimeout overrides the statement timeout for one call (0 disables it).
func WithTimeout(d time.Duration) QueryOption {
	return func(c *callConfig) { c.timeout, c.hasTimeout = d, true }
}

// WithRowCap overrides the row cap for one Query (0 disables it).
func WithRowCap(n int) QueryOption {
	return func(c *callConfig) { c.rowCap, c.hasRowCap = n, true }
}

// WithCatalog runs the call with this catalog as the default for unqualified
// names, on a pinned connection (USE is per connection). Unset means the
// session default. Per-connection timeout/row-cap defaults of that catalog's
// Spec apply unless overridden.
func WithCatalog(name string) QueryOption { return func(c *callConfig) { c.catalog = name } }

// WithRedactor shares a redactor with the session, so secrets another
// component resolved are scrubbed from the session's errors and logs. Without
// it the session keeps its own, fed by CreateTemporarySecret.
func WithRedactor(r *secrets.Redactor) Option {
	return func(c *config) {
		if r != nil {
			c.redactor = r
		}
	}
}

// WithConnectionTypes registers connection types (postgres, motherduck, ...)
// beside the built-in duckdb file type. internal/connections supplies them.
func WithConnectionTypes(types ...ConnectionType) Option {
	return func(c *config) { c.extraTypes = append(c.extraTypes, types...) }
}

// WithMotherDuckToken gives the hub the MotherDuck token of the workspace's
// one MotherDuck account. Before the configuration is locked the session
// removes MOTHERDUCK_TOKEN / motherduck_token from the process environment
// (an ambient token would otherwise be picked up silently at LOAD, spike
// finding H), loads the extension when it is installed and SETs the token.
// The extension is never installed here: without it the token is held back
// and the first MotherDuck connect reports extension-missing, then a relaunch
// requirement. Do not pass an empty token; omit the option instead.
func WithMotherDuckToken(token string) Option {
	return func(c *config) { c.mdToken, c.mdSet = token, token != "" }
}

// WithClock replaces the wall clock the hub compares credential deadlines with
// (tests).
func WithClock(now func() time.Time) Option {
	return func(c *config) { c.clock = now }
}

// WithStatusListener calls f(name) when a connection's state changes on its
// own, not because a caller asked: a storage secret that could not be renewed
// and signed the connection out during somebody's statement. f runs on the
// goroutine that noticed, with no hub lock held; it must not block.
func WithStatusListener(f func(name string)) Option {
	return func(c *config) { c.onStatus = f }
}
