package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

// SessionIssue is a connection that could not be registered or attached. The
// message is redacted and never carries a credential, a DSN or an environment
// value.
type SessionIssue struct {
	Connection string `json:"connection"`
	Message    string `json:"message"`
}

// SessionOption configures OpenSession.
type SessionOption func(*sessionOpts)

type sessionOpts struct {
	skipLocal bool
	lookup    LookupFunc
	session   []session.Option
	readOnly  bool
	// policyGated: see WithPolicyGatedRuns.
	policyGated bool
	preparers   map[string]preparer
}

// WithForceReadOnly attaches every connection read-only whatever its
// definition says, for a process that does not own the hub (`slaw query
// --read-only`, `slaw workspace info`). A local DuckDB file that does not
// exist yet is reported as an issue instead of being created, and no folder
// is created for it.
func WithForceReadOnly() SessionOption { return func(o *sessionOpts) { o.readOnly = true } }

// WithPolicyGatedRuns goes with WithForceReadOnly, for a process whose every
// statement runs through the agent policy's read-only path (`slaw semantics
// check` and `profile`, through hub.ReadOnlyRunner). A connection type that
// cannot be attached read-only at all (MotherDuck) is then attached as it is
// defined, the way Studio attaches it for the same runs, instead of staying
// registered but unconnectable. Every other connection is still attached
// read-only, local files included; the policy, not the attach, is what keeps
// such a process from writing.
func WithPolicyGatedRuns() SessionOption { return func(o *sessionOpts) { o.policyGated = true } }

// WithSkipLocalFiles registers local duckdb and sqlite connections without
// attaching them (nor creating their files), for a command that only needs the
// remote ones and does not own the hub (`slaw conn connect`).
func WithSkipLocalFiles() SessionOption { return func(o *sessionOpts) { o.skipLocal = true } }

// WithSessionEnv replaces the environment lookup for ${ENV} interpolation.
func WithSessionEnv(f LookupFunc) SessionOption { return func(o *sessionOpts) { o.lookup = f } }

// WithSessionOptions passes options through to session.New.
func WithSessionOptions(opts ...session.Option) SessionOption {
	return func(o *sessionOpts) { o.session = append(o.session, opts...) }
}

// OpenSession builds the workspace's DuckDB hub: session.New() plus every
// merged connection registered.
//
//   - Local `duckdb` and `sqlite` file connections are attached eagerly (design
//     #106 item 8: a local file, nothing to dial). Every other type is only
//     registered and preflighted (credential and extension state, no network);
//     it connects on first use. The one MotherDuck token the hub may hold is
//     resolved here and handed to session.New, before the configuration locks.
//   - Relative paths resolve against the workspace root and may not leave it,
//     lexically or through a symlink. An absolute path is accepted only from
//     your own scopes (local, user), never from the shared slaw.yaml, and a
//     folder is only ever created inside the workspace. A relative path in a
//     user-scope connection (which belongs to no workspace) is refused.
//   - ${ENV} references are expanded here, and only in your own scopes; a
//     missing variable fails that one connection (reported as an issue), never
//     the whole workspace.
//   - default_connection becomes the session's default catalog once attached.
//
// A connection that cannot be set up is returned as a SessionIssue and left
// out; the workspace still opens with the rest. The error return is for
// failures of the hub itself.
func OpenSession(ctx context.Context, ws Workspace, logger *slog.Logger, opts ...SessionOption) (session.Session, []SessionIssue, error) {
	o := sessionOpts{preparers: defaultPreparers()}
	for _, f := range opts {
		f(&o)
	}
	if logger == nil {
		logger = slog.Default()
	}
	b := &sessionBuild{ws: ws, o: o, logger: logger, resolver: ws.Resolver()}

	// Pass 1: resolve every definition (no network, no dialling). The one
	// MotherDuck token the hub opens with is claimed here, because it must be
	// SET before the configuration is locked (spike #107).
	items := b.resolveAll(ctx, defaultFirst(ws.Connections(), ws.DefaultConnection()))

	sess, err := b.openHub()
	if err != nil {
		return nil, nil, err
	}
	// Pass 2: register everything; attach the eager (local file) connections;
	// leave the rest to connect on first use (lazy attach).
	attached := b.registerAll(ctx, sess, items)
	b.applyDefault(ctx, sess, ws.DefaultConnection(), attached)
	return sess, b.issues, nil
}

