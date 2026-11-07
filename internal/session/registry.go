package session

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// ConnectionType is the seam #115 extends: each type validates its Spec and
// emits its attach SQL. Rule (plan §4.2): a DSN or credential never appears in
// an ATTACH string, and every identifier and literal goes through QuoteIdent /
// QuoteLiteral. v1 of the seam only knows the file-based duckdb type, which
// needs no secret.
type ConnectionType interface {
	Name() string
	// Validate checks the Spec's type-specific fields (no network).
	Validate(Spec) error
	// AttachSQL returns the statements that attach the catalog.
	AttachSQL(Spec) ([]string, error)
	// Remote reports whether the catalog's schema can change outside this
	// process (the schema cache then trusts only explicit invalidation).
	Remote() bool
}

// SecretStep is one CREATE TEMPORARY SECRET a type needs before its attach.
// Name is a secret name (ValidateName), Type DuckDB's secret type.
type SecretStep struct {
	Name   string
	Type   string
	Params []SecretParam
}

// CredentialedType is implemented by types that reach a remote service: they
// need an extension, usually a credential, and emit secret DDL separately from
// the attach statement so that no credential can ever appear in an ATTACH
// string (plan §4.2).
type CredentialedType interface {
	ConnectionType
	// Extensions lists the DuckDB extensions the type needs (names as
	// duckdb_extensions() knows them, or their aliases). The session loads
	// them when installed and reports extension-missing when not; it never
	// installs on its own (consent).
	Extensions() []string
	// NeedsCredential reports whether Connect must resolve a credential.
	NeedsCredential(Spec) bool
	// SecretSteps returns the secret DDL for the resolved credential. The
	// session runs it on its dedicated non-logging path.
	SecretSteps(Spec, secrets.Credential) ([]SecretStep, error)
}

// SpecExtensionsType is implemented by a CredentialedType whose extensions
// depend on the connection (DuckLake: sqlite for a SQLite catalog, httpfs for a
// catalog or data path in object storage). The session gates a connect on
// these, in order, instead of Extensions(), which stays the type's base list
// (what the person-facing install flows and the install allow-list read).
type SpecExtensionsType interface {
	CredentialedType
	SpecExtensions(Spec) []string
}

// ExtensionsFor is the extensions ct needs to connect spec, in install order.
func ExtensionsFor(ct CredentialedType, spec Spec) []string {
	if st, ok := ct.(SpecExtensionsType); ok {
		return st.SpecExtensions(spec)
	}
	return ct.Extensions()
}

// CataloglessType is implemented by storage connections (s3, gcs, r2): they
// only create secrets and attach no catalog.
type CataloglessType interface {
	ConnectionType
	Catalogless() bool
}

// NamespaceCatalogType is implemented by a catalog type with namespaces and no
// default schema (an Iceberg catalog): USE <catalog> alone fails, so a pinned
// connection USEs <catalog>.<namespace>. DefaultNamespace is the one the
// definition configures ("" when it names none).
type NamespaceCatalogType interface {
	ConnectionType
	DefaultNamespace(Spec) string
}

// StorageScopeType is implemented by the storage connections (s3, gcs, r2,
// azure) whose Scope is the URL prefix agent reads are allowed inside. It is
// the only way a connection widens what the agent may read: a catalog-less
// type that does not implement it (an iceberg or delta table) exports no scope
// whatever its definition says.
type StorageScopeType interface {
	ConnectionType
	StorageScope() bool
}

// PostAttacher is implemented by types that must vet what they attached before
// it is usable (DuckLake: its catalog names the files it reads). When the check
// fails the session detaches the catalog and reports the connection as an error.
type PostAttacher interface {
	ConnectionType
	// PostAttach runs after the ATTACH statements, on the admin connection.
	PostAttach(ctx context.Context, q Querier, spec Spec) error
}

// PreAttacher is implemented by types that must vet what they are about to
// attach before any ATTACH statement runs (DuckLake: a crafted catalog file can
// hold views or macros that DuckLake would query as its own metadata tables).
// When the check fails nothing is attached and the connection is reported as an
// error. It runs on the admin connection, after the secrets and before the
// attach statements.
type PreAttacher interface {
	ConnectionType
	PreAttach(ctx context.Context, q QueryExecer, spec Spec) error
}

// Stager is implemented by types that must read a remote file once and work
// from that copy (DuckLake: a catalog in object storage). The server could
// answer a later read with different bytes than the one that was vetted, so the
// vetting and the attach must both see one private snapshot. Stage runs on the
// admin connection after the secrets and before AttachSQL, PreAttach and
// PostAttach, which all get the spec it returns. dir is a private (0700),
// empty, per-connection folder that the session removes when the connection
// detaches, fails to connect or the session closes.
type Stager interface {
	ConnectionType
	Stage(ctx context.Context, q QueryExecer, spec Spec, dir string) (Spec, error)
}

// QueryExecer is the surface PreAttach gets: reads, plus the statements to open
// and close a scratch attachment.
type QueryExecer interface {
	Querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Querier is the read surface PostAttach gets.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// HubTokenType is implemented by MotherDuck: its token is a hub-wide setting
// that must be in place before the configuration is locked (spike #107).
type HubTokenType interface {
	ConnectionType
	UsesHubToken() bool
}

// Refresher is implemented by remote types whose catalog caches its own
// metadata (Postgres: CALL pg_clear_cache()).
type Refresher interface {
	RefreshSQL() string
}

// Registry maps type names to implementations.
type Registry struct {
	types map[string]ConnectionType
}

// NewRegistry returns a registry holding the built-in types.
func NewRegistry() *Registry {
	r := &Registry{types: map[string]ConnectionType{}}
	r.Register(duckdbFileType{})
	return r
}

// Register adds or replaces a type.
func (r *Registry) Register(t ConnectionType) { r.types[t.Name()] = t }

// Get returns a type by name.
func (r *Registry) Get(name string) (ConnectionType, error) {
	t, ok := r.types[name]
	if !ok {
		return nil, fmt.Errorf("unknown connection type %q", name)
	}
	return t, nil
}

// Names lists registered type names.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.types))
	for n := range r.types {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// duckdbFileType attaches a local DuckDB file: ATTACH 'path' AS name (READ_ONLY).
type duckdbFileType struct{}

func (duckdbFileType) Name() string { return "duckdb" }
func (duckdbFileType) Remote() bool { return false }

func (duckdbFileType) Validate(s Spec) error {
	if s.Path == "" {
		return fmt.Errorf("duckdb connection %q needs a path", s.Name)
	}
	return nil
}

func (duckdbFileType) AttachSQL(s Spec) ([]string, error) {
	if s.ReadOnly {
		// A read-only attach of a missing file is a confusing DuckDB error.
		if _, err := os.Stat(s.Path); err != nil {
			return nil, fmt.Errorf("database file for %q is not readable: %w", s.Name, err)
		}
	}
	stmt := "ATTACH " + QuoteLiteral(s.Path) + " AS " + QuoteIdent(s.Name)
	if s.ReadOnly {
		stmt += " (READ_ONLY)"
	}
	return []string{stmt}, nil
}
