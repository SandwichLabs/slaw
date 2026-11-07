package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/secrets"
)

func (w *workspace) Resolver() *secrets.Resolver { return w.res }

// validateDefFor applies the scope's rules (the slaw.yaml rules for shared:
// no absolute path, no ${}), then the type's rules. A definition that uses
// ${ENV} (your own scopes) is type-checked when it is resolved at open.
func validateDefFor(scope Scope, d ConnectionDef) error {
	switch scope {
	case ScopeShared, ScopeLocal, ScopeUser:
	default:
		return fmt.Errorf("unknown scope %q (want user, shared or local)", scope)
	}
	if err := d.ValidateIn(scope); err != nil {
		return err
	}
	if scope == ScopeShared && OwnScopeOnlyType(d.Type) {
		return fmt.Errorf("connection %q: %s", d.Name, OwnScopeOnlyMessage(d.Type))
	}
	for _, v := range d.stringValues() {
		if strings.Contains(v, "${") {
			return nil
		}
	}
	if err := connections.Validate(toConnDef(d, d.IsReadOnly(), d.Path)); err != nil {
		return err
	}
	if scope == ScopeUser && usesLocalPath(d) && !isAbsLike(d.Path) {
		return fmt.Errorf("connection %q: a user connection belongs to no workspace, so its path must be absolute", d.Name)
	}
	if dp := d.Options["data_path"]; scope == ScopeUser && d.Type == "ducklake" && dp != "" && !connections.IsDataURL(dp) && !isAbsLike(dp) {
		return fmt.Errorf("connection %q: a user connection belongs to no workspace, so its data path must be absolute", d.Name)
	}
	return nil
}

func (w *workspace) scopeDefs(scope Scope) ([]ConnectionDef, error) {
	switch scope {
	case ScopeShared:
		return append([]ConnectionDef(nil), w.cfg.Connections...), nil
	case ScopeLocal:
		return append([]ConnectionDef(nil), w.local.Connections...), nil
	case ScopeUser:
		return append([]ConnectionDef(nil), w.user.Connections...), nil
	}
	return nil, fmt.Errorf("unknown scope %q (want user, shared or local)", scope)
}

// saveScopeDefs writes defs back to the scope's file. w.mu is not held.
func (w *workspace) saveScopeDefs(scope Scope, defs []ConnectionDef) error {
	switch scope {
	case ScopeShared:
		c := w.Config()
		c.Connections = defs
		return w.SaveConfig(c)
	case ScopeLocal:
		l := w.Local()
		l.Connections = defs
		return w.SaveLocal(l)
	case ScopeUser:
		w.mu.RLock()
		u := w.user
		w.mu.RUnlock()
		u.Connections = defs
		return w.saveUser(u)
	}
	return fmt.Errorf("unknown scope %q", scope)
}

func (w *workspace) readUserRev() string {
	if w.configDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(w.configDir, UserConfigFile))
	if err != nil {
		return ""
	}
	return filestore.HashBytes(data)
}

func (w *workspace) saveUser(u UserConfig) error {
	if w.configDir == "" {
		return fmt.Errorf("no per-user config directory is available to keep your connections in")
	}
	if u.Version == 0 {
		u.Version = ConfigVersion
	}
	if err := u.Validate(); err != nil {
		return err
	}
	data, err := MarshalUserConfig(u)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(w.configDir, 0o700); err != nil {
		return fmt.Errorf("failed to create your config directory: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := filestore.WriteAtomic(w.configDir, filestore.Write{Rel: UserConfigFile, Data: data, Expect: w.userRev}, nil); err != nil {
		return fmt.Errorf("failed to save %s: %w", UserConfigFile, err)
	}
	w.user, w.userRev = u, filestore.HashBytes(data)
	w.merged = Merge(w.user, w.cfg, w.local, w.cfg.DefaultConnection)
	return nil
}

func indexOf(defs []ConnectionDef, name string) int {
	for i, d := range defs {
		if d.Name == name {
			return i
		}
	}
	return -1
}

// mutateDefs is the read-merge-write of one scope's definitions, done inside
// the compare-and-write: when the file changed on disk since it was loaded (a
// teammate's pull, Studio in another process) the scope is re-read and the
// change applied to the fresh content, a few times, instead of failing or
// overwriting the other writer's edit.
func (w *workspace) mutateDefs(scope Scope, fn func([]ConnectionDef) ([]ConnectionDef, error)) error {
	w.crudMu.Lock()
	defer w.crudMu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		w.mu.RLock()
		defs, err := w.scopeDefs(scope)
		w.mu.RUnlock()
		if err != nil {
			return err
		}
		defs, err = fn(defs)
		if err != nil {
			return err
		}
		err = w.saveScopeDefs(scope, defs)
		if err == nil || !errors.Is(err, ErrConflict) {
			return err
		}
		lastErr = err
		if rerr := w.reload(); rerr != nil {
			return rerr
		}
	}
	return fmt.Errorf("the %s connections changed on disk while saving; try again: %w", scopeWords(scope), lastErr)
}

