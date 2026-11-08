package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/session"
)

// --- Data handlers ---

// RunReadOnlySQL executes sql as a live A2UI data-bound component's query
// (RunA2UIQuery, #67 step-2 review fix) — a distinct entry point from the
// JSON-RPC "run_query" dispatch (handleRunQuery), not a JSON-RPC method
// itself, because a data-bound "sql" field must be denied a write/DDL
// statement regardless of the file's sandbox mode (checkSQLClassReadOnly),
// where handleRunQuery's checkSQLClass deliberately lets read-write/full
// through — that permissiveness is correct for an agent's own one-shot
// run_query tool call, wrong for a query that re-executes on every render.
// It reuses classifySQL and h.engine.Execute exactly as handleRunQuery
// does — no second classifier, no second execution path.
//
// maxRows > 0 pushes the row cap down into the query itself (review fix,
// round 4): a component's SQL was already classified sqlClassRead above —
// a single statement, no side effects — so it is safe to run it as
// `SELECT * FROM (<sql>) AS _a2ui_limit LIMIT maxRows+1` instead of
// fetching a potentially huge/unbounded result (SELECT * FROM
// range(100000000)) into memory just to slice it down to maxRows
// afterward. truncated reports whether the cap actually cut anything.
// maxRows <= 0 skips the wrap and returns the full result, uncapped.
//
// Not every class-Read shape can be wrapped as a FROM-subquery this way —
// confirmed empirically: a plain SELECT/CTE, DESCRIBE, SHOW (TABLES), and
// SUMMARIZE all wrap fine, but PRAGMA table_info(...) and EXPLAIN are a
// DuckDB parser error as a subquery. Rather than hand-maintain a list of
// exactly which shapes are exempt (classifySQL already has enough of that
// bookkeeping), this just tries the wrapped form first and falls back to
// running sqlStr directly (then truncating client-side, exactly like
// before this fix) on any error from the wrapped attempt — safe to retry
// because the statement is already proven read-only, and these excluded
// shapes are metadata queries that never return a huge row count anyway,
// so the fallback's un-limited execution costs nothing in practice.
func (h *Handler) RunReadOnlySQL(ctx context.Context, sqlStr string, params map[string]any, maxRows int) (rs *engine.ResultSet, truncated bool, err error) {
	return h.RunReadOnlySQLIn(ctx, "", sqlStr, params, maxRows)
}

// ConnectionEngine is the optional engine capability behind a notebook cell's
// or A2UI binding's "connection" (notebook format v2): run a statement with
// that connection's catalog as the default for unqualified names.
type ConnectionEngine interface {
	ExecuteIn(ctx context.Context, connection, sql string, params map[string]any) (*engine.ResultSet, error)
}

// cappedEngine is the optional engine capability of keeping only the first
// maxRows rows of a run (engine.Manager has it).
type cappedEngine interface {
	ExecuteCappedIn(ctx context.Context, connection, sql string, params map[string]any, maxRows int) (*engine.ResultSet, int, error)
}

