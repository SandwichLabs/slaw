package session

// Registering, attaching and connecting a hub's connections: backoff, preflight,
// credential readiness and the qualifier scan that decides what to dial.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// --- connections ---

func (h *hub) Register(spec Spec) error {
	if err := ValidateName(spec.Name); err != nil {
		return err
	}
	if spec.Type == "" {
		spec.Type = "duckdb"
	}
	t, err := h.types.Get(spec.Type)
	if err != nil {
		return err
	}
	if err := t.Validate(spec); err != nil {
		return err
	}
	// adminMu first (then mu): swapping an entry while connectLocked is between
	// its re-read and setting attach would orphan the entry the catalog (and
	// its secrets) were attached under.
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.conns[spec.Name]; ok && old.attach {
		return fmt.Errorf("connection %q is already attached", spec.Name)
	}
	h.conns[spec.Name] = &entry{spec: spec, status: Status{
		Name: spec.Name, Type: spec.Type, Kind: StatusNotConnected, ReadOnly: spec.ReadOnly,
	}}
	return nil
}

func (h *hub) Attach(ctx context.Context, spec Spec) error {
	if err := h.Register(spec); err != nil {
		return err
	}
	return h.Connect(ctx, spec.Name)
}

// Connect dials a registered connection now. It is the explicit path (the
// person asked): it clears any remembered failure and always tries.
func (h *hub) Connect(ctx context.Context, name string) error {
	h.clearFailure(name)
	return h.connect(ctx, name)
}

// failureBackoff is how long an implicit connect (a query that merely names
// the connection) trusts a recorded failure before dialling again.
const failureBackoff = 30 * time.Second

type failure struct {
	at  time.Time
	err error
}

func (h *hub) clearFailure(name string) {
	h.mu.Lock()
	delete(h.failures, name)
	h.mu.Unlock()
}

func (h *hub) recordFailure(name string, err error) {
	h.mu.Lock()
	if h.failures == nil {
		h.failures = map[string]failure{}
	}
	h.failures[name] = failure{at: time.Now(), err: err}
	h.mu.Unlock()
}

// connectImplicit is Connect for first use: a failure inside the backoff
// window is returned as recorded instead of dialling (and sending a password)
// on every statement.
func (h *hub) connectImplicit(ctx context.Context, name string) error {
	h.mu.RLock()
	f, ok := h.failures[name]
	h.mu.RUnlock()
	if ok && time.Since(f.at) < failureBackoff {
		return f.err
	}
	return h.connect(ctx, name)
}

func (h *hub) connect(ctx context.Context, name string) error {
	e, att := h.entryOf(name)
	if e == nil {
		return fmt.Errorf("unknown connection %q", name)
	}
	if att {
		return nil
	}
	// Requirements first, with no lock held: they take adminMu themselves.
	if err := h.connectRequires(ctx, name, e.spec.Requires); err != nil {
		return err // not recorded as a failure: nothing was dialled, and fixing the requirement must not wait out a backoff
	}
	err := h.connectLocked(ctx, name)
	if err != nil {
		h.recordFailure(name, err)
	}
	return err
}

