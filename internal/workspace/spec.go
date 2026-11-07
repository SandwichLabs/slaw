package workspace

import (
	"context"
	"fmt"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

// SessionSpec builds the session.Spec for one connection of ws, exactly as
// OpenSession would register it: ${ENV} expanded, paths resolved, the
// credential closure bound to the connection's fingerprint and sign-in method.
// Studio uses it to re-register a connection after the person edits it, or
// after slaw.yaml changes on disk, so a stale credential binding never outlives
// the definition it was made for. It dials nothing. MotherDuck's hub-wide token
// is not handled here: a change of it is reported by Session.RelaunchRequired.
func SessionSpec(ws Workspace, name string, opts ...SessionOption) (session.Spec, error) {
	var o sessionOpts
	for _, f := range opts {
		f(&o)
	}
	resolver := ws.Resolver()
	c, found := ws.Connection(name)
	if !found {
		return session.Spec{}, fmt.Errorf("unknown connection %q", name)
	}
	rc, err := ResolveConnection(ws, c, o.lookup)
	if err != nil {
		return session.Spec{}, fmt.Errorf("%s", secrets.RedactPatterns(err.Error()))
	}
	resolved := rc.Resolved
	if resolved.Type == "motherduck" && resolved.ReadOnly != nil && *resolved.ReadOnly {
		return session.Spec{}, fmt.Errorf("%s", connections.ErrMotherDuckReadOnly)
	}
	readOnly := resolved.IsReadOnly() || o.readOnly
	if resolved.Type == "motherduck" {
		readOnly = false
	}
	cdef := rc.Def(readOnly)
	spec := cdef.Spec()
	spec.Requires = rc.Requires
	if resolved.Type == "motherduck" && o.readOnly && !o.policyGated {
		spec.Params["forced_read_only"] = "true"
	}
	if resolved.Type == "duckdb" {
		spec.Path = rc.Path
	} else if resolved.Type != "" {
		if err := connections.Validate(cdef); err != nil {
			return session.Spec{}, fmt.Errorf("%s", secrets.RedactPatterns(err.Error()))
		}
	}
	if connections.NeedsCredentialFor(cdef) {
		ref := credentialRef(ws.ID(), c.Scope, c.Name)
		b, method := rc.Binding(), c.SignIn
		guard := signInGuard(resolver, c, rc.Resolved)
		spec.Credential = func(ctx context.Context) (secrets.Credential, error) {
			if guard != nil {
				return secrets.Credential{}, guard
			}
			return resolver.Resolve(ctx, ref, method, b)
		}
	}
	return spec, nil
}

// CredentialRef is the keychain reference of a connection's credential in the
// given scope (the workspace id for workspace scopes, none for user).
func CredentialRef(wsID string, scope Scope, name string) secrets.Ref {
	return credentialRef(wsID, scope, name)
}
