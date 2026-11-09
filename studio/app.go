package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/config"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/lsp"
	"github.com/SandwichLabs/slaw/internal/prefs"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/selfupdate"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the main application struct bound to the Wails frontend.
//
// Studio holds one workspace at a time (#111): the folder (ws), the DuckDB hub
// built over its connections (sess), the hub lock that makes this process the
// only writer (hold, nil when opened read-only), and, when switched on, the
// MCP server others reach the workspace through (wmcp). Notebooks, queries and
// skills are plain files behind ws; data writes go straight to the source.
type App struct {
	// views serves custom artifact views under /slaw-view/ (viewserver.go).
	views *viewServer
	// pages owns the page windows of scripted HTML pages (pagehost.go).
	pages *pageHost
	// pageApp holds the capability services' App-side state (pagecaps_app.go).
	pageApp pageAppState
	// renderIndex caches the cells of notebooks that render sources named (render_source.go).
	renderIndex notebookIndexCache

	ctx    context.Context
	logger *slog.Logger
	mu     sync.RWMutex

	ws     workspace.Workspace
	sess   session.Session
	engine *engine.Manager
	hold   *hub.Hold
	wmcp   *hub.WorkspaceMCP
	// mcpTool is the wrapped handler wmcp serves (what takes a.mu and shows the
	// approval cards); nil while MCP is off.
	mcpTool *agentToolHandler
	// mcpStartErr is why the remembered server did not restart on open (shown in
	// the MCP status). mcpOn caches the remembered choice for the open workspace
	// (read once on open, kept in step by Start/Stop) so the status never reads
	// the prefs file; mcpSaveErr is why the choice could not be saved.
	mcpStartErr string
	mcpOn       bool
	mcpSaveErr  string
	// mcpStarter is a TEST SEAM only: when set, restoreMCPLocked calls it
	// instead of startMCPLocked, so a test can force a failed restart. Never
	// set outside tests.
	mcpStarter func() error
	watcher    *workspace.Watcher
	// watchDone is closed by closeWorkspaceLocked to end the watcher forwarder.
	watchDone chan struct{}
	// issues are the connections that could not be set up (redacted messages).
	issues []workspace.SessionIssue
	// readOnly is true when another process owns the hub: stores stay editable
	// (they are files), local DuckDB files are not reachable. holder says who.
	readOnly bool
	holder   *hub.Holder
	// duplicateOf lists other known workspace folders that carry this one's id.
	duplicateOf []string

	// connSigs is, per connection, a signature of the definition and sign-in
	// the session was last registered from (connections.go syncConnections):
	// a change on disk or in the editor re-registers just that connection.
	connSigMu sync.Mutex
	// syncMu serialises whole syncs (an editor save and the file watcher can race).
	syncMu   sync.Mutex
	connSigs map[string]string
	// connDeps is, per connection, what it was registered as requiring
	// (guarded by connSigMu; see connections_deps.go).
	connDeps map[string]connDeps

	// initial is what was named on the command line (or opened from the OS
	// before the window was ready): a folder, a .slaw bundle, or a note.
	// frontendReady flips when the frontend first asks for it; OS open
	// requests after that are sent as events (HandleOpenPath).
	launchMu      sync.Mutex
	initial       InitialWorkspace
	frontendReady bool

	// sessionID identifies the CURRENTLY open workspace. It is regenerated on
	// every successful open and cleared on close. A frontend view that reads
	// data at load time (NotebookView.vue's loadedSessionId) stamps itself
	// with the value in effect at that moment and sends it back with every
	// write (SaveNotebook); the backend rejects a write whose token does not
	// match the CURRENT sessionID. That closes a real data-corruption bug: a
	// still-mounted view of the PREVIOUS workspace let an in-flight debounced
	// save land in the NEW one after a switch. The token check is the one
	// guard a caller that skips the frontend's own flush cannot bypass.
	sessionID string
	// liveSess is the session the status listener may speak for (nil: none open).
	liveSess atomic.Pointer[session.Session]
	// liveCat is the catalog SQL completion reads (nil: no workspace open). It
	// is atomic because afterRun feeds it under a.mu and ConnectConnection
	// without it.
	liveCat atomic.Pointer[lsp.Live]

	// activeNotebookID is the id of the notebook currently open in
	// NotebookView.vue, if any — set via SetActiveNotebook. Consulted by
	// add_query_cell/create_dashboard's "append to the open notebook" fallback
	// via agents.Handler.SetActiveNotebook.
	activeNotebookID string

	// taskCancel holds one context.CancelFunc per in-flight AgentTask call,
	// keyed by the frontend-generated taskID — so closing the AI-cell dialog
	// (CancelAgentTask) can stop a task turn without touching a.ctx (the
	// whole app's lifecycle context) or any concurrently running chat turn.
	chat         chatTurns
	runs         runRegistry
	taskCancelMu sync.Mutex
	taskCancel   map[string]context.CancelFunc

	// emit publishes a frontend event. In production it is nil and emitEvent
	// falls back to the Wails runtime.EventsEmit (which requires the real Wails
	// lifecycle context). Tests inject a sink so the open lifecycle can be
	// asserted without a live Wails runtime.
	emit func(event string, data ...any)

	// prefs stores app-global user preferences (theme, appearance, the recent
	// workspaces list) outside any workspace — see internal/prefs. nil in tests
	// that don't need it.
	prefs *prefs.Store
	// recents is the launcher's recent-workspaces list inside the same document;
	// nil-safe when prefs is nil.
	recents *prefs.Recents

	// llmCfg is the app-global, viper-driven LLM provider/model configuration
	// for the wizard agent (loaded at startup from ~/.slaw.yaml + SLAW_* env).
	llmCfg config.LLMConfig

	// secrets resolves credentials (keychain, env, prompt) and owns the
	// redactor that scrubs resolved secret values from logs and errors
	// (#114). keyCache holds the resolved LLM key briefly; see secrets.go.
	secrets  *secrets.Resolver
	keyCache llmKeyCache
	llmSnap  atomic.Pointer[config.LLMConfig]

	// quitConfirmed gates beforeClose: the FIRST window-close/quit attempt is
	// intercepted (OnBeforeClose returns true, preventing it) so the frontend
	// gets a chance to flush any pending debounced saves via the
	// "app:before-close" event; once it has (or a timeout fires), it calls
	// Quit(), which sets this flag so the NEXT beforeClose lets the close
	// through.
	quitConfirmed atomic.Bool

	// closePending is true while a close attempt is already in flight (the
	// frontend is flushing, or the watchdog is armed); a second attempt while
	// it is true quits immediately (see beforeClose).
	closePending atomic.Bool

	// closeWatchdog and quitFn are testing seams for beforeClose's timeout
	// logic: a hung webview never responds to "app:before-close", and the
	// window must still close. Production leaves both nil and beforeClose
	// lazily defaults them to a real time.AfterFunc and the real Quit().
	closeWatchdog func(d time.Duration, f func()) (stop func())
	quitFn        func()
	// watchdogStop stops the armed watchdog (HoldQuit, CancelQuit).
	watchdogMu   sync.Mutex
	watchdogStop func()

	// approvals is the one process-lifetime registry backing every pending
	// write-approval card across every open chat turn and the in-Studio MCP
	// server (#79) — see agentToolHandler.enableApprovals and
	// ResolveAgentApproval. It outlives any single workspace (a close or switch
	// cancels whatever is pending on it before the session goes away).
	approvals *agents.ApprovalQueue

	// locateSlaw finds the `slaw` CLI for the Claude config snippets; tests
	// replace it (nil means hub.LocateSlaw).
	locateSlaw func() (string, error)

	// approvalTimeout bounds how long a pending approval card waits for an
	// answer before the tool call gives up. An App field, not a package
	// constant, so tests can shorten it instead of waiting out 5 minutes.
	approvalTimeout time.Duration

	// updates is the update-check service (settings in prefs, install detection, apply);
	// restart is the pending "Restart now" after an update; relaunch starts the new Studio
	// (selfupdate.Relaunch; a test seam).
	updates *selfupdate.Service
	restart restartRequest
	// installing is true while ApplyUpdate runs install.sh: a quit is held until it ends, so the
	// installer is never orphaned (beforeClose).
	installing atomic.Bool
	relaunch   func(goos, target string, args ...string) error

	// flows are the AWS sign-in flows in flight (signin.go); openURL opens the
	// page a flow shows (nil: the Wails runtime; tests and the ssofake dev build
	// replace it).
	flows   signInFlows
	openURL func(string)

	// consoles are the open console sessions of the workspace (repl_host.go).
	consoles *consoleHost

	// bundles holds .slaw bundles that were previewed and await their commit (#118).
	bundles bundleStore
}