func (h *hub) connectLocked(ctx context.Context, name string) error {
	// Attach / secret DDL is serialised on a dedicated connection and is never
	// logged: not the statements, not the parameters.
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	// Another goroutine may have connected while this one waited. After this
	// point nothing else can attach or fail this entry, so a failure below
	// never touches an already-attached connection's secrets or status.
	e, att := h.entryOf(name)
	if e == nil {
		return fmt.Errorf("unknown connection %q", name)
	}
	if att {
		return nil
	}
	// Fail closed: the requirements were connected before adminMu was taken, and
	// a Preflight may have dropped a storage secret since. Secrets change only
	// under adminMu, so what holds here holds through the attach.
	if dep, why := h.unmetRequirement(e.spec.Requires); dep != "" {
		return h.requirementError(name, dep, why)
	}
	t, err := h.types.Get(e.spec.Type)
	if err != nil {
		return err
	}
	conn, err := h.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get admin connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	var secretNames []string
	var expires time.Time
	if ct, ok := t.(CredentialedType); ok {
		cred, kind, msg, err := h.readyForConnect(ctx, conn, e, ct)
		if err != nil {
			h.setStatusCause(name, kind, msg, err)
			return fmt.Errorf("failed to connect %q: %s", name, msg)
		}
		expires = cred.Expires
		steps, serr := ct.SecretSteps(e.spec, cred)
		if serr != nil {
			msg := h.redact(serr.Error())
			h.setStatus(name, StatusError, msg, 0)
			return fmt.Errorf("failed to connect %q: %s", name, msg)
		}
		for _, st := range steps {
			if err := h.createSecretOn(ctx, conn, st.Name, st.Type, st.Params); err != nil {
				msg := h.redact(err.Error())
				h.dropSecretsOn(ctx, conn, secretNames)
				h.setStatus(name, StatusError, msg, 0)
				return fmt.Errorf("failed to connect %q: %s", name, msg)
			}
			secretNames = append(secretNames, st.Name)
		}
	}

	spec, done, err := h.stage(ctx, conn, t, e.spec)
	if err != nil {
		h.dropSecretsOn(ctx, conn, secretNames)
		msg := h.redact(err.Error())
		h.setStatus(name, StatusError, msg, 0)
		return fmt.Errorf("failed to attach %q: %s", name, msg)
	}
	attached := false
	defer func() {
		if !attached && done {
			h.removeStage(name)
		}
	}()
	stmts, err := t.AttachSQL(spec)
	if err != nil {
		h.dropSecretsOn(ctx, conn, secretNames)
		h.setStatus(name, StatusError, h.redact(err.Error()), 0)
		return fmt.Errorf("failed to attach %q: %s", name, h.redact(err.Error()))
	}
	if pre, ok := t.(PreAttacher); ok {
		if perr := pre.PreAttach(ctx, conn, spec); perr != nil {
			h.dropSecretsOn(ctx, conn, secretNames)
			msg := h.redact(perr.Error())
			h.setStatus(name, StatusError, msg, 0)
			return fmt.Errorf("failed to attach %q: %s", name, msg)
		}
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			kind, msg, pid := classifyAttachError(err, h.redact)
			h.dropSecretsOn(ctx, conn, secretNames)
			h.setStatus(name, kind, msg, pid)
			return fmt.Errorf("failed to attach %q: %s", name, msg)
		}
	}
	if pa, ok := t.(PostAttacher); ok {
		if perr := pa.PostAttach(ctx, conn, spec); perr != nil {
			_, _ = conn.ExecContext(ctx, "DETACH "+QuoteIdent(name))
			h.dropSecretsOn(ctx, conn, secretNames)
			msg := h.redact(perr.Error())
			h.setStatus(name, StatusError, msg, 0)
			return fmt.Errorf("failed to attach %q: %s", name, msg)
		}
	}
	attached = true
	h.mu.Lock()
	e.attach = true
	e.secrets = secretNames
	e.expires = expires
	e.secretGen++
	e.closeRefused = false
	e.status = Status{Name: name, Type: e.spec.Type, Kind: StatusOK, ReadOnly: e.spec.ReadOnly}
	delete(h.failures, name)
	h.mu.Unlock()
	h.InvalidateSchema("")
	h.logger.Info("connection attached", "name", name, "type", e.spec.Type)
	return nil
}

func (h *hub) dropSecretsOn(ctx context.Context, conn *sql.Conn, names []string) {
	for _, n := range names {
		_, _ = conn.ExecContext(ctx, "DROP TEMPORARY SECRET IF EXISTS "+QuoteIdent(n))
	}
}

