package session

import (
	"context"
	"fmt"
	"github.com/SandwichLabs/slaw/internal/connname"
	"regexp"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// StatusKind is a connection's state (plan §4.2). Lazy attach means a freshly
// registered connection is StatusNotConnected until first use.
type StatusKind string

const (
	StatusOK               StatusKind = "ok"
	StatusNotConnected     StatusKind = "not-connected"
	StatusLockedBy         StatusKind = "locked-by"
	StatusError            StatusKind = "error"
	StatusNeedsCredentials StatusKind = "needs-credentials"
	StatusExtensionMissing StatusKind = "extension-missing"
)

// precedence is the plan §4.2 (design #106) ordering, highest first:
// extension-missing > needs-credentials > error > locked-by > not connected > ok.
var precedence = []StatusKind{
	StatusExtensionMissing, StatusNeedsCredentials, StatusError,
	StatusLockedBy, StatusNotConnected, StatusOK,
}

// Status describes one connection. Message is already redacted; it never
// carries a credential, DSN or path.
type Status struct {
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Kind        StatusKind `json:"status"`
	Message     string     `json:"message,omitempty"`
	LockedByPID int        `json:"locked_by_pid,omitempty"`
	ReadOnly    bool       `json:"read_only"`
	Default     bool       `json:"default,omitempty"`
	// Extension is the first missing extension of an extension-missing status:
	// the one download to offer next.
	Extension string `json:"extension,omitempty"`
	// ServerChanged is set on a needs-credentials status that is because the
	// definition now points at a different server than the saved sign-in was
	// made for. It carries host-level descriptions, never a secret.
	ServerChanged *ServerChange `json:"server_changed,omitempty"`
	// SignIn is set on a needs-credentials status that is because the person
	// must sign in with an AWS SSO profile (missing or expired session). It
	// carries the profile name and a reason, never an account, role or key.
	SignIn *secrets.SignInHint `json:"sign_in,omitempty"`
	// BlockedBy names the connection this one waits on: it reads with that
	// connection's secret (Spec.Requires) and that connection is not ready. A
	// needs-credentials status with BlockedBy set is the other connection's
	// sign-in to do, never this one's: the prompt, the sign-in banner and the
	// CLI hint all point at BlockedBy, and this connection carries no SignIn.
	BlockedBy string `json:"blocked_by,omitempty"`
}

// ServerChange names the server a saved sign-in was made for (Old) and the one
// the connection points at now (New).
type ServerChange struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// ConnectionInfo is what the agent SQL policy (#116) and list_connections need to
// know about a registered connection. It never carries a DSN, a credential or a
// path: Scope is a storage URL prefix the person configured, nothing more.
type ConnectionInfo struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Kind     StatusKind `json:"status"`
	ReadOnly bool       `json:"read_only"`
	// Remote is true for anything that is not a file on this computer
	// (Postgres, MotherDuck, object storage). Writes to a remote connection
	// always need the person's approval.
	Remote bool `json:"remote"`
	// Catalogless connections (s3, gcs, r2) attach no catalog; they only add a
	// scoped secret, and Scope is the URL prefix reads are allowed inside.
	Catalogless bool   `json:"catalogless,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Default     bool   `json:"default,omitempty"`
	// SignIn is set when the connection needs the person to sign in with an AWS
	// SSO profile (the profile name and why; no secret).
	SignIn *secrets.SignInHint `json:"sign_in,omitempty"`
}

// Spec declares a connection. For the duckdb type, Path is the file to attach.
// Timeout / RowCap are this connection's statement timeout and row cap
// defaults (0 = none).
type Spec struct {
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	Path     string        `json:"path,omitempty"`
	ReadOnly bool          `json:"read_only,omitempty"`
	Timeout  time.Duration `json:"-"`
	RowCap   int           `json:"-"`

	// Params are the type-specific, non-secret, already ${ENV}-resolved
	// fields (host, port, database, region, scope, options). Types read them
	// by name; they never carry a credential.
	Params map[string]string `json:"params,omitempty"`
	// Credential fetches the person's credential when the connection is used
	// (never at Register). It returns secrets.ErrNeedsCredentials,
	// *secrets.ErrFingerprintChanged or a keychain-unavailable error when
	// there is none, which the session reports as needs-credentials. Nil on a
	// type that needs none.
	Credential CredentialFunc `json:"-"`

	// Requires names connections to bring up before this one connects: a
	// DuckLake or table read from a storage URL needs the storage connection
	// whose secret reads it. A requirement that is not ready leaves this
	// connection in StatusError with a message naming it.
	Requires []string `json:"requires,omitempty"`
}

// CredentialFunc resolves a connection's credential on demand.
type CredentialFunc func(ctx context.Context) (secrets.Credential, error)

// ValidateName enforces the catalog-alias rule from plan §4.2 (the rule itself
// lives in the dependency-free internal/connname).
func ValidateName(name string) error { return connname.Validate(name) }

// CatalogInfo is one attached catalog.
type CatalogInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // DuckDB's engine type: duckdb, postgres, sqlite, ...
	ReadOnly bool   `json:"read_only"`
	Default  bool   `json:"default,omitempty"`
}

// TableInfo is one table or view.
type TableInfo struct {
	Catalog string `json:"catalog"`
	Schema  string `json:"schema"`
	Name    string `json:"name"`
	Type    string `json:"type"` // BASE TABLE | VIEW | ...
}

// Ref returns the table's qualified reference.
func (t TableInfo) Ref() TableRef {
	return TableRef{Catalog: t.Catalog, Schema: t.Schema, Name: t.Name}
}

// ColumnInfo is one column.
type ColumnInfo struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	NotNull  bool   `json:"not_null"`
	Position int    `json:"position"`
}

// TableDescription is a resolved table and its columns.
type TableDescription struct {
	Ref     TableRef     `json:"ref"`
	Columns []ColumnInfo `json:"columns"`
}

var pidRE = regexp.MustCompile(`\(PID (\d+)\)`)

// classifyAttachError maps a DuckDB ATTACH failure to a status.
func classifyAttachError(err error, redact func(string) string) (StatusKind, string, int) {
	msg := redact(err.Error())
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "could not set lock on file") {
		pid := 0
		if m := pidRE.FindStringSubmatch(msg); m != nil {
			fmt.Sscanf(m[1], "%d", &pid) //nolint:errcheck
		}
		return StatusLockedBy, msg, pid
	}
	if strings.Contains(lower, "extension") && (strings.Contains(lower, "not found") || strings.Contains(lower, "not installed") || strings.Contains(lower, "to be loaded")) {
		return StatusExtensionMissing, msg, 0
	}
	if IsExpiredToken(msg) {
		return StatusNeedsCredentials, msg, 0
	}
	if strings.Contains(lower, "password authentication failed") || strings.Contains(lower, "authentication failed") || strings.Contains(lower, "access denied for user") || strings.Contains(lower, "not authenticated") || strings.Contains(lower, "invalid token") {
		return StatusNeedsCredentials, msg, 0
	}
	return StatusError, msg, 0
}