// defaultFirst orders conns with the default connection first (stable).
func defaultFirst(conns []Connection, def string) []Connection {
	sort.SliceStable(conns, func(i, j int) bool {
		return conns[i].Name == def && conns[j].Name != def
	})
	return conns
}

// sessionItem is one connection that passed pass 1.
type sessionItem struct {
	rc   ResolvedConnection
	spec session.Spec
	prep preparer
}

// sessionBuild carries the state of one OpenSession call across its steps.
type sessionBuild struct {
	ws       Workspace
	o        sessionOpts
	logger   *slog.Logger
	resolver *secrets.Resolver
	issues   []SessionIssue
	// mdToken and mdOwner are the one MotherDuck token the hub opens with and
	// the connection that supplied it.
	mdToken, mdOwner string
}

func (b *sessionBuild) failMsg(name, msg string) {
	b.logger.Warn("connection not available", "name", name, "reason", msg)
	b.issues = append(b.issues, SessionIssue{Connection: name, Message: msg})
}

// claimHubToken records the MotherDuck token a connection resolved. One
// workspace uses one account, so a second, different token is refused.
func (b *sessionBuild) claimHubToken(name, token string) error {
	switch {
	case b.mdToken == "":
		b.mdToken, b.mdOwner = token, name
	case token != b.mdToken:
		return fmt.Errorf("this workspace already uses the MotherDuck account of %q, and one workspace uses one account; sign this connection in with the same account or remove it", b.mdOwner)
	}
	return nil
}

func (b *sessionBuild) env() prepareEnv {
	return prepareEnv{
		Root:          b.ws.Root(),
		ForceReadOnly: b.o.readOnly,
		PolicyGated:   b.o.policyGated,
		SkipLocal:     b.o.skipLocal,
		Fail:          b.failMsg,
		ClaimHubToken: b.claimHubToken,
	}
}

// resolveAll runs pass 1 over conns; a connection that cannot be set up is
// recorded as an issue and left out.
func (b *sessionBuild) resolveAll(ctx context.Context, conns []Connection) []sessionItem {
	var items []sessionItem
	for _, c := range conns {
		it, err := b.resolveOne(ctx, c)
		if err != nil {
			b.failMsg(c.Name, secrets.RedactPatterns(err.Error()))
			continue
		}
		items = append(items, it)
	}
	return items
}

// resolveOne resolves one connection, then lets its type's preparer adjust the
// spec (validation, folders, hub token).
func (b *sessionBuild) resolveOne(ctx context.Context, c Connection) (sessionItem, error) {
	rc, err := ResolveConnection(b.ws, c, b.o.lookup)
	if err != nil {
		return sessionItem{}, err
	}
	prep := b.o.preparerFor(rc.Resolved.Type)
	readOnly, err := prep.ReadOnly(rc, b.o.readOnly)
	if err != nil {
		return sessionItem{}, err
	}
	def := rc.Def(readOnly)
	spec := def.Spec()
	spec.Requires = rc.Requires
	if connections.NeedsCredentialFor(def) {
		b.attachCredential(&spec, rc)
	}
	if err := prep.Prepare(ctx, b.env(), rc, def, &spec); err != nil {
		return sessionItem{}, err
	}
	return sessionItem{rc: rc, spec: spec, prep: prep}, nil
}

// attachCredential gives spec the lazy credential lookup for rc.
func (b *sessionBuild) attachCredential(spec *session.Spec, rc ResolvedConnection) {
	ref := credentialRef(b.ws.ID(), rc.Scope, rc.Name)
	binding, method := rc.Binding(), rc.SignIn
	resolver := b.resolver
	guard := signInGuard(resolver, rc.Connection, rc.Resolved)
	spec.Credential = func(ctx context.Context) (secrets.Credential, error) {
		if guard != nil {
			return secrets.Credential{}, guard
		}
		return resolver.Resolve(ctx, ref, method, binding)
	}
}