// credentialKind maps a credential-resolution failure to a status.
func credentialKind(err error) (StatusKind, string) {
	var nc *secrets.ErrNeedsCredentials
	var fp *secrets.ErrFingerprintChanged
	switch {
	case errors.As(err, &nc), errors.As(err, &fp), errors.Is(err, secrets.ErrUnavailable), errors.Is(err, secrets.ErrNotFound):
		return StatusNeedsCredentials, err.Error()
	}
	return StatusError, err.Error()
}

// readyForConnect checks everything that can be known without dialling, in the
// order of the status precedence: extension, then credential, then (MotherDuck)
// the hub token. It loads an installed-but-unloaded extension (local) and never
// installs one.
func (h *hub) readyForConnect(ctx context.Context, conn *sql.Conn, e *entry, ct CredentialedType) (secrets.Credential, StatusKind, string, error) {
	hubTok := false
	if ht, ok := ct.(HubTokenType); ok && ht.UsesHubToken() {
		hubTok = true
	}
	for _, ext := range ExtensionsFor(ct, e.spec) {
		installed, loaded, err := extensionState(ctx, conn, ext)
		if err != nil {
			return secrets.Credential{}, StatusError, h.redact(err.Error()), err
		}
		switch {
		case !installed:
			return secrets.Credential{}, StatusExtensionMissing, fmt.Sprintf("The DuckDB %q extension is not installed. Install it (a one-time download) to use %s connections.", ext, ct.Name()), &missingExtensionError{name: ext}
		case !loaded && hubTok:
			// MotherDuck must be loaded with its token before the lock.
		case !loaded:
			if _, err := conn.ExecContext(ctx, "LOAD "+ext); err != nil {
				return secrets.Credential{}, StatusError, h.redact(err.Error()), err
			}
		}
	}
	if err := DenyUncoveredStorage(ctx, conn); err != nil {
		return secrets.Credential{}, StatusError, h.redact(err.Error()), err
	}
	var cred secrets.Credential
	if ct.NeedsCredential(e.spec) {
		if e.spec.Credential == nil {
			return secrets.Credential{}, StatusNeedsCredentials, "No credentials are set for this connection.", errNotReady
		}
		var err error
		cred, err = e.spec.Credential(ctx)
		if err != nil {
			kind, msg := credentialKind(err)
			return secrets.Credential{}, kind, h.redact(msg), err
		}
		h.redactor.AddCredential(cred)
		if hubTok {
			if cred.Token == "" {
				return secrets.Credential{}, StatusNeedsCredentials, "No MotherDuck token is set for this connection.", errNotReady
			}
			if msg := h.checkHubToken(cred.Token); msg != "" {
				h.setRelaunch(msg)
				return secrets.Credential{}, StatusError, msg, errNotReady
			}
		}
	} else if hubTok {
		return secrets.Credential{}, StatusError, "a MotherDuck connection needs a token", errNotReady
	}
	return cred, StatusOK, "", nil
}

var errNotReady = errors.New("connection is not ready")

// missingExtensionError is errNotReady for an extension that is not installed;
// it names the first missing one, so the status can offer exactly that download
// (a type may need several, and they install one at a time, in order).
type missingExtensionError struct{ name string }

func (e *missingExtensionError) Error() string        { return errNotReady.Error() }
func (e *missingExtensionError) Is(target error) bool { return target == errNotReady }

// Preflight implements Session. It also resets a remembered connect failure
// (the person changed something), and brings a storage connection (s3, gcs,
// r2: a secret, no catalog) up as soon as it is ready, because creating a
// secret dials nothing; a read of s3://... then just works. Call it again after
// credentials change: storage secrets are replaced, or dropped when the
// credential is gone.
func (h *hub) Preflight(ctx context.Context, name string) {
	h.clearFailure(name)
	e, att := h.entryOf(name)
	if e == nil {
		return
	}
	h.preflightEntry(ctx, name, e, att)
}

