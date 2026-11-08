package session

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = QuoteLiteral(it)
	}
	return strings.Join(q, ", ")
}

// Why this exists (#135). When httpfs loads, DuckDB copies the host's AWS_*
// environment (key id, secret, session token, region) into its s3_* settings,
// even in a lock_configuration session, and httpfs is loaded lazily, after the
// lock, so those settings can no longer be cleared with SET. A URL that no
// scoped secret covers would then be read, and signed, as the host's AWS
// identity.
//
// Secrets win over settings, and the secret with the longest matching scope
// wins. So every session that loads httpfs gets one lowest-priority "deny"
// secret per storage scheme: empty keys and an endpoint that never resolves
// (RFC 2606 .invalid). An uncovered URL fails to connect and is never signed.
// The endpoint alone is not the protection: httpfs lets a URL's own ?s3_endpoint=
// override it, so the secret also writes SESSION_TOKEN and REGION explicitly
// (DuckDB fills any omitted field from the host-filled settings). With empty
// keys and nothing host-derived left to fill in, nothing of the host's can be
// sent wherever a request ends up.
// The scope is the bare scheme ("s3", not "s3://"), one character group
// shorter than any real scope, so a connection whose scope is the whole
// scheme still wins instead of tying with the deny secret (a tie goes to the
// secret created first, which would be an accident of ordering).

// Why URL parameters are switched off (#138). httpfs reads ?s3_endpoint=,
// ?s3_use_ssl=, ?s3_url_style=, ?s3_access_key_id=, ... from an s3://, s3a://,
// s3n://, r2://, gcs:// or gs:// URL and lets them override the matching
// secret's own settings, after the secret has been chosen by the URL's scope. A
// URL inside a connection's scope that carries ?s3_endpoint=attacker is still
// signed with that connection's key id and session token, and the request goes
// to the attacker. Such a URL can come from data Slaw cannot vet (the data-file
// paths in an Iceberg manifest, a DuckLake catalog) or from SQL a person runs.
//
// The only switch in DuckDB 1.5.6 that stops it is the global setting
// s3_url_compatibility_mode: the whole URL is then the key, "?" and "*"
// included. (The secret parameter URL_COMPATIBILITY_MODE exists but httpfs
// stores it under a name its reader never asks for, so it does nothing.) It
// also turns S3 globbing off. Every session that loads httpfs sets it.
//
// Accepted residuals: a microsecond window between a lazy LOAD httpfs and the
// SET, and a SET SESSION on one pooled connection (only reachable by SQL the
// editor guard refuses).
//
// The setting belongs to httpfs, so it can be set only once httpfs is loaded,
// and httpfs is often loaded after lock_configuration. A session whose httpfs
// is not loaded when it opens therefore allows exactly this one setting through
// the lock (allowed_configs), and the load path sets and reads it back
// (sealURLParams). The statements usersql.CheckManaged lets through refuse to
// change it.
const urlParamSetting = "s3_url_compatibility_mode"

// Why the HTTP limits are set here (lakehouse S0 item 11). A context cancel does
// not interrupt a hung REST catalog or object-store request during a bind or a
// scan; only httpfs's own http_timeout (seconds) bounds it, and http_retries
// multiplies it (DuckDB 1.5.6: http_timeout = 2 failed after about 9.5 s with the
// default retries). Both belong to httpfs, so like the URL setting they exist
// only once it is loaded, are let through the lock when it is not loaded yet
// (allowed_configs), and are set and read back where httpfs is loaded.
const (
	httpTimeoutSetting = "http_timeout"
	httpRetriesSetting = "http_retries"

	// DefaultHTTPTimeoutSeconds is the per-request bound, in seconds.
	DefaultHTTPTimeoutSeconds = 30
	// DefaultHTTPRetries is how often a failed request is repeated.
	DefaultHTTPRetries = 1
)

// httpLimits are the two values a session sets.
type httpLimits struct{ timeout, retries int }

func defaultHTTPLimits() httpLimits {
	return httpLimits{timeout: DefaultHTTPTimeoutSeconds, retries: DefaultHTTPRetries}
}

