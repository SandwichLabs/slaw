package session

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// Session is the DuckDB hub: one DuckDB instance whose attached catalogs are
// the user's connections. Callers name data as catalog.schema.table and never
// assume the default catalog is called "main" or "memory": ask DefaultCatalog.
type Session interface {
	// DefaultCatalog is the catalog unqualified names resolve against.
	DefaultCatalog() string
	// DefaultSchema is catalog's default schema ("" catalog = DefaultCatalog).
	DefaultSchema(ctx context.Context, catalog string) (string, error)
	// Resolve fills the catalog and schema a TableRef leaves empty.
	Resolve(ctx context.Context, ref TableRef) (TableRef, error)

	// DB is the connection pool. Transitional handle for consumers that need
	// database/sql transactions (crud, importer) and for the agent SQL
	// classifier's EXPLAIN. Every pooled connection's default catalog is
	// DefaultCatalog. Do not run USE on it: use Conn / WithCatalog.
	DB() *sql.DB

	// Query runs a statement with cancellation, the statement timeout and the
	// row cap that apply (per call, per connection, then session default).
	// The caller must Close the Rows.
	Query(ctx context.Context, query string, args ...any) (*Rows, error)
	QueryWith(ctx context.Context, query string, args []any, opts ...QueryOption) (*Rows, error)
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	ExecWith(ctx context.Context, query string, args []any, opts ...QueryOption) (sql.Result, error)
	// Conn pins one connection for a run. WithCatalog issues USE on it; Close
	// restores the default before the connection returns to the pool.
	Conn(ctx context.Context, opts ...QueryOption) (*Conn, error)

	// SetDefaultCatalog makes an attached connection the catalog unqualified
	// names resolve against (the workspace's default_connection, #109). Only a
	// hub that owns its instance (New) can move it; a Wrap'd container hub keeps
	// the file as its default and returns an error. The catalog fields are
	// guarded, but connections already pinned or checked out keep the catalog
	// they started in, so call it right after attaching and before the session
	// is shared with other goroutines.
	SetDefaultCatalog(ctx context.Context, name string) error

	// Register declares a connection without dialling (lazy attach); Attach
	// registers and connects; Connect dials a registered one.
	Register(spec Spec) error
	Attach(ctx context.Context, spec Spec) error
	Connect(ctx context.Context, name string) error
	Detach(ctx context.Context, name string) error
	Connections() []Status
	// Status is the state of the connection called name.
	Status(name string) (Status, bool)
	// ConnectionInfos describes every registered connection for the agent SQL
	// policy: type, state, read-only flag, remote or local, and the storage
	// scope. No DSN, credential or path.
	ConnectionInfos() []ConnectionInfo
	// ConnectReferenced connects every registered, not yet attached connection
	// that sqlText (or the default catalog argument) names, so a query that
	// mentions pg.public.orders dials pg on first use. Opening a workspace
	// dials nothing. It returns the first connect failure (already redacted).
	ConnectReferenced(ctx context.Context, catalog, sqlText string) error
	// Preflight resolves a registered connection's credential and extension
	// state without dialling, so its status reads needs-credentials or
	// extension-missing before anyone connects.
	Preflight(ctx context.Context, name string)
	// InstallExtension downloads and loads a DuckDB extension that a registered
	// connection type needs. It is the consent step: Connect never installs.
	InstallExtension(ctx context.Context, name string) error
	// RefreshCatalog clears a remote catalog's own metadata cache
	// (Postgres: CALL pg_clear_cache()) and the session's schema cache.
	RefreshCatalog(ctx context.Context, name string) error
	// RelaunchRequired reports that a MotherDuck token could not be applied to
	// this hub (it opens with at most one account, D10) and the workspace has
	// to be reopened. The message is plain and holds no secret.
	RelaunchRequired() (bool, string)

	ListCatalogs(ctx context.Context) ([]CatalogInfo, error)
	ListSchemas(ctx context.Context, catalog string) ([]string, error)
	ListTables(ctx context.Context, catalog, schema string) ([]TableInfo, error)
	DescribeTable(ctx context.Context, qualified string) (*TableDescription, error)
	// InvalidateSchema drops cached introspection (all catalogs when name is "").
	// Session-run DDL, attach and detach invalidate on their own.
	InvalidateSchema(name string)

	// Redact scrubs every secret value this session knows about, plus
	// credential-shaped patterns (password='...'), from a message. Errors
	// the session returns are already scrubbed; use it for text that did
	// not come through the session.
	Redact(s string) string
	// Redactor is the redactor behind Redact. Give it to the secrets
	// Resolver so resolved credentials are scrubbed too.
	Redactor() *secrets.Redactor

	// CreateTemporarySecret runs CREATE OR REPLACE TEMPORARY SECRET on the
	// dedicated admin connection, under the attach mutex. Values are quoted
	// with QuoteLiteral, registered with the redactor, and neither the
	// statement nor the values are ever logged or put in an error.
	CreateTemporarySecret(ctx context.Context, name, secretType string, params []SecretParam) error
	// DropSecret drops a temporary secret (no error if it does not exist).
	DropSecret(ctx context.Context, name string) error

	Close() error
}