// preflightEntry is Preflight for the entry (and attached state) it was
// handed, which may be stale by the time adminMu is taken.
func (h *hub) preflightEntry(ctx context.Context, name string, e *entry, att bool) {
	t, err := h.types.Get(e.spec.Type)
	if err != nil {
		return
	}
	ct, ok := t.(CredentialedType)
	if !ok {
		return
	}
	storage := h.catalogless(e)
	if att && !storage {
		return
	}
	if att && storage {
		// Drop the old secret first: the credential may have changed or gone.
		h.adminMu.Lock()
		// e and att were read before adminMu: Detach and Register may have
		// replaced the entry since. Dropping the stale entry's secrets would
		// then drop the secret of the new entry (same secret names), which
		// stays attached with none and lets its dependents attach bare.
		cur, curAtt := h.entryOf(name)
		if cur != e {
			h.adminMu.Unlock()
			return
		}
		if !curAtt {
			h.adminMu.Unlock()
			h.preflightDetached(ctx, name, e, ct, storage)
			return
		}
		if conn, err := h.db.Conn(ctx); err == nil {
			if derr := h.detachDependents(ctx, conn, name); derr != nil {
				// Fail closed: keep the old secret rather than leave a dependent
				// reading without one. Nothing else changes.
				_ = conn.Close()
				h.adminMu.Unlock()
				h.logger.Warn("storage credential change not applied", "name", name)
				return
			}
			h.dropSecretsOn(ctx, conn, e.secrets)
			_ = conn.Close()
		}
		h.mu.Lock()
		e.attach, e.secrets = false, nil
		h.mu.Unlock()
		h.adminMu.Unlock()
	}
	h.preflightDetached(ctx, name, e, ct, storage)
}

// preflightDetached is the rest of Preflight for a connection that is not
// attached: record whether it is ready, and bring a storage connection up.
func (h *hub) preflightDetached(ctx context.Context, name string, e *entry, ct CredentialedType, storage bool) {
	conn, err := h.db.Conn(ctx)
	if err != nil {
		return
	}
	// readyForConnect would LOAD an installed extension; that is local and
	// cheap, and Connect would do the same.
	_, kind, msg, rerr := h.readyForConnect(ctx, conn, e, ct)
	_ = conn.Close()
	if rerr != nil {
		h.setStatusCause(name, kind, msg, rerr)
		return
	}
	h.setStatus(name, StatusNotConnected, "", 0)
	if storage {
		if h.connect(ctx, name) == nil {
			h.readyDependents(ctx, name)
		}
	}
}

