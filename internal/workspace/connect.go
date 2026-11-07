package workspace

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// SnapshotEnv returns an environment lookup over a copy of the process
// environment taken now. Build the secrets Resolver with it
// (secrets.WithEnv): opening a MotherDuck connection removes MOTHERDUCK_TOKEN
// from the live environment (so a stray token is never picked up silently),
// and an env:MOTHERDUCK sign-in must keep working afterwards.
func SnapshotEnv() func(string) string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return func(k string) string { return m[k] }
}

// credentialRef is the keychain reference of a connection's credential: user
// connections are keyed by name alone, workspace ones (shared and local) by
// workspace id and name. A move between those two kinds migrates the entry.
func credentialRef(wsID string, scope Scope, name string) secrets.Ref {
	if scope == ScopeUser {
		return secrets.UserRef(name)
	}
	return secrets.WorkspaceRef(wsID, name)
}

// ServerDescription is the host-level, non-secret description stored beside a
// credential for the "server changed" message.
func ServerDescription(d ConnectionDef) string {
	switch d.Type {
	case "postgres":
		port := d.Port
		if port == 0 {
			port = 5432
		}
		return net.JoinHostPort(d.Host, strconv.Itoa(port)) + "/" + d.Database
	case "motherduck":
		return "md:" + d.Database
	case "mysql":
		port := d.Port
		if port == 0 {
			port = 3306
		}
		return net.JoinHostPort(d.Host, strconv.Itoa(port)) + "/" + d.Database
	case "s3", "gcs", "r2", "azure":
		return storageServer(d)
	case "sqlite", "duckdb":
		return filepath.Base(d.Path)
	case "ducklake", "iceberg", "delta":
		if connections.IsDataURL(d.Path) {
			return d.Path
		}
		return filepath.Base(d.Path)
	}
	return d.Type
}

// storageServer describes a storage connection: its scope, plus the endpoint,
// URL style and TLS setting when they are not the defaults, so a pulled change
// that only redirects the endpoint reads as one in the "server changed" text
// ("s3://lake/ to s3://lake/ via host") instead of as no change at all.
func storageServer(d ConnectionDef) string {
	out := d.Scope
	if ep := strings.TrimSpace(d.Options["endpoint"]); ep != "" {
		out += " via " + ep
	}
	if u := strings.TrimSpace(d.Options["url_style"]); u != "" {
		out += " (" + u + " style)"
	}
	if ssl := strings.TrimSpace(d.Options["use_ssl"]); ssl != "" && ssl != "true" {
		out += " (no TLS)"
	}
	return out
}

// BindingFor is the fingerprint binding of an environment-resolved definition.
func BindingFor(d ConnectionDef) secrets.Binding {
	return secrets.Binding{Fingerprint: Fingerprint(d), Server: ServerDescription(d), Type: d.Type}
}

// toConnDef converts a resolved definition into the type registry's shape.
// path is the resolved, absolute file path for file types.
func toConnDef(d ConnectionDef, readOnly bool, path string) connections.Def {
	return connections.Def{
		Name: d.Name, Type: d.Type, Path: path, Host: d.Host, Port: d.Port, Database: d.Database,
		Region: d.Region, Scope: d.Scope, ReadOnly: readOnly, Options: d.Options,
	}
}

// signInGuard withholds an env: or aws-sso: credential unless this person's
// sign-in was chosen for exactly this definition. A keychain or prompt
// credential carries its own fingerprint (checked by the resolver); these two
// have no stored value, so the binding lives with the choice (review #115). An
// aws-sso sign-in is also refused for a definition whose endpoint is not AWS
// over TLS, however it got there (a hand-edited local.yaml).
func signInGuard(res *secrets.Resolver, c Connection, resolved ConnectionDef) error {
	if !boundSignIn(c.SignIn) {
		return nil
	}
	if c.SignInFingerprint == "" || c.SignInFingerprint != Fingerprint(resolved) {
		return &secrets.ErrFingerprintChanged{Old: c.SignInServer, New: ServerDescription(resolved)}
	}
	if err := res.CheckTarget(c.SignIn, targetOf(resolved)); err != nil {
		return fmt.Errorf("connection %q: %w", c.Name, err)
	}
	return nil
}

// ResolvedConnection is a merged connection with its ${ENV} references
// expanded and its file path resolved.
type ResolvedConnection struct {
	Connection
	Resolved ConnectionDef
	// Path is the absolute file path for duckdb and sqlite connections.
	Path string
	// Requires names the storage connections whose scope holds the definition's
	// storage URLs (catalog, data path, table location): the session connects
	// them first, because the lake or table is read with their secret.
	Requires []string
}

