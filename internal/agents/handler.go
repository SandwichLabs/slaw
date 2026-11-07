// Package agents is the one tool surface for the built-in agent and MCP
// clients: the ToolDef registry, sandbox tiers, the approval queue, the SQL
// allow-list policy and the JSON-RPC handler that dispatches tool calls.
package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/semantics/proposals"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/skills"
)

// WorkspaceAccess provides access to the open workspace (interface to avoid circular imports).
type WorkspaceAccess interface {
	DuckDBDB() *sql.DB
	// Session is the catalog-aware DuckDB hub over DuckDBDB. Introspection
	// goes through it so no tool assumes the default catalog is "main".
	Session() session.Session
	// Notebooks, Queries and Skills are the content stores: file-backed in the
	// workspace folder (notebooks/, queries/, skills/). The agent never
	// touches the files directly.
	Notebooks() notebooks.Store
	Queries() queries.Store
	Skills() skills.Store
	// Semantics is the semantics/ folder: entities, metrics, joins and the
	// glossary, read only. The type has no Put or Delete, so no tool can
	// change a definition; accepting a proposal is the person's act.
	Semantics() semantics.ReadStore
	// Proposals is the inbox of proposed definitions as a proposer sees it:
	// add one, list and count. It cannot accept, reject or change a proposal.
	Proposals() proposals.Proposer
}

// ProtectedPathsProvider is implemented by a WorkspaceAccess that knows the
// files and folders of its attached connections (a DuckLake's catalog and data
// folder, duckdb and sqlite files): COPY ... TO never writes into them.
type ProtectedPathsProvider interface {
	ProtectedPaths() []string
}

// DataDirProvider is implemented by a WorkspaceAccess that has a workspace:
// the folder (data/) the agent may read files from and COPY ... TO. A legacy
// container does not implement it, so agent SQL there reads no files at all.
type DataDirProvider interface {
	DataDir() string
}

// EngineAccess provides query execution.
type EngineAccess interface {
	Execute(ctx context.Context, sql string, params map[string]any) (*engine.ResultSet, error)
}

// Handler dispatches JSON-RPC requests to the appropriate tool handler.
type Handler struct {
	access WorkspaceAccess
	engine EngineAccess
	// sandbox is an atomic.Pointer, not a plain *SandboxConfig field, because
	// a long-lived Handler (StartMCPServer builds exactly one for the whole
	// life of the MCP server) can be dispatched to concurrently while
	// SetFilePolicy swaps in a live "agent_sandbox"/"ask before changes"
	// setting change — a caller that mutated a shared *SandboxConfig's
	// fields in place instead would race with every concurrent
	// Handle()/RunReadOnlySQL() reading those same fields. Load() once per
	// call, use the returned pointer for the rest of that call — never
	// re-Load mid-call, so one call sees one consistent sandbox even if
	// SetFilePolicy fires concurrently.
	sandbox atomic.Pointer[SandboxConfig]
	// approver, when set, is called to obtain the user's OK for a write
	// (#79) — see awaitApproval. nil means "nobody can answer": headless
	// `slaw mcp serve` and Studio's SSE MCP server never call SetApprover,
	// so a write needing approval on either of those is refused outright
	// rather than hanging forever waiting for an answer nobody can give.
	approver ApproveFunc
	logger   *slog.Logger

	// mode gates task-only tools (preview_query, write_cell,
	// need_more_info — see ToolDef.Modes/IsModeVisible). Defaults to ""
	// (deny every Modes-restricted tool) — safe-by-default for chat and MCP,
	// which never set it. internal/llm's Task() loop calls SetMode(ModeTask)
	// on the Handler it builds for a task() call. This is a second,
	// independent gate on top of the tool-list filtering
	// ListToolsForMode(ModeChat)/ListToolsForMode(ModeMCP) already does in
	// the runner/MCP server — a caller that bypasses the advertised tool
	// list and dispatches "write_cell" directly from chat is still denied
	// here.
	mode string

	// activeNotebookID is the id of the notebook currently open in Studio
	// (set via App.SetActiveNotebook, itself called by NotebookView.vue on
	// mount/switch), consulted by add_query_cell/create_dashboard when the
	// caller omits notebook_id. Empty when no notebook is open (a fresh
	// Studio session, or an external MCP client with no Studio window at
	// all) — in that case those tools create a brand-new notebook instead.
	//
	// activeNotebookMu guards activeNotebookID specifically (review fix,
	// finding 8: a DATA RACE). StartMCPServer builds ONE Handler for the
	// server's whole lifetime, and mark3labs/mcp-go dispatches each
	// tools/call on its own goroutine (per internal/mcp/server.go's own Handler
	// doc comment) — two concurrent MCP calls previously wrote/read this
	// field with no synchronization at all. studio/app_agent.go's
	// agentToolHandler.Handle already holds a.mu.RLock() for the whole
	// dispatch and reads a.activeNotebookID under that lock before calling
	// SetActiveNotebook here — but the WRITE into this struct (a completely
	// different lock domain from a.mu) still needs its own guard, since
	// a.mu.RLock() permits multiple concurrent readers, each of which then
	// calls SetActiveNotebook concurrently on this SAME Handler instance.
	// This mutex is deliberately separate from a.mu — reusing a.mu here
	// would mean resolveActiveNotebook (called from inside Handle, which is
	// invoked while a.mu is already held) would have to re-acquire a.mu.RLock
	// from within the SAME goroutine that already holds it: a well-known Go
	// RWMutex hazard (a queued Lock() from a concurrent SetActiveNotebook
	// Wails-binding call can strand that nested RLock forever if it arrives
	// between the two RLock calls). A private mutex sidesteps that entirely.
	activeNotebookMu sync.RWMutex
	activeNotebookID string

	// previewedMu / previewedSQL track which exact (trimmed) sql strings
	// preview_query has succeeded on THIS Handler's lifetime — for
	// chat/MCP that's the process lifetime (irrelevant there: write_cell is
	// task-only), for task mode it's exactly one AgentTask call (a fresh
	// Handler is built per call — see studio/app_agent.go's AgentTask), which is
	// exactly "this task turn". handleWriteCell requires its sql to be a
	// member of this set (review fix, finding 4).
	previewedMu  sync.Mutex
	previewedSQL map[string]bool
}