// RunReadOnlySQLIn is RunReadOnlySQL with connection (a catalog name; "" is
// the session default) as the default for unqualified table names. The
// statement is classified on a connection pinned to that catalog, so the
// classifier resolves names exactly as the run will, and runs the same way.
func (h *Handler) RunReadOnlySQLIn(ctx context.Context, connection, sqlStr string, params map[string]any, maxRows int) (rs *engine.ResultSet, truncated bool, err error) {
	userDB := h.access.DuckDBDB()
	if userDB == nil {
		return nil, false, fmt.Errorf("DuckDB not available")
	}
	exec, err := h.executorFor(connection)
	if err != nil {
		return nil, false, err
	}
	orig := sqlStr
	sqlStr, params, ctx, _, err = h.readOnlyVerdict(ctx, connection, orig, params)
	if err != nil {
		return nil, false, err
	}

	if maxRows > 0 {
		trimmed := strings.TrimSuffix(strings.TrimSpace(sqlStr), ";")
		wrapped := fmt.Sprintf("SELECT * FROM (%s\n) AS _a2ui_limit LIMIT %d", trimmed, maxRows+1)
		rs, err = exec(ctx, wrapped, params)
		if errors.Is(err, engine.ErrResultTooLarge) {
			// Not a shape the push-down cannot wrap: the unwrapped run would
			// read the same rows again.
			return nil, false, err
		}
		if err == nil {
			rs.Rows = dropDuckLakeInternalRows(rs.Rows)
			if len(rs.Rows) > maxRows {
				rs.Rows = rs.Rows[:maxRows]
				truncated = true
			}
			return rs, truncated, nil
		}
		h.logger.Debug("render-time sql binding: LIMIT push-down not supported for this statement shape, falling back to unwrapped execution", "query_sha256", engine.QueryDigest(orig), "query_bytes", len(orig), "wrap_error", engine.ErrorForLog(err))
	}

	// The unwrapped run keeps maxRows+1 rows (one probe row, so truncation is
	// seen) and neither scans nor counts the rest against a byte budget.
	if ce, ok := h.engine.(cappedEngine); ok && maxRows > 0 {
		rs, _, err = ce.ExecuteCappedIn(ctx, connection, sqlStr, params, maxRows+1)
	} else {
		rs, err = exec(ctx, sqlStr, params)
	}
	if errors.Is(err, engine.ErrResultTooLarge) {
		return nil, false, err
	}
	if err != nil {
		return nil, false, fmt.Errorf("query execution failed: %w", err)
	}
	rs.Rows = dropDuckLakeInternalRows(rs.Rows)
	if maxRows > 0 && len(rs.Rows) > maxRows {
		rs.Rows = rs.Rows[:maxRows]
		truncated = true
	}
	return rs, truncated, nil
}

// executorFor returns the function that runs a statement on the named
// connection's catalog ("" is the session default). It does no policy check:
// callers run readOnlyVerdict first.
func (h *Handler) executorFor(connection string) (func(ctx context.Context, q string, p map[string]any) (*engine.ResultSet, error), error) {
	if connection == "" {
		return func(ctx context.Context, q string, p map[string]any) (*engine.ResultSet, error) {
			return h.engine.Execute(ctx, q, p)
		}, nil
	}
	ce, ok := h.engine.(ConnectionEngine)
	if !ok {
		return nil, fmt.Errorf("connections are not available here")
	}
	return func(ctx context.Context, q string, p map[string]any) (*engine.ResultSet, error) {
		return ce.ExecuteIn(ctx, connection, q, p)
	}, nil
}

// denial is a refused statement's classifier reason and what is known about it
// (see defKind); the zero value is "no reason".
type denial struct {
	Reason string
	Kind   defKind
}

// readOnlyVerdict is the policy half of every render-time read: connect what
// the statement references, expand s3/gcs/r2 wildcards, classify the statement
// on a connection pinned to the named catalog, and apply the read-only gate. It
// returns the statement as it will run (wildcards expanded), the context to
// run it under, the params to run it with (the caller's plus the defaults of any
// metric() saved query's parameters), and the classifier's denial (empty for a read). err is the
// connection's own failure, ErrReadOnlyBindingDenied, or an expansion or
// classification failure. RunReadOnlySQLIn and the save-time artifact vet both
// call it, so a statement is vetted by exactly the code that later runs it.
func (h *Handler) readOnlyVerdict(ctx context.Context, connection, orig string, params map[string]any) (runSQL string, runParams map[string]any, runCtx context.Context, den denial, err error) {
	// metric() calls are expanded first, then wildcards on s3/gcs/r2; the
	// statement is classified and run as expanded, so the cell and the binding
	// keep the metric call and the wildcard.
	mx, params, err := h.expandMetrics(ctx, connection, orig, params)
	if err != nil {
		return "", params, ctx, denial{}, fmt.Errorf("query execution failed: %w", &DefinitionError{Err: err})
	}
	connectErr := h.connectReferenced(ctx, connection, mx.SQL)
	runSQL, runCtx, err = h.expand(ctx, mx.SQL)
	if err != nil {
		return "", params, ctx, denial{}, fmt.Errorf("query execution failed: %w", err)
	}
	v, err := h.classifyIn(runCtx, connection, runSQL, params)
	if err != nil {
		// A connection that failed to connect cannot be pinned: say why it
		// failed, not that it "is not attached" (#302).
		return "", params, ctx, denial{}, withConnectError(err, connectErr)
	}
	if err := h.sandboxFor(ctx).checkSQLClassReadOnly(v); err != nil {
		if connectErr != nil {
			return "", params, ctx, denial{v.Reason, v.Kind}, withConnectError(err, connectErr)
		}
		// The user/agent-facing error is one fixed, mode-agnostic message
		// (see ErrReadOnlyBindingDenied) — it never surfaces the policy's
		// reason, which can name a tier requirement that is false for a
		// full-tier caller. The reason is still useful for debugging a denial
		// that looks surprising, so it's logged here at debug level only.
		if v.Kind&defStatement != 0 {
			// The reason is shown to the caller and can quote the statement
			// (a binder error's LINE context): never log it (#276).
			h.logger.Debug("render-time sql binding denied", "class", v.Class, "query_sha256", engine.QueryDigest(orig), "query_bytes", len(orig))
		} else {
			h.logger.Debug("render-time sql binding denied", "class", v.Class, "reason", v.Reason, "query_sha256", engine.QueryDigest(orig), "query_bytes", len(orig))
		}
		return "", params, ctx, denial{v.Reason, v.Kind}, err
	}
	return runSQL, params, runCtx, denial{v.Reason, v.Kind}, nil
}