// Def converts the resolved connection for the type registry. readOnly is the
// effective flag (the definition's, or forced by a read-only open).
func (r ResolvedConnection) Def(readOnly bool) connections.Def {
	return toConnDef(r.Resolved, readOnly, r.Path)
}

// Binding is the credential binding of the resolved definition.
func (r ResolvedConnection) Binding() secrets.Binding { return BindingFor(r.Resolved) }

// ResolveConnection expands and path-resolves one connection of ws.
func ResolveConnection(ws Workspace, c Connection, lookup LookupFunc) (ResolvedConnection, error) {
	resolved, err := c.ConnectionDef.Resolve(lookup, c.Scope)
	if err != nil {
		return ResolvedConnection{}, err
	}
	out := ResolvedConnection{Connection: c, Resolved: resolved}
	if usesLocalPath(resolved) {
		p, err := ResolveLocalPath(ws.Root(), resolved.Path, c.Scope)
		if err != nil {
			return ResolvedConnection{}, fmt.Errorf("connection %q: %w", c.Name, err)
		}
		out.Path = p
	} else if resolved.Type == "iceberg" || resolved.Type == "delta" || resolved.Type == "ducklake" {
		out.Path = resolved.Path // a storage URL: read with a storage connection's secret
	}
	if dp := resolved.Options["data_path"]; resolved.Type == "ducklake" && dp != "" && !connections.IsDataURL(dp) {
		abs, err := ResolveLocalPath(ws.Root(), dp, c.Scope)
		if err != nil {
			return ResolvedConnection{}, fmt.Errorf("connection %q: data path: %w", c.Name, err)
		}
		opts := make(map[string]string, len(resolved.Options))
		for k, v := range resolved.Options {
			opts[k] = v
		}
		opts["data_path"] = abs
		out.Resolved.Options = opts
	}
	requires, err := checkStorageURLs(ws, out.Resolved, out.Path, lookup)
	if err != nil {
		return ResolvedConnection{}, fmt.Errorf("connection %q: %w", c.Name, err)
	}
	out.Requires = requires
	return out, nil
}

// storageTypes are the connection types that add a scoped storage secret.
var storageTypes = map[string]bool{"s3": true, "gcs": true, "r2": true, "azure": true}

// IsStorageType reports whether a connection type adds a scoped storage secret
// (the types a lake or table in object storage depends on).
func IsStorageType(t string) bool { return storageTypes[t] }

// checkStorageURLs holds a DuckLake catalog and data path and an Iceberg or Delta
// location that is a URL to the scope of a storage connection of this workspace: the
// lake or table is read with that connection's secret, and a definition that
// named any other bucket would either fail or reach data nobody configured.
// Local paths are held to the workspace by ResolveLocalPath. It returns the
// names of the storage connections that matched (sorted, without duplicates).
func checkStorageURLs(ws Workspace, d ConnectionDef, path string, lookup LookupFunc) ([]string, error) {
	var requires []string
	for _, t := range storageTargets(d, path) {
		if t.url == "" || !connections.IsDataURL(t.url) {
			continue
		}
		name, err := checkStorageURL(ws, d.Name, t.url, t.what, lookup)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(requires, name) {
			requires = append(requires, name)
		}
	}
	slices.Sort(requires)
	return requires, nil
}

// checkStorageURL holds one URL to the scope of a storage connection of ws and
// returns that connection's name.
func checkStorageURL(ws Workspace, self, url, what string, lookup LookupFunc) (string, error) {
	if err := storageurl.Validate(url); err != nil {
		return "", fmt.Errorf("the %s is not a plain storage URL: %w", what, err)
	}
	if m, ok := bestStorageMatch(ws, self, url, lookup); ok {
		return m.Name, nil
	}
	return "", fmt.Errorf("the %s %q is not inside the scope of any S3, GCS, R2 or Azure connection of this workspace; add one whose scope contains it", what, url)
}

// usesLocalPath reports whether the definition's path is a file or folder on
// this computer that must be resolved against the workspace: duckdb, sqlite and
// ducklake files, and the folder of an iceberg or delta table (a storage URL,
// also a lake's catalog, is left alone).
func usesLocalPath(d ConnectionDef) bool {
	switch d.Type {
	case "duckdb", "sqlite":
		return true
	case "ducklake", "iceberg", "delta":
		return !connections.IsDataURL(d.Path)
	}
	return false
}