// SetMode sets the agent-surface mode this Handler dispatches for (see the
// mode field doc). Must be called before Handle for a Handler that should
// allow task-only tools.
func (h *Handler) SetMode(mode string) { h.mode = mode }

// SetActiveNotebook records which notebook (if any) is currently open in
// Studio, for add_query_cell/create_dashboard's "append to the open
// notebook" fallback. Safe to call concurrently with Handle (see
// activeNotebookMu's doc comment on the Handler struct).
func (h *Handler) SetActiveNotebook(id string) {
	h.activeNotebookMu.Lock()
	h.activeNotebookID = id
	h.activeNotebookMu.Unlock()
}

// resolveActiveNotebook reads activeNotebookID under its own lock — see
// activeNotebookMu's doc comment for why this can't just reuse a2ui/callers'
// own locking.
func (h *Handler) resolveActiveNotebook() string {
	h.activeNotebookMu.RLock()
	defer h.activeNotebookMu.RUnlock()
	return h.activeNotebookID
}

// markPreviewed records sql (trimmed) as successfully previewed this turn.
func (h *Handler) markPreviewed(connection, sql string) {
	if h.mode != ModeTask {
		return // only write_cell reads the set; a long-lived handler must not grow it
	}
	h.previewedMu.Lock()
	if h.previewedSQL == nil {
		h.previewedSQL = make(map[string]bool, 4)
	}
	h.previewedSQL[connection+"\x00"+strings.TrimSpace(sql)] = true
	h.previewedMu.Unlock()
}

// wasPreviewed reports whether sql (trimmed) was previously recorded by
// markPreviewed.
func (h *Handler) wasPreviewed(connection, sql string) bool {
	h.previewedMu.Lock()
	defer h.previewedMu.Unlock()
	return h.previewedSQL[connection+"\x00"+strings.TrimSpace(sql)]
}

// duckDB returns the DuckDB connection or an error response if unavailable.
func (h *Handler) duckDB(reqID any) (*sql.DB, *Response) {
	db := h.access.DuckDBDB()
	if db == nil {
		return nil, NewErrorResponse(reqID, ErrInternal, "DuckDB not available")
	}
	return db, nil
}

