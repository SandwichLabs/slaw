// Package mcp serves a workspace's agent tools to external agents over the
// Model Context Protocol (SSE and stdio) and proxies to the process that owns
// the workspace hub.
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/jsonenc"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Handler is the tool-dispatch surface Server needs. The production
// implementation is *agents.Handler, but a caller (e.g. studio/app.go) may
// wrap it to add its own locking/lifecycle checks around each call — see
// that package's agentToolHandler — since Server invokes Handle from its own
// background HTTP goroutines, well outside of whatever locked the container
// when the Handler was constructed.
type Handler interface {
	Handle(ctx context.Context, req *agents.Request) *agents.Response
}

// Server wraps an MCP server with SSE transport, bridging to a Handler.
type Server struct {
	handler    Handler
	mcpServer  *server.MCPServer
	sseServer  *server.SSEServer
	httpServer *http.Server
	listener   net.Listener
	logger     *slog.Logger

	// name and version are the serverInfo of initialize; instructions, when
	// set, supplies its instructions text.
	name, version string
	instructions  func(ctx context.Context) string

	// tier, when set, reports the sandbox tier a tools/list is answered at
	// (the handler's policy tightened by the session's ceiling), so the list
	// leaves out the tools that tier can never run (#221).
	tier func(ctx context.Context) agents.SandboxMode

	// token is a random bearer credential generated fresh on every Start
	// (#64/#70 security review: the SSE MCP server listens on localhost
	// HTTP with no auth, reachable by ANY page open in the user's browser —
	// a classic CSRF/DNS-rebinding target, since "localhost" is exempt from
	// most browser cross-origin protections). A client must present it as
	// `Authorization: Bearer <token>` (or `?token=` for transports that
	// can't set headers, e.g. a raw EventSource) on every request. Empty
	// only before the first Start.
	token string

	// sessionCeil maps an SSE session id to the sandbox ceiling its client
	// asked for with the SandboxHeader on the stream request. Bound at
	// connection time and applied tighten-only to every call on that session,
	// so a later request cannot drop it.
	sessionCeil sync.Map // string -> agents.SandboxMode

	// sseSessions holds the ids of the live SSE sessions, and stdioSession is
	// the one id every stdio call of this process shares (random, made once). A
	// run receipt names the session that asked (receipts.WithAgentSession), so
	// get_receipt can later tell one client's receipts from another's even though
	// one handler serves every client.
	sseSessions  sync.Map // string -> struct{}
	stdioSession string
}

// SandboxHeader is the request header a proxying `slaw mcp serve --sandbox X`
// sends on its SSE connection. The owner caps that session's sandbox at X
// (never above its own policy). Unknown values are rejected with 400.
const SandboxHeader = "X-Slaw-Sandbox"

// Option configures a Server.
type Option func(*Server)

// WithServerInfo sets the name and version the server reports in initialize's
// serverInfo. Without it the server calls itself DefaultServerName at version
// "dev".
func WithServerInfo(name, version string) Option {
	return func(s *Server) { s.name, s.version = name, version }
}

// WithInstructions sets the function that supplies the server instructions
// every client gets in its initialize response (or, on protocol 2026-07-28, in
// its server/discover result). It runs once per initialize or discover, so a
// client that connects later sees whatever the function reports then; one
// already connected keeps the text it was given. Whether that tracks a setting
// the person changed meanwhile depends on the function: Studio's refreshes its
// policy first, while a headless server fixes its policy when it starts.
func WithInstructions(f func(ctx context.Context) string) Option {
	return func(s *Server) { s.instructions = f }
}

// WithTier sets the function that reports the sandbox tier of the session whose
// tools/list is being answered. A tool whose category that tier does not allow
// (agents.TierAllows) is left out of the list. It only shapes the list: every
// call is gated by the handler exactly as before, so a client that calls an
// unlisted tool anyway gets the tier refusal, not "unknown tool". The function
// runs on every tools/list, so a tier changed in Studio shows on the next list;
// NotifyToolsChanged tells a client that already listed to ask again.
func WithTier(f func(ctx context.Context) agents.SandboxMode) Option {
	return func(s *Server) { s.tier = f }
}