// hubLimits holds the limits each hub was configured with, keyed by its
// database, so a load path that only has the *sql.DB (EnsureExtension,
// DenyUncoveredStorage from Studio) re-sets the same values the hub opened with
// instead of different ones. The entry is removed when the hub closes. A path
// holding only a *sql.Conn (the importer) uses the defaults.
var hubLimits sync.Map // *sql.DB -> httpLimits

func limitsFor(q storageQuerier) httpLimits {
	if db, ok := q.(*sql.DB); ok {
		if l, ok := hubLimits.Load(db); ok {
			if lim, ok := l.(httpLimits); ok {
				return lim
			}
		}
	}
	return defaultHTTPLimits()
}

// lazyAllowedConfigs is the list lock_configuration lets through before httpfs loads.
func lazyAllowedConfigs() []string {
	return []string{urlParamSetting, httpTimeoutSetting, httpRetriesSetting}
}

// denyEndpoint never resolves.
const denyEndpoint = "deny.invalid"

// denySecrets are the catch-all secrets. Names contain a hyphen, which a
// connection name cannot, so no connection's own secret ("slaw_<name>")
// can share one.
var denySecrets = []struct{ name, kind, scope, region string }{
	{"slaw-deny-s3", "s3", "s3", "us-east-1"}, // also s3a:// and s3n://
	{"slaw-deny-r2", "r2", "r2", "auto"},
	{"slaw-deny-gcs", "gcs", "gcs", "auto"},
	{"slaw-deny-gs", "gcs", "gs", "auto"},
}

// denyStatements is the DDL, a pure function so it is testable without httpfs.
func denyStatements() []string {
	out := make([]string, 0, len(denySecrets))
	for _, d := range denySecrets {
		out = append(out, "CREATE TEMPORARY SECRET IF NOT EXISTS "+QuoteIdent(d.name)+
			" (TYPE "+d.kind+", KEY_ID '', SECRET '', SESSION_TOKEN '', REGION "+QuoteLiteral(d.region)+
			", ENDPOINT "+QuoteLiteral(denyEndpoint)+", SCOPE "+QuoteLiteral(d.scope)+")")
	}
	return out
}

// storageQuerier is what the deny step needs; *sql.DB and *sql.Conn both have it.
type storageQuerier interface {
	Execer
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// denyMu serialises the deny step: two first creations racing on different
// connections fail with a catalog write-write conflict even with IF NOT EXISTS.
var denyMu sync.Mutex

// DenyUncoveredStorage protects a session once httpfs is loaded: it turns URL
// parameters and globs off (#138) and creates the catch-all deny secrets
// (#135). It is idempotent and a no-op until httpfs is loaded, so every place
// that loads an extension calls it afterwards. It runs on a connection of the
// session being protected, because settings and secrets belong to the DuckDB
// instance.
func DenyUncoveredStorage(ctx context.Context, q storageQuerier) error {
	return protectStorage(ctx, q, true, limitsFor(q))
}

// protectStorage is DenyUncoveredStorage; sealParams false leaves URL
// parameters and globs on, for the private listing session only.
func protectStorage(ctx context.Context, q storageQuerier, sealParams bool, lim httpLimits) error {
	denyMu.Lock()
	defer denyMu.Unlock()
	_, loaded, err := extensionState(ctx, q, "httpfs")
	if err != nil {
		return fmt.Errorf("failed to inspect extensions: %w", err)
	}
	if !loaded {
		return nil
	}
	if sealParams {
		if err := sealURLParams(ctx, q); err != nil {
			return err
		}
	}
	if err := setHTTPLimits(ctx, q, lim); err != nil {
		return err
	}
	for _, stmt := range denyStatements() {
		if _, err := q.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to protect storage reads from host credentials: %w", err)
		}
	}
	return nil
}