// tableSQL renders a tool's table argument as fully resolved, quoted SQL
// (catalog.schema.table), so a write cannot be redirected by the connection's
// current catalog or shadowed by a same-named TEMP table. A name that is
// exactly an existing table of the default catalog and schema wins over
// dotted-path parsing (a legacy table may legitimately contain dots); a name
// that cannot be resolved falls back to engine.TableSQL's safe quoting.
func (h *Handler) tableSQL(ctx context.Context, name string) (string, error) {
	sess := h.access.Session()
	if sess == nil || sess.DB() == nil {
		return engine.TableSQL(name), nil
	}
	bare := session.TableRef{Name: name}
	if _, ok, _ := session.LookupTable(ctx, sess.DB(), bare); ok {
		if r, err := sess.Resolve(ctx, bare); err == nil {
			return r.SQL(), nil
		}
		return bare.SQL(), nil
	}
	ref, err := session.ParseTableRef(name)
	if err != nil {
		return session.QuoteIdent(name), nil
	}
	r, err := sess.Resolve(ctx, ref)
	if err != nil && ref.Catalog == "" && ref.Schema == "" {
		// Nothing to resolve against (not a DuckDB handle): a bare name can
		// only mean the connection's own current table.
		return ref.SQL(), nil
	}
	if err != nil {
		// An unknown connection (or the internal "system"/"temp" catalogs)
		// is refused rather than passed to DuckDB.
		return "", err
	}
	return r.SQL(), nil
}

// sess returns the DuckDB session or an error response if unavailable.
func (h *Handler) sess(reqID any) (session.Session, *Response) {
	s := h.access.Session()
	if s == nil {
		return nil, NewErrorResponse(reqID, ErrInternal, "DuckDB not available")
	}
	return s, nil
}

// NewHandler creates a new request handler.
// connectReferenced dials the registered, not yet connected connections the
// statement names (lazy attach, #115) so the classifier's EXPLAIN can bind
// them. A failure is returned, not fatal: the caller carries on, and only when
// the classifier then denies the statement (it names a catalog that is not
// there) is the connection's own problem reported instead of a misleading
// "external access" denial (see withConnectError).
func (h *Handler) connectReferenced(ctx context.Context, catalog, sqlText string) error {
	if h.access == nil {
		return nil
	}
	if s := h.access.Session(); s != nil {
		return s.ConnectReferencedExcept(ctx, catalog, sqlText, agentNeverConnects...)
	}
	return nil
}

// agentNeverConnects are the connection types an agent statement never dials:
// until the lake reader routes them (L4), the policy refuses their tables, and
// refusing must not first attach the catalog (a catalog request, and a vended
// secret) on the main hub.
var agentNeverConnects = []string{"iceberg_catalog"}

// withConnectError prefers the connection's own failure over a policy denial
// that it caused.
func withConnectError(denial, connectErr error) error {
	if connectErr != nil {
		return &ConnectError{Err: connectErr}
	}
	return denial
}

// ConnectError is a connection's own failure to connect, as opposed to a
// refusal or a binder error about the statement. It prints as the wrapped
// error; ConnectFailure lets a caller that cannot import agents (the
// assertion runner) tell "the connection is down" from "the definition is
// wrong" with errors.As.
type ConnectError struct{ Err error }

func (e *ConnectError) Error() string        { return e.Err.Error() }
func (e *ConnectError) Unwrap() error        { return e.Err }
func (e *ConnectError) ConnectFailure() bool { return true }

func NewHandler(access WorkspaceAccess, engine EngineAccess, sandbox *SandboxConfig, logger *slog.Logger) *Handler {
	h := &Handler{
		access: access,
		engine: engine,
		logger: logger,
	}
	h.sandbox.Store(sandbox)
	return h
}

// ApproveFunc obtains the user's OK for a pending write (#79). It is called
// synchronously from inside Handle/handleRunQuery — a blocking call is
// exactly the intended behaviour (the model's tool call waits for a human
// answer) — and returns nil only on approval. studio/app_agent.go's
// agentToolHandler.enableApprovals is the only production implementation: it
// releases the caller's file lock for the duration of the wait (documented
// on that function) so Save/Close/Settings stay responsive while a card is
// pending.
type ApproveFunc func(ctx context.Context, req ApprovalRequest) error