// openHub creates the session with the types, redactor and MotherDuck token.
func (b *sessionBuild) openHub() (session.Session, error) {
	sopts := []session.Option{
		session.WithLogger(b.logger),
		session.WithConnectionTypes(connections.Types()...),
		session.WithRedactor(b.resolver.Redactor()),
	}
	if b.mdToken != "" {
		sopts = append(sopts, session.WithMotherDuckToken(b.mdToken))
	}
	sess, err := session.New(append(sopts, b.o.session...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to open the workspace session: %w", err)
	}
	return sess, nil
}

// registerAll runs pass 2 and returns the names that were attached.
func (b *sessionBuild) registerAll(ctx context.Context, sess session.Session, items []sessionItem) map[string]bool {
	attached := map[string]bool{}
	fail := func(name string, err error) { b.failMsg(name, sess.Redact(err.Error())) }
	// Everything is registered before anything is preflighted or connected: a
	// Delta table or lake connects at once and needs the storage connection
	// that reads it, whatever order their names sort in.
	var registered []sessionItem
	for _, it := range items {
		if err := sess.Register(it.spec); err != nil {
			fail(it.rc.Name, err)
			continue
		}
		registered = append(registered, it)
	}
	for _, it := range registered {
		name := it.rc.Name
		if !it.prep.Eager() {
			sess.Preflight(ctx, name)
			continue
		}
		if b.o.skipLocal {
			continue
		}
		if err := sess.Connect(ctx, name); err != nil {
			fail(name, err)
			continue
		}
		attached[name] = true
	}
	return attached
}

// applyDefault makes def the session's default catalog once it is attached.
func (b *sessionBuild) applyDefault(ctx context.Context, sess session.Session, def string, attached map[string]bool) {
	if def == "" {
		return
	}
	if attached[def] {
		if err := sess.SetDefaultCatalog(ctx, def); err != nil {
			b.failMsg(def, sess.Redact(err.Error()))
		}
		return
	}
	if !hasIssue(b.issues, def) {
		b.failMsg(def, sess.Redact(fmt.Sprintf("default connection %q is not attached, so unqualified table names use the in-memory catalog", def)))
	}
}

func hasIssue(issues []SessionIssue, name string) bool {
	for _, i := range issues {
		if i.Connection == name {
			return true
		}
	}
	return false
}

// insideRoot reports whether the resolved path p lies within the workspace.
func insideRoot(root, p string) bool {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	return filestore.Within(real, filepath.Clean(p))
}

// ResolveLocalPath turns a duckdb connection's path into the file to attach.
// A relative path is resolved against root and must stay inside it, including
// through symlinks. An absolute path is accepted only from the scopes that
// belong to one person (local and user): the shared slaw.yaml is written by
// someone else, so an absolute path there is refused (review #109, D-C).
// scope is the scope the definition came from.
func ResolveLocalPath(root, p string, scope Scope) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("a duckdb connection needs a path")
	}
	if isAbsLike(p) {
		if scope == ScopeShared {
			return "", fmt.Errorf("path %q is absolute, which is not allowed in slaw.yaml; use a path inside the workspace", p)
		}
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("path %q is not a usable absolute path on this system", p)
		}
		return filepath.Clean(p), nil
	}
	if scope == ScopeUser {
		return "", fmt.Errorf("path %q is relative, but this connection belongs to no workspace: use an absolute path", p)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the workspace folder: %w", err)
	}
	abs := filepath.Join(realRoot, filepath.FromSlash(p))
	if !filestore.Within(realRoot, abs) {
		return "", fmt.Errorf("path %q leaves the workspace folder; use an absolute path if that is intended", p)
	}
	// Resolve the deepest existing ancestor (the file itself when present) so a
	// symlinked directory or file cannot point the attach outside the folder.
	existing := abs
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %q: %w", p, err)
	}
	if !filestore.Within(realRoot, resolved) {
		return "", fmt.Errorf("path %q resolves outside the workspace folder through a symbolic link; use an absolute path if that is intended", p)
	}
	tail, _ := filepath.Rel(existing, abs)
	if tail == "." {
		return resolved, nil
	}
	return filepath.Join(resolved, tail), nil
}
