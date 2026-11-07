package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/SandwichLabs/slaw/internal/awssso"
	"github.com/SandwichLabs/slaw/internal/sandbox"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/SandwichLabs/slaw/internal/session"
)

// File and directory names.
const (
	ConfigFile      = "slaw.yaml"
	LocalDir        = ".slaw-local"
	LocalConfigFile = "connections.yaml"
	LocalStateFile  = "state.json"
	UserConfigFile  = "connections.yaml"
	GitignoreFile   = ".gitignore"
	GitattrsFile    = ".gitattributes"
	DataDir         = "data"
	// ConfigVersion is the slaw.yaml / connections.yaml schema version.
	ConfigVersion = 1
	// DefaultConnectionName is the workspace's own DuckDB (D3).
	DefaultConnectionName = "local"
	// DefaultConnectionPath is where it lives, relative to the root.
	DefaultConnectionPath = "data/local.duckdb"
)

// Sandbox tiers, the strings slaw.yaml accepts.
const (
	SandboxReadOnly  = "read-only"
	SandboxReadWrite = "read-write"
	SandboxFull      = "full"
)

// Sentinel errors.
var (
	// ErrNotWorkspace means no slaw.yaml was found.
	ErrNotWorkspace = errors.New("not a slaw workspace (no slaw.yaml found)")
	// ErrMissingID means slaw.yaml has no id; EnsureID can add one.
	ErrMissingID = errors.New("slaw.yaml has no id")
	// ErrAlreadyWorkspace means Init found an existing workspace.
	ErrAlreadyWorkspace = errors.New("already a slaw workspace")
)

// Config is the committed slaw.yaml. Unknown keys are rejected.
type Config struct {
	Version           int             `yaml:"version" json:"version"`
	ID                string          `yaml:"id" json:"id"`
	Name              string          `yaml:"name,omitempty" json:"name"`
	DefaultConnection string          `yaml:"default_connection,omitempty" json:"default_connection,omitempty"`
	Sandbox           string          `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	AskBeforeWrites   *bool           `yaml:"ask_before_writes,omitempty" json:"ask_before_writes,omitempty"`
	Connections       []ConnectionDef `yaml:"connections,omitempty" json:"connections,omitempty"`
}

// ConnectionDef is one connection definition: what a connection needs, never a
// credential. String fields other than name, type and credentials may contain
// ${ENV} references, resolved when the connection is opened.
type ConnectionDef struct {
	Name        string            `yaml:"name" json:"name"`
	Type        string            `yaml:"type" json:"type"`
	Path        string            `yaml:"path,omitempty" json:"path,omitempty"`
	Host        string            `yaml:"host,omitempty" json:"host,omitempty"`
	Port        int               `yaml:"port,omitempty" json:"port,omitempty"`
	Database    string            `yaml:"database,omitempty" json:"database,omitempty"`
	Region      string            `yaml:"region,omitempty" json:"region,omitempty"`
	Scope       string            `yaml:"scope,omitempty" json:"scope,omitempty"` // storage scope such as s3://bucket/
	ReadOnly    *bool             `yaml:"read_only,omitempty" json:"read_only,omitempty"`
	Credentials string            `yaml:"credentials,omitempty" json:"credentials,omitempty"` // default sign-in suggestion
	Options     map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

// IsReadOnly reports the effective read-only flag: true unless explicitly
// set false (connections default to read-only). MotherDuck is the exception:
// it cannot be attached read-only (MotherDuck's ATTACH has no READ_ONLY), so it
// defaults to false and an explicit true is rejected (review #115 D-2); use a
// read-scaling token for read-only access.
func (d ConnectionDef) IsReadOnly() bool {
	if d.ReadOnly == nil {
		return d.Type != "motherduck"
	}
	return *d.ReadOnly
}

// Bool returns a pointer to b, for ConnectionDef.ReadOnly literals.
func Bool(b bool) *bool { return &b }

var (
	typeRE       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	envPrefixRE  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	optionKeyRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)
	secretKeyRE  = regexp.MustCompile(`(?i)(passw|passwd|pwd|secret|token|api[_-]?key|credential|private[_-]?key|access[_-]?key|auth)`)
	urlUserinfoR = regexp.MustCompile(`://[^/\s:@]*:[^/\s@]+@`)
	pairUserinfo = regexp.MustCompile(`^[^/\s:@]+:[^/\s@]+@`)
)