// retryConflict runs fn, which reads the current state itself, again after a
// reload when a compare-and-write lost to another writer.
func (w *workspace) retryConflict(fn func() error) error {
	w.crudMu.Lock()
	defer w.crudMu.Unlock()
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = fn(); err == nil || !errors.Is(err, ErrConflict) {
			return err
		}
		if rerr := w.reload(); rerr != nil {
			return rerr
		}
	}
	return fmt.Errorf("your settings changed on disk while saving; try again: %w", err)
}

func (w *workspace) AddConnection(scope Scope, def ConnectionDef) error {
	if err := validateDefFor(scope, def); err != nil {
		return err
	}
	return w.mutateDefs(scope, func(defs []ConnectionDef) ([]ConnectionDef, error) {
		if indexOf(defs, def.Name) >= 0 {
			return nil, fmt.Errorf("a %s connection named %q already exists", scopeWords(scope), def.Name)
		}
		return append(defs, def), nil
	})
}

func (w *workspace) UpdateConnection(scope Scope, def ConnectionDef) error {
	if err := validateDefFor(scope, def); err != nil {
		return err
	}
	return w.mutateDefs(scope, func(defs []ConnectionDef) ([]ConnectionDef, error) {
		i := indexOf(defs, def.Name)
		if i < 0 {
			return nil, fmt.Errorf("there is no %s connection named %q", scopeWords(scope), def.Name)
		}
		defs[i] = def
		return defs, nil
	})
}

func scopeWords(s Scope) string {
	switch s {
	case ScopeShared:
		return "shared"
	case ScopeLocal:
		return "local (this computer)"
	}
	return "user (all your workspaces)"
}

// stillUsesRef reports whether, after removing name from scope, another scope
// still defines name with the same credential reference.
func (w *workspace) stillUsesRef(scope Scope, name string) bool {
	for _, s := range []Scope{ScopeShared, ScopeLocal, ScopeUser} {
		if s == scope {
			continue
		}
		defs, _ := w.scopeDefs(s)
		if indexOf(defs, name) >= 0 && (s == ScopeUser) == (scope == ScopeUser) {
			return true
		}
	}
	return false
}

