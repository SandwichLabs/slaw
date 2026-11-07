package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// credentialTimeout bounds the credential lookup of one test (a keychain may
// prompt).
const credentialTimeout = 15 * time.Second

// TestCredential is the credential `slaw conn test` and Studio's Test send for
// rc: this person's saved sign-in by the method in effect (signIn, or the saved
// connection's, or the keychain), with whatever was typed in this edit laid
// over it. rc is the definition being tested: the saved one, or an edit of it.
//
// Server-changed guard: when rc differs, in any fingerprint field, from the
// saved definition, or the saved sign-in is no longer bound to the saved
// definition (a pull changed it), no stored credential of any method (keychain,
// environment or prompt) is used. Only typed values are. When the type needs a
// secret and none was typed (a prefilled user name is not a secret), it returns
// *secrets.ErrFingerprintChanged and no credential. A connection that does not
// exist yet has nothing stored, but an environment or AWS SSO sign-in the
// person chose for it is theirs to use.
//
// A failure to find a stored credential is returned as err only when nothing
// typed made up for it; the (possibly partial) credential comes back with it,
// and a caller that wants Test to report the missing credential may ignore err
// unless it is *secrets.ErrFingerprintChanged.
func TestCredential(ctx context.Context, ws Workspace, rc ResolvedConnection, signIn string, typed secrets.Credential) (secrets.Credential, error) {
	needs := rc.Resolved.NeedsCredential()
	if !needs {
		// Nothing to look up (duckdb, sqlite, ...): the keychain is never touched.
		return secrets.Credential{}.Overlay(typed), nil
	}
	base, changed, resolveErr := savedTestCredential(ctx, ws, rc, signIn)
	if changed != nil {
		if needs && !typed.HasSecret() {
			return secrets.Credential{}, changed
		}
		base, resolveErr = secrets.Credential{}, nil
	}
	cred := base.Overlay(typed)
	if resolveErr != nil && needs && !cred.HasSecret() {
		return cred, resolveErr
	}
	return cred, nil
}

// savedTestCredential is the stored credential a test starts from. changed is
// set (and no credential returned) when the server guard applies; resolveErr is
// why nothing stored could be resolved.
func savedTestCredential(ctx context.Context, ws Workspace, rc ResolvedConnection, signIn string) (cred secrets.Credential, changed *secrets.ErrFingerprintChanged, resolveErr error) {
	method := signIn
	saved, hasSaved := ws.Connection(rc.Name)
	if hasSaved && method == "" {
		method = saved.SignIn
	}
	if method == "" {
		method = "keychain"
	}
	bound := boundSignIn(method)
	if !hasSaved && !bound {
		return secrets.Credential{}, nil, nil
	}
	if bound {
		// An AWS SSO identity goes only to AWS over TLS, whatever the form says.
		if err := ws.Resolver().CheckTarget(method, targetOf(rc.Resolved)); err != nil {
			return secrets.Credential{}, nil, fmt.Errorf("connection %q: %w", rc.Name, err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, credentialTimeout)
	defer cancel()
	if hasSaved {
		now := ServerDescription(rc.Resolved)
		savedResolved, err := saved.ConnectionDef.Resolve(nil, saved.Scope)
		if err != nil || Fingerprint(savedResolved) != Fingerprint(rc.Resolved) {
			return secrets.Credential{}, &secrets.ErrFingerprintChanged{Old: savedServer(saved), New: now}, nil
		}
		// The saved sign-in itself must still match its definition (the same guard
		// the session applies): keychain and prompt carry their own fingerprint,
		// an environment or AWS SSO sign-in is bound by the choice.
		if st, err := ws.CredentialStatus(ctx, rc.Name); err != nil || !st.FingerprintMatches {
			return secrets.Credential{}, &secrets.ErrFingerprintChanged{Old: savedServer(saved), New: now}, nil
		}
		if bound && saved.SignInFingerprint != Fingerprint(savedResolved) {
			return secrets.Credential{}, &secrets.ErrFingerprintChanged{Old: savedServer(saved), New: now}, nil
		}
	}
	cred, err := ws.Resolver().Resolve(ctx, CredentialRef(ws.ID(), rc.Scope, rc.Name), method, rc.Binding())
	if err != nil {
		var fp *secrets.ErrFingerprintChanged
		if errors.As(err, &fp) {
			return secrets.Credential{}, fp, nil
		}
		return secrets.Credential{}, nil, err
	}
	return cred, nil, nil
}

// savedServer names the server the saved sign-in was made for ("" when never
// chosen here: the message is then generic, never "X to X").
func savedServer(c Connection) string { return c.SignInServer }