// defaultApprovalTimeout is how long an approval card waits for the user
// before the tool call it's blocking reports "no answer, nothing changed".
const defaultApprovalTimeout = 5 * time.Minute

// closeWatchdogTimeout bounds how long beforeClose waits for the frontend to
// respond to "app:before-close" (flush pending saves, then call Quit())
// before quitting anyway — a hung/suspended webview must never be able to
// make the window impossible to close.
const closeWatchdogTimeout = 5 * time.Second

// NewApp creates a new App instance. The LLM config is loaded eagerly so it is
// available even before the Wails startup hook (and in tests).
func NewApp(logger *slog.Logger) *App {
	red := secrets.NewRedactor()
	logger = slog.New(secrets.NewRedactingHandler(logger.Handler(), red))
	app := &App{
		logger:          logger,
		secrets:         workspace.NewResolver(append(ssoDevOptions(), workspace.WithSecretsOptions(secrets.WithRedactor(red), secrets.WithResolverLogger(logger)))...),
		openURL:         ssoDevOpenURL(),
		llmCfg:          config.Load().LLM,
		approvals:       agents.NewApprovalQueue(),
		approvalTimeout: defaultApprovalTimeout,
		consoles:        newConsoleHost(),
	}
	app.views = newViewServer(viewWorkerJS, viewLibs(), app.viewStillAllowed)
	app.views.log = logger
	app.pages = newPageHost(logger, viewLibs(), app.emitEvent)
	app.setLLMSnapshot(app.llmCfg)
	if dir, err := prefs.DefaultDir(logger); err == nil {
		app.prefs = prefs.NewStore(dir).WithLogger(logger).WithNotify(func(msg string) { app.emitEvent("app:prefs-notice", msg) })
		app.recents = prefs.NewRecents(app.prefs, logger)
	} else {
		logger.Warn("preferences disabled: no config dir", "error", err)
	}
	app.updates = selfupdate.NewService(selfupdate.New(version), app.recents, logger)
	app.relaunch = selfupdate.Relaunch
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.views.setBase(ctx)
	// In production, route frontend events through the live Wails runtime using
	// the real lifecycle context. Tests inject their own emit sink and never call
	// startup, so emitEvent stays runtime-free under test.
	if a.emit == nil {
		a.emit = func(event string, data ...any) {
			runtime.EventsEmit(a.ctx, event, data...)
		}
	}
}