// ReservedNames cannot be used as connection names: they are DuckDB's own
// catalogs.
var ReservedNames = []string{"system", "temp", "memory", "main"}

// ValidateConnectionName applies the catalog-alias rule
// ^[a-z_][a-z0-9_]{0,62}$ and rejects the reserved names.
func ValidateConnectionName(name string) error {
	if err := session.ValidateName(name); err != nil {
		return err
	}
	for _, r := range ReservedNames {
		if name == r {
			return fmt.Errorf("connection name %q is reserved (DuckDB's own catalog)", name)
		}
	}
	return nil
}

// ValidateSignIn checks a sign-in method: keychain, prompt, env:PREFIX or
// aws-sso:PROFILE (a safe subset of AWS profile names).
func ValidateSignIn(m string) error {
	switch {
	case m == "keychain", m == "prompt":
		return nil
	case strings.HasPrefix(m, "env:") && envPrefixRE.MatchString(strings.TrimPrefix(m, "env:")):
		return nil
	case strings.HasPrefix(m, secrets.MethodAWSSSOPref):
		if !awssso.ValidProfileName(strings.TrimPrefix(m, secrets.MethodAWSSSOPref)) {
			return fmt.Errorf("invalid sign-in method %q: the profile name after aws-sso: must be letters, digits and _ . + @ -, up to 64 characters", m)
		}
		return nil
	}
	return fmt.Errorf("invalid sign-in method %q: want keychain, prompt, env:PREFIX or aws-sso:PROFILE", m)
}

// Validate checks one definition. It never reads the environment.
func (d ConnectionDef) Validate() error {
	if err := ValidateConnectionName(d.Name); err != nil {
		return err
	}
	if !typeRE.MatchString(d.Type) {
		return fmt.Errorf("connection %q: invalid type %q", d.Name, d.Type)
	}
	if d.Port < 0 || d.Port > 65535 {
		return fmt.Errorf("connection %q: port %d out of range", d.Name, d.Port)
	}
	if d.Credentials != "" {
		if err := ValidateSignIn(d.Credentials); err != nil {
			return fmt.Errorf("connection %q: %w", d.Name, err)
		}
	}
	for field, v := range map[string]string{"path": d.Path, "host": d.Host, "database": d.Database, "region": d.Region, "scope": d.Scope} {
		if strings.ContainsAny(v, "\x00\n\r") {
			return fmt.Errorf("connection %q: %s contains a control character", d.Name, field)
		}
		if looksLikeEmbeddedSecret(v) {
			return fmt.Errorf("connection %q: %s looks like it embeds a password; credentials never go in config, use the keychain (or ${ENV} for host names)", d.Name, field)
		}
	}
	keys := make([]string, 0, len(d.Options))
	for k := range d.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lower := map[string]string{}
	for _, k := range keys {
		v := d.Options[k]
		if !optionKeyRE.MatchString(k) {
			return fmt.Errorf("connection %q: invalid option name %q", d.Name, k)
		}
		// The session lowercases option names, so two that differ only in case
		// would be one option with a value that depends on map order.
		if other, dup := lower[strings.ToLower(k)]; dup {
			return fmt.Errorf("connection %q: options %q and %q are the same option written twice (names are not case-sensitive)", d.Name, other, k)
		}
		lower[strings.ToLower(k)] = k
		if secretKeyRE.MatchString(k) && !nonSecretOption(d.Type, k) {
			return fmt.Errorf("connection %q: option %q looks like a credential; credentials never go in config, use the keychain", d.Name, k)
		}
		if strings.ContainsAny(v, "\x00\n\r") || looksLikeEmbeddedSecret(v) {
			return fmt.Errorf("connection %q: option %q has an invalid or secret-looking value", d.Name, k)
		}
	}
	return nil
}

