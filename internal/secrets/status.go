package secrets

import (
	"context"
	"errors"
	"strings"
	"time"
)

// EnvVarStatus says whether one environment variable is set, never its value.
type EnvVarStatus struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// CredentialStatus is what the UI may know about a connection's credential:
// the readable fields, and whether the secret is set. It never carries a
// secret value.
type CredentialStatus struct {
	Method    string `json:"method"`
	Username  string `json:"username,omitempty"`
	KeyID     string `json:"keyId,omitempty"`
	SecretSet bool   `json:"secretSet"`
	// SessionOnly is true when the credential is held in memory for this session
	// only (the sign-in prompt with "Remember" off), not in the keychain.
	SessionOnly        bool           `json:"sessionOnly,omitempty"`
	FingerprintMatches bool           `json:"fingerprintMatches"`
	Available          bool           `json:"available"`
	Detail             string         `json:"detail,omitempty"`
	Env                []EnvVarStatus `json:"env,omitempty"`
	// Profile, SignedIn and ExpiresAt describe an aws-sso sign-in: the profile
	// name, whether its session is usable, and when the session ends. They are
	// never a key.
	Profile   string     `json:"profile,omitempty"`
	SignedIn  bool       `json:"signedIn,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// Status describes the credential for ref under method. Errors from the store
// become Available=false with a plain Detail; the method is "keychain" when
// empty.
func (r *Resolver) Status(ctx context.Context, ref Ref, method string, b Binding) CredentialStatus {
	kind, prefix, err := ParseMethod(method)
	if err != nil {
		return CredentialStatus{Method: method, Available: true, FingerprintMatches: true, Detail: err.Error()}
	}
	if method == "" {
		method = MethodKeychain
	}
	if kind == MethodAWSSSO {
		return r.providedStatus(ctx, kind, prefix, method)
	}
	st := CredentialStatus{Method: method, Available: true, FingerprintMatches: true}

	if kind == "env" {
		e := r.env(prefix)
		set := e.SetVars()
		names := EnvVarNamesFor(prefix, b.EnvFields)
		for _, n := range names {
			st.Env = append(st.Env, EnvVarStatus{Name: n, Set: set[n]})
		}
		if c, ok := e.Load(); ok {
			st.Username, st.KeyID, st.SecretSet = c.User, c.KeyID, c.HasSecret()
		}
		if !st.SecretSet {
			st.Detail = "None of " + prefix + "_PASSWORD, " + prefix + "_TOKEN or " + prefix + "_SECRET is set."
			if len(b.EnvFields) > 0 {
				st.Detail = "None of " + strings.Join(names, ", ") + " is set."
			}
			if r.envHint != nil {
				if h := r.envHint(prefix); h != "" {
					st.Detail += " " + h
				}
			}
		}
		return st
	}

	store := r.keyring
	if kind == MethodPrompt {
		store = r.memory
	} else if _, merr := r.memory.Get(ctx, ref); merr == nil {
		store, st.SessionOnly = r.memory, true
	}
	raw, err := store.Get(ctx, ref)
	switch {
	case errors.Is(err, ErrNotFound):
		return st
	case err != nil:
		st.Available = false
		st.Detail = "Can't reach your keychain. Set the credentials as environment variables instead and choose Environment variables for this connection."
		if !errors.Is(err, ErrUnavailable) {
			st.Detail = err.Error()
		}
		return st
	}
	cred, err := ParseCredential(raw)
	if err != nil {
		st.Detail = err.Error()
		return st
	}
	st.Username, st.KeyID, st.SecretSet = cred.User, cred.KeyID, cred.HasSecret()
	if cred.Fingerprint != b.Fingerprint {
		st.FingerprintMatches = false
		st.Detail = (&ErrFingerprintChanged{Old: cred.Server, New: b.Server}).Error()
	}
	return st
}

// Probe reports whether the keychain is usable, for the settings screen.
type Probe struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	Detail    string `json:"detail,omitempty"`
}

// ProbeKeychain checks the keychain with one lookup under the usual timeout.
func (r *Resolver) ProbeKeychain(ctx context.Context) Probe {
	p := Probe{Backend: BackendName()}
	var err error
	if kp, ok := r.keyring.(interface{ Probe(context.Context) error }); ok {
		err = kp.Probe(ctx)
	} else if _, gerr := r.keyring.Get(ctx, AppRef("probe")); !errors.Is(gerr, ErrNotFound) {
		err = gerr
	}
	if err != nil {
		p.Detail = "Slaw can't reach your keychain (" + strings.TrimPrefix(err.Error(), ErrUnavailable.Error()+": ") + "). Set credentials as environment variables instead."
		return p
	}
	p.Available = true
	return p
}
