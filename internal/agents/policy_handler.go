package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/storageglob"
)

// policyEnv describes the workspace to the SQL policy: its data/ folder (when
// the container is a workspace) and the registered connections.
func (h *Handler) policyEnv() *sqlEnv {
	if h.access == nil {
		return newSQLEnv("", nil)
	}
	var conns []session.ConnectionInfo
	if s := h.access.Session(); s != nil {
		conns = s.ConnectionInfos()
	}
	dir := ""
	if p, ok := h.access.(DataDirProvider); ok {
		dir = p.DataDir()
	}
	var protected []string
	if p, ok := h.access.(ProtectedPathsProvider); ok {
		protected = p.ProtectedPaths()
	}
	return newSQLEnv(dir, conns, protected...)
}

// expand rewrites the s3, gcs and r2 wildcard patterns in raw into explicit
// lists of the objects they match (internal/storageglob), before the policy
// sees the statement. Each pattern is checked against the ready storage scopes
// first (an uncovered one is refused, never listed), the expanded text is then
// vetted like any other (every key against its scope, then EXPLAIN), and the
// statement runs as expanded: the returned context tells the engine not to
// expand it again and to keep its errors short. Text with no pattern comes
// back unchanged.
func (h *Handler) expand(ctx context.Context, raw string) (string, context.Context, error) {
	var lister storageglob.Lister
	if h.access != nil {
		if s := h.access.Session(); s != nil {
			lister, _ = s.(storageglob.Lister)
		}
	}
	text, rw, err := storageglob.Expand(ctx, raw, storageglob.Options{
		Lister:       lister,
		Vet:          h.policyEnv().pathPolicy().vetPattern,
		RequirePlain: true,
	})
	if err != nil {
		return "", ctx, err
	}
	return text, storageglob.WithRewrite(ctx, rw), nil
}

// expandMetrics is the first step of every expansion: metric('name', by => ...)
// calls in raw become the parenthesised SELECT over the metric's saved query
// (internal/semantics, on the policy's own lexer), before the wildcard step,
// the connect step and every gate see the text. Every path that classifies and
// runs SQL (run_query, run_saved_query, preview_query, write_cell and the cell
// and dashboard vets, notebook cells and A2UI bindings, artifact queries)
// calls it, so the policy only ever judges the expanded statement and a
// metric whose saved query was edited into something hostile is refused like
// any other text. params gains the declared defaults of the saved queries'
// $name parameters; a value the caller sent wins. Text with no metric call
// comes back unchanged and params is returned as given.
func (h *Handler) expandMetrics(ctx context.Context, connection, raw string, params map[string]any) (*semantics.Result, map[string]any, error) {
	if !semantics.HasMetricCall(raw) {
		return &semantics.Result{SQL: raw}, params, nil
	}
	var src semantics.Reader
	o := semantics.Options{Connection: connection}
	if h.access != nil {
		if sess := h.access.Session(); sess != nil {
			o.DefaultConnection = sess.DefaultCatalog()
		}
		src = semantics.NewReader(h.access.Semantics(), h.access.Queries())
	}
	res, err := semantics.Expand(ctx, raw, src, o)
	if err == nil && res.Changed {
		err = res.CheckOuterParams(raw, params, nil)
	}
	if err != nil {
		return nil, params, scrubbedError(err)
	}
	if !res.Changed {
		return res, params, nil
	}
	if params, err = res.BindParams(params, time.Now()); err != nil {
		return nil, params, scrubbedError(err)
	}
	return res, params, nil
}

// scrubbedError is an expansion error as an agent may read it: it came from the
// stores (a saved query's error text can hold an absolute path), so the text is
// scrubbed, and the chain (errors.Is) is kept.
type scrubbedErr struct{ err error }

func (e *scrubbedErr) Error() string { return scrubStoreText(e.err.Error()) }
func (e *scrubbedErr) Unwrap() error { return e.err }

func scrubbedError(err error) error { return &scrubbedErr{err: err} }

