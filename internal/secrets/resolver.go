package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Sign-in methods. A method is "keychain", "prompt" or "env:PREFIX"; it is
// per person (workspace local.yaml or user scope), never committed.
const (
	MethodKeychain = "keychain"
	MethodPrompt   = "prompt"
	MethodEnvPref  = "env:"
)

// Binding ties a credential to the server it was entered for.
type Binding struct {
	// Fingerprint is workspace.Fingerprint of the environment-resolved
	// definition.
	Fingerprint string
	// Server is a host-level description ("db.internal:5432") for messages.
	Server string
	// Type is the connection type, used only to word the missing-credential
	// hint (MotherDuck takes a token, not a user and password).
	Type string
	// EnvFields, when set, are the credential fields this definition uses (an
	// Iceberg REST catalog with a bearer token: "token"), so an env sign-in's
	// hints name only their variables. Empty means every credential variable.
	EnvFields []string
}

// ErrFingerprintChanged means the connection now points at a different server
// than the one the saved credential was entered for. The secret is not handed
// out. Old and New are host-level descriptions, never secrets.
type ErrFingerprintChanged struct {
	Old, New string
}

func (e *ErrFingerprintChanged) Error() string {
	switch {
	case e.Old != "" && e.New != "":
		return fmt.Sprintf("the server changed since you signed in (%s to %s). Sign in again", e.Old, e.New)
	default:
		return "the server changed since you signed in. Sign in again"
	}
}

// ErrNeedsCredentials means no credential is available by the chosen method.
// Its message names the env: alternative.
type ErrNeedsCredentials struct {
	Method string
	// Reason is "not set" or the store's unavailability detail.
	Reason string
	Ref    Ref
	// Type is the connection type ("" when unknown).
	Type string
	// SignIn is set when the person must sign in with an external identity
	// (an AWS SSO profile) instead of entering a secret.
	SignIn *SignInHint
}

func (e *ErrNeedsCredentials) Error() string {
	if h := e.SignIn; h != nil && h.Kind == MethodAWSSSO {
		cmd := "`aws sso login --profile " + h.Subject + "`"
		if h.Reason == "expired" {
			return fmt.Sprintf("AWS session for profile %s has expired. Sign in, or run %s.", h.Subject, cmd)
		}
		return fmt.Sprintf("No AWS session for profile %s. Sign in, or run %s.", h.Subject, cmd)
	}
	if e.Type == "motherduck" {
		return fmt.Sprintf("no credentials for %s (%s): sign in with a MotherDuck token, or choose Environment variables and set MOTHERDUCK_TOKEN", e.Ref.Name, e.Reason)
	}
	ex := strings.ToUpper(e.Ref.Name)
	return fmt.Sprintf("no credentials for %s (%s): sign in, or choose Environment variables and set %s_USER and %s_PASSWORD (or %s_TOKEN)", e.Ref.Name, e.Reason, ex, ex, ex)
}

// Resolver picks the store for a person's sign-in method and enforces the
// fingerprint binding. Every secret it resolves or stores is registered with
// its Redactor.
type Resolver struct {
	keyring  Store
	memory   *MemoryStore
	redactor *Redactor
	logger   *slog.Logger
	getenv   func(string) string
	// providers resolve the sign-in methods whose secrets are not stored
	// (aws-sso), by method kind.
	providers map[string]MethodProvider
	// envHint adds a sentence to the "not set" message of an env: sign-in
	// (a workspace .env that is present but not allowed).
	envHint func(prefix string) string
}

// ResolverOption configures NewResolver.
type ResolverOption func(*Resolver)

// WithKeychain sets the keychain store (default: the OS keyring).
func WithKeychain(s Store) ResolverOption { return func(r *Resolver) { r.keyring = s } }

// WithRedactor sets the redactor that learns resolved values.
func WithRedactor(x *Redactor) ResolverOption { return func(r *Resolver) { r.redactor = x } }

