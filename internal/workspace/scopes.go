package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/SandwichLabs/slaw/internal/prefs"
)

// Scope says which file a connection definition came from.
type Scope string

// The three scopes, least to most specific. The UI never shows these words
// (it says "All my workspaces", "Team", "This computer"); they are the
// file-level names.
const (
	ScopeUser   Scope = "user"
	ScopeShared Scope = "shared"
	ScopeLocal  Scope = "local"
)

// ParseScope maps a scope's file-level name ("user", "shared", "local") to its
// Scope. Each adapter words its own error for ok == false.
func ParseScope(s string) (Scope, bool) {
	switch sc := Scope(s); sc {
	case ScopeUser, ScopeShared, ScopeLocal:
		return sc, true
	}
	return "", false
}

// scopeRank: higher wins.
var scopeRank = map[Scope]int{ScopeUser: 0, ScopeShared: 1, ScopeLocal: 2}

// UserConfig is <config dir>/slaw/connections.yaml: this person's connections
// across every workspace.
type UserConfig struct {
	Version     int               `yaml:"version"`
	Connections []ConnectionDef   `yaml:"connections,omitempty"`
	SignIn      map[string]string `yaml:"sign_in,omitempty"`
	// SignInFingerprint is, per connection, the fingerprint of the definition
	// the sign-in was chosen for (review #115). A changed definition withholds
	// even an env: credential until the person signs in again.
	SignInFingerprint map[string]string `yaml:"sign_in_fingerprint,omitempty"`
	// SignInServer is, per connection, the host-level description of the server
	// the sign-in was chosen for, so "the server changed (a to b)" can name both.
	SignInServer map[string]string `yaml:"sign_in_server,omitempty"`
}

// LocalConfig is this person's connections and settings for one workspace on
// this computer. It lives in the user config directory, never inside the
// workspace folder (review #109, D-A): a folder arrives with whatever a
// stranger committed, and these settings can loosen the agent, redefine
// connections and choose how credentials are fetched, so they are only
// trusted from a place only you write to. See LocalConfigPath.
type LocalConfig struct {
	Version int `yaml:"version"`
	// Connections are private connections, and overrides: an entry named like
	// a shared one replaces it wholesale on this computer.
	Connections []ConnectionDef `yaml:"connections,omitempty"`
	// SignIn is the per-person sign-in method (keychain, prompt or env:PREFIX)
	// by connection name, for connections from any scope.
	SignIn map[string]string `yaml:"sign_in,omitempty"`
	// SignInFingerprint binds each sign-in to the definition it was chosen for.
	SignInFingerprint map[string]string `yaml:"sign_in_fingerprint,omitempty"`
	// SignInServer is the host-level description of the server each sign-in was chosen for.
	SignInServer map[string]string `yaml:"sign_in_server,omitempty"`
	// Agent holds this person's agent loosening; see EffectivePolicy.
	Agent LocalAgent `yaml:"agent,omitempty"`
	// Receipts is this person's receipt retention and privacy setting
	// (ReceiptPolicy). Never in slaw.yaml or a bundle.
	Receipts LocalReceipts `yaml:"receipts,omitempty"`
	// MCPPort is the loopback port Studio's MCP server last used for this
	// workspace, so a client's saved address survives a restart (#112). 0 means
	// none yet. It is not a secret and grants nothing: the bearer token is
	// regenerated on every start.
	MCPPort int `yaml:"mcp_port,omitempty"`
	// Nonce ties the file to the folder it was made for (the same value is in
	// the folder's git-ignored .slaw-local/nonce). Written by SaveLocal.
	Nonce string `yaml:"nonce,omitempty"`
}

func validateSignInMap(m map[string]string) error {
	for name, method := range m {
		if err := ValidateConnectionName(name); err != nil {
			return fmt.Errorf("sign_in: %w", err)
		}
		if err := ValidateSignIn(method); err != nil {
			return fmt.Errorf("sign_in %q: %w", name, err)
		}
	}
	return nil
}

var fingerprintRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validateFingerprintMap(m map[string]string) error {
	for name, fp := range m {
		if err := ValidateConnectionName(name); err != nil {
			return fmt.Errorf("sign_in_fingerprint: %w", err)
		}
		if !fingerprintRE.MatchString(fp) {
			return fmt.Errorf("sign_in_fingerprint %q is not a fingerprint", name)
		}
	}
	return nil
}

// Validate checks a user-scope document.
func (u UserConfig) Validate() error {
	if u.Version > ConfigVersion {
		return fmt.Errorf("connections.yaml was written by a newer version of Slaw (version %d)", u.Version)
	}
	if err := validateDefs(u.Connections, ScopeUser); err != nil {
		return err
	}
	if err := validateSignInMap(u.SignIn); err != nil {
		return err
	}
	return validateFingerprintMap(u.SignInFingerprint)
}

// Validate checks a local-scope document.
func (l LocalConfig) Validate() error {
	if l.Version > ConfigVersion {
		return fmt.Errorf("connections.yaml was written by a newer version of Slaw (version %d)", l.Version)
	}
	if err := validateDefs(l.Connections, ScopeLocal); err != nil {
		return err
	}
	if err := ValidateSandbox(l.Agent.Sandbox); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	if l.MCPPort < 0 || l.MCPPort > 65535 {
		return fmt.Errorf("mcp_port %d is not a valid port", l.MCPPort)
	}
	if err := validateSignInMap(l.SignIn); err != nil {
		return err
	}
	return validateFingerprintMap(l.SignInFingerprint)
}

// ParseUserConfig parses and validates user-scope bytes.
func ParseUserConfig(data []byte) (UserConfig, error) {
	var u UserConfig
	if err := decodeStrict(data, &u); err != nil {
		return UserConfig{}, fmt.Errorf("invalid connections.yaml: %w", err)
	}
	if u.Version == 0 {
		u.Version = ConfigVersion
	}
	return u, u.Validate()
}

// ParseLocalConfig parses and validates local-scope bytes.
func ParseLocalConfig(data []byte) (LocalConfig, error) {
	var l LocalConfig
	if err := decodeStrict(data, &l); err != nil {
		return LocalConfig{}, fmt.Errorf("invalid .slaw-local/connections.yaml: %w", err)
	}
	if l.Version == 0 {
		l.Version = ConfigVersion
	}
	return l, l.Validate()
}

// MarshalUserConfig renders user-scope yaml.
func MarshalUserConfig(u UserConfig) ([]byte, error) {
	if u.Version == 0 {
		u.Version = ConfigVersion
	}
	return marshalYAML(u, "# Your connections, in every Slaw workspace on this computer. No passwords here.\n")
}

// LocalConfigPath is where this person's settings for the workspace with the
// given id and root live: <config dir>/workspaces/<id>-<hash>/local.yaml. The
// hash is of the real, symlink-resolved root path, so two clones of one
// repository on one machine (same id) keep separate settings, and a clone can
// never read another's. configDir is $SLAW_CONFIG_DIR or the user config dir.
func LocalConfigPath(configDir, id, root string) string {
	real := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		real = r
	}
	sum := sha256.Sum256([]byte(filepath.Clean(real)))
	return filepath.Join(configDir, "workspaces", id+"-"+hex.EncodeToString(sum[:])[:8], LocalFile)
}

// LocalFile is the per-person settings file name inside its directory.
const LocalFile = "local.yaml"

// MarshalLocalConfig renders local-scope yaml.
func MarshalLocalConfig(l LocalConfig) ([]byte, error) {
	if l.Version == 0 {
		l.Version = ConfigVersion
	}
	return marshalYAML(l, "# Your own settings for one workspace, on this computer only. No passwords here.\n")
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to read %s: %w", filepath.Base(path), err)
	}
	return data, true, nil
}