// SetApprover installs (or, with nil, removes) the function Handle/
// handleRunQuery calls to obtain approval for a gated write. A Handler with
// no approver refuses every write that needs one (see awaitApproval) instead
// of blocking — that is the deliberate headless behaviour. Headless
// `slaw mcp serve` attaches no approver; Studio's SSE server does
// (studio/workspace_mcp.go), so its writes show an approval card.
func (h *Handler) SetApprover(fn ApproveFunc) { h.approver = fn }

// SetFilePolicy atomically swaps in both the sandbox mode and the
// RequireApproval flag together, preserving every other SandboxConfig field
// (MaxQueryRows, AllowedTables, ...) from whatever is currently stored. One
// atomic swap — not two separate setters — so a concurrent Handle() call can
// never observe a mode/approval combination that was never actually the
// file's policy (e.g. the new mode with the old approval flag). Safe to call
// concurrently with Handle()/RunReadOnlySQL() — see the sandbox field's doc
// comment.
func (h *Handler) SetFilePolicy(mode SandboxMode, requireApproval bool) {
	cur := h.sandbox.Load()
	next := *cur
	next.Mode = mode
	next.RequireApproval = requireApproval
	h.sandbox.Store(&next)
}

// Policy is the sandbox tier and ask-before-changes setting a call made with
// ctx would run under now (the live file policy, tightened by any ceiling on
// ctx).
func (h *Handler) Policy(ctx context.Context) FilePolicy {
	sb := h.sandboxFor(ctx)
	return FilePolicy{Mode: sb.Mode, AskBeforeWrites: sb.RequireApproval}
}

// HasApprover reports whether someone can approve a write (Studio's approval
// card); false means a write that needs an OK is refused.
func (h *Handler) HasApprover() bool { return h.approver != nil }

// Handle processes a JSON-RPC request and returns a response.
func (h *Handler) Handle(ctx context.Context, req *Request) *Response {
	h.logger.Info("handling request", "method", req.Method, "id", req.ID)

	// Validate the method exists.
	tool := GetTool(req.Method)
	if tool == nil {
		return NewErrorResponse(req.ID, ErrMethodNotFound, fmt.Sprintf("unknown method: %q", req.Method))
	}
	if !IsModeVisible(req.Method, h.mode) {
		return NewErrorResponse(req.ID, ErrMethodNotFound, fmt.Sprintf("method %q is not available in this context", req.Method))
	}

	// Validate required params.
	if err := validateParams(tool, req.Params); err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, err.Error())
	}

	// Load the sandbox ONCE for this call (see the field's doc comment) so a
	// concurrent SetFilePolicy can't make one request's checks straddle two
	// different sandbox configs.
	sb := h.sandboxFor(ctx)

	// Check sandbox permissions.
	if err := sb.Check(req.Method); err != nil {
		return NewErrorResponse(req.ID, ErrPermission, err.Error())
	}

	// Check table allowlist for table-specific operations.
	if table := extractTableParam(req.Params); table != "" {
		if !sb.IsTableAllowed(table) {
			return NewErrorResponse(req.ID, ErrPermission, fmt.Sprintf("table %q is not in the allowed tables list", table))
		}
	}

	// Approval gate (#79): every write EXCEPT run_query asks here, using the
	// sandbox's own category gate above as "would this even be allowed" —
	// approval runs strictly after that check passes, so a call the sandbox
	// denies never shows a card (a read-only-mode delete_record fails with
	// ErrPermission above and never reaches this point). run_query is
	// handled separately, inside handleRunQuery, because whether it asks at
	// all depends on classifying the SQL text, not just its "data" category.
	if isWriteOperation(tool.Category) && req.Method != "run_query" {
		var nonLocal []string
		if t, ok := h.toolTarget(ctx, req); ok {
			// Three-gate writes (#116): the tier allowed the tool above; the
			// target connection must be writable; and anything that is not a
			// local file is always approved by the person.
			var gerr error
			if nonLocal, gerr = writeGates([]sqlTarget{t}); gerr != nil {
				return NewErrorResponse(req.ID, ErrPermission, gerr.Error())
			}
		}
		if (sb.RequireApproval && !approvalExempt(req.Method)) || len(nonLocal) > 0 {
			if r := h.awaitApproval(ctx, req, sb, "", "", nil, sqlClassWrite, nonLocal); r != nil {
				return r
			}
		}
	}

	return h.dispatch(ctx, req)
}

