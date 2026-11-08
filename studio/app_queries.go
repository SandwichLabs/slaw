package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/session"
)

// --- Queries ---

// QueryResult holds the results of an executed query.
type QueryResult struct {
	Columns  []ColumnInfo `json:"columns"`
	Rows     [][]any      `json:"rows"`
	Duration string       `json:"duration"`
	RowCount int          `json:"row_count"`
	// TotalRows is the query's true row count; RowCount is how many are
	// actually in Rows (capped at queryGridRowCap). #91: a query like
	// `SELECT * FROM range(100000)` used to materialize every row into the
	// webview and could crash it.
	TotalRows int `json:"total_rows"`
	// Truncated is set by a run that stopped at the cap without counting the
	// rest (a metric's Try it): TotalRows is then only RowCount, not the true
	// count. RunQuery counts, so it leaves this false.
	Truncated bool `json:"truncated,omitempty"`
}

// queryGridRowCap bounds how many rows the Query editor's grid will
// materialize in one ExecuteQuery call — independent of a2uiQueryRowCap
// (a2ui components render a compact inline view; the query grid is a full
// result set the user pages through). #84 adds a cancel button/context on
// top of this.
const queryGridRowCap = 5000

// jsonRoundTripGuard is a last-resort, defence-in-depth check (review fix,
// #67 step 2 round 3): Wails marshals a binding's return value to JSON
// itself on the way back to the frontend, and an unmarshalable value there
// does not surface as an ordinary error to the JS caller — it took the
// ENTIRE Studio process down, confirmed live (round 1 review: a raw NaN
// float reaching a binding logged "FAT | json: unsupported value: NaN" and
// exited the backend). engine.NormalizeValue now recursively sanitizes
// every known composite shape DuckDB can return, so this should never
// trigger in practice — but it is cheap (one json.Marshal, only ever
// discarding the bytes) and every row-returning binding calls it on its
// result immediately before returning, so a value NormalizeValue somehow
// missed (a future composite type, a code path that doesn't call it) still
// becomes an ordinary Go error surfaced to the one caller, never a crash of
// every open file/every other in-flight call.
func jsonRoundTripGuard(v any) error {
	if _, err := json.Marshal(v); err != nil {
		return fmt.Errorf("result contains a value that could not be serialized (%w) — this is almost certainly a bug in Slaw, not your query; please report it", err)
	}
	return nil
}

// humanDuration renders a query's elapsed time the way the run bar shows
// it: sub-millisecond as "<1 ms" (duration.String() would print something
// like "312.4µs", a unit the grid never uses elsewhere), whole milliseconds
// below a second as "%d ms", and a second or more as "%.2f s".
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1 ms"
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
}