// denialWithReason is a read-only denial followed by the classifier's reason,
// unless the denial already is that reason (a definitionRefusal) or there is none.
func denialWithReason(err error, reason string) string {
	var dr *definitionRefusal
	if reason == "" || errors.As(err, &dr) {
		return err.Error()
	}
	return err.Error() + ": " + reason
}

// sqlParam reads run_query's sql argument. A value that is not a string (sql:
// 123, an array) is named as such instead of being reported as an empty
// statement.
func sqlParam(req *Request) (string, *Response) {
	raw, present := req.Params["sql"]
	if !present || raw == nil {
		return "", nil // an absent sql is the policy's "statement is empty"
	}
	s, ok := raw.(string)
	if !ok {
		return "", NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("run_query: sql must be a string holding one SQL statement (got %s)", jsonKind(raw)))
	}
	return s, nil
}

// jsonKind names a decoded JSON value's type for an error message.
func jsonKind(v any) string {
	switch v.(type) {
	case bool:
		return "a boolean"
	case float64, float32, int, int64:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

// discardedReadsMsg is the refusal for a script with reads before its last
// statement.
func discardedReadsMsg(reads, total int) string {
	return fmt.Sprintf("run_query returns the result of the last statement only, but this script has %d statements and the %d read(s) before the last would be dropped without a word. Send each SELECT as its own call (a script is for writes)", total, reads)
}

func (h *Handler) handleRunQuery(ctx context.Context, req *Request) *Response {
	conn, err := h.queryConnection(req)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, "run_query: "+err.Error())
	}
	return h.runQuery(ctx, req, runTarget{connection: conn})
}

// emptyResultNote is explainEmptyResult on the connection the statement ran
// on, so its probes resolve unqualified names the way the statement did.
func (h *Handler) emptyResultNote(ctx context.Context, connection string, userDB *sql.DB, sqlStr string, rows [][]any) string {
	if connection == "" {
		return explainEmptyResult(ctx, userDB, sqlStr, rows)
	}
	sess := h.access.Session()
	if sess == nil {
		return ""
	}
	pinned, err := sess.Conn(ctx, session.WithCatalog(connection))
	if err != nil {
		return ""
	}
	defer func() { _ = pinned.Close() }()
	return explainEmptyResult(ctx, pinned.SQLConn(), sqlStr, rows)
}

// queryConnection reads run_query's optional connection argument: the
// connection whose catalog unqualified names resolve against, as a notebook
// cell's connection does. It must be a defined connection with a catalog;
// "" and the workspace's default catalog both mean the default (no pinning).
// Every gate runQuery applies still applies to the pinned run.
func (h *Handler) queryConnection(req *Request) (string, error) {
	raw, present := req.Params["connection"]
	if !present || raw == nil {
		return "", nil
	}
	name, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("connection must be a connection name (got %s; see list_connections)", jsonKind(raw))
	}
	conn, err := h.validConnection(name)
	if err != nil || conn == "" {
		return "", err
	}
	if strings.EqualFold(conn, h.access.Session().DefaultCatalog()) {
		return "", nil
	}
	return conn, nil
}

// runTarget narrows one run of the run_query pipeline. The zero value is
// run_query itself: the workspace default connection, any statement the
// sandbox and approval gates allow. run_saved_query sets a connection (the
// saved query's own) and readsOnly, so a stored statement that has become a
// write is refused rather than run or put in front of the person.
type runTarget struct {
	connection string
	readsOnly  bool
}