// approvalExempt reports whether "Ask before changes" does not gate a tool.
// propose_definition only files into the machine-local inbox; the person's
// accept step is the approval, so asking first would approve the same change
// twice (feedback-2026-11 D1). The tier gate and the inbox caps still apply.
func approvalExempt(method string) bool { return method == "propose_definition" }

// awaitApproval is the one place Handle/handleRunQuery ask for the user's OK
// on a write that the sandbox has already allowed. sqlText/params/class are
// only meaningful for run_query (describeWrite's "run_query" case uses class
// to pick the DML-vs-DDL wording, and a post-approval re-check below
// reclassifies sqlText/params against the file's live sandbox); every other
// tool passes sqlText="" and params=nil and ignores class. Returns nil to
// mean "proceed"; any non-nil *Response is the final answer to send back to
// the caller without dispatching the tool at all.
//
// nonLocal names the connections this write changes that are not the
// workspace's own local files. Approval is forced for those whatever the
// person's "Ask before changes" setting is (the third write gate, #116).
// runSQL is the text that will actually run (wildcards expanded): the re-check after
// the wait vets exactly it, not a fresh expansion.
func (h *Handler) awaitApproval(ctx context.Context, req *Request, sb *SandboxConfig, sqlText, runSQL string, params map[string]any, class sqlClass, nonLocal []string) *Response {
	if h.approver == nil {
		if tool := GetTool(req.Method); tool.DryRunSafe && len(nonLocal) == 0 {
			// Validate first: an invalid request gets its own error, and a valid
			// one is told it is ready and only needs the person's OK.
			resp, dry := h.validateOnly(ctx, req)
			if resp != nil {
				return resp
			}
			return contentNeedsApproval(req, sb, dry)
		}
		if len(nonLocal) > 0 {
			return NewErrorResponse(req.ID, ErrApprovalNeeded, fmt.Sprintf(
				"%s changes %s, which is not a local file in this workspace, so it always needs your OK in Slaw (even when \"Ask before changes\" is off), and there is no one to ask over MCP. "+
					"Nothing was changed. Open the workspace in Slaw: with its MCP server on, `slaw mcp serve` passes through to it and Studio shows you an approval card, or make the change from Studio's agent chat.",
				req.Method, strings.Join(nonLocal, ", ")))
		}
		return dataNeedsApproval(req, sb)
	}

	userDB := h.access.DuckDBDB()
	ar := describeWrite(ctx, req, userDB, class)
	h.describeCellEdit(ctx, req, &ar)
	if sqlText != "" {
		ar.SQL = sqlText
	}
	if len(nonLocal) > 0 {
		ar.Connections = nonLocal
		ar.Summary = fmt.Sprintf("%s (on %s, not a local file)", ar.Summary, strings.Join(nonLocal, ", "))
	}
	ar.Mode = string(sb.Mode)
	ar.ID = uuid.New().String()

	err := h.approver(ctx, ar)
	switch {
	case err == nil:
		// Round 2 review finding 7: the approver call above can block for
		// minutes with the caller's file lock released (see
		// studio/app_agent.go's enableApprovals), during which SetFilePolicy may
		// have tightened the file's live mode. A human did approve this
		// exact change, but "approved" must still mean "still allowed" the
		// moment it actually runs, not "was allowed when the card first
		// appeared" — so re-check against the CURRENT sandbox (the studio
		// approver refreshes it via SetFilePolicy right before returning
		// nil) rather than trusting the sb loaded before the wait.
		fresh := h.sandboxFor(ctx)
		if err := fresh.Check(req.Method); err != nil {
			return NewErrorResponse(req.ID, ErrPermission, fmt.Sprintf(
				"the agent's access changed while this was waiting for approval: %s", err.Error()))
		}
		if req.Method == "run_query" {
			v := h.classify(ctx, userDB, runSQL, params)
			if err := fresh.checkSQLClass(v.Class, v.gateReason()); err != nil {
				return NewErrorResponse(req.ID, ErrPermission, fmt.Sprintf(
					"the agent's access changed while this was waiting for approval: %s", err.Error()))
			}
			if _, err := writeGates(v.Targets); err != nil {
				return NewErrorResponse(req.ID, ErrPermission, fmt.Sprintf(
					"a connection changed while this was waiting for approval: %s", err.Error()))
			}
		}
		return nil
	case errors.Is(err, ErrApprovalDeclined):
		return NewErrorResponse(req.ID, ErrApprovalNeeded, fmt.Sprintf(
			"The user declined this change (%s). Nothing was changed. Don't retry it unless the user asks again.", ar.Summary))
	case errors.Is(err, ErrApprovalExpired):
		return NewErrorResponse(req.ID, ErrApprovalNeeded, "No answer in time, so nothing was changed.")
	case errors.Is(err, ErrApprovalCancelled):
		return NewErrorResponse(req.ID, ErrApprovalNeeded, "Stopped before this change ran (the turn was cancelled or the workspace was closed). Nothing was changed.")
	default:
		return NewErrorResponse(req.ID, ErrApprovalNeeded, err.Error())
	}
}