// nonSecretOptions are option names a type defines that the credential-name
// pattern would otherwise refuse ("auth" is a setting, never a secret: the
// secrets of these types are per person, in the keychain). The list is per type
// and exact, so the pattern keeps guarding every other option.
var nonSecretOptions = map[string][]string{
	"iceberg_catalog": {"auth", "oauth2_server_uri"},
}

func nonSecretOption(typ, key string) bool {
	return slices.Contains(nonSecretOptions[typ], strings.ToLower(key))
}

func looksLikeEmbeddedSecret(v string) bool {
	if v == "" {
		return false
	}
	if urlUserinfoR.MatchString(v) {
		return true
	}
	if u, err := url.Parse(v); err == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			return true
		}
	}
	return pairUserinfo.MatchString(v)
}

// ValidateSandbox checks a sandbox tier string ("" is allowed: unset).
func ValidateSandbox(s string) error {
	if s == "" {
		return nil
	}
	if _, err := sandbox.Parse(s); err == nil {
		return nil
	}
	return fmt.Errorf("unknown sandbox %q (want %s, %s or %s)", s, SandboxReadOnly, SandboxReadWrite, SandboxFull)
}

// ValidateIn is Validate plus the rules of the scope the definition lives in
// (review #109, D-B and D-C):
//
//   - shared (slaw.yaml, committed by someone else): no ${ENV} references at
//     all, and every path must be relative and stay inside the workspace. A
//     pulled commit therefore cannot read your environment or point a
//     connection at a file elsewhere on your disk.
//   - local (your own file, outside the folder) and user: ${ENV} and absolute
//     paths are allowed, but an environment variable whose name looks like a
//     secret cannot be expanded into a non-secret field.
func (d ConnectionDef) ValidateIn(scope Scope) error {
	if err := d.Validate(); err != nil {
		return err
	}
	values := d.stringValues()
	switch scope {
	case ScopeShared:
		for field, v := range values {
			if strings.Contains(v, "${") {
				return fmt.Errorf("connection %q: %s uses ${ENV}, which is not allowed in slaw.yaml (it is shared; put per-person values in your own connections file or in the keychain)", d.Name, field)
			}
		}
		if p := d.Path; p != "" {
			if isAbsLike(p) {
				return fmt.Errorf("connection %q: path %q is absolute, which is not allowed in slaw.yaml (use a path inside the workspace, or define the connection in your own connections file)", d.Name, p)
			}
			if c := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p))); c == ".." || strings.HasPrefix(c, "../") {
				return fmt.Errorf("connection %q: path %q leaves the workspace folder", d.Name, p)
			}
		}
	default:
		for field, v := range values {
			if err := checkEnvRefs(v); err != nil {
				return fmt.Errorf("connection %q: %s: %w", d.Name, field, err)
			}
		}
	}
	return nil
}

// stringValues lists the fields ${ENV} could appear in, by name.
func (d ConnectionDef) stringValues() map[string]string {
	m := map[string]string{"path": d.Path, "host": d.Host, "database": d.Database, "region": d.Region, "scope": d.Scope}
	for k, v := range d.Options {
		m["option "+k] = v
	}
	return m
}

// isAbsLike reports a path that is absolute on any platform: a leading slash
// or backslash, a drive letter, or a home reference.
func isAbsLike(p string) bool {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "~") {
		return true
	}
	return len(p) >= 2 && p[1] == ':' && (p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z'
}