// emitEvent publishes a frontend event, using the injected emitter (tests) when
// set and otherwise the Wails runtime.
func (a *App) emitEvent(event string, data ...any) {
	if a.emit == nil {
		// No emit sink: either a unit test that did not inject one, or a call
		// before startup wired the Wails runtime emitter. Skip emission rather
		// than invoking runtime.EventsEmit with a non-lifecycle context (which
		// log.Fatals). Production always installs the runtime emitter in startup.
		if a.logger != nil {
			a.logger.Debug("emitEvent skipped: no emit sink", "event", event)
		}
		return
	}
	a.emit(event, data...)
}

// shutdown runs when the process quits: MCP off, session closed, then the hub
// lock released last, so the next owner can open the DuckDB files the moment
// the lock is free.
func (a *App) shutdown(_ context.Context) {
	a.cancelAgentWork()
	a.consoles.closeAll(a.logger)
	a.mu.Lock()
	defer a.mu.Unlock()
	root := ""
	if a.ws != nil {
		root = a.ws.Root()
	}
	a.closeWorkspaceLocked("")
	// An update's "Restart now": the new Studio starts only now, with the lock free.
	a.relaunchAfterQuit(root)
}

// errNoWorkspace is what every binding that needs an open workspace returns
// when there is none.
var errNoWorkspace = errors.New("no workspace open")

// requireWorkspace is the one gate every workspace-bound binding runs first.
// The caller holds a.mu (read or write).
func (a *App) requireWorkspace() error {
	if a.ws == nil {
		return errNoWorkspace
	}
	return nil
}

// ctxOrBackground is a.ctx, or Background before startup / in tests.
func (a *App) ctxOrBackground() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
