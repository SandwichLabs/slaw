package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/llm"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// --- Agent ---

// agentPolicy resolves the open workspace's effective agent policy for this
// person — the committed ceiling and this person's own setting, whichever is
// stricter (workspace.EffectivePolicy). It is the one place every caller in
// this file reads it. Caller holds at least a.mu.RLock. With no workspace open
// it reports the fail-safe default.
func (a *App) agentPolicy() agents.FilePolicy {
	if a.ws == nil {
		return agents.FilePolicy{Mode: agents.DefaultSandboxMode, AskBeforeWrites: true}
	}
	policy := agents.PolicyFrom(a.ws)
	if a.readOnly {
		// A workspace opened without owning the hub cannot change data, whatever
		// the settings say.
		policy.Mode = agents.ModeReadOnly
	}
	return policy
}

// AgentConfig surfaces the agent configuration to the UI. Provider/model/endpoint
// are app-global and viper-driven (read-only here — configured via ~/.slaw.yaml
// or SLAW_LLM_* env); the rest is per workspace: Sandbox and AskBeforeWrites are
// what is in effect for this person (the committed team limit and this
// person's own choice, whichever is stricter), TeamSandbox and TeamAsk are the
// committed limit alone.
type AgentConfig struct {
	Provider        string `json:"provider"`        // effective (viper-driven); edit via SetLLMSettings
	Model           string `json:"model"`           // effective (viper-driven); edit via SetLLMSettings
	Endpoint        string `json:"endpoint"`        // effective (viper-driven); edit via SetLLMSettings
	Sandbox         string `json:"sandbox"`         // effective tier
	AskBeforeWrites bool   `json:"askBeforeWrites"` // effective gate
	// TeamSandbox is the highest tier slaw.yaml allows ("" means no limit).
	TeamSandbox string `json:"teamSandbox"`
	// TeamAskBeforeWrites is true when slaw.yaml requires the gate for everyone.
	TeamAskBeforeWrites bool `json:"teamAskBeforeWrites"`
}

// GetAgentConfig returns the viper-driven provider/model/endpoint plus the
// workspace's agent settings.
func (a *App) GetAgentConfig() (*AgentConfig, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	policy := a.agentPolicy()
	cfg := a.ws.Config()
	return &AgentConfig{
		Provider:            a.llmCfg.Provider,
		Model:               a.llmCfg.EffectiveModel(),
		Endpoint:            a.llmCfg.Endpoint,
		Sandbox:             string(policy.Mode),
		AskBeforeWrites:     policy.AskBeforeWrites,
		TeamSandbox:         cfg.Sandbox,
		TeamAskBeforeWrites: cfg.AskBeforeWrites != nil && *cfg.AskBeforeWrites,
	}, nil
}

