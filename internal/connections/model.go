package connections

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
)

// Def is a connection definition as the types see it: what a connection needs,
// never a credential. internal/workspace converts its ConnectionDef to this
// after ${ENV} expansion and path resolution.
type Def struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Path     string            `json:"path,omitempty"`
	Host     string            `json:"host,omitempty"`
	Port     int               `json:"port,omitempty"`
	Database string            `json:"database,omitempty"`
	Region   string            `json:"region,omitempty"`
	Scope    string            `json:"scope,omitempty"` // storage scope such as s3://bucket/prefix/
	ReadOnly bool              `json:"read_only"`
	Options  map[string]string `json:"options,omitempty"`
}

const optPrefix = "opt."

// Params flattens the type-specific fields for session.Spec.Params.
func (d Def) Params() map[string]string {
	p := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			p[k] = v
		}
	}
	set("host", d.Host)
	if d.Port != 0 {
		p["port"] = strconv.Itoa(d.Port)
	}
	set("database", d.Database)
	set("region", d.Region)
	set("scope", d.Scope)
	for k, v := range d.Options {
		p[optPrefix+strings.ToLower(k)] = v
	}
	return p
}

// Spec builds the session spec for the definition. Credential is left for the
// caller (it depends on the sign-in method).
func (d Def) Spec() session.Spec {
	return session.Spec{Name: d.Name, Type: d.Type, Path: d.Path, ReadOnly: d.ReadOnly, Params: d.Params()}
}

func param(s session.Spec, k string) string { return s.Params[k] }
func opt(s session.Spec, k string) string   { return s.Params[optPrefix+k] }

// FieldKind is the kind of input a form should render.
type FieldKind string

const (
	KindText   FieldKind = "text"
	KindNumber FieldKind = "number"
	KindEnum   FieldKind = "enum"
	KindPath   FieldKind = "path"
	KindURL    FieldKind = "url"
)

// Field describes one input of a connection type.
type Field struct {
	Name    string    `json:"name"`
	Label   string    `json:"label"`
	Kind    FieldKind `json:"kind"`
	Help    string    `json:"help,omitempty"`
	Default string    `json:"default,omitempty"`
	Choices []string  `json:"choices,omitempty"`
	// Required means the type cannot connect without it.
	Required bool `json:"required"`
	// Secret fields are write-only: never read back across a boundary.
	Secret bool `json:"secret"`
	// PerPerson fields are not part of the committed definition: they live
	// with the person's credential (user name, password, token, key id).
	PerPerson bool `json:"perPerson"`
	// Option marks a definition field stored under `options:` rather than as
	// a top-level key.
	Option bool `json:"option,omitempty"`
	// Prominent asks the form to open the Advanced section on a new
	// connection, for an option most people must look at (Azure's sign-in).
	Prominent bool `json:"prominent,omitempty"`
}

// Capabilities says what a type can do.
type Capabilities struct {
	// Catalog is true when the connection attaches a catalog (tables); storage
	// connections (s3, gcs, r2) only add a scoped secret.
	Catalog bool `json:"catalog"`
	// Writable is true when the type can accept writes at all.
	Writable bool `json:"writable"`
	// ReadOnlyAttach is true when read_only is enforced by the ATTACH itself.
	// For MotherDuck it is false (READ_ONLY is not supported on md:): use a
	// read-scaling token. For Postgres it is client-side only: use a
	// read-only database role.
	ReadOnlyAttach bool `json:"readOnlyAttach"`
	// Remote connections dial on first use; local ones attach when the
	// workspace opens.
	Remote bool `json:"remote"`
	// Test is true when Test can probe the type.
	Test bool `json:"test"`
	// Subprocess marks types tested in a subprocess (one token per process).
	Subprocess bool `json:"subprocess,omitempty"`
}