// RunQueryRequest is the query editor's run (#117, SPEC 08). Connection is the
// "Run on" choice of this editor tab: the catalog unqualified names resolve
// against, pinned to one pooled connection for this run (USE is per
// connection). Empty is the workspace default. Qualified names
// (prod_pg.public.orders) work whatever it is, and may span connections.
type RunQueryRequest struct {
	SQL        string `json:"sql"`
	Connection string `json:"connection"`
	// TabID names the editor tab. It keys the run for CancelRun ("query:<tabId>"),
	// and a second run with the same TabID replaces the first; the connection
	// choice is never session-global.
	TabID string `json:"tabId"`
	// QueryID names the saved query the editor has open, and Params the values
	// of its declared $name parameters (typed JSON, as the inputs hold them). A
	// name left out takes its declared default. The SQL stays the buffer's
	// text: values are bound, never written into it.
	QueryID string         `json:"queryId,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
}

// validateRunRequest checks the connection name.
func validateRunRequest(req RunQueryRequest) error {
	if req.Connection != "" {
		if err := session.ValidateName(req.Connection); err != nil {
			return fmt.Errorf("unknown connection %q", req.Connection)
		}
	}
	return nil
}

// prepareRun is the editor's statement as it will run: the saved query's
// parameter values bound, then hub.PrepareForPerson (the editor's guard on the
// typed text, metric() calls expanded with each saved query behind one held to a
// plain SELECT, the guard again on the expanded text). The caller holds a.mu.
func (a *App) prepareRun(ctx context.Context, req RunQueryRequest) (string, map[string]any, error) {
	if err := a.requireWorkspace(); err != nil {
		return "", nil, err
	}
	params, declared, err := a.bindRunParams(req)
	if err != nil {
		return "", nil, err
	}
	exp, err := hub.PrepareForPerson(ctx, a.ws, a.sess, a.logger, hub.PersonRun{
		SQL: req.SQL, Connection: req.Connection, Params: params, Declared: declared, Guard: hub.InteractiveGuard(a.sess),
	})
	if err != nil {
		return "", nil, err
	}
	return exp.SQL, exp.Params, nil
}

// RunQuery runs the query editor's statement on the chosen connection. The run
// is registered under the tab's key, so CancelRun("query:<tabId>") stops it.
func (a *App) RunQuery(req RunQueryRequest) (*QueryResult, error) {
	if err := validateRunRequest(req); err != nil {
		return nil, err
	}
	ctx, end := a.runs.begin(a.ctxOrBackground(), queryRunKey(req.TabID))
	defer end()
	a.mu.RLock()
	defer a.mu.RUnlock()
	res, err := a.runQuery(ctx, req)
	return res, cancelledOr(ctx, err)
}

// runQuery is RunQuery under ctx; the caller holds a.mu.
func (a *App) runQuery(ctx context.Context, req RunQueryRequest) (*QueryResult, error) {
	sqlStr, params, err := a.prepareRun(ctx, req)
	if err != nil {
		return nil, err
	}
	before := session.StatusSnapshot(a.sess)
	start := time.Now()
	rs, total, err := a.engine.ExecuteCappedIn(ctx, req.Connection, sqlStr, params, queryGridRowCap)
	a.afterRun(a.sess, before, sqlStr)
	duration := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	result := &QueryResult{
		Duration:  humanDuration(duration),
		RowCount:  rs.RowCount(),
		TotalRows: total,
		Rows:      rs.Rows,
	}

	result.Columns = columnInfos(rs)

	if err := jsonRoundTripGuard(result); err != nil {
		return nil, err
	}
	return result, nil
}

// columnInfos names and types a result set's columns for the grid.
func columnInfos(rs *engine.ResultSet) []ColumnInfo {
	var out []ColumnInfo
	for _, col := range rs.Columns {
		out = append(out, ColumnInfo{Name: col.Name, Type: col.Type})
	}
	return out
}

// bindRunParams binds the editor's parameter values against the declarations of
// the saved query it has open (queries.Bind, the binder run_saved_query uses).
// The buffer may have been edited, so only what it names is bound, and a $name
// the saved query does not declare is refused. Without a saved query there is
// nothing declared: values are refused, and the statement runs as written. The
// caller holds a.mu.
func (a *App) bindRunParams(req RunQueryRequest) (map[string]any, []queries.Param, error) {
	if req.QueryID == "" {
		if len(req.Params) > 0 {
			return nil, nil, errors.New("parameter values need a saved query that declares them")
		}
		return nil, nil, nil
	}
	if err := a.requireWorkspace(); err != nil {
		return nil, nil, err
	}
	saved, err := a.ws.Queries().Get(a.ctxOrBackground(), req.QueryID)
	if err != nil {
		return nil, nil, err
	}
	run := *saved
	run.SQL = req.SQL
	params, err := queries.Bind(&run, req.Params, queries.BindOptions{Now: time.Now()})
	return params, saved.Parameters, err
}

// a2uiQueryRowCap bounds how many rows a single A2UI data-bound component
// (stat/table/barChart/lineChart) will render — independent of, and tighter
// than, the agent's run_query MaxQueryRows, since a component's whole point
// is a compact chart/stat/table inline in a chat reply or notebook cell, not
// a full result grid.
const a2uiQueryRowCap = 500

// a2uiQueryTimeout bounds how long one RunA2UIQuery call may run before its
// context is cancelled, interrupting the underlying DuckDB query (review
// fix, step 4a #1 — a live binding must never be able to hang the file's
// one DuckDB connection for other bindings/queries behind it).
const a2uiQueryTimeout = 30 * time.Second

// A2UIQueryResult is RunA2UIQuery's result shape: just columns + rows, the
// minimum the Vue renderer's data-bound components need (see toPoints/
// buildTable in the ported renderer).
type A2UIQueryResult struct {
	Columns []string `json:"columns"`
	// Types is the DuckDB type name of each column, parallel to Columns
	// — A2UITable/A2UIStat need it to format a cell (DECIMAL scale,
	// alignment) instead of guessing from typeof value.
	Types []string `json:"types"`
	Rows  [][]any  `json:"rows"`
	// Truncated is true when the query returned more than a2uiQueryRowCap
	// rows and was cut off — the cap is now pushed into the query itself
	// (RunReadOnlySQL's LIMIT push-down) rather than fetched-then-sliced,
	// so this reports whether that LIMIT actually cut anything.
	Truncated bool `json:"truncated"`
}

// RunA2UIQuery executes one A2UI component's "sql" binding. It is called by
// the Vue renderer (studio/frontend/src/a2ui/) once per data-bound
// component when an A2UI page renders, live, on every mount.
//
// This SQL was authored by the model, not typed by the user in the query
// editor — unlike ExecuteQuery (the query editor's binding, deliberately
// unrestricted for the human at the keyboard), RunA2UIQuery must go through
// the sandbox classifier, not a bypass. It does NOT reuse run_query's own
// JSON-RPC dispatch (review fix, #67 step 2): run_query's checkSQLClass lets
// read-write/full execute a write, which is correct for an agent's one-shot
// tool call but wrong here — a "sql" binding re-runs every time the page
// renders, so a write there would fire on every render, not once. Instead it
// calls agents.Handler.RunReadOnlySQL, which reuses the exact same
// classifySQL (no second, drifted copy of the classifier) but gates the
// result with checkSQLClassReadOnly: sqlClassRead only, regardless of
// sandbox mode.
func (a *App) RunA2UIQuery(sqlStr string) (*A2UIQueryResult, error) {
	return a.RunA2UIQueryIn(sqlStr, "")
}

// RunA2UIQueryIn is RunA2UIQuery for a binding (or notebook sql cell) that
// names a connection (notebook format v2): the statement runs with that
// connection's catalog as the default for unqualified table names, on a
// pinned connection. An empty connection is the session default, exactly
// RunA2UIQuery. Every gate (read-only classification, row cap, timeout)
// applies identically.
func (a *App) RunA2UIQueryIn(sqlStr, connection string) (*A2UIQueryResult, error) {
	if connection != "" {
		if err := session.ValidateName(connection); err != nil {
			return nil, fmt.Errorf("unknown connection %q", connection)
		}
	}
	handler, runSess, err := a.readOnlyRunner()
	if err != nil {
		return nil, err
	}

	// Row cap enforced server side (review fix) and pushed down into the
	// query itself via LIMIT (review fix, round 4) rather than fetched in
	// full then sliced — the frontend never gets a chance to render more
	// than this many rows for one component, and a query like
	// `SELECT * FROM range(100000000)` never materializes more than
	// a2uiQueryRowCap+1 rows in the first place.
	//
	// Per-query timeout (review fix, step 4a #1): a2uiQueryTimeout bounds
	// how long ANY one live binding may run. Without it, a notebook packed
	// with many data-bound components (even after the Coerce-time cap) or
	// an accidentally expensive query (e.g. a cross join) could hang this
	// call indefinitely — and since this DB connection is shared, block
	// every OTHER binding behind it too. go-duckdb's QueryContext honors
	// ctx cancellation by interrupting the running query on the DuckDB
	// connection, so this deadline actually stops the query, not just the
	// Go-side wait.
	ctx, cancel := context.WithTimeout(a.ctx, a2uiQueryTimeout)
	defer cancel()
	before := session.StatusSnapshot(runSess)
	rs, truncated, err := handler.RunReadOnlySQLIn(agents.ArtifactQueryContext(ctx), connection, sqlStr, nil, a2uiQueryRowCap)
	a.afterRun(runSess, before, sqlStr)
	if err != nil {
		return nil, a.explainReadOnlyDenial(err)
	}

	return a2uiResult(rs, truncated)
}

// readOnlyRunner is the handler (and the session it runs on) behind a live
// read-only binding.
func (a *App) readOnlyRunner() (*agentToolHandler, session.Session, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, nil, err
	}
	// ceiling = ModeFull: RunReadOnlySQL forces read-only regardless of the
	// sandbox mode, via its own checkSQLClassReadOnly gate — see
	// RunA2UIQuery's doc comment — so no additional cap is needed here.
	return a.newAgentToolHandler(agents.ModeFull), a.sess, nil
}

// a2uiResult shapes a result set for the renderer, refusing one that does not
// survive a JSON round trip.
func a2uiResult(rs *engine.ResultSet, truncated bool) (*A2UIQueryResult, error) {
	out := &A2UIQueryResult{Truncated: truncated}
	out.Columns, out.Types = rs.NamesAndTypes()
	out.Rows = rs.Rows
	if err := jsonRoundTripGuard(out); err != nil {
		return nil, err
	}
	return out, nil
}

// QuerySummary holds a query list entry.
type QuerySummary struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Engine string `json:"engine"`
}

// ListQueries returns all saved queries.
func (a *App) ListQueries() ([]QuerySummary, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}

	qs, err := a.ws.Queries().List(a.ctxOrBackground())
	if err != nil {
		return nil, err
	}

	result := make([]QuerySummary, 0, len(qs))
	for _, q := range qs {
		result = append(result, QuerySummary{
			ID:     q.ID,
			Title:  q.Title,
			Engine: q.Engine,
		})
	}
	return result, nil
}

// GetQuery returns a saved query by ID.
func (a *App) GetQuery(id string) (*queries.Query, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}

	return a.ws.Queries().Get(a.ctxOrBackground(), id)
}

// QuerySaveResult is what SaveQueryOn returns: the query's id and the revision
// the store now holds for it (empty for a legacy .slaw), which the editor sends
// back with its next save.
type QuerySaveResult struct {
	ID  string `json:"id"`
	Rev string `json:"rev"`
}

// SaveQueryRequest is a saved-query write with the editor's "Run on" choice (#117): the
// connection is saved with the query (its front matter) so a teammate runs it
// on the same connection. An empty Connection clears it (the workspace default).
type SaveQueryRequest struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	SQL         string `json:"sql"`
	Description string `json:"description"`
	Rev         string `json:"rev"`
	Connection  string `json:"connection"`
}

// SaveQueryOn creates or updates a saved query together with its connection.
func (a *App) SaveQueryOn(req SaveQueryRequest) (*QuerySaveResult, error) {
	if req.Connection != "" {
		if err := session.ValidateName(req.Connection); err != nil {
			return nil, fmt.Errorf("unknown connection %q", req.Connection)
		}
	}
	res, err := a.saveQuery(req.ID, req.Title, req.SQL, req.Description, req.Rev, req.Connection)
	if err == nil {
		a.recheckGrants() // a page that reads a metric over this query may no longer be allowed
	}
	return res, err
}

func (a *App) saveQuery(id, title, sqlStr, description, rev, connection string) (*QuerySaveResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}

	q := &queries.Query{
		ID:          id,  // "" lets the store assign one
		Rev:         rev, // the revision the editor loaded; a stale one is a conflict
		Title:       title,
		Description: description,
		SQL:         sqlStr,
		Engine:      "duckdb",
		Parameters:  []queries.Param{},
	}
	// The editor edits title, SQL and the "Run on" connection (the request's
	// connection is authoritative, set below); whatever else the file carries
	// (parameters, a description) is kept.
	if id != "" {
		if base, err := a.ws.Queries().Get(a.ctxOrBackground(), id); err == nil && base != nil {
			q.Parameters, q.Engine = base.Parameters, base.Engine
			if description == "" {
				q.Description = base.Description
			}
		}
	}
	q.Connection = connection

	// Update first for an existing query; a new one (or an id the store does
	// not know) is created. Anything else, a conflict on a file that changed
	// on disk included, is returned to the caller instead of being retried as
	// a create.
	if id != "" {
		err := a.ws.Queries().Update(a.ctxOrBackground(), q)
		if err == nil {
			return &QuerySaveResult{ID: q.ID, Rev: q.Rev}, nil
		}
		if !errors.Is(err, queries.ErrNotFound) {
			return nil, conflictErr(err)
		}
	}
	if err := a.ws.Queries().Create(a.ctxOrBackground(), q); err != nil {
		return nil, err
	}
	return &QuerySaveResult{ID: q.ID, Rev: q.Rev}, nil
}

// DeleteQuery deletes a saved query.
func (a *App) DeleteQuery(id string) error {
	defer a.recheckGrants() // registered first, so it runs after the lock is released
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return err
	}

	return a.ws.Queries().Delete(a.ctxOrBackground(), id)
}

// --- Dashboard ---

// DashboardData holds overview stats.
type DashboardData struct {
	Title         string `json:"title"`
	Description   string `json:"description"`
	NotebookCount int    `json:"notebook_count"`
	TableCount    int    `json:"table_count"`
	QueryCount    int    `json:"query_count"`
	SkillCount    int    `json:"skill_count"`
	// ArtifactCounts counts the workspace's artifacts by Library kind (live
	// views, pages, chart scripts) and the ones that need fixing.
	ArtifactCounts ArtifactCounts `json:"artifact_counts"`
	// LocalTables are the tables of the workspace's default connection (its own
	// `local` database unless the workspace says otherwise), as qualified names
	// the datasheet accepts (design #106 item 8: Home lists them).
	LocalTables []string `json:"local_tables"`
	// DefaultConnection names the connection LocalTables belong to.
	DefaultConnection string `json:"default_connection"`
	ReadOnly          bool   `json:"read_only"`
}

// GetDashboard returns the Home screen's live overview: the workspace's name,
// its counts and its own tables.
func (a *App) GetDashboard() (*DashboardData, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}

	d := &DashboardData{
		Title:             a.ws.Name(),
		DefaultConnection: a.ws.DefaultConnection(),
		ReadOnly:          a.readOnly,
		LocalTables:       []string{},
	}
	ctx := a.ctxOrBackground()
	if nbs, err := a.ws.Notebooks().List(ctx); err == nil {
		d.NotebookCount = len(nbs)
	}
	if qs, err := a.ws.Queries().List(ctx); err == nil {
		d.QueryCount = len(qs)
	}
	if sk, err := a.ws.Skills().List(ctx); err == nil {
		d.SkillCount = len(sk)
	}
	d.ArtifactCounts = countArtifacts(ctx, a.ws.Artifacts())
	if tabs, err := a.sess.ListTables(ctx, "", ""); err == nil {
		d.TableCount = len(tabs)
		for _, t := range tabs {
			d.LocalTables = append(d.LocalTables, t.Ref().String())
		}
	}
	return d, nil
}

// explainReadOnlyDenial turns the classifier's generic "live bindings are
// read-only" refusal into the real reason when this window opened the workspace
// read-only: its own database is in use by another program, so the tables are
// simply not reachable yet. A refusal about the statement itself (empty, a
// syntax or binding mistake) is shown as it is; only a missing table or catalog
// can be the unreachable database.
func (a *App) explainReadOnlyDenial(err error) error {
	if !errors.Is(err, agents.ErrReadOnlyBindingDenied) {
		return err
	}
	var about interface{ MissingObject() bool }
	if errors.As(err, &about) && !about.MissingObject() {
		return err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.readOnly || a.ws == nil {
		return err
	}
	who := "another program"
	if a.holder != nil {
		who = a.holder.String()
	}
	return fmt.Errorf("%s is in use by %s, so this can't run yet. Take over the workspace to read it", a.ws.DefaultConnection(), who)
}