// DefaultServerName is the serverInfo name when none is set.
const DefaultServerName = "Slaw"

// New creates a new MCP server backed by the given tool-dispatch Handler.
func New(handler Handler, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		handler:      handler,
		logger:       logger,
		name:         DefaultServerName,
		version:      "dev",
		stdioSession: receipts.NewSessionID(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// buildMCPServer creates the underlying *server.MCPServer and registers
// every ModeMCP tool against it. Shared by Start (SSE transport) and
// ServeStdio (stdio transport) so a stdio and an SSE client see byte
// identical tool lists — one registration path, not two.
func (s *Server) buildMCPServer(hooks *server.Hooks) *server.MCPServer {
	if hooks == nil {
		hooks = &server.Hooks{}
	}
	if s.instructions != nil {
		// Set per initialize, not at build time, so a client that connects after
		// the person changed a setting is told the current state.
		hooks.AddAfterInitialize(func(ctx context.Context, _ any, _ *mcp.InitializeRequest, result *mcp.InitializeResult) {
			result.Instructions = s.instructions(s.withSessionCeiling(ctx))
		})
		// Protocol 2026-07-28 has no initialize: a client calls server/discover,
		// whose result would otherwise carry the (empty) static instructions.
		// Its serverInfo comes from the server's name and version.
		hooks.AddAfterDiscover(func(ctx context.Context, _ any, _ *mcp.DiscoverRequest, result *mcp.DiscoverResult) {
			result.Instructions = s.instructions(s.withSessionCeiling(ctx))
		})
	}
	if s.tier != nil {
		hooks.AddAfterListTools(func(ctx context.Context, _ any, _ *mcp.ListToolsRequest, result *mcp.ListToolsResult) {
			result.Tools = s.tierTools(s.withSessionCeiling(ctx), result.Tools)
		})
	}
	mcpServer := server.NewMCPServer(
		s.name,
		s.version,
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
	)
	for _, tool := range agents.ListToolsForMode(agents.ModeMCP) {
		mcpTool := toolDefToMCPTool(tool)
		toolName := tool.Name
		mcpServer.AddTool(mcpTool, s.makeToolHandler(toolName))
	}
	return mcpServer
}

// tierTools drops from a tools/list the tools the session's tier can never run.
// A tool the registry does not know stays (it is not ours to judge).
func (s *Server) tierTools(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	mode := s.tier(ctx)
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if td := agents.GetTool(t.Name); td != nil && !agents.TierAllows(*td, mode) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// NotifyToolsChanged sends notifications/tools/list_changed to every connected
// client, for an owner (Studio) whose tier just changed: the server advertises
// the listChanged capability, and a client that cached the list asks again.
// It does nothing when the server is not running.
func (s *Server) NotifyToolsChanged() {
	if m := s.mcpServer; m != nil {
		m.SendNotificationToAllClients("notifications/tools/list_changed", nil)
	}
}

// generateToken creates a fresh random bearer token (32 bytes, hex-encoded).
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate MCP auth token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Start creates the MCP server, registers tools, and starts the HTTP/SSE
// listener bound to addr. addr must resolve to a loopback interface (Studio
// and cmd/mcp.go both pass "127.0.0.1:0") — Start does not itself restrict
// the bind address, but every request is additionally gated at request time
// by requireLocalOrigin/checkToken below, which reject anything that didn't
// arrive as a same-machine, non-browser client regardless of what Start was
// told to bind to.
func (s *Server) Start(addr string) error {
	hooks := &server.Hooks{}
	hooks.AddOnUnregisterSession(func(_ context.Context, sess server.ClientSession) {
		s.sessionCeil.Delete(sess.SessionID())
		s.sseSessions.Delete(sess.SessionID())
	})
	s.mcpServer = s.buildMCPServer(hooks)

	token, err := generateToken()
	if err != nil {
		return err
	}
	s.token = token

	sseServer := server.NewSSEServer(s.mcpServer, server.WithSessionIDGenerator(func(_ context.Context, r *http.Request) (string, error) {
		id := uuid.New().String()
		s.sseSessions.Store(id, struct{}{})
		if v := r.Header.Get(SandboxHeader); v != "" {
			mode, err := agents.ParseSandboxMode(v)
			if err != nil {
				s.sseSessions.Delete(id) // no session was made
				return "", err           // authMiddleware already rejected this; fail closed anyway
			}
			s.sessionCeil.Store(id, mode)
		}
		return id, nil
	}))
	s.sseServer = sseServer

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.listener = ln

	httpServer := &http.Server{
		Handler: s.authMiddleware(sseServer),
	}
	s.httpServer = httpServer

	s.logger.Info("MCP server starting", "addr", ln.Addr().String())

	// Capture httpServer/ln as locals so the serve goroutine never reads the
	// struct fields that Stop() nils out concurrently (avoids a nil-pointer race
	// when Start and Stop happen in quick succession, e.g. an MCP restart on a
	// live mode flip).
	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("MCP server error", "error", err)
		}
	}()

	return nil
}

// ServeStdio serves this Server's tool registry over stdio (JSON-RPC framed
// on stdin/stdout, one message per line), reading from in and writing to
// out, until ctx is cancelled or in hits EOF. Used by `slaw mcp serve
// --transport stdio` (cmd/mcp.go), which owns ctx (cancelled on
// SIGINT/SIGTERM) so it can checkpoint + Save + Close the container exactly
// once, after this call returns, regardless of which of the three lifecycle
// events (EOF, SIGINT, SIGTERM) caused it to return.
//
// Deliberately uses the lower-level StdioServer.Listen rather than the
// package-level server.ServeStdio convenience function: that function
// installs its OWN signal.Notify(SIGTERM, SIGINT) handling on a context it
// creates internally, which would run concurrently with (and race) the
// caller's own signal handling. Listen takes reader/writer/ctx directly, so
// cmd/mcp.go is the single place that owns process lifecycle.
//
// Studio never calls this — it only ever uses SSE (Start) — so stdio has no
// token/Host/Origin gating: stdin/stdout are inherently local to whatever
// process spawned this one (e.g. Claude Desktop), there is no HTTP surface
// for a browser to reach.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	mcpServer := s.buildMCPServer(nil)
	stdioServer := server.NewStdioServer(mcpServer)
	errLogger := slog.NewLogLogger(s.logger.Handler(), slog.LevelError)
	stdioServer.SetErrorLogger(errLogger)
	return stdioServer.Listen(ctx, in, out)
}