func (w *workspace) RemoveConnection(ctx context.Context, scope Scope, name string) error {
	err := w.mutateDefs(scope, func(defs []ConnectionDef) ([]ConnectionDef, error) {
		i := indexOf(defs, name)
		if i < 0 {
			return nil, fmt.Errorf("there is no %s connection named %q", scopeWords(scope), name)
		}
		if scope == ScopeShared && w.DefaultConnection() == name {
			return nil, fmt.Errorf("%q is the workspace's default connection; choose another default first", name)
		}
		return append(defs[:i], defs[i+1:]...), nil
	})
	if err != nil {
		return err
	}
	w.mu.RLock()
	keep := w.stillUsesRef(scope, name)
	w.mu.RUnlock()
	if keep {
		return nil
	}
	var first error
	if err := w.Resolver().ClearConnectionCredentials(ctx, credentialRef(w.ID(), scope, name)); err != nil {
		first = err
	}
	// The sign-in choice goes with it.
	if scope == ScopeUser {
		w.mu.RLock()
		u := w.user
		w.mu.RUnlock()
		if _, ok := u.SignIn[name]; ok {
			delete(u.SignIn, name)
			delete(u.SignInFingerprint, name)
			delete(u.SignInServer, name)
			if err := w.saveUser(u); err != nil && first == nil {
				first = err
			}
		}
	} else if l := w.Local(); l.SignIn != nil {
		if _, ok := l.SignIn[name]; ok {
			delete(l.SignIn, name)
			delete(l.SignInFingerprint, name)
			delete(l.SignInServer, name)
			if err := w.SaveLocal(l); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (w *workspace) MoveConnection(ctx context.Context, name string, from, to Scope) error {
	if from == to {
		return fmt.Errorf("%q is already a %s connection", name, scopeWords(to))
	}
	w.mu.RLock()
	srcDefs, err := w.scopeDefs(from)
	w.mu.RUnlock()
	if err != nil {
		return err
	}
	i := indexOf(srcDefs, name)
	if i < 0 {
		return fmt.Errorf("there is no %s connection named %q", scopeWords(from), name)
	}
	def := srcDefs[i]
	// The destination's rules decide: shared refuses ${} and absolute paths.
	if err := validateDefFor(to, def); err != nil {
		return fmt.Errorf("cannot move %q to %s: %w", name, scopeWords(to), err)
	}
	// Migrate the keychain entry when the reference changes, before anything
	// is written, so a failure leaves the connection where it was.
	oldRef, newRef := credentialRef(w.ID(), from, name), credentialRef(w.ID(), to, name)
	if oldRef != newRef {
		if err := w.migrateCredential(ctx, def, from, oldRef, newRef); err != nil {
			return err
		}
	}
	if err := w.mutateDefs(to, func(defs []ConnectionDef) ([]ConnectionDef, error) {
		if indexOf(defs, name) >= 0 {
			return nil, fmt.Errorf("a %s connection named %q already exists", scopeWords(to), name)
		}
		return append(defs, def), nil
	}); err != nil {
		return err
	}
	if err := w.mutateDefs(from, func(defs []ConnectionDef) ([]ConnectionDef, error) {
		j := indexOf(defs, name)
		if j < 0 {
			return defs, nil
		}
		return append(defs[:j], defs[j+1:]...), nil
	}); err != nil {
		return fmt.Errorf("copied %q to %s but could not remove it from %s: %w", name, scopeWords(to), scopeWords(from), err)
	}
	if oldRef != newRef {
		_ = w.Resolver().ClearConnectionCredentials(ctx, oldRef)
	}
	if from == ScopeUser {
		return w.carryUserSignIn(name)
	}
	return nil
}

// carryUserSignIn copies the person's user-file sign-in choice (method,
// fingerprint, server) for a connection that left the user scope into local.yaml,
// where a workspace-scope connection reads it. Without it an env: or aws-sso:
// choice silently became the keychain. A choice already in local.yaml stays.
func (w *workspace) carryUserSignIn(name string) error {
	w.mu.RLock()
	u := w.user
	w.mu.RUnlock()
	method, ok := u.SignIn[name]
	if !ok {
		return nil
	}
	return w.retryConflict(func() error {
		l := w.Local()
		if _, has := l.SignIn[name]; has {
			return nil
		}
		l.SignIn, l.SignInFingerprint, l.SignInServer = copyMap(l.SignIn), copyMap(l.SignInFingerprint), copyMap(l.SignInServer)
		l.SignIn[name], l.SignInFingerprint[name], l.SignInServer[name] = method, u.SignInFingerprint[name], u.SignInServer[name]
		return w.SaveLocal(l)
	})
}

// migrateCredential copies a saved credential to its new keychain reference.
// Nothing saved is not an error. The secret is read and written inside the
// process; it never leaves it.
func (w *workspace) migrateCredential(ctx context.Context, def ConnectionDef, from Scope, oldRef, newRef secrets.Ref) error {
	method := w.signInFor(from, def)
	if kind, _, err := secrets.ParseMethod(method); err != nil || secrets.StoresNoSecret(kind) {
		return nil
	}
	resolved, err := def.Resolve(nil, from)
	if err != nil {
		return nil //nolint:nilerr // an unresolvable definition has no usable credential to move
	}
	b := BindingFor(resolved)
	cred, err := w.Resolver().Resolve(ctx, oldRef, method, b)
	if err != nil {
		return nil //nolint:nilerr // none saved, or bound to another server: nothing to move
	}
	if err := w.Resolver().SetConnectionCredentials(ctx, newRef, method, cred, b); err != nil {
		return fmt.Errorf("failed to move the saved credentials: %w", err)
	}
	return nil
}

func (w *workspace) signInFor(scope Scope, d ConnectionDef) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	m, _, _ := signInFor(d, scope, w.user, w.local)
	return m
}

func (w *workspace) Connection(name string) (Connection, bool) {
	for _, c := range w.Connections() {
		if c.Name == name {
			return c, true
		}
	}
	return Connection{}, false
}

func (w *workspace) findConnection(name string) (Connection, error) {
	if c, ok := w.Connection(name); ok {
		return c, nil
	}
	return Connection{}, fmt.Errorf("unknown connection %q", name)
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ErrRebindNeedsConfirm is returned by SetSignIn when it would bind an
// environment or AWS SSO sign-in to a server other than the one the saved sign-in was
// chosen for (a pull changed the host): the variables would go to the new server.
// RebindSignIn is the explicit, confirmed form.
var ErrRebindNeedsConfirm = errors.New("the server changed since this sign-in was chosen, so it would now be used for the new server; confirm to keep using it")

// SetSignIn records this person's sign-in method for a connection together
// with the fingerprint of the definition it is chosen for, so that if the
// definition later points elsewhere (a pull changes the host) even an env:
// credential is withheld until the person signs in again.
//
// It enforces what the type takes (TypeInfo.SignInMethods) and, for aws-sso,
// that the endpoint is AWS over TLS. It never re-binds silently: choosing an
// environment or AWS SSO sign-in for a connection whose saved sign-in was bound
// to a different server returns ErrRebindNeedsConfirm. A first binding (nothing
// saved) and a switch to a method that carries its own binding (keychain,
// prompt) are fine.
func (w *workspace) SetSignIn(name, method string) error { return w.setSignIn(name, method, false) }

// RebindSignIn is SetSignIn after the person confirmed the server change.
func (w *workspace) RebindSignIn(name, method string) error { return w.setSignIn(name, method, true) }

func (w *workspace) setSignIn(name, method string, confirmed bool) error {
	if err := ValidateSignIn(method); err != nil {
		return err
	}
	c, err := w.findConnection(name)
	if err != nil {
		return err
	}
	resolved, err := c.ConnectionDef.Resolve(nil, c.Scope)
	if err != nil {
		return err
	}
	if err := CheckSignIn(w.Resolver(), resolved, method); err != nil {
		return err
	}
	fp := Fingerprint(resolved)
	server := ServerDescription(resolved)
	if boundSignIn(method) && !confirmed && c.SignInFingerprint != "" && c.SignInFingerprint != fp {
		return ErrRebindNeedsConfirm
	}
	if c.Scope == ScopeUser {
		return w.retryConflict(func() error {
			w.mu.RLock()
			u := w.user
			w.mu.RUnlock()
			u.SignIn, u.SignInFingerprint, u.SignInServer = copyMap(u.SignIn), copyMap(u.SignInFingerprint), copyMap(u.SignInServer)
			u.SignIn[name], u.SignInFingerprint[name], u.SignInServer[name] = method, fp, server
			return w.saveUser(u)
		})
	}
	return w.retryConflict(func() error {
		l := w.Local()
		l.SignIn, l.SignInFingerprint, l.SignInServer = copyMap(l.SignIn), copyMap(l.SignInFingerprint), copyMap(l.SignInServer)
		l.SignIn[name], l.SignInFingerprint[name], l.SignInServer[name] = method, fp, server
		return w.SaveLocal(l)
	})
}

func (w *workspace) SetCredentials(ctx context.Context, name string, cred secrets.Credential, remember bool) error {
	c, err := w.findConnection(name)
	if err != nil {
		return err
	}
	resolved, err := c.ConnectionDef.Resolve(nil, c.Scope)
	if err != nil {
		return err
	}
	if !resolved.NeedsCredential() {
		return fmt.Errorf("%s connections have no credentials", c.Type)
	}
	return w.Resolver().SetCredentials(ctx, credentialRef(w.ID(), c.Scope, name), c.SignIn, cred, BindingFor(resolved), remember)
}

func (w *workspace) ClearCredentials(ctx context.Context, name string) error {
	c, err := w.findConnection(name)
	if err != nil {
		return err
	}
	return w.Resolver().ClearConnectionCredentials(ctx, credentialRef(w.ID(), c.Scope, name))
}

func (w *workspace) CredentialStatus(ctx context.Context, name string) (secrets.CredentialStatus, error) {
	c, err := w.findConnection(name)
	if err != nil {
		return secrets.CredentialStatus{}, err
	}
	resolved, err := c.ConnectionDef.Resolve(nil, c.Scope)
	if err != nil {
		return secrets.CredentialStatus{}, err
	}
	st := w.Resolver().GetConnectionCredentialStatus(ctx, credentialRef(w.ID(), c.Scope, name), c.SignIn, BindingFor(resolved))
	if gerr := signInGuard(w.Resolver(), c, resolved); gerr != nil {
		st.FingerprintMatches, st.SecretSet, st.SignedIn, st.Detail = false, false, false, gerr.Error()
	}
	return st, nil
}