// TypeInfo is the form metadata of a type (ListConnectionTypes).
type TypeInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Fields are the committed definition fields; Credentials the per-person
	// ones (user, password, token, key id, secret).
	Fields       []Field      `json:"fields"`
	Credentials  []Field      `json:"credentials"`
	Extensions   []string     `json:"extensions,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	// FingerprintFields name the definition fields whose change makes a saved
	// credential stop applying (workspace.Fingerprint covers at least these). A UI
	// hint for the help text only: the editor compares workspace.Fingerprint values.
	// Option fields are written "option:<name>".
	FingerprintFields []string `json:"fingerprintFields"`
	// SignInMethods the type supports (all three, today).
	SignInMethods []string `json:"signInMethods,omitempty"`
	// Presets fill the form for a known S3-compatible store (s3 only). They
	// only prefill; nothing about a preset is stored in the definition.
	Presets []Preset `json:"presets,omitempty"`
}

// Type is a registered connection type: a session type plus form metadata and
// a probe.
type Type interface {
	session.ConnectionType
	Info() TypeInfo
	// Probe runs after a successful attach in the throwaway session and returns
	// a short, non-secret description ("12 tables", "37 objects").
	Probe(ctx context.Context, sess session.Session, spec session.Spec) (count int, detail string, err error)
}

var registry = map[string]Type{}

func register(t Type) { registry[t.Name()] = t }

func init() {
	register(postgresType{})
	register(motherduckType{})
	register(sqliteType{})
	register(storageType{kind: "s3"})
	register(storageType{kind: "gcs"})
	register(storageType{kind: "r2"})
	register(mysqlType{})
	register(azureType{})
	register(ducklakeType{})
	register(tableSourceType{kind: "iceberg"})
	register(tableSourceType{kind: "delta"})
	register(icebergCatalogType{})
}

// Types returns the session types to hand to session.WithConnectionTypes.
func Types() []session.ConnectionType {
	out := make([]session.ConnectionType, 0, len(registry))
	for _, n := range TypeNames() {
		out = append(out, registry[n])
	}
	return out
}

// TypeNames lists the registered type names, sorted ("duckdb" is built into
// the session and listed by ListConnectionTypes too).
func TypeNames() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ListConnectionTypes returns the metadata of every connection type, duckdb
// included, in a stable order, for form generation (#117).
func ListConnectionTypes() []TypeInfo {
	names := TypeNames()
	out := make([]TypeInfo, 0, 1+len(names))
	out = append(out, duckdbInfo())
	for _, n := range names {
		out = append(out, registry[n].Info())
	}
	return out
}

// TypeInfoFor returns one type's metadata.
func TypeInfoFor(name string) (TypeInfo, error) {
	for _, i := range ListConnectionTypes() {
		if i.Name == name {
			return i, nil
		}
	}
	return TypeInfo{}, fmt.Errorf("unknown connection type %q (want one of %s)", name, strings.Join(AllTypeNames(), ", "))
}

// ExtensionsFor lists the extensions one connection definition needs, in
// install order: the type's static list, widened by the spec for a type whose
// needs depend on it (a DuckLake with a SQLite catalog or a storage URL).
func ExtensionsFor(d Def) []string {
	if ct, ok := registry[d.Type].(session.SpecExtensionsType); ok {
		return ct.SpecExtensions(d.Spec())
	}
	ti, err := TypeInfoFor(d.Type)
	if err != nil {
		return nil
	}
	return ti.Extensions
}

// AllTypeNames lists every type name, duckdb first.
func AllTypeNames() []string { return append([]string{"duckdb"}, TypeNames()...) }

func duckdbInfo() TypeInfo {
	return TypeInfo{
		Name: "duckdb", Label: "DuckDB file",
		Fields: []Field{
			{Name: "path", Label: "File", Kind: KindPath, Required: true, Help: "A .duckdb file. Inside the workspace for a shared connection."},
		},
		Capabilities:      Capabilities{Catalog: true, Writable: true, ReadOnlyAttach: true},
		FingerprintFields: []string{"type", "path"},
	}
}

// NeedsCredential reports whether a connection of this type signs in.
func NeedsCredential(typ string) bool {
	if t, ok := registry[typ]; ok {
		if ct, ok := t.(session.CredentialedType); ok {
			return ct.NeedsCredential(session.Spec{Type: typ})
		}
	}
	return false
}

// IsRemote reports whether a type dials on first use (lazy attach).
func IsRemote(typ string) bool {
	t, ok := registry[typ]
	return ok && t.Remote()
}