// stopTimeout bounds how long Stop waits for in-flight requests to drain
// gracefully before forcing the listener closed. Review fix: an earlier
// version called httpServer.Shutdown(context.Background()) with no
// deadline at all — as long as a single SSE client stayed connected (a
// long-lived GET /sse, by design never returning on its own), Shutdown
// never returned, so `slaw mcp serve --transport sse` hung on SIGINT for as
// long as that client stayed connected (observed 20s+ live), and the final
// Save never ran.
const stopTimeout = 2 * time.Second

// Stop gracefully shuts down the HTTP server, bounded by stopTimeout so a
// still-connected client can never block it indefinitely.
func (s *Server) Stop() error {
	if s.httpServer == nil {
		return nil
	}
	s.logger.Info("MCP server stopping")

	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()

	// Close every active SSE session FIRST: sseServer.Shutdown closes each
	// session's done channel, which is what actually unblocks a long-held
	// GET /sse handler goroutine — httpServer.Shutdown alone has nothing
	// that tells a handler mid-write to stop; it only waits for handlers to
	// return on their own and refuses new connections in the meantime.
	if s.sseServer != nil {
		if err := s.sseServer.Shutdown(ctx); err != nil {
			s.logger.Warn("MCP SSE session shutdown error", "error", err)
		}
	}

	shutdownErr := s.httpServer.Shutdown(ctx)
	// Always follow with Close(), even on a clean Shutdown: Close is a safe,
	// idempotent no-op once every connection already closed on its own, and
	// is what actually terminates the listener/any connection Shutdown's
	// stopTimeout wasn't enough to drain gracefully (Shutdown alone would
	// otherwise leave a stubborn connection open past its own deadline).
	closeErr := s.httpServer.Close()

	s.httpServer = nil
	s.listener = nil
	s.mcpServer = nil
	s.sseServer = nil

	if shutdownErr != nil && !errors.Is(shutdownErr, context.DeadlineExceeded) {
		return shutdownErr
	}
	return closeErr
}