// WithResolverLogger sets the logger (names and statuses only).
func WithResolverLogger(l *slog.Logger) ResolverOption {
	return func(r *Resolver) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithEnv replaces os.Getenv (tests).
func WithEnv(f func(string) string) ResolverOption { return func(r *Resolver) { r.getenv = f } }

// NewResolver returns a resolver over the OS keychain, process memory and the
// environment.
func NewResolver(opts ...ResolverOption) *Resolver {
	r := &Resolver{
		memory:   NewMemoryStore(),
		redactor: NewRedactor(),
		logger:   slog.Default(),
		getenv:   os.Getenv,
	}
	for _, o := range opts {
		o(r)
	}
	if r.keyring == nil {
		r.keyring = NewKeyringStore()
	}
	return r
}

// Redactor returns the redactor learning this resolver's secrets.
func (r *Resolver) Redactor() *Redactor { return r.redactor }

// Keychain returns the keychain store (for the LLM key and probes).
func (r *Resolver) Keychain() Store { return r.keyring }

// ParseMethod splits a sign-in method. "" means keychain. For env the second
// result is the variable prefix; for aws-sso it is the profile name.
func ParseMethod(m string) (kind, prefix string, err error) {
	switch {
	case m == "" || m == MethodKeychain:
		return MethodKeychain, "", nil
	case m == MethodPrompt:
		return MethodPrompt, "", nil
	case strings.HasPrefix(m, MethodEnvPref) && len(m) > len(MethodEnvPref):
		return "env", strings.TrimPrefix(m, MethodEnvPref), nil
	case strings.HasPrefix(m, MethodAWSSSOPref) && len(m) > len(MethodAWSSSOPref):
		return MethodAWSSSO, strings.TrimPrefix(m, MethodAWSSSOPref), nil
	}
	return "", "", fmt.Errorf("invalid sign-in method %q: want keychain, prompt, env:PREFIX or aws-sso:PROFILE", m)
}

// WithEnvLayer returns a resolver that shares this one's keychain, session
// memory and redactor but reads env: sign-ins from layer first, then from the
// environment this resolver already reads. layer reports whether it has the
// variable. It is how a workspace's allowed .env reaches env:PREFIX without
// touching the process environment. hint, when non-nil, may add a sentence to
// the "nothing is set" message for a prefix (it must not include values).
func (r *Resolver) WithEnvLayer(layer func(string) (string, bool), hint func(prefix string) string) *Resolver {
	cp := *r
	base := r.getenv
	cp.getenv = func(k string) string {
		if v, ok := layer(k); ok {
			return v
		}
		return base(k)
	}
	cp.envHint = hint
	return &cp
}

func (r *Resolver) noEnvReason(prefix string, fields []string) string {
	reason := "none of " + strings.Join(EnvVarNamesFor(prefix, fields), ", ") + " is set"
	if r.envHint != nil {
		if h := r.envHint(prefix); h != "" {
			reason += ". " + h
		}
	}
	return reason
}

func (r *Resolver) env(prefix string) *EnvStore { return NewEnvStore(prefix).WithGetenv(r.getenv) }

// Resolve returns the credential for ref by the person's sign-in method, after
// checking the binding. It never returns a secret from a keychain or prompt
// entry that was saved for a different server.
func (r *Resolver) Resolve(ctx context.Context, ref Ref, method string, b Binding) (Credential, error) {
	kind, prefix, err := ParseMethod(method)
	if err != nil {
		return Credential{}, err
	}
	var cred Credential
	switch kind {
	case MethodAWSSSO:
		// No stored value, so no stored fingerprint to compare: the sign-in is
		// bound to its definition by the caller's guard (workspace.signInGuard).
		return r.resolveProvided(ctx, kind, prefix, method, ref, b.Type)
	case "env":
		c, ok := r.env(prefix).Load()
		if !ok {
			return Credential{}, &ErrNeedsCredentials{Method: method, Reason: r.noEnvReason(prefix, b.EnvFields), Ref: ref, Type: b.Type}
		}
		r.redactor.AddCredential(c)
		return c, nil
	case MethodPrompt:
		cred, err = r.load(ctx, r.memory, ref, method, b.Type)
	default:
		// A credential typed in the sign-in prompt with "Remember" off is held
		// in memory for this session and wins over the keychain's.
		if c, merr := r.load(ctx, r.memory, ref, method, b.Type); merr == nil {
			cred = c
		} else {
			cred, err = r.load(ctx, r.keyring, ref, method, b.Type)
		}
	}
	if err != nil {
		return Credential{}, err
	}
	if cred.Fingerprint != b.Fingerprint {
		return Credential{}, &ErrFingerprintChanged{Old: cred.Server, New: b.Server}
	}
	r.redactor.AddCredential(cred)
	return cred, nil
}

func (r *Resolver) load(ctx context.Context, s Store, ref Ref, method, typ string) (Credential, error) {
	raw, err := s.Get(ctx, ref)
	switch {
	case errors.Is(err, ErrNotFound):
		return Credential{}, &ErrNeedsCredentials{Method: method, Reason: "not set", Ref: ref, Type: typ}
	case err != nil:
		return Credential{}, err
	}
	return ParseCredential(raw)
}

// SetCredentials stores cred for ref under method, bound to b. Empty secret
// fields keep the stored value when the stored binding still matches (so the
// write-only UI can change a username alone); after a server change they do
// not carry over. For "prompt" the value is kept in memory only; pass
// remember to also promote it to the keychain (D9).
func (r *Resolver) SetCredentials(ctx context.Context, ref Ref, method string, cred Credential, b Binding, remember bool) error {
	kind, _, err := ParseMethod(method)
	if err != nil {
		return err
	}
	if err := refuseUnstored(kind); err != nil {
		return err
	}
	store := r.keyring
	if kind == MethodPrompt {
		store = r.memory
	}
	prev, gerr := r.loadExisting(ctx, store, ref)
	if gerr != nil && !errors.Is(gerr, ErrNotFound) {
		// Could not tell what is stored (keychain down, unreadable entry):
		// do not overwrite blind and lose a secret the person meant to keep.
		return fmt.Errorf("failed to read the existing credentials for %s: %w", ref.Name, gerr)
	}
	if gerr == nil && prev.Fingerprint == b.Fingerprint {
		if cred.User == "" {
			cred.User = prev.User
		}
		if cred.Password == "" {
			cred.Password = prev.Password
		}
		if cred.Token == "" {
			cred.Token = prev.Token
		}
		if cred.KeyID == "" {
			cred.KeyID = prev.KeyID
		}
		if cred.Secret == "" {
			cred.Secret = prev.Secret
		}
	}
	cred.Fingerprint, cred.Server = b.Fingerprint, b.Server
	raw, err := cred.Marshal()
	if err != nil {
		return err
	}
	if err := store.Set(ctx, ref, raw); err != nil {
		return fmt.Errorf("failed to save credentials for %s: %w", ref.Name, err)
	}
	r.redactor.AddCredential(cred)
	if kind == MethodPrompt && remember {
		if err := r.keyring.Set(ctx, ref, raw); err != nil {
			return fmt.Errorf("failed to remember credentials for %s in the keychain: %w", ref.Name, err)
		}
	}
	return nil
}

func (r *Resolver) loadExisting(ctx context.Context, s Store, ref Ref) (Credential, error) {
	raw, err := s.Get(ctx, ref)
	if err != nil {
		return Credential{}, err
	}
	return ParseCredential(raw)
}

// refuseUnstored is the error for writing a secret under a method that has no
// stored secret.
func refuseUnstored(kind string) error {
	switch kind {
	case "env":
		return fmt.Errorf("%w: environment variables are set outside Slaw", ErrReadOnly)
	case MethodAWSSSO:
		return fmt.Errorf("%w: an AWS SSO sign-in has no stored secret; sign in with your profile", ErrReadOnly)
	}
	return nil
}

// Credential field names for ClearCredentialField.
const (
	FieldUser     = "user"
	FieldPassword = "password"
	FieldToken    = "token"
	FieldKeyID    = "key_id"
	FieldSecret   = "secret"
)

// ClearCredentialField removes one field of the stored credential (an empty
// field in SetCredentials means "keep", so this is the way to drop one).
func (r *Resolver) ClearCredentialField(ctx context.Context, ref Ref, method, field string) error {
	kind, _, err := ParseMethod(method)
	if err != nil {
		return err
	}
	if err := refuseUnstored(kind); err != nil {
		return err
	}
	store := r.keyring
	if kind == MethodPrompt {
		store = r.memory
	}
	// A credential held for this session only (sign-in prompt, Remember off) is
	// cleared from memory too.
	var memErr error
	if kind != MethodPrompt {
		if _, merr := r.memory.Get(ctx, ref); merr == nil {
			memErr = r.clearField(ctx, r.memory, ref, field)
			if _, kerr := r.keyring.Get(ctx, ref); errors.Is(kerr, ErrNotFound) {
				return memErr
			}
		}
	}
	if err := r.clearField(ctx, store, ref, field); err != nil {
		return err
	}
	return memErr
}

func (r *Resolver) clearField(ctx context.Context, store Store, ref Ref, field string) error {
	cred, err := r.loadExisting(ctx, store, ref)
	if err != nil {
		return err
	}
	switch field {
	case FieldUser:
		cred.User = ""
	case FieldPassword:
		cred.Password = ""
	case FieldToken:
		cred.Token = ""
	case FieldKeyID:
		cred.KeyID = ""
	case FieldSecret:
		cred.Secret = ""
	default:
		return fmt.Errorf("unknown credential field %q", field)
	}
	raw, err := cred.Marshal()
	if err != nil {
		return err
	}
	return store.Set(ctx, ref, raw)
}

// RememberPrompted promotes a prompt credential held in memory to the
// keychain. Off by default: nothing calls it unless the person ticks Remember.
func (r *Resolver) RememberPrompted(ctx context.Context, ref Ref) error {
	raw, err := r.memory.Get(ctx, ref)
	if err != nil {
		return fmt.Errorf("nothing to remember for %s: %w", ref.Name, err)
	}
	if err := r.keyring.Set(ctx, ref, raw); err != nil {
		return fmt.Errorf("failed to remember credentials for %s in the keychain: %w", ref.Name, err)
	}
	return nil
}

// SetSessionCredentials holds cred in memory only, bound to b, for a
// connection whose sign-in method stays the keychain: the sign-in prompt with
// "Remember" off. Nothing is written to the keychain, and the person's sign-in
// method is not changed. It is gone when the process ends or the credential is
// cleared.
func (r *Resolver) SetSessionCredentials(ctx context.Context, ref Ref, cred Credential, b Binding) error {
	cred.Fingerprint, cred.Server = b.Fingerprint, b.Server
	raw, err := cred.Marshal()
	if err != nil {
		return err
	}
	if err := r.memory.Set(ctx, ref, raw); err != nil {
		return fmt.Errorf("failed to hold credentials for %s: %w", ref.Name, err)
	}
	r.redactor.AddCredential(cred)
	return nil
}

// ForgetSession zeroes everything held in memory: "ask each time" credentials and
// session-only ones from the sign-in prompt. Studio calls it when a workspace
// closes, is switched away from, or is released, so a credential typed for one
// session never outlives it.
func (r *Resolver) ForgetSession() { r.memory.Clear() }

// ClearCredentials removes the keychain entry and any in-memory one.
func (r *Resolver) ClearCredentials(ctx context.Context, ref Ref) error {
	_ = r.memory.Delete(ctx, ref)
	if err := r.keyring.Delete(ctx, ref); err != nil {
		return fmt.Errorf("failed to clear credentials for %s: %w", ref.Name, err)
	}
	return nil
}