// AgentToolStep is one tool invocation the agent made during a chat turn.
type AgentToolStep struct {
	Tool    string `json:"tool"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	IsError bool   `json:"isError,omitempty"`
}

// AgentChatResult is the panel-facing result of one agent turn.
type AgentChatResult struct {
	Text  string          `json:"text"`
	Steps []AgentToolStep `json:"steps"`
	// ConversationID names the conversation this turn belongs to. The panel
	// sends it back with the next turn; Go accepts back only an id it issued in
	// this workspace (chatTurns.conversation).
	ConversationID string `json:"conversationId"`
	// Runs are the turn's reads that made a receipt (the chips' receipt id and
	// row count), and Numbers the verdict on each distinct number of Text
	// against those reads' rows: matched, or not from a query.
	Runs    []ChatRun        `json:"runs,omitempty"`
	Numbers []llm.NumberMark `json:"numbers,omitempty"`
}

// AgentTurn is one prior message of conversation history passed from the panel
// for multi-turn memory.
type AgentTurn struct {
	Role string `json:"role"` // "user" | "agent"
	Text string `json:"text"`
}

// AgentChatStream runs a streaming agent turn. As the agent works it emits
// "agent:stream" Wails events tagged with turnID (kind: delta | tool |
// tool_done | final | error) so the panel can show live progress and tool use.
// history is the prior transcript for multi-turn memory; conversationID is the
// id the previous turn's result returned ("" for a new chat). Studio-only.
func (a *App) AgentChatStream(turnID, message, pinnedContext string, history []AgentTurn, conversationID string) (*AgentChatResult, error) {
	cfg, handler, err := a.chatSetup(turnID)
	if err != nil {
		return nil, err
	}
	hist := make([]llm.Turn, 0, len(history))
	for _, h := range history {
		hist = append(hist, llm.Turn{Role: h.Role, Text: h.Text})
	}
	ctx, release, err := a.chat.begin(a.ctxOrBackground(), turnID)
	if err != nil {
		return nil, err
	}
	defer release()
	// The runs this turn makes are the agent's, in this conversation (receipts).
	conv := a.chat.conversation(conversationID)
	ctx = receipts.WithAgentSession(ctx, conv)
	runner := llm.NewRunner(handler, a.logger)
	res, err := runner.ChatStream(ctx, cfg, message, pinnedContext, hist, a.streamEmitter(turnID, conv))
	if err != nil {
		return nil, err
	}
	out := &AgentChatResult{Text: res.Text, ConversationID: conv}
	for _, s := range res.Steps {
		out.Steps = append(out.Steps, AgentToolStep{Tool: s.Tool, Input: s.Input, Output: s.Output, IsError: s.IsError})
	}
	out.Numbers, out.Runs = handler.turn.answerNumbers(res.Text)
	return out, nil
}

// chatSetup is the provider config and tool handler for one streamed chat turn.
func (a *App) chatSetup(turnID string) (llm.Config, *agentToolHandler, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return llm.Config{}, nil, err
	}
	policy := a.agentPolicy()
	cfg := a.llmConfig(string(policy.Mode), policy.AskBeforeWrites)
	// Chat has no cap of its own beyond whatever the file's agent_sandbox
	// setting allows — see AgentChat's identical comment.
	handler := a.newAgentToolHandler(agents.ModeFull)
	// AgentChatStream is the one surface with a human actually watching this
	// turn, so it's the only place an approver is ever attached (#79 D4) —
	// every write the model calls for during this turn now blocks on an
	// inline "agent:approval" card instead of running (or, absent this,
	// being refused outright the way MCP/AgentChat are).
	handler.enableApprovals(turnID)
	// The reads of this turn, for the chips and for checking the answer's numbers.
	handler.trackRuns(turnID)
	return cfg, handler, nil
}

// llmConfig is the app-wide provider config with the given sandbox tier and
// write gate. The caller holds a.mu.
func (a *App) llmConfig(sandbox string, askBeforeWrites bool) llm.Config {
	return llm.Config{
		Provider:        a.llmCfg.Provider,
		Endpoint:        a.llmCfg.Endpoint,
		AccountID:       a.llmCfg.AccountID,
		Gateway:         a.llmCfg.Gateway,
		Model:           a.llmCfg.Model,
		APIKey:          a.llmAPIKey(),
		Sandbox:         sandbox,
		AskBeforeWrites: askBeforeWrites,
	}
}

// streamEmitter publishes a chat turn's progress as "agent:stream" events
// tagged with turnID. Each carries the turn's conversation id, so the panel has
// it even for a turn that is stopped or fails before it returns a result.
func (a *App) streamEmitter(turnID, conversationID string) func(llm.StreamEvent) {
	return func(ev llm.StreamEvent) {
		a.emitEvent("agent:stream", map[string]any{
			"turnId":         turnID,
			"conversationId": conversationID,
			"kind":           ev.Kind,
			"text":           ev.Text,
			"id":             ev.ID,
			"tool":           ev.Tool,
			"input":          ev.Input,
			"output":         ev.Output,
			"isError":        ev.IsError,
		})
	}
}

// TaskCellArg / TaskTurnArg / AgentTaskRequest mirror internal/llm's
// TaskCell/TaskTurn/TaskRequest as Wails-bound JSON shapes (Wails generates
// TS types from these, not from internal/llm's — keeping the request/
// response Go types in studio/app.go, like every other binding here, rather
// than exposing internal/llm's types to the frontend directly).
type TaskCellArg struct {
	Type   string `json:"type"`
	SQL    string `json:"sql,omitempty"`
	View   string `json:"view,omitempty"`
	Label  string `json:"label,omitempty"`
	Source string `json:"source,omitempty"`
	// Connection is the connection the cell runs on ("" = the workspace default);
	// the model is told to keep it (#117).
	Connection string `json:"connection,omitempty"`
}

type TaskTurnArg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// AgentTaskRequest is AgentTask's input. taskID is caller-generated (the
// dialog mints one per open, not per turn — a REFINE/ANSWER-AND-RETRY
// follow-up within the same dialog session reuses it) and is both the
// "agent:task" event-stream correlation key and the CancelAgentTask key.
type AgentTaskRequest struct {
	TaskID          string        `json:"taskId"`
	Mode            string        `json:"mode"`
	Description     string        `json:"description"`
	Cell            *TaskCellArg  `json:"cell,omitempty"`
	Tables          []string      `json:"tables,omitempty"`
	NotebookSummary []string      `json:"notebookSummary,omitempty"`
	FollowUp        []TaskTurnArg `json:"followUp,omitempty"`
}

// taskCellFrom converts the cell the person is editing into the task's cell. A
// connection name is validated before it can reach the prompt.
func taskCellFrom(c *TaskCellArg) (*llm.TaskCell, error) {
	if c == nil {
		return nil, nil
	}
	if c.Connection != "" {
		if err := session.ValidateName(c.Connection); err != nil {
			return nil, fmt.Errorf("unknown connection %q", c.Connection)
		}
	}
	return &llm.TaskCell{Type: c.Type, SQL: c.SQL, View: c.View, Label: c.Label, Source: c.Source, Connection: c.Connection}, nil
}

// AgentTask runs one task-mode turn (#66 part B): a bounded, single-purpose
// loop that authors one notebook cell (create or edit) and returns a
// TaskEnvelope — never free chat prose. Distinct from AgentChat/
// AgentChatStream in every dimension that matters for isolation: its own
// tool set (internal/agents.ModeTask — preview_query/write_cell/
// need_more_info, task-only), its own system prompt (no a2ui block, the
// UNFILTERED skills list so task-* skills are visible), its own event name
// ("agent:task", never "agent:stream" — so a concurrently open chat panel's
// stream can never collide with or be mistaken for a task dialog's), and its
// own cancellation (taskCancel, keyed by TaskID — closing the dialog calls
// CancelAgentTask, not anything that could also cancel a chat turn).
func (a *App) AgentTask(req AgentTaskRequest) (*llm.TaskEnvelope, error) {
	cfg, handler, err := a.taskSetup()
	if err != nil {
		return nil, err
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		return nil, fmt.Errorf("taskId is required")
	}
	ctx, release := a.trackTask(taskID)
	defer release()
	// A task's runs are the agent's, in this dialog (receipts): the dialog's
	// follow-ups all send its one taskId.
	ctx = receipts.WithAgentSession(ctx, taskSession(taskID))

	cell, err := taskCellFrom(req.Cell)
	if err != nil {
		return nil, err
	}
	followUp := make([]llm.TaskTurn, 0, len(req.FollowUp))
	for _, t := range req.FollowUp {
		followUp = append(followUp, llm.TaskTurn{Role: t.Role, Text: t.Text})
	}
	runner := llm.NewRunner(handler, a.logger)
	envelope, err := runner.Task(ctx, cfg, llm.TaskRequest{
		Mode: req.Mode, Description: req.Description, Cell: cell,
		Tables: req.Tables, NotebookSummary: req.NotebookSummary, FollowUp: followUp,
	}, a.taskEmitter(taskID))
	if err != nil {
		return nil, err
	}
	return &envelope, nil
}

// taskSetup is the provider config and tool handler for one task-mode turn.
func (a *App) taskSetup() (llm.Config, *agentToolHandler, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return llm.Config{}, nil, err
	}
	// Task mode is always read-only (llm.TaskConfig), and its handler is capped
	// at the same tier: the ceiling must survive every later per-dispatch
	// SetFilePolicy refresh in agentToolHandler.Handle (see its doc comment).
	cfg := llm.TaskConfig(a.llmConfig("", false))
	return cfg, a.newAgentToolHandler(agents.TaskSandbox), nil
}

// trackTask registers the cancel func of the task running under taskID, so
// CancelAgentTask can stop it without touching a.ctx (the whole app's lifecycle
// context) or a concurrent chat turn. release unregisters it and cancels the
// context.
func (a *App) trackTask(taskID string) (ctx context.Context, release func()) {
	ctx, cancel := context.WithCancel(a.ctx)
	a.taskCancelMu.Lock()
	if a.taskCancel == nil {
		a.taskCancel = make(map[string]context.CancelFunc)
	}
	a.taskCancel[taskID] = cancel
	a.taskCancelMu.Unlock()
	return ctx, func() {
		a.taskCancelMu.Lock()
		delete(a.taskCancel, taskID)
		a.taskCancelMu.Unlock()
		cancel()
	}
}

// taskEmitter publishes a task turn's progress as "agent:task" events tagged
// with taskID.
func (a *App) taskEmitter(taskID string) func(llm.TaskEvent) {
	return func(ev llm.TaskEvent) {
		a.emitEvent("agent:task", map[string]any{
			"taskId":  taskID,
			"kind":    ev.Kind,
			"text":    ev.Text,
			"id":      ev.ID,
			"tool":    ev.Tool,
			"input":   ev.Input,
			"output":  ev.Output,
			"isError": ev.IsError,
		})
	}
}

// CancelAgentTask stops the AgentTask call running under taskID, if any —
// called when the AI-cell dialog closes or a new request supersedes an
// in-flight one. A no-op if the task already finished or taskID is unknown
// (the dialog closing after the turn already resolved is the common case,
// not an error).
func (a *App) CancelAgentTask(taskID string) {
	a.taskCancelMu.Lock()
	defer a.taskCancelMu.Unlock()
	if cancel, ok := a.taskCancel[taskID]; ok {
		cancel()
		delete(a.taskCancel, taskID)
	}
}

// newAgentToolHandler builds the tool-dispatch Handler that backs a single
// Studio chat turn (AgentChat/AgentChatStream) or a task turn (AgentTask),
// gated by BOTH the workspace's live policy AND the given ceiling — see
// agentToolHandler.ceiling's doc comment for why a ceiling is needed at all
// (AgentTask's "always read-only" contract must survive the per-dispatch
// policy refresh). The underlying *agents.Handler is the same one
// hub.NewAgentHandler builds for `slaw mcp serve` and Studio's own MCP server,
// so every caller dispatches every tool call through one registry with one set
// of sandbox/approval rules (#63). It is wrapped in agentToolHandler so each
// individual tool call — not just this construction — is locked and
// workspace-identity-checked. Callers must hold at least a.mu.RLock and have
// already checked a.ws != nil.
func (a *App) newAgentToolHandler(ceiling agents.SandboxMode) *agentToolHandler {
	tighten := ""
	if ceiling != agents.ModeFull {
		tighten = string(ceiling)
	}
	inner, _, err := hub.NewAgentHandler(a.ws, a.sess, tighten, a.logger)
	if err != nil {
		// Unreachable: every ceiling is one of the three valid modes. Fail
		// closed anyway.
		a.logger.Error("failed to build the agent handler", "error", err)
		sc := &agents.SandboxConfig{Mode: agents.ModeReadOnly, MaxQueryRows: 10000, RequireApproval: true}
		inner = agents.NewHandler(workspace.NewAgentAccess(a.ws, a.sess), engine.NewManager(a.sess, engine.WithLogger(a.logger)), sc, a.logger)
		inner.SetReceipts(a.ws.Receipts())
	}
	inner.SetActiveNotebook(a.activeNotebookID)
	return &agentToolHandler{app: a, ws: a.ws, sess: a.sess, inner: inner, ceiling: ceiling}
}

// agentToolHandler wraps an *agents.Handler with App's workspace-lifecycle
// lock. internal/agents.Handler methods talk directly to the hub's raw
// *sql.DB, bypassing every other App method's a.mu locking entirely. Without
// this wrapper, a chat turn that built its handler once (under a brief RLock)
// then ran several tool calls unlocked could race a concurrent
// CloseWorkspace/OpenWorkspace — each of which takes a.mu.Lock() to swap or
// nil out a.ws — and end up querying a closed or already-replaced session
// mid-turn.
//
// Each dispatch here re-takes a.mu.RLock for exactly the duration of that one
// tool call (not the whole multi-step turn, so a slow model thinking between
// tool calls never blocks a close), and rechecks a.ws and a.sess are still the
// exact ones this handler was built for, erroring instead of silently
// operating on a stale one if the workspace was closed or replaced mid-turn.
// On a successful data/schema/content-category tool call it also notifies the
// frontend so Home/Tables/etc. refresh instead of going stale.
type agentToolHandler struct {
	app   *App
	ws    workspace.Workspace
	sess  session.Session
	inner *agents.Handler
	// ceiling is the MOST PERMISSIVE sandbox mode this handler may ever run
	// at, regardless of what the workspace's own policy allows. Every dispatch
	// computes agents.Tighter(ceiling, policy) fresh — see Handle/RunReadOnlySQL
	// — rather than just re-reading the policy alone.
	//
	// This exists because of a real security regression a reviewer caught: once
	// dispatch started re-reading the live policy on every call, that SAME
	// refresh silently OVERWROTE AgentTask's "always read-only, regardless of
	// the setting" contract on its very first tool call. AgentTask sets
	// ceiling = ModeReadOnly; AgentChat/AgentChatStream/StartMCP/RunA2UIQuery
	// set ceiling = ModeFull (no additional cap beyond what the policy allows).
	ceiling agents.SandboxMode
	// turn and turnID are set for a chat turn only (trackRuns): its reads.
	turn   *turnRuns
	turnID string
}

// SetMode forwards to the wrapped *agents.Handler, satisfying internal/llm's
// modeSetter interface so Runner.Task can flip this handler into task mode
// (agents.ModeTask) for the duration of one AgentTask call — see
// setHandlerMode in internal/llm/task.go. AgentChat/AgentChatStream never
// call this, so their handler stays at its zero-value mode (task-only tools
// denied) unless something explicitly opts in.
func (h *agentToolHandler) SetMode(mode string) { h.inner.SetMode(mode) }

// refreshPolicy re-reads the saved agent settings into the handler, so the MCP
// server instructions a client gets on connect report the gate as it is now.
func (h *agentToolHandler) refreshPolicy() {
	h.app.mu.RLock()
	defer h.app.mu.RUnlock()
	policy := h.app.agentPolicy()
	h.inner.SetFilePolicy(agents.Tighter(h.ceiling, policy.Mode), policy.AskBeforeWrites)
}

func (h *agentToolHandler) Handle(ctx context.Context, req *agents.Request) *agents.Response {
	h.app.mu.RLock()
	defer h.app.mu.RUnlock()

	if h.app.ws != h.ws || h.app.sess != h.sess {
		return agents.NewErrorResponse(req.ID, agents.ErrInternal, "the workspace was closed or replaced during this assistant turn; reopen it and try again")
	}

	// Re-read the live policy on every dispatch so a settings change takes
	// effect on this handler's very next call, not just for handlers built
	// after the change. Clamped through h.ceiling (agents.Tighter) so this
	// refresh can never grant a handler more than its ceiling allows. The
	// policy is swapped atomically inside h.inner, so this is safe under
	// concurrent MCP calls (go test -race covers it).
	policy := h.app.agentPolicy()
	h.inner.SetFilePolicy(agents.Tighter(h.ceiling, policy.Mode), policy.AskBeforeWrites)

	// Re-read the live active notebook on every dispatch: Studio's MCP server
	// builds ONE handler for its whole lifetime, so an MCP client's
	// add_query_cell/create_dashboard call minutes after the server started
	// must still see whichever notebook is open in Studio right now.
	h.inner.SetActiveNotebook(h.app.activeNotebookID)
	resp := h.inner.Handle(ctx, req)
	if resp.Error == nil {
		h.app.notifyAgentToolChange(req.Method, resp.Result)
	}
	h.noteRun(req, resp)
	return resp
}

// RunReadOnlySQL is RunA2UIQuery's dispatch path: the same workspace-lifecycle
// lock and identity check Handle gives every JSON-RPC tool call, for
// agents.Handler.RunReadOnlySQL (a data-bound component's "sql", always gated
// to reads regardless of sandbox mode — see RunA2UIQuery). It never calls
// notifyAgentToolChange: RunReadOnlySQL can only ever read.
func (h *agentToolHandler) RunReadOnlySQL(ctx context.Context, sqlStr string, params map[string]any, maxRows int) (rs *engine.ResultSet, truncated bool, err error) {
	return h.RunReadOnlySQLIn(ctx, "", sqlStr, params, maxRows)
}

// RunReadOnlySQLIn is RunReadOnlySQL for a statement bound to a connection.
func (h *agentToolHandler) RunReadOnlySQLIn(ctx context.Context, connection, sqlStr string, params map[string]any, maxRows int) (rs *engine.ResultSet, truncated bool, err error) {
	h.app.mu.RLock()
	defer h.app.mu.RUnlock()

	if h.app.ws != h.ws || h.app.sess != h.sess {
		return nil, false, fmt.Errorf("the workspace was closed or replaced during this assistant turn; reopen it and try again")
	}
	policy := h.app.agentPolicy()
	h.inner.SetFilePolicy(agents.Tighter(h.ceiling, policy.Mode), policy.AskBeforeWrites)
	return h.inner.RunReadOnlySQLIn(ctx, connection, sqlStr, params, maxRows)
}

// VetReadOnlySQLIn is RunReadOnlySQLIn without the run: the same lock, identity
// check and policy verdict, for a compile check of a definition.
func (h *agentToolHandler) VetReadOnlySQLIn(ctx context.Context, connection, sqlStr string, params map[string]any) error {
	h.app.mu.RLock()
	defer h.app.mu.RUnlock()

	if h.app.ws != h.ws || h.app.sess != h.sess {
		return fmt.Errorf("the workspace was closed or replaced during this assistant turn; reopen it and try again")
	}
	policy := h.app.agentPolicy()
	h.inner.SetFilePolicy(agents.Tighter(h.ceiling, policy.Mode), policy.AskBeforeWrites)
	return h.inner.VetReadOnlySQLIn(ctx, connection, sqlStr, params)
}

// enableApprovals installs an approver on h that surfaces each pending write
// as an "agent:approval" event tagged with turnID, and blocks until the
// frontend answers via ResolveAgentApproval (or the request times out/is
// cancelled). Chat calls it once per turn (turnID = the turn); Studio's own MCP
// server calls it once with turnID "mcp", whose cards the shell shows.
//
// Invariant this whole thing depends on: the returned func is only ever
// invoked from inside h.Handle (via h.inner's approver, itself only called
// from agents.Handler.Handle/handleRunQuery), which holds h.app.mu.RLock()
// for its whole dispatch. Releasing that RLock for the DURATION of the wait
// — the h.app.mu.RUnlock()/RLock() pair below — is what keeps close/switch/
// settings responsive while a card sits pending; without it, every one of
// those (all a.mu.Lock() callers) would queue behind this one in-flight tool
// call for up to approvalTimeout. h.app.mu is an RWMutex, so releasing this
// goroutine's read lock while OTHER concurrent readers may still hold theirs
// is safe.
func (h *agentToolHandler) enableApprovals(turnID string) {
	h.inner.SetApprover(func(ctx context.Context, req agents.ApprovalRequest) error {
		timeout := h.app.approvalTimeout
		if timeout <= 0 {
			timeout = defaultApprovalTimeout
		}
		ctx2, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		// sess captures the session this write was approved against (while the
		// lock is still held, below), so the post-wait check can tell a
		// swapped session apart from an unchanged one.
		sess := h.sess

		// The RUnlock happens inside announce, which Wait calls exactly once,
		// after it registers req and before it blocks. So by the time a close
		// holds a.mu.Lock, this waiter is registered and its CancelAll under
		// the lock reaches it.
		err := h.app.approvals.Wait(ctx2, req, func(r agents.ApprovalRequest) {
			h.app.mu.RUnlock()
			h.app.emitEvent("agent:approval", map[string]any{"turnId": turnID, "request": r})
		})
		h.app.mu.RLock()

		// Re-check identity BEFORE deriving the outcome: the workspace may have
		// been closed or replaced while this goroutine had the lock released,
		// and that must win over whatever Wait itself returned — otherwise a
		// Resolve(true) that raced the swap would emit "approved" for a change
		// that must never run on the new workspace.
		if h.app.ws != h.ws || h.app.sess != sess {
			err = agents.ErrApprovalCancelled
		}
		// A "Do it" racing a Stop: the turn was cancelled, so the approved
		// write must not run.
		if err == nil && ctx.Err() != nil {
			err = agents.ErrApprovalCancelled
		}

		outcome := "approved"
		switch {
		case errors.Is(err, agents.ErrApprovalDeclined):
			outcome = "declined"
		case errors.Is(err, agents.ErrApprovalExpired):
			outcome = "expired"
		case errors.Is(err, agents.ErrApprovalCancelled):
			outcome = "cancelled"
		}
		h.app.emitEvent("agent:approval_done", map[string]any{"turnId": turnID, "id": req.ID, "outcome": outcome})

		if err != nil {
			return err
		}

		// Refresh h.inner's sandbox with the CURRENT policy before reporting
		// "approved": the wait can span minutes with a.mu released, during
		// which Settings may have tightened the tier or turned ask back on.
		// A human did approve this exact change, but "approved" must still
		// mean "still allowed" when it actually runs.
		policy := h.app.agentPolicy()
		h.inner.SetFilePolicy(agents.Tighter(h.ceiling, policy.Mode), policy.AskBeforeWrites)
		return nil
	})
}

// ResolveAgentApproval answers a pending approval card (id, from the
// "agent:approval" event's request.id) with the user's decision. Deliberately
// takes no a.mu — the goroutine it wakes up (enableApprovals' approver) is
// the one holding the file lock, and this call must never queue behind that.
func (a *App) ResolveAgentApproval(id string, approved bool) error {
	if a.approvals == nil || !a.approvals.Resolve(id, approved) {
		return fmt.Errorf("this request is no longer waiting")
	}
	return nil
}

// notifyAgentToolChange emits "data:changed" after a successful agent tool
// call in the data/schema/content categories, so views listening for that event
// (Dashboard, Tables, ...) refresh to reflect what the agent just did — the
// same event every other mutating App binding already emits (review fix #6:
// agent-driven writes previously left the UI stale until a manual refresh).
func (a *App) notifyAgentToolChange(method string, result any) {
	td := agents.GetTool(method)
	if td == nil {
		return
	}
	switch td.Category {
	case "data", "schema", "content":
		a.emitEvent("data:changed")
		// save_query and friends change what a page that reads a metric reads.
		// (async: the tool call that got here still holds the workspace lock)
		go a.recheckGrants()
	}
	// add_query_cell/create_dashboard (#67 deferred part) write a new cell to
	// _vibe_notebooks, not the live DuckDB data/schema "data:changed" covers
	// — an open NotebookView only reloads/merges on "notebooks:changed" (the
	// same event CreateNotebook/SaveNotebook/DeleteNotebook/
	// ImportNotebook already emit), so without this an agent-appended cell
	// never appears in an already-open notebook until the user manually
	// reopens it. result is the tool's own JSON-RPC result
	// (handleAddQueryCell/handleCreateDashboard's {notebook_id, cell_id,
	// title, warnings} map) — extracted here so the emitted event carries
	// the SPECIFIC appended cell id (review fix, finding 2): the frontend
	// merges only that id, never "every remote id it doesn't already have",
	// which is what let a locally-deleted cell come back.
	switch method {
	case "propose_definition":
		// The proposal went through this workspace's own store, which the file
		// watcher does not report: the Semantics badge learns of it here.
		a.emitEvent("semantics:proposed")
	case "add_query_cell", "create_dashboard", "add_markdown_cell":
		a.emitCellsAdded(method, result)
	case "update_cell", "delete_cell", "move_cell":
		resultMap, _ := result.(map[string]any)
		if notebookID, _ := resultMap["notebook_id"].(string); notebookID != "" {
			a.emitNotebookEdited(notebookID)
		}
	}
}

// emitCellsAdded announces a cell an agent tool appended. A markdown cell
// inserted before the end (add_markdown_cell's after_cell_id) is an edit of
// the order, not an append, so the open copy reloads instead of merging it in
// at the end.
func (a *App) emitCellsAdded(method string, result any) {
	resultMap, _ := result.(map[string]any)
	notebookID, _ := resultMap["notebook_id"].(string)
	cellID, _ := resultMap["cell_id"].(string)
	if notebookID == "" || cellID == "" {
		a.logger.Warn("agent tool change: missing notebook_id/cell_id in result, not emitting notebooks:changed", "method", method)
		return
	}
	if inserted, _ := resultMap["inserted"].(bool); inserted {
		a.emitNotebookEdited(notebookID)
		return
	}
	a.emitNotebooksChanged(notebookID, []string{cellID})
}