// validateOnly dry-runs a DryRunSafe tool. It returns the tool's error, or nil
// when the request is valid, plus the dry run's result map (what a clean vet
// learned, such as each query's columns). A "success" that does not carry the dry-run marker
// means the handler did not stop before its write; that is reported as an
// internal error and never as a valid request.
func (h *Handler) validateOnly(ctx context.Context, req *Request) (*Response, map[string]any) {
	resp := h.dispatch(withDryRun(ctx), req)
	if resp.Error != nil {
		return resp, nil
	}
	m, ok := resp.Result.(map[string]any)
	if !ok || m["dry_run"] != true {
		return NewErrorResponse(req.ID, ErrInternal, fmt.Sprintf("%s could not be checked without saving; nothing was reported as valid", req.Method)), nil
	}
	return nil, m
}

// dispatch routes the request to the appropriate handler method.
func (h *Handler) dispatch(ctx context.Context, req *Request) *Response {
	switch req.Method {
	// Introspect
	case "list_connections":
		return h.handleListConnections(ctx, req)
	case "introspect_schema":
		return h.handleIntrospectSchema(ctx, req)
	case "describe_table":
		return h.handleDescribeTable(ctx, req)
	case "list_queries":
		return h.handleListQueries(ctx, req)
	case "list_notebooks":
		return h.handleListNotebooks(ctx, req)
	case "get_notebook":
		return h.handleGetNotebook(ctx, req)
	case "help_search":
		return h.handleHelpSearch(ctx, req)
	case "list_skills":
		return h.handleListSkills(ctx, req)
	case "read_skill":
		return h.handleReadSkill(ctx, req)

	// Data
	case "run_query":
		return h.handleRunQuery(ctx, req)
	case "create_record":
		return h.handleCreateRecord(ctx, req)
	case "update_record":
		return h.handleUpdateRecord(ctx, req)
	case "delete_record":
		return h.handleDeleteRecord(ctx, req)
	case "bulk_insert":
		return h.handleBulkInsert(ctx, req)

	// Schema
	case "create_table":
		return h.handleCreateTable(ctx, req)
	case "add_column":
		return h.handleAddColumn(ctx, req)
	case "drop_table":
		return h.handleDropTable(ctx, req)
	case "import_csv":
		return h.handleImportCSV(ctx, req)

	// Query Wizard
	case "build_query":
		return h.handleBuildQuery(ctx, req)

	// Schema Builder draft + saved-SQL query (ported from internal/llm, #63)
	case "propose_schema":
		return h.handleProposeSchema(ctx, req)
	case "save_query":
		return h.handleSaveQuery(ctx, req)
	case "run_saved_query":
		return h.handleRunSavedQuery(ctx, req)

	// The semantic layer: read it, or propose a change (the person accepts)
	case "list_definitions":
		return h.handleListDefinitions(ctx, req)
	case "get_definition":
		return h.handleGetDefinition(ctx, req)
	case "propose_definition":
		return h.handleProposeDefinition(ctx, req)

	// Watches (E7 W3): read the files, write one; no tool checks a watch
	case "list_watches":
		return h.handleListWatches(ctx, req)
	case "get_watch":
		return h.handleGetWatch(ctx, req)
	case "write_watch":
		return h.handleWriteWatch(ctx, req)

	// A2UI artifacts (#67)
	case "present_data":
		return h.handlePresentData(ctx, req)

	// Data artifacts (epic #162)
	case "list_artifacts":
		return h.handleListArtifacts(ctx, req)
	case "get_artifact":
		return h.handleGetArtifact(ctx, req)
	case "write_artifact":
		return h.handleWriteArtifact(ctx, req)

	// Skills (#62)
	case "create_or_update_skill":
		return h.handleCreateOrUpdateSkill(ctx, req)

	// Task mode (#66 part B)
	case "preview_query":
		return h.handlePreviewQuery(ctx, req)
	case "write_cell":
		return h.handleWriteCell(ctx, req)
	case "need_more_info":
		return h.handleNeedMoreInfo(ctx, req)

	// Notebook-authoring (#67 deferred part)
	case "add_query_cell":
		return h.handleAddQueryCell(ctx, req)
	case "create_dashboard":
		return h.handleCreateDashboard(ctx, req)
	case "add_markdown_cell":
		return h.handleAddMarkdownCell(ctx, req)
	case "update_cell":
		return h.handleUpdateCell(ctx, req)
	case "delete_cell":
		return h.handleDeleteCell(ctx, req)
	case "move_cell":
		return h.handleMoveCell(ctx, req)

	default:
		return NewErrorResponse(req.ID, ErrMethodNotFound, fmt.Sprintf("unknown method: %q", req.Method))
	}
}