// LoadUserConfig reads the user-scope file; a missing file is empty.
func LoadUserConfig() (UserConfig, error) {
	dir, err := prefs.DefaultDir(nil)
	if err != nil {
		return UserConfig{Version: ConfigVersion}, nil //nolint:nilerr // no config dir: no user scope
	}
	return LoadUserConfigFrom(dir)
}

// LoadUserConfigFrom reads connections.yaml from the given config directory.
func LoadUserConfigFrom(dir string) (UserConfig, error) {
	path := filepath.Join(dir, UserConfigFile)
	data, ok, err := readOptional(path)
	if err != nil {
		return UserConfig{}, err
	}
	if !ok {
		return UserConfig{Version: ConfigVersion}, nil
	}
	u, err := ParseUserConfig(data)
	if err != nil {
		return UserConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	return u, nil
}

// Connection is one merged connection: the winning definition plus where it
// came from and what it hides.
type Connection struct {
	ConnectionDef
	// Scope is the scope the winning definition came from.
	Scope Scope `json:"scope"`
	// Shadows lists the lower-precedence scopes that also define this name,
	// most specific first.
	Shadows []Scope `json:"shadows,omitempty"`
	// SignIn is the per-person sign-in method in effect: this person's
	// local setting, else (for a user-scope connection) the user-scope
	// setting, else keychain. A shared definition's `credentials:` is only a
	// hint (SignInHint) and is never the effective method (review #115: a
	// pulled commit must not choose how your credentials are fetched).
	SignIn string `json:"sign_in"`
	// SignInHint is the definition's suggested method, for the UI to
	// pre-select. It has no effect.
	SignInHint string `json:"sign_in_hint,omitempty"`
	// SignInFingerprint is the definition fingerprint this person's sign-in
	// was chosen for ("" when never chosen here).
	SignInFingerprint string `json:"-"`
	// SignInServer describes the server that sign-in was chosen for ("" when never chosen here).
	SignInServer string `json:"-"`
	// Default marks the workspace's default connection.
	Default bool `json:"default,omitempty"`
	// Fingerprint is Fingerprint(ConnectionDef) of the unresolved definition;
	// the session layer recomputes it on the environment-resolved one.
	Fingerprint string `json:"fingerprint"`
}

// Merge combines the three scopes most-specific-wins (local > shared > user).
// A winning entry replaces the whole lower entry of the same name. The result
// is sorted by name.
func Merge(user UserConfig, shared Config, local LocalConfig, defaultName string) []Connection {
	type cand struct {
		def   ConnectionDef
		scope Scope
	}
	byName := map[string][]cand{}
	add := func(defs []ConnectionDef, s Scope) {
		for _, d := range defs {
			byName[d.Name] = append(byName[d.Name], cand{d, s})
		}
	}
	add(user.Connections, ScopeUser)
	add(shared.Connections, ScopeShared)
	add(local.Connections, ScopeLocal)

	out := make([]Connection, 0, len(byName))
	for name, cs := range byName {
		sort.Slice(cs, func(i, j int) bool { return scopeRank[cs[i].scope] > scopeRank[cs[j].scope] })
		win := cs[0]
		c := Connection{ConnectionDef: win.def, Scope: win.scope, Default: name == defaultName}
		for _, lo := range cs[1:] {
			c.Shadows = append(c.Shadows, lo.scope)
		}
		c.SignIn, c.SignInFingerprint, c.SignInServer = signInFor(win.def, win.scope, user, local)
		c.SignInHint = win.def.Credentials
		c.Fingerprint = Fingerprint(win.def)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func signInFor(d ConnectionDef, scope Scope, user UserConfig, local LocalConfig) (method, fp, server string) {
	if m, ok := local.SignIn[d.Name]; ok {
		return m, local.SignInFingerprint[d.Name], local.SignInServer[d.Name]
	}
	if scope == ScopeUser {
		if m, ok := user.SignIn[d.Name]; ok {
			return m, user.SignInFingerprint[d.Name], user.SignInServer[d.Name]
		}
	}
	return "keychain", "", ""
}