type entry struct {
	spec    Spec
	status  Status
	attach  bool     // currently attached
	secrets []string // temporary secrets created for it
	// depBlocked names the requirement whose state last kept it from
	// connecting ("" when none): cleared when that requirement is ready.
	depBlocked string

	// The deadline of the credential its secrets were made from (zero: none, so
	// never rotated), how many times they were made, when a rotation last
	// started, and the rotation in flight. All guarded by hub.mu.
	expires    time.Time
	secretGen  int
	lastRotate time.Time
	// lastRevive is when a signed-out connection last re-read its sign-in
	// (reviveSignedOut).
	lastRevive time.Time
	// closeRefused: a sign-out was refused (a dependent would not detach), so
	// ended keys are throttled too.
	closeRefused bool
	rot          *rotation
}

type hub struct {
	db      *sql.DB
	ownsDB  bool
	cfg     *config
	logger  *slog.Logger
	types   *Registry
	tempDir string // removed on Close when ownsTemp
	ownTemp bool

	defMu          sync.RWMutex // guards defaultCatalog and defSchemaName
	defaultCatalog string

	redactor *secrets.Redactor

	adminMu sync.Mutex // serialises ATTACH / DETACH / secret DDL

	stageMu   sync.Mutex // guards stageRoot
	stageRoot string     // private folder of the Stager snapshots ("" until one is made)

	mu    sync.RWMutex
	conns map[string]*entry

	failures map[string]failure // guarded by mu
	md       *mdOpen            // what the hub did for MotherDuck at open (nil: nothing)
	relaunch string             // guarded by mu

	gen   atomic.Int64
	cache cacheStore

	schemaMu      sync.Mutex
	defaultSchema map[string]string
	// defSchemaName is the default catalog's default schema at open, the
	// state every pooled connection must be in when it is handed back.
	defSchemaName string
}

var _ Session = (*hub)(nil)

// DefaultDirs returns the app-private extension and secret directories.
func DefaultDirs() (extensionDir, secretDir string) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("slaw-cache-%d", os.Getuid()))
	}
	root := filepath.Join(base, "slaw", "duckdb")
	return filepath.Join(root, "extensions"), filepath.Join(root, "secrets")
}