func (h *Handler) runQuery(ctx context.Context, req *Request, tgt runTarget) *Response {
	sqlStr, errResp := sqlParam(req)
	if errResp != nil {
		return errResp
	}
	var params map[string]any
	if p, ok := req.Params["params"].(map[string]any); ok {
		params = p
	}

	// Per-statement sandbox gate (review fix for #63): run_query's "data"
	// category alone let read-write execute CREATE/DROP/COPY/ATTACH — the
	// same operations the dedicated schema tools deny outside full sandbox.
	// The statement count and the read decision both come from DuckDB
	// itself, never hand-written lexing (round 3 review fix: statement
	// counting via duckdb_extract_statements, table references resolved
	// against the real catalog via duckdb_tables()/duckdb_views() on
	// userDB — not extension-guessing or ';' counting, both of which were
	// bypassable). classifySQL binds params itself (round 5 review fix) so
	// EXPLAIN can actually bind a parameterized query instead of choking on
	// a bare $name placeholder.
	userDB, errResp := h.duckDB(req.ID)
	if errResp != nil {
		return errResp
	}
	sb := h.sandboxFor(ctx)
	// metric() calls, then wildcards on s3/gcs/r2, are expanded before the
	// policy looks at the statement; what is vetted, approved-for and run is
	// the expanded text.
	mx, params, err := h.expandMetrics(ctx, tgt.connection, sqlStr, params)
	if err != nil {
		return NewErrorResponse(req.ID, expandErrorCode(err), "run_query: "+err.Error())
	}
	connectErr := h.connectReferenced(ctx, tgt.connection, mx.SQL)
	runSQL, runCtx, err := h.expand(ctx, mx.SQL)
	if err != nil {
		return NewErrorResponse(req.ID, expandErrorCode(err), "run_query: "+err.Error())
	}
	v, err := h.classifyIn(runCtx, tgt.connection, runSQL, params)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}
	class := v.Class
	if tgt.readsOnly && (class == sqlClassWrite || class == sqlClassDDL) {
		return NewErrorResponse(req.ID, ErrPermission, metricPrefix(mx)+notAReadMsg(v.gateReason()))
	}
	if err := sb.checkSQLClass(v.Class, v.gateReason()); err != nil {
		return NewErrorResponse(req.ID, ErrPermission, metricPrefix(mx)+withConnectError(err, connectErr).Error())
	}
	if tgt.readsOnly && class != sqlClassRead {
		return NewErrorResponse(req.ID, ErrPermission, metricPrefix(mx)+notAReadMsg(v.gateReason()))
	}
	// The second write gate: every connection the statement changes must be
	// writable.
	nonLocal, err := writeGates(v.Targets)
	if err != nil {
		return NewErrorResponse(req.ID, ErrPermission, "run_query: "+err.Error())
	}
	// run_query returns the last statement's result only, so a read that
	// comes before it would be dropped without a word. Writes may be scripted
	// (each is checked and gated above); a read must be its own call.
	if v.discardedReads > 0 {
		return NewErrorResponse(req.ID, ErrInvalidParams, discardedReadsMsg(v.discardedReads, v.Statements))
	}
	// Approval gate (#79): run_query asks only when the statement actually
	// changes something (DML) or reaches outside a plain read (DDL/ATTACH/
	// COPY/INSTALL/multi-statement) — never for a SELECT. Runs after
	// checkSQLClass, so a statement the sandbox denies outright never shows
	// a card.
	// The third write gate: the person's OK. It is forced on for anything that
	// is not a local file, whatever the "Ask before changes" setting is.
	if class != sqlClassRead && (sb.RequireApproval || len(nonLocal) > 0) {
		if r := h.awaitApproval(ctx, req, sb, sqlStr, runSQL, tgt.connection, params, class, nonLocal); r != nil {
			return r
		}
	}

	exec, err := h.executorFor(tgt.connection)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}
	rs, err := exec(runCtx, runSQL, params)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, fmt.Sprintf("query execution failed: %v", err))
	}

	// Enforce max rows. DuckLake's own metadata catalog never shows in what the
	// agent reads, not even as a name in duckdb_tables() or SHOW ALL TABLES.
	rows := dropDuckLakeInternalRows(rs.Rows)
	if sb.MaxQueryRows > 0 && len(rows) > sb.MaxQueryRows {
		rows = rows[:sb.MaxQueryRows]
	}

	cols, types := rs.NamesAndTypes()

	result := map[string]any{
		"columns": cols,
		// types is the DuckDB type name of each column, parallel to
		// columns — an agent or MCP client reading a run_query result needs
		// it to know "9007199254740993" is an exact BIGINT and
		// "12345678901234567.89" an exact DECIMAL, not a value that lost
		// precision in transit.
		"types": types,
		"rows":  rows,
	}
	// metrics_used is the receipts seam: which metrics the statement expanded.
	if len(mx.Uses) > 0 {
		result["metrics_used"] = mx.UsesJSON()
	}

	// Empty-result explanation (#70): must run BEFORE the row budget below
	// (an empty result is never truncated anyway) and only ever issues
	// read-only, internally-constructed probes against real,
	// information_schema-verified columns — see explainEmptyResult's doc
	// comment. userDB is the same connection already gated by
	// checkSQLClass above; the probes themselves never touch classifySQL
	// because they are fixed SELECT DISTINCT templates this handler builds,
	// never the caller's SQL text executed a second time.
	if len(rows) <= 1 {
		if note := h.emptyResultNote(ctx, tgt.connection, userDB, sqlStr, rows); note != "" {
			result["note"] = note
		}
	}

	// Tool-result budget (#70): cap the row JSON at ~RowResultBudgetChars so
	// a large SELECT never floods the model's (or MCP client's) context.
	// Applied here at the handler layer so chat, task mode, and MCP all get
	// it uniformly — see budget.go.
	if budgeted, truncated := budgetRows(rows); truncated != nil {
		result["rows"] = budgeted
		result["truncated"] = truncated
	}

	return NewResponse(req.ID, result)
}