// Addr returns the bound listener address, or empty string if not running.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Token returns the current SSE bearer token (empty before the first
// Start, or after Stop). Studio's MCP status UI surfaces this alongside
// Addr() so the user can copy both into an external MCP client config.
func (s *Server) Token() string {
	return s.token
}

// authMiddleware gates every SSE request behind three checks, in order:
//  1. no browser Origin header — a legitimate MCP client (Claude Desktop, a
//     CLI, curl) never sets Origin; only a website running in a browser tab does,
//     via fetch/XHR/EventSource. Rejecting any request that carries one at
//     all (regardless of value) blocks the CSRF/DNS-rebinding attack a
//     malicious web page would otherwise be able to mount against a
//     no-auth localhost server, without needing an origin allowlist that
//     would need maintaining.
//  2. a loopback Host header — defends against DNS rebinding, where an
//     attacker-controlled DNS name resolves to 127.0.0.1 so the browser's
//     same-origin checks see a "same origin" request even though Origin (1)
//     already denies it; this is defense in depth for the case some future
//     change relaxes (1).
//  3. the bearer token — the actual credential; without it, even a
//     same-machine non-browser process (e.g. another local user's script)
//     can't call tools it wasn't handed the token for.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			s.logger.Warn("MCP request rejected: browser Origin header present", "origin", origin, "path", r.URL.Path)
			http.Error(w, "forbidden: browser requests are not permitted", http.StatusForbidden)
			return
		}
		if !isLoopbackHost(r.Host) {
			s.logger.Warn("MCP request rejected: non-loopback Host header", "host", r.Host, "path", r.URL.Path)
			http.Error(w, "forbidden: non-loopback Host", http.StatusForbidden)
			return
		}
		if !s.checkToken(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="slaw-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if v := r.Header.Get(SandboxHeader); v != "" {
			if _, err := agents.ParseSandboxMode(v); err != nil {
				http.Error(w, "bad request: unknown "+SandboxHeader, http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkToken reports whether r carries the current session's bearer token,
// either as `Authorization: Bearer <token>` (every request, and the ONLY
// accepted form for a POST) or, for a GET only, as a `?token=` query
// parameter. Comparison is constant-time to avoid a timing side channel on
// the token itself.
//
// The GET-only restriction (review fix, round 2 — finding 9) is deliberate:
// mcp-go's SSE server (server.NewSSEServer, wrapped by authMiddleware) opens
// with a GET to /sse — which a raw browser EventSource can issue but can
// never attach a custom Authorization header to, hence the query-param
// fallback existing at all — and then sends that connection an "endpoint"
// SSE event naming the URL a client POSTs actual JSON-RPC tool calls to.
// That URL is generated entirely inside mcp-go and never carries `?token=`,
// so honoring the query param on POST as well would only invite a client to
// copy the token into a URL (logs, history, a Referer header) for a request
// shape that can always set a proper header instead. Every real MCP client
// (Claude Desktop, a Go/Python SDK, curl) issues POSTs with arbitrary
// headers, so this costs nothing in practice; only a hand-rolled
// EventSource-based GET needs the fallback at all, and this is documented in
// docs/reference/mcp.md.
func (s *Server) checkToken(r *http.Request) bool {
	if s.token == "" {
		// Should not happen post-Start, but fail closed rather than open.
		return false
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		candidate := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(s.token)) == 1 {
			return true
		}
	}
	if r.Method == http.MethodGet {
		if q := r.URL.Query().Get("token"); q != "" {
			if subtle.ConstantTimeCompare([]byte(q), []byte(s.token)) == 1 {
				return true
			}
		}
	}
	return false
}

// isLoopbackHost reports whether an HTTP request's Host header names the
// loopback interface (with or without a port). Deliberately strict — an
// empty or malformed Host is treated as NOT loopback (fail closed).
func isLoopbackHost(host string) bool {
	h := host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		h = hostOnly
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]") // strip IPv6 brackets if SplitHostPort didn't
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// withSessionCeiling binds the sandbox ceiling the session's client asked for
// (a proxy's --sandbox) to ctx, tighten-only.
func (s *Server) withSessionCeiling(ctx context.Context) context.Context {
	if sess := server.ClientSessionFromContext(ctx); sess != nil {
		if v, ok := s.sessionCeil.Load(sess.SessionID()); ok {
			if mode, ok := v.(agents.SandboxMode); ok {
				return agents.WithSandboxCeiling(ctx, mode)
			}
		}
	}
	return ctx
}

// withAgentSession binds the session that is calling to ctx, for the receipt of
// any read the call makes: the SSE transport session id, or, on stdio, the one
// random id of this process.
func (s *Server) withAgentSession(ctx context.Context) context.Context {
	if sess := server.ClientSessionFromContext(ctx); sess != nil {
		if _, ok := s.sseSessions.Load(sess.SessionID()); ok {
			return receipts.WithAgentSession(ctx, sess.SessionID())
		}
	}
	return receipts.WithAgentSession(ctx, s.stdioSession)
}

// makeToolHandler returns an MCP tool handler that dispatches to the agents handler.
func (s *Server) makeToolHandler(toolName string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// A chat-only tool (present_data) is not registered here, but the handler
		// behind this server also serves Studio's chat: refuse it by name too.
		if !agents.IsModeVisible(toolName, agents.ModeMCP) {
			return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %q", toolName)), nil
		}
		params := make(map[string]any)
		if args := request.GetArguments(); args != nil {
			for k, v := range args {
				params[k] = v
			}
		}

		req := &agents.Request{
			JSONRPC: "2.0",
			ID:      request.Params.Name,
			Method:  toolName,
			Params:  params,
		}

		resp := s.handler.Handle(s.withAgentSession(s.withSessionCeiling(ctx)), req)

		if resp.Error != nil {
			return errorResult(resp.Error), nil
		}

		data, err := json.Marshal(resp.Result)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDefToMCPTool converts an agents.ToolDef to an mcp.Tool. The input
// schema is built by agents.ParamsToJSONSchema, the single shared helper
// internal/llm's fantasy tool wrapper also uses, so an MCP client and the
// Studio wizard see byte-identical schemas (#63 review fix).
func toolDefToMCPTool(td agents.ToolDef) mcp.Tool {
	td = agents.ForMCP(td) // the wording for a client with no Studio window
	opts := []mcp.ToolOption{
		mcp.WithDescription(td.Description),
	}

	if len(td.Params) > 0 {
		properties, required := agents.ParamsToJSONSchema(td.Params)

		opts = append(opts, func(t *mcp.Tool) {
			if t.InputSchema.Properties == nil {
				t.InputSchema.Properties = make(map[string]any)
			}
			for k, v := range properties {
				t.InputSchema.Properties[k] = v
			}
			t.InputSchema.Required = required
		})
	}

	opts = append(opts, mcp.WithToolAnnotation(annotationsFor(td)))
	return mcp.NewTool(td.Name, opts...)
}

// errorResult is the tool error for a refused call. Its text is the message; a
// refusal that carries data (a valid request that only lacks an approval) also
// returns it as structuredContent so a client can act on it without parsing
// the sentence, and as a second text block (the MCP spec's advice) for clients
// that show the model only the text of an error.
func errorResult(e *agents.Error) *mcp.CallToolResult {
	res := mcp.NewToolResultError(e.Message)
	if e.Data != nil {
		res.StructuredContent = e.Data
		if b, err := jsonenc.Encode(e.Data, ""); err == nil {
			res.Content = append(res.Content, mcp.NewTextContent(strings.TrimSpace(string(b))))
		}
	}
	return res
}