// New opens an in-memory hub (no data of its own; connections attach into it)
// with open-time hardening applied and verified. This is the shape every
// workspace will use from #109.
func New(opts ...Option) (Session, error) {
	c := newConfig(opts)
	ownTemp := false
	if c.tempDir == "" {
		d, err := os.MkdirTemp("", fmt.Sprintf("slaw-duckdb-%d-hub-*", os.Getpid()))
		if err != nil {
			return nil, fmt.Errorf("failed to create hub temp directory: %w", err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			_ = os.RemoveAll(d)
			return nil, fmt.Errorf("failed to secure hub temp directory: %w", err)
		}
		c.tempDir, ownTemp = d, true
	}
	applyDirDefaults(c)
	db, err := openDB("", c)
	if err != nil {
		if ownTemp {
			_ = os.RemoveAll(c.tempDir)
		}
		return nil, err
	}
	h, err := wrap(db, c, true)
	if err != nil {
		_ = db.Close()
		if ownTemp {
			_ = os.RemoveAll(c.tempDir)
		}
		return nil, err
	}
	h.ownTemp = ownTemp
	return h, nil
}

func applyDirDefaults(c *config) {
	ext, sec := DefaultDirs()
	if c.extensionDir == "" {
		c.extensionDir = ext
	}
	if c.secretDir == "" {
		c.secretDir = sec
	}
}

func wrap(db *sql.DB, c *config, owns bool) (*hub, error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open session connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	if err := applyHardening(ctx, conn, c); err != nil {
		return nil, err
	}
	var def, defSchema string
	if err := conn.QueryRowContext(ctx, "SELECT current_database(), current_schema()").Scan(&def, &defSchema); err != nil {
		return nil, fmt.Errorf("failed to read default catalog: %w", err)
	}
	h := newHub(db, c, def, defSchema, owns)
	h.md = c.md
	return h, nil
}

// newHub builds the hub value; def is the instance's current_database().
func newHub(db *sql.DB, c *config, def, defSchema string, owns bool) *hub {
	h := &hub{
		db: db, ownsDB: owns, cfg: c, types: NewRegistry(), redactor: c.redactor,
		logger:  slog.New(secrets.NewRedactingHandler(c.logger.Handler(), c.redactor)),
		tempDir: c.tempDir, defaultCatalog: def,
		conns:         map[string]*entry{},
		defaultSchema: map[string]string{def: defSchema},
		defSchemaName: defSchema,
	}
	if defSchema == "" {
		delete(h.defaultSchema, def)
	}
	for _, t := range c.extraTypes {
		h.types.Register(t)
	}
	h.cache.init()
	h.conns[def] = &entry{
		spec:   Spec{Name: def, Type: "duckdb", Timeout: c.defaultTO, RowCap: c.defaultRowCap},
		status: Status{Name: def, Type: "duckdb", Kind: StatusOK, Default: true},
		attach: true,
	}
	return h
}

func (h *hub) DefaultCatalog() string { return h.defCat() }

// defCat and defSchema read the default catalog and its schema under defMu,
// which SetDefaultCatalog (the only writer) holds for writing.
func (h *hub) defCat() string {
	h.defMu.RLock()
	defer h.defMu.RUnlock()
	return h.defaultCatalog
}

func (h *hub) defSchema() string {
	h.defMu.RLock()
	defer h.defMu.RUnlock()
	return h.defSchemaName
}
func (h *hub) DB() *sql.DB { return h.db }

// Close releases the session. A hub that owns its DuckDB instance closes it
// and removes its temp directory; a wrapped instance is left to its owner.
func (h *hub) Close() error {
	var first error
	if h.ownsDB {
		if err := h.db.Close(); err != nil {
			first = fmt.Errorf("failed to close hub: %w", err)
		}
	}
	h.removeStages() // after the database is closed: an open file cannot be deleted on Windows
	if h.ownTemp && h.tempDir != "" {
		if err := os.RemoveAll(h.tempDir); err != nil && first == nil {
			first = fmt.Errorf("failed to remove hub temp directory: %w", err)
		}
	}
	return first
}

// --- running statements ---

func (h *hub) callCfg(opts []QueryOption) callConfig {
	var cc callConfig
	for _, o := range opts {
		o(&cc)
	}
	catalog := cc.catalog
	if catalog == "" {
		catalog = h.defCat()
	}
	e, _ := h.entryOf(catalog)
	if !cc.hasTimeout {
		cc.timeout = h.cfg.defaultTO
		if e != nil && e.spec.Timeout > 0 {
			cc.timeout = e.spec.Timeout
		}
	}
	if !cc.hasRowCap {
		cc.rowCap = h.cfg.defaultRowCap
		if e != nil && e.spec.RowCap > 0 {
			cc.rowCap = e.spec.RowCap
		}
	}
	if cc.catalog == h.defCat() {
		cc.catalog = ""
	}
	return cc
}

func (h *hub) Query(ctx context.Context, query string, args ...any) (*Rows, error) {
	return h.QueryWith(ctx, query, args)
}

func (h *hub) QueryWith(ctx context.Context, query string, args []any, opts ...QueryOption) (*Rows, error) {
	cc := h.callCfg(opts)
	ctx, cancel := withTimeout(ctx, cc)
	if cc.catalog != "" {
		conn, err := h.pin(ctx, cc.catalog)
		if err != nil {
			cancel()
			return nil, err
		}
		r, err := conn.c.QueryContext(ctx, query, args...)
		if err != nil {
			_ = conn.Close()
			cancel()
			return nil, h.readErr(ctx, "failed to run query", err)
		}
		return &Rows{Rows: r, cap: cc.rowCap, cancel: cancel, release: conn.Close}, nil
	}
	// Default-catalog statements run on a pinned connection too, so a USE (or
	// SET search_path) in user SQL cannot leak into the pool: Close verifies
	// the connection is back on the default catalog and schema, restores it,
	// or discards it.
	conn, err := h.acquire(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	r, err := conn.c.QueryContext(ctx, query, args...)
	if isDDL(query) {
		h.InvalidateSchema("")
	}
	if err != nil {
		_ = conn.Close()
		cancel()
		return nil, h.readErr(ctx, "failed to run query", err)
	}
	return &Rows{Rows: r, cap: cc.rowCap, cancel: cancel, release: conn.Close}, nil
}

// acquire pins a connection on the session default catalog.
func (h *hub) acquire(ctx context.Context) (*Conn, error) {
	c, err := h.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to pin connection: %w", err)
	}
	return &Conn{c: c, h: h}, nil
}

func (h *hub) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return h.ExecWith(ctx, query, args)
}