// InstallExtension implements Session.
func (h *hub) InstallExtension(ctx context.Context, name string) error {
	allowed := false
	h.mu.RLock()
	typesList := make([]ConnectionType, 0, len(h.types.types))
	for _, t := range h.types.types {
		typesList = append(typesList, t)
	}
	h.mu.RUnlock()
	for _, t := range typesList {
		if ct, ok := t.(CredentialedType); ok {
			for _, x := range ct.Extensions() {
				if x == name {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return fmt.Errorf("%q is not an extension any connection type needs", name)
	}
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	conn, err := h.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get admin connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	if name == "motherduck" {
		// Install only: loading it needs the token set before the lock, so the
		// workspace is reopened afterwards.
		if _, err := conn.ExecContext(ctx, "INSTALL motherduck"); err != nil {
			return fmt.Errorf("MotherDuck needs a one-time download of its DuckDB extension, and it could not be fetched. Check your network connection and try again: %s", h.redact(err.Error()))
		}
		h.mu.RLock()
		loaded := h.md != nil && h.md.loaded
		h.mu.RUnlock()
		if !loaded {
			h.setRelaunch("The MotherDuck extension is installed now. Reopen the workspace so it can sign in.")
		}
		return nil
	}
	return EnsureExtension(ctx, conn, name, name+" connections")
}

// RefreshCatalog implements Session.
func (h *hub) RefreshCatalog(ctx context.Context, name string) error {
	e, eAtt := h.entryOf(name)
	if e == nil || !eAtt {
		return fmt.Errorf("connection %q is not connected", name)
	}
	if t, err := h.types.Get(e.spec.Type); err == nil {
		if r, ok := t.(Refresher); ok {
			if _, err := h.db.ExecContext(ctx, r.RefreshSQL()); err != nil {
				return fmt.Errorf("failed to refresh %q: %s", name, h.redact(err.Error()))
			}
		}
	}
	h.InvalidateSchema("")
	return nil
}

// ConnectReferenced implements Session.
func (h *hub) ConnectReferenced(ctx context.Context, catalog, sqlText string) error {
	// Before the "nothing pending" early return: it is the attached storage
	// secrets that may be about to end, not the pending connections.
	rotErr := h.rotateDue(ctx)
	h.reviveSignedOut(ctx)
	h.mu.RLock()
	var pending []string
	for n, e := range h.conns {
		if !e.attach {
			pending = append(pending, n)
		}
	}
	h.mu.RUnlock()
	if len(pending) == 0 {
		return rotErr
	}
	sort.Strings(pending)
	named := qualifiers(sqlText)
	firstErr := rotErr
	for _, n := range pending {
		if n == catalog || named[strings.ToLower(n)] {
			if e, _ := h.entryOf(n); e != nil && h.catalogless(e) {
				continue // storage secrets are made at preflight; a URL names no catalog
			}
			if err := h.connectImplicit(ctx, n); err != nil {
				firstErr = errors.Join(firstErr, err)
			}
		}
	}
	return firstErr
}

// qualifiers returns the lower-cased names that appear in qualifier position
// (immediately followed by a dot: pg.public.orders, "pg".t) outside string
// literals (single-quoted, E”, dollar-quoted) and comments. A word that is
// merely a column alias or a local table name (CREATE TABLE sales, AS sales)
// is not a qualifier, so a pending connection that shares it is not dialled.
func qualifiers(q string) map[string]bool {
	out := map[string]bool{}
	rs := []rune(q)
	n := len(rs)
	isWord := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	for i := 0; i < n; {
		r := rs[i]
		switch {
		case r == '\'':
			for i++; i < n; i++ {
				if rs[i] == '\'' {
					if i+1 < n && rs[i+1] == '\'' {
						i++
						continue
					}
					break
				}
			}
			i++
		case r == '-' && i+1 < n && rs[i+1] == '-':
			for i < n && rs[i] != '\n' {
				i++
			}
		case r == '/' && i+1 < n && rs[i+1] == '*':
			i += 2
			for i+1 < n && !(rs[i] == '*' && rs[i+1] == '/') {
				i++
			}
			i += 2
		case r == '$':
			j := i + 1
			for j < n && isWord(rs[j]) {
				j++
			}
			if j < n && rs[j] == '$' && (j == i+1 || !unicode.IsDigit(rs[i+1])) {
				delim := string(rs[i : j+1])
				k := j + 1
				for k < n && !strings.HasPrefix(string(rs[k:min(k+len(delim), n)]), delim) {
					k++
				}
				i = k + len(delim)
			} else {
				i++
			}
		case r == '"':
			var b strings.Builder
			for i++; i < n; i++ {
				if rs[i] == '"' {
					if i+1 < n && rs[i+1] == '"' {
						b.WriteRune('"')
						i++
						continue
					}
					break
				}
				b.WriteRune(rs[i])
			}
			i++
			if i < n && rs[i] == '.' {
				out[strings.ToLower(b.String())] = true
			}
		case r == '_' || unicode.IsLetter(r):
			j := i
			for j < n && (isWord(rs[j]) || rs[j] == '$') { // x$a$ is one name
				j++
			}
			// A dot before the word means it is not the first qualifier part.
			if j < n && rs[j] == '.' && (i == 0 || rs[i-1] != '.') {
				out[strings.ToLower(string(rs[i:j]))] = true
			}
			i = j
		default:
			i++
		}
	}
	return out
}