// setHTTPLimits sets http_timeout and http_retries and reads them back; a
// session that cannot prove them fails closed (a hung endpoint would hang it).
// Every check sets both GLOBAL and SESSION unconditionally: a value another
// pooled connection or a lazy session's SQL changed is put back, and a
// connection's own override cannot shadow the global one. A locked session that
// never let the settings through refuses the SET, and then they cannot have
// changed: the read-back decides. The read-back is this connection's value (it
// shows the session scope), so the global value is proven by its SET not failing.
func setHTTPLimits(ctx context.Context, q storageQuerier, lim httpLimits) error {
	for _, st := range []struct {
		name string
		want int
	}{{httpTimeoutSetting, lim.timeout}, {httpRetriesSetting, lim.retries}} {
		want := strconv.Itoa(st.want)
		var setErr error
		for _, scope := range []string{"GLOBAL ", "SESSION "} {
			if _, err := q.ExecContext(ctx, "SET "+scope+st.name+" = "+want); err != nil && setErr == nil {
				setErr = err
			}
		}
		var got string
		if err := q.QueryRowContext(ctx, "SELECT CAST(current_setting("+QuoteLiteral(st.name)+") AS VARCHAR)").Scan(&got); err != nil {
			return fmt.Errorf("failed to read back %s: %w", st.name, err)
		}
		if got != want {
			if setErr != nil {
				return fmt.Errorf("%s did not take (%q, want %q); refusing to read object storage: %w", st.name, got, want, setErr)
			}
			return fmt.Errorf("%s did not take (%q, want %q); refusing to read object storage", st.name, got, want)
		}
	}
	return nil
}

// sealURLParams turns URL parameters and globs off for every S3-like scheme
// and reads the setting back: a session that cannot prove it fails closed.
func sealURLParams(ctx context.Context, q storageQuerier) error {
	read := func() (string, error) {
		var got string
		err := q.QueryRowContext(ctx, "SELECT CAST(current_setting("+QuoteLiteral(urlParamSetting)+") AS VARCHAR)").Scan(&got)
		return got, err
	}
	got, err := read()
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", urlParamSetting, err)
	}
	if strings.EqualFold(got, "true") {
		// Whichever pooled connection q is, a session-level override on it would
		// shadow the global value; best effort (a sealed, locked session refuses).
		_, _ = q.ExecContext(ctx, "SET SESSION "+urlParamSetting+" = true")
	}
	if !strings.EqualFold(got, "true") {
		// Only when it is not already on: once the configuration is locked
		// and the setting sealed, a second SET would be refused.
		if _, err := q.ExecContext(ctx, "SET GLOBAL "+urlParamSetting+" = true"); err != nil {
			return fmt.Errorf("failed to turn off S3 URL parameters: %w", err)
		}
		// A session-level override (PRAGMA/SET SESSION on a pooled connection)
		// would shadow the global value on that connection: set it too.
		if _, err := q.ExecContext(ctx, "SET SESSION "+urlParamSetting+" = true"); err != nil {
			return fmt.Errorf("failed to turn off S3 URL parameters: %w", err)
		}
		if got, err = read(); err != nil {
			return fmt.Errorf("failed to read back %s: %w", urlParamSetting, err)
		}
	}
	if !strings.EqualFold(got, "true") {
		return fmt.Errorf("S3 URL parameters are still on (%s=%q); refusing to read object storage", urlParamSetting, got)
	}
	return nil
}

// prepareStorage runs while a session opens, before the lock: when httpfs is
// already installed it is loaded now and protected, so there is no window in
// which it is loaded without protection. When it is not loaded, the one
// setting a later load must change is let through the lock (see above) and
// the session remembers it.
func prepareStorage(ctx context.Context, conn *sql.Conn, c *config) error {
	log := c.logger
	installed, loaded, err := extensionState(ctx, conn, "httpfs")
	if err != nil {
		return fmt.Errorf("failed to inspect extensions: %w", err)
	}
	if installed && !loaded {
		if _, err := conn.ExecContext(ctx, "LOAD httpfs"); err != nil {
			// A broken httpfs file must not stop the session opening: it is
			// reported as an error when a connection needs it. Not loaded
			// means the host settings are never filled either.
			log.Debug("httpfs could not be loaded while opening the session", "error", err)
		}
	}
	if _, loaded, err = extensionState(ctx, conn, "httpfs"); err != nil {
		return fmt.Errorf("failed to inspect extensions: %w", err)
	}
	if !loaded {
		if _, err := conn.ExecContext(ctx, "SET allowed_configs = ["+quoteList(lazyAllowedConfigs())+"]"); err != nil {
			return fmt.Errorf("hardening: failed to allow the storage URL setting: %w", err)
		}
		c.lazyStorage = true
		return nil
	}
	return protectStorage(ctx, conn, !c.listing, c.httpLimits())
}