// --- Helpers ---

// validateParams checks that all required parameters are present.
func validateParams(tool *ToolDef, params map[string]any) error {
	for _, p := range tool.Params {
		if p.Required {
			if _, ok := params[p.Name]; !ok {
				return fmt.Errorf("missing required parameter: %q", p.Name)
			}
		}
	}
	return nil
}

// extractTableParam extracts the table name from params if present.
func extractTableParam(params map[string]any) string {
	if t, ok := params["table"].(string); ok {
		return t
	}
	return ""
}

// isWriteOperation returns true if the category involves modifications.
func isWriteOperation(category string) bool {
	return category == "data" || category == "content" || category == "schema"
}

// dropDuckLakeInternalRows removes the rows that name a DuckLake metadata
// catalog (__ducklake_metadata_<lake>) in any text column.
func dropDuckLakeInternalRows(rows [][]any) [][]any {
	var out [][]any
	for i, r := range rows {
		hide := false
		for _, c := range r {
			if s, ok := c.(string); ok && isDuckLakeInternal(s) {
				hide = true
				break
			}
		}
		if hide && out == nil {
			out = append(make([][]any, 0, len(rows)), rows[:i]...)
		}
		if !hide && out != nil {
			out = append(out, r)
		}
	}
	if out == nil {
		return rows
	}
	return out
}

// DefinitionError is a statement the policy or the database's binder refused:
// the text is wrong (a missing column, a table that does not exist, a path the
// policy forbids), as opposed to a connection or I/O failure while checking
// it. It prints as the wrapped error; DefinitionProblem lets a caller that
// cannot import agents (the assertion runner) report it as a failed check
// rather than one that could not run.
type DefinitionError struct{ Err error }

func (e *DefinitionError) Error() string           { return e.Err.Error() }
func (e *DefinitionError) Unwrap() error           { return e.Err }
func (e *DefinitionError) DefinitionProblem() bool { return true }

// BindFailure is a statement the database could not bind (an unknown column or
// table, a syntax error), as opposed to one the policy refuses. BindFailure lets
// a caller that cannot import agents (the watch check) show the database's
// message instead of a policy refusal.
type BindFailure struct{ Err error }

func (e *BindFailure) Error() string           { return e.Err.Error() }
func (e *BindFailure) Unwrap() error           { return e.Err }
func (e *BindFailure) DefinitionProblem() bool { return true }
func (e *BindFailure) BindFailure() bool       { return true }

// binderErrorPrefixes are DuckDB's stable error-class names for a statement
// that is wrong. This is the one place the vet looks at the database's message
// text: EXPLAIN's failure reaches the policy as a string.
var binderErrorPrefixes = []string{"Binder Error", "Catalog Error", "Parser Error"}

// definitionReason reports whether a read-only denial reason is about the
// statement. A policy denial always is; "could not validate query: ..." is
// only when the database says Binder, Catalog or Parser Error (an I/O error
// from a dropped remote, or no database at all, is not).
func definitionReason(reason string) bool {
	rest, ok := strings.CutPrefix(reason, "could not validate query:")
	if !ok {
		return true
	}
	for _, p := range binderErrorPrefixes {
		if strings.Contains(rest, p) {
			return true
		}
	}
	return false
}