// VetReadOnlySQLIn runs the read-only verdict a render-time read gets (every
// gate of RunReadOnlySQLIn: connect, expand, classify, read-only) without
// executing the statement. A person's own surfaces use it on the SQL a metric
// hides, so a cloned repository's metric is held to the same policy as a
// cloned artifact query.
func (h *Handler) VetReadOnlySQLIn(ctx context.Context, connection, sqlStr string, params map[string]any) error {
	_, _, _, reason, err := h.readOnlyVerdict(ctx, connection, sqlStr, params)
	if errors.Is(err, ErrReadOnlyBindingDenied) && reason != "" {
		if definitionReason(reason) {
			if strings.HasPrefix(reason, "could not validate query:") {
				return &BindFailure{Err: errors.New(reason)}
			}
			return &DefinitionError{Err: errors.New(reason)}
		}
		return errors.New(reason)
	}
	return err
}

// metricPrefix names the metrics a refused statement used, so a denial the
// policy words about the expanded text can be traced to the metric behind it.
func metricPrefix(res *semantics.Result) string {
	if res == nil || len(res.Uses) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var names []string
	for _, u := range res.Uses {
		if !seen[u.Name] {
			seen[u.Name] = true
			names = append(names, "'"+u.Name+"'")
		}
	}
	return "metric " + strings.Join(names, ", ") + ": "
}

// expandErrorCode maps an expansion failure to the response code: a pattern the
// policy refuses is a permission problem, anything else (nothing matched, too
// many matches, a listing failure) is not.
func expandErrorCode(err error) int {
	var refused *patternRefusal
	if errors.As(err, &refused) {
		return ErrPermission
	}
	return ErrInvalidParams
}

// classify runs the agent SQL policy on raw (with its $name params) against db.
func (h *Handler) classify(ctx context.Context, db queryDB, raw string, params map[string]any) *sqlVerdict {
	v := analyzeSQL(ctx, db, h.policyEnv(), raw, params)
	// A denial may quote the expanded statement (EXPLAIN's context line).
	if rw, ok := storageglob.FromContext(ctx); ok && v.Reason != "" {
		v.Reason = rw.Redact(v.Reason)
	}
	return v
}

// classifyIn is classify on a connection pinned to the named catalog ("" is the
// session default), so unqualified names resolve exactly as they will when the
// statement runs. The pinned connection is released before it returns.
func (h *Handler) classifyIn(ctx context.Context, connection, raw string, params map[string]any) (*sqlVerdict, error) {
	userDB := h.access.DuckDBDB()
	if userDB == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}
	if connection == "" {
		return h.classify(ctx, userDB, raw, params), nil
	}
	sess := h.access.Session()
	if sess == nil {
		return nil, fmt.Errorf("connections are not available here")
	}
	pinned, err := sess.Conn(ctx, session.WithCatalog(connection))
	if err != nil {
		return nil, fmt.Errorf("unknown connection %q: %w", connection, err)
	}
	defer func() { _ = pinned.Close() }()
	return h.classify(ctx, pinned.SQLConn(), raw, params), nil
}

// writeGates is the second write gate: every connection a statement changes
// must be writable. It returns the names of the targets that are not local
// files, for which the person's approval is forced (the third gate).
func writeGates(targets []sqlTarget) (nonLocal []string, err error) {
	seen := map[string]bool{}
	for _, t := range targets {
		if t.System {
			return nil, fmt.Errorf("DuckDB's system catalog cannot be changed")
		}
		if t.ReadOnly {
			return nil, fmt.Errorf("the connection %q is read-only, so the agent cannot change it (an owner can set read_only: false on the connection; a read-only database role is the real guard)", t.Catalog)
		}
		if !t.Local && !seen[t.Catalog] {
			seen[t.Catalog] = true
			nonLocal = append(nonLocal, t.Catalog)
		}
	}
	return nonLocal, nil
}