func (h *hub) ExecWith(ctx context.Context, query string, args []any, opts ...QueryOption) (sql.Result, error) {
	cc := h.callCfg(opts)
	ctx, cancel := withTimeout(ctx, cc)
	defer cancel()
	var (
		res sql.Result
		err error
	)
	if cc.catalog != "" {
		var conn *Conn
		conn, err = h.pin(ctx, cc.catalog)
		if err != nil {
			return nil, err
		}
		defer conn.Close() //nolint:errcheck
		res, err = conn.c.ExecContext(ctx, query, args...)
	} else {
		var conn *Conn
		conn, err = h.acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close() //nolint:errcheck
		res, err = conn.c.ExecContext(ctx, query, args...)
	}
	if isDDL(query) {
		h.InvalidateSchema("")
	}
	if err != nil {
		return nil, h.readErr(ctx, "failed to execute statement", err)
	}
	return res, nil
}

// Conn pins one connection. With WithCatalog it runs USE on it.
func (h *hub) Conn(ctx context.Context, opts ...QueryOption) (*Conn, error) {
	cc := h.callCfg(opts)
	if cc.catalog != "" {
		return h.pin(ctx, cc.catalog)
	}
	conn, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	conn.cfg = cc
	return conn, nil
}

func (h *hub) pin(ctx context.Context, catalog string) (*Conn, error) {
	if err := h.checkPinnable(catalog); err != nil {
		return nil, err
	}
	c, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.Use(ctx, catalog); err != nil {
		_ = c.c.Close()
		return nil, err
	}
	return c, nil
}

// checkPinnable refuses a catalog a connection cannot be switched to: unknown,
// not attached, or storage (no catalog of its own).
func (h *hub) checkPinnable(catalog string) error {
	e, eAtt := h.entryOf(catalog)
	if e == nil || !eAtt {
		return fmt.Errorf("connection %q is not attached", catalog)
	}
	if h.catalogless(e) {
		return fmt.Errorf("connection %q is storage, not a database: read it with read_parquet('<its scope>...') instead", catalog)
	}
	return nil
}

// Use switches the pinned connection to catalog (USE), with the checks pin
// makes: the catalog must be registered, attached and not storage. Only this
// connection moves; Close restores the session default.
func (c *Conn) Use(ctx context.Context, catalog string) error {
	if err := c.h.checkPinnable(catalog); err != nil {
		return err
	}
	if nt := c.h.namespaceCatalog(catalog); nt != nil {
		return c.useNamespace(ctx, catalog, nt)
	}
	if _, err := c.c.ExecContext(ctx, "USE "+QuoteIdent(catalog)); err != nil {
		return fmt.Errorf("failed to switch to connection %q: %w", catalog, err)
	}
	c.catalog = catalog
	c.cfg = c.h.callCfg([]QueryOption{WithCatalog(catalog)})
	return nil
}

// useNamespace pins a catalog that has namespaces and no default schema (an
// Iceberg catalog). Unqualified names resolve in the configured default
// namespace, or in the catalog's only namespace; with several and none
// configured there is no stable answer (a new namespace would silently take
// unqualified names over), so it is an error that says how to fix it.
func (c *Conn) useNamespace(ctx context.Context, catalog string, nt NamespaceCatalogType) error {
	spec := c.h.specOf(catalog)
	ns := nt.DefaultNamespace(spec)
	if ns == "" {
		rows, err := c.c.QueryContext(ctx, `SELECT schema_name FROM duckdb_schemas()
			WHERE database_name = $1 AND NOT internal AND schema_name NOT IN ('information_schema', 'pg_catalog')
			ORDER BY schema_name LIMIT 2`, catalog)
		if err != nil {
			return fmt.Errorf("failed to list the namespaces of %q: %w", catalog, err)
		}
		var found []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return fmt.Errorf("failed to list the namespaces of %q: %w", catalog, err)
			}
			found = append(found, n)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("failed to list the namespaces of %q: %w", catalog, err)
		}
		switch len(found) {
		case 0:
			return fmt.Errorf("connection %q has no namespaces yet: qualify names as %s.ns.table once it has", catalog, catalog)
		case 1:
			ns = found[0]
		default:
			return fmt.Errorf("this catalog has several namespaces: qualify names as %s.ns.table, or set default_namespace on the connection %q", catalog, catalog)
		}
	}
	if _, err := c.c.ExecContext(ctx, "USE "+QuoteQualified(catalog, ns)); err != nil {
		return fmt.Errorf("failed to switch to namespace %q of connection %q: %w", ns, catalog, err)
	}
	return nil
}

