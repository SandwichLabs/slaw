package session

// Connection requirements: a connection that reads with another connection's
// secret (a DuckLake catalog or data path, an Iceberg or Delta table in object
// storage) names it in Spec.Requires, and the hub brings it up first.

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

type requireChainKey struct{}

// connectRequires connects each required connection, then checks that it is
// ready. A requirement that is missing, itself in the chain, or not ready sets
// the dependent to StatusError ("needs the "floci" connection: it needs
// credentials") and returns an error. The message names the requirement and
// its state only; it never carries the requirement's own (possibly raw) error.
func (h *hub) connectRequires(ctx context.Context, name string, requires []string) error {
	if len(requires) == 0 {
		return nil
	}
	chain, _ := ctx.Value(requireChainKey{}).([]string)
	chain = append(slices.Clone(chain), name)
	ctx = context.WithValue(ctx, requireChainKey{}, chain)
	for _, dep := range requires {
		if msg := h.requirementProblem(ctx, dep, chain); msg != "" {
			return h.requirementError(name, dep, msg)
		}
	}
	return nil
}

// requirementError records "needs the "dep" connection: why" as name's status
// (unless it is attached: an attached connection keeps its status) and returns
// the error to report.
func (h *hub) requirementError(name, dep, why string) error {
	msg := fmt.Sprintf("needs the %q connection: %s", dep, why)
	h.mu.Lock()
	if e := h.conns[name]; e != nil && !e.attach {
		kind, hint := StatusError, (*secrets.SignInHint)(nil)
		// A requirement that only needs the person to sign in is a sign-in to
		// do, not a failure: the dependent shows the same state (and the same
		// hint) as the connection it waits on.
		if d := h.conns[dep]; d != nil && d.status.Kind == StatusNeedsCredentials {
			kind, hint = StatusNeedsCredentials, d.status.SignIn
		}
		e.status.Kind, e.status.Message, e.status.LockedByPID, e.status.ServerChanged, e.status.Extension, e.status.SignIn = kind, msg, 0, nil, "", hint
		e.depBlocked = dep
	}
	h.mu.Unlock()
	return fmt.Errorf("failed to connect %q: %s", name, msg)
}

// unmetRequirement is the fail-closed check made under adminMu, right before
// anything is staged or attached: every requirement must be attached now. A
// storage secret is dropped only under adminMu, so what this sees holds until
// the attach is done; without it DuckDB would read the URL anonymously.
// It connects nothing.
func (h *hub) unmetRequirement(requires []string) (dep, why string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, d := range requires {
		e := h.conns[d]
		switch {
		case e == nil:
			return d, "it is not defined"
		case !e.attach:
			return d, requirementPhrase(e.status.Kind)
		}
	}
	return "", ""
}

// dependentsLocked lists the connections that name dep in Requires. Callers hold
// h.mu (either mode).
func (h *hub) dependentsLocked(dep string) []*entry {
	var out []*entry
	for _, e := range h.conns {
		if slices.Contains(e.spec.Requires, dep) {
			out = append(out, e)
		}
	}
	return out
}

// detachDependents detaches every attached connection that requires dep. The
// caller holds adminMu and has just dropped (or is about to drop) dep's
// secret: a dependent left attached would read its URLs with no secret, which
// DuckDB answers from the AWS default endpoint, anonymously. A detached
// dependent is "not connected" and attaches again on next use (the requirement
// is checked then). A dependent that cannot be detached is reported, and the
// caller keeps the secret.
func (h *hub) detachDependents(ctx context.Context, conn *sql.Conn, dep string) error {
	h.mu.RLock()
	deps := h.dependentsLocked(dep)
	h.mu.RUnlock()
	var failed []string
	for _, e := range deps {
		name := e.spec.Name
		if e2, att := h.entryOf(name); e2 == nil || !att {
			continue
		}
		if !h.catalogless(e) {
			if _, err := conn.ExecContext(ctx, "DETACH "+QuoteIdent(name)); err != nil {
				h.logger.Warn("could not detach a dependent", "name", name, "requires", dep)
				failed = append(failed, name)
				continue
			}
		}
		h.dropSecretsOn(ctx, conn, e.secrets)
		h.removeStage(name)
		h.mu.Lock()
		e.attach, e.secrets = false, nil
		e.status = Status{Name: name, Type: e.spec.Type, Kind: StatusNotConnected, ReadOnly: e.spec.ReadOnly}
		h.mu.Unlock()
	}
	h.InvalidateSchema("")
	if len(failed) > 0 {
		// Fail closed: the caller must then keep dep's secret, or these would
		// read without one.
		return fmt.Errorf("could not detach %s, which reads with %q", strings.Join(failed, ", "), dep)
	}
	return nil
}

// readyDependents is called when dep has become attached: dependents that were
// waiting on it ("needs the dep connection") go back to not connected, and the
// catalog-less ones (a Delta or Iceberg location, which connect at preflight)
// are preflighted again, because nothing else would connect them.
func (h *hub) readyDependents(ctx context.Context, dep string) {
	h.mu.Lock()
	var again []string
	for _, e := range h.dependentsLocked(dep) {
		if e.attach {
			continue
		}
		if e.depBlocked == dep {
			e.depBlocked = ""
			e.status = Status{Name: e.spec.Name, Type: e.spec.Type, Kind: StatusNotConnected, ReadOnly: e.spec.ReadOnly}
		}
		again = append(again, e.spec.Name)
	}
	h.mu.Unlock()
	for _, n := range again {
		if e, _ := h.entryOf(n); e != nil && h.catalogless(e) {
			h.Preflight(ctx, n)
		}
	}
}

// requirementProblem connects dep and returns why it is not usable ("" when it
// is ready).
func (h *hub) requirementProblem(ctx context.Context, dep string, chain []string) string {
	if slices.Contains(chain, dep) {
		return "that would depend on itself"
	}
	if e, _ := h.entryOf(dep); e == nil {
		return "it is not defined"
	}
	_ = h.connectImplicit(ctx, dep) // the outcome is read back from its status
	e, att := h.entryOf(dep)
	if e == nil {
		return "it is not defined"
	}
	if att {
		return ""
	}
	h.mu.RLock()
	kind := e.status.Kind
	h.mu.RUnlock()
	return requirementPhrase(kind)
}

// requirementPhrase is a short human phrase for a requirement's state.
func requirementPhrase(k StatusKind) string {
	switch k {
	case StatusNeedsCredentials:
		return "it needs credentials"
	case StatusExtensionMissing:
		return "its DuckDB extension is not installed"
	case StatusLockedBy:
		return "it is locked by another process"
	case StatusError:
		return "it could not connect"
	}
	return "it is not connected"
}