// toolTarget names the connection a record or schema tool writes, from its
// table or name parameter (qualified catalog.schema.table, or the default
// connection). ok is false for tools that write no table, and when there is no
// session to ask (the handler then refuses the unresolvable name itself).
func (h *Handler) toolTarget(ctx context.Context, req *Request) (sqlTarget, bool) {
	param := ""
	switch req.Method {
	case "create_record", "update_record", "delete_record", "bulk_insert", "add_column", "import_csv":
		param = "table"
	case "create_table", "drop_table":
		param = "name"
	default:
		return sqlTarget{}, false
	}
	name, _ := req.Params[param].(string)
	sess := h.access.Session()
	if sess == nil || strings.TrimSpace(name) == "" {
		return sqlTarget{}, false
	}
	ref, err := session.ParseTableRef(name)
	if err != nil {
		return sqlTarget{}, false
	}
	r, err := sess.Resolve(ctx, ref)
	if err != nil {
		return sqlTarget{}, false
	}
	return h.catalogTarget(ctx, sess, r.Catalog), true
}

// catalogTarget builds the write target for an attached catalog.
func (h *Handler) catalogTarget(ctx context.Context, sess session.Session, catalog string) sqlTarget {
	t := sqlTarget{Catalog: strings.ToLower(catalog)}
	// Fail closed: a catalog we cannot describe is not writable and not local.
	t.ReadOnly = true
	cats, err := sess.ListCatalogs(ctx)
	if err != nil {
		return t
	}
	for _, c := range cats {
		if strings.EqualFold(c.Name, catalog) {
			t.ReadOnly = c.ReadOnly
			t.Local = c.Type == "duckdb" || c.Type == "sqlite"
			break
		}
	}
	for _, ci := range sess.ConnectionInfos() {
		if strings.EqualFold(ci.Name, catalog) {
			t.ReadOnly = t.ReadOnly || ci.ReadOnly
			if ci.Remote {
				t.Local = false
			}
		}
	}
	return t
}

// --- list_connections ---------------------------------------------------------

func (h *Handler) handleListConnections(_ context.Context, req *Request) *Response {
	sess, errResp := h.sess(req.ID)
	if errResp != nil {
		return errResp
	}
	infos := sess.ConnectionInfos()
	out := make([]map[string]any, 0, len(infos)+1)
	for _, c := range infos {
		if builtinCatalogName(c.Name) && !strings.EqualFold(c.Name, sess.DefaultCatalog()) {
			continue // DuckDB's own scratch catalog, not a connection of the workspace
		}
		loc := "local"
		if c.Remote {
			loc = "remote"
		}
		m := map[string]any{
			"name":      c.Name,
			"type":      c.Type,
			"status":    string(c.Kind),
			"read_only": c.ReadOnly,
			"location":  loc,
		}
		if c.SignIn != nil {
			m["sign_in"] = c.SignIn
		}
		if c.Default || strings.EqualFold(c.Name, sess.DefaultCatalog()) {
			m["default"] = true
		}
		if c.Catalogless {
			m["catalog"] = false
			m["scope"] = c.Scope
		} else {
			m["catalog"] = true
		}
		out = append(out, m)
	}
	// A legacy file has no registered connections: describe its one catalog.
	if len(infos) == 0 {
		out = append(out, map[string]any{
			"name": sess.DefaultCatalog(), "type": "duckdb", "status": string(session.StatusOK),
			"read_only": false, "location": "local", "default": true, "catalog": true,
		})
	}
	res := map[string]any{"connections": out, "default": sess.DefaultCatalog()}
	if p, ok := h.access.(DataDirProvider); ok && p.DataDir() != "" {
		res["data_dir"] = p.DataDir()
	}
	return NewResponse(req.ID, res)
}

// validConnection checks that name is a registered connection with a catalog
// (what a notebook cell's "connection" may be) and returns its canonical name.
func (h *Handler) validConnection(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	sess := h.access.Session()
	if sess == nil {
		return "", fmt.Errorf("connections are not available here")
	}
	for _, c := range sess.ConnectionInfos() {
		if strings.EqualFold(c.Name, name) {
			if c.Catalogless {
				return "", fmt.Errorf("%q is a storage connection with no catalog; read its files with read_parquet('<scope>...') in a cell on a catalog connection", c.Name)
			}
			return c.Name, nil
		}
	}
	if strings.EqualFold(name, sess.DefaultCatalog()) {
		return sess.DefaultCatalog(), nil
	}
	return "", fmt.Errorf("there is no connection named %q (see list_connections)", name)
}