// namespaceCatalog returns the catalog's type when it is a NamespaceCatalogType.
func (h *hub) namespaceCatalog(catalog string) NamespaceCatalogType {
	e, _ := h.entryOf(catalog)
	if e == nil {
		return nil
	}
	t, err := h.types.Get(e.spec.Type)
	if err != nil {
		return nil
	}
	nt, _ := t.(NamespaceCatalogType)
	return nt
}

func (h *hub) specOf(catalog string) Spec {
	if e, _ := h.entryOf(catalog); e != nil {
		return e.spec
	}
	return Spec{}
}

// Conn is one pinned connection. Not safe for concurrent use.
type Conn struct {
	c       *sql.Conn
	h       *hub
	catalog string
	cfg     callConfig
	closed  bool
}

// SQLConn exposes the pinned *sql.Conn (for BeginTx).
func (c *Conn) SQLConn() *sql.Conn { return c.c }

// Catalog is the catalog USE selected ("" = session default).
func (c *Conn) Catalog() string { return c.catalog }

// Query runs a statement on the pinned connection with its timeout and cap.
func (c *Conn) Query(ctx context.Context, query string, args ...any) (*Rows, error) {
	ctx, cancel := withTimeout(ctx, c.cfg)
	r, err := c.c.QueryContext(ctx, query, args...)
	if isDDL(query) {
		c.h.InvalidateSchema("")
	}
	if err != nil {
		cancel()
		return nil, c.h.readErr(ctx, "failed to run query", err)
	}
	return &Rows{Rows: r, cap: c.cfg.rowCap, cancel: cancel}, nil
}

// Exec runs a statement on the pinned connection.
func (c *Conn) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx, cancel := withTimeout(ctx, c.cfg)
	defer cancel()
	res, err := c.c.ExecContext(ctx, query, args...)
	if isDDL(query) {
		c.h.InvalidateSchema("")
	}
	if err != nil {
		return nil, c.h.readErr(ctx, "failed to execute statement", err)
	}
	return res, nil
}

// Follow records that the connection now works in catalog (a USE the person
// typed moved it), so later statements get that catalog's timeout and row cap,
// as after Use. It runs no SQL.
func (c *Conn) Follow(catalog string) {
	if catalog == c.h.defCat() {
		c.catalog = ""
		c.cfg = c.h.callCfg(nil)
		return
	}
	c.catalog = catalog
	c.cfg = c.h.callCfg([]QueryOption{WithCatalog(catalog)})
}

// Close verifies the connection is back on the session's default catalog and
// schema (the run may have issued USE, pinned or not), restores it if not,
// and returns it to the pool. If the check or the restore fails the connection
// is discarded, never reused.
func (c *Conn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	ctx := context.Background()
	// A run that opened a transaction and never ended it (BEGIN with no
	// COMMIT) must not leave it open on a pooled connection: later writes
	// would join it, fail with "transaction is aborted", or block CHECKPOINT.
	// Roll it back; "no transaction is active" is the normal case. A run is at
	// most one transaction, and Rows.Close releases the conn only after the
	// result is fully consumed and closed, so this never cuts a read short.
	if _, err := c.c.ExecContext(ctx, "ROLLBACK"); err != nil && !isNoTransaction(err) {
		_ = c.c.Raw(func(any) error { return driver.ErrBadConn })
		return c.c.Close()
	}
	// A TEMP macro or function belongs to this one connection, where the agent's
	// SQL policy (which looks at functions on whichever connection it is handed)
	// cannot see it, yet it can replace list_value or a reader for the next run
	// that lands here. It does not outlive the run that made it: the connection
	// is discarded. TEMP tables and views are not affected.
	var tempFuncs int
	if err := c.c.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_functions() WHERE database_name = 'temp' AND NOT internal").Scan(&tempFuncs); err != nil || tempFuncs > 0 {
		_ = c.c.Raw(func(any) error { return driver.ErrBadConn })
		return c.c.Close()
	}
	var cat, schema string
	err := c.c.QueryRowContext(ctx, "SELECT current_database(), current_schema()").Scan(&cat, &schema)
	if err != nil || cat != c.h.defCat() || schema != c.h.defSchema() {
		if _, uerr := c.c.ExecContext(ctx, "USE "+QuoteQualified(c.h.defCat(), c.h.defSchema())); uerr != nil {
			_ = c.c.Raw(func(any) error { return driver.ErrBadConn })
		}
	}
	return c.c.Close()
}