// requireWriteStatement classifies a fully-built SQL statement (identifiers
// already quoted via session.QuoteIdent, values addressed only via $name
// placeholders bound through params — never a raw attacker value
// concatenated in) through the SAME real-DuckDB classifier run_query uses
// (classifySQL), and requires it come back sqlClassWrite. This is what
// stops a `where` param like `1=0; DROP TABLE users` or `1=0; COPY (...) TO
// '<path>'`: DuckDB's own statement counter (via countStatements) sees two
// statements and classifySQL fails the whole thing to sqlClassOther, denied
// here — no hand-rolled semicolon/comment scanning involved. A
// single-statement subquery reaching outside the database (e.g. a `where`
// containing `IN (SELECT ... FROM read_csv(...))`) is caught by
// classifyFallback's own checkPlanExternalAccess (DuckDB's bound physical
// plan), not by this function — it just enforces the class this record tool
// needs.
func (h *Handler) requireWriteStatement(ctx context.Context, userDB *sql.DB, query string, params map[string]any) error {
	v := h.classify(ctx, userDB, query, params)
	if v.Class != sqlClassWrite {
		if v.Reason != "" {
			return fmt.Errorf("%s", v.Reason)
		}
		return fmt.Errorf("this operation is not a single, plain write statement — denied")
	}
	if _, err := writeGates(v.Targets); err != nil {
		return err
	}
	return nil
}

// namedPlaceholders builds "$p0, $p1, ..." style placeholders (the same
// $name addressing engine.BindParams/classifySQL understand — see
// requireWriteStatement) for n values, returning the placeholder list and a
// params map assigning vals[i] to $p<i>. Both the classification call and
// the real execution use the identical (query, params) pair via
// engine.BindParams, so what gets classified is byte-for-byte what runs.
func namedPlaceholders(prefix string, vals []any) (placeholders []string, params map[string]any) {
	placeholders = make([]string, len(vals))
	params = make(map[string]any, len(vals))
	for i, v := range vals {
		name := fmt.Sprintf("%s%d", prefix, i)
		placeholders[i] = "$" + name
		params[name] = v
	}
	return placeholders, params
}

// notAReadMsg is the refusal when a saved query, which runs reads only in every
// tier, is no longer a plain read.
func notAReadMsg(reason string) string {
	msg := "a saved query runs reads only, and this one is no longer a plain read (it changes data or schema, or runs several statements)"
	if reason != "" {
		msg += ": " + reason
	}
	return msg
}