// checkEnvRefs refuses ${NAME} references whose name looks like a credential.
func checkEnvRefs(v string) error {
	for _, m := range envRefRE.FindAllStringSubmatch(v, -1) {
		if len(m) > 1 && m[1] != "" && secretKeyRE.MatchString(m[1]) {
			return fmt.Errorf("environment variable %s looks like a credential and cannot be expanded into a connection definition; use the keychain", m[1])
		}
	}
	return nil
}

func validateDefs(defs []ConnectionDef, scope Scope) error {
	seen := map[string]bool{}
	for _, d := range defs {
		if err := d.ValidateIn(scope); err != nil {
			return err
		}
		if seen[d.Name] {
			return fmt.Errorf("connection %q is defined more than once", d.Name)
		}
		seen[d.Name] = true
	}
	return nil
}

// Validate checks the whole document.
func (c Config) Validate() error {
	if c.Version > ConfigVersion {
		return fmt.Errorf("slaw.yaml was written by a newer version of Slaw (version %d)", c.Version)
	}
	if c.Version < 0 {
		return fmt.Errorf("slaw.yaml: invalid version %d", c.Version)
	}
	if c.ID == "" {
		return ErrMissingID
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return fmt.Errorf("slaw.yaml: id %q is not a UUID", c.ID)
	}
	if strings.ContainsAny(c.Name, "\x00\n\r") {
		return fmt.Errorf("slaw.yaml: name contains a control character")
	}
	if err := ValidateSandbox(c.Sandbox); err != nil {
		return fmt.Errorf("slaw.yaml: %w", err)
	}
	if c.DefaultConnection != "" {
		if err := ValidateConnectionName(c.DefaultConnection); err != nil {
			return fmt.Errorf("slaw.yaml: default_connection: %w", err)
		}
	}
	if err := validateDefs(c.Connections, ScopeShared); err != nil {
		return fmt.Errorf("slaw.yaml: %w", err)
	}
	return nil
}

// decodeStrict decodes exactly one YAML document into v, rejecting unknown and
// duplicate keys. An empty document leaves v untouched.
func decodeStrict(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return fmt.Errorf("more than one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ParseConfig parses and validates slaw.yaml bytes.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	if err := decodeStrict(data, &c); err != nil {
		return Config{}, fmt.Errorf("invalid slaw.yaml: %w", err)
	}
	if c.Version == 0 {
		c.Version = ConfigVersion
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// MarshalConfig renders slaw.yaml deterministically (2-space indent, fixed key
// order) so it diffs cleanly.
func MarshalConfig(c Config) ([]byte, error) {
	if c.Version == 0 {
		c.Version = ConfigVersion
	}
	return marshalYAML(c, "# Slaw workspace. Committed: shared settings and connection definitions only.\n# Never put a password, token or key in this file; Slaw keeps those in your keychain.\n")
}

func marshalYAML(v any, header string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("failed to encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("failed to encode yaml: %w", err)
	}
	return buf.Bytes(), nil
}

// OwnScopeOnlyType reports a connection type that trusts the metadata of what it
// points at: a DuckLake catalog, an Iceberg or Delta table decides which files
// are read (a crafted one can name any file on this computer). Only the person
// adds one, in their own scopes ("This computer" or all their workspaces); the
// shared slaw.yaml, which a pull can change, and a bundle import refuse them.
func OwnScopeOnlyType(typ string) bool {
	switch typ {
	case "ducklake", "iceberg", "delta":
		return true
	}
	return false
}

// OwnScopeOnlyMessage is the refusal shown for such a type. It is checked when a
// definition is added or moved (validateDefFor) and again when the workspace is
// opened (ResolveConnection), where a definition that arrived in slaw.yaml by a
// pull is skipped with an issue instead of making the whole file unreadable.
func OwnScopeOnlyMessage(typ string) string {
	return typ + " connections trust the metadata of what they point at (it decides which files are read), so only you can add them: choose \"This computer\" or \"All my workspaces\", not the team's slaw.yaml"
}
