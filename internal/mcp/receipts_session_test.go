package mcp

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

// sessionRecorder is a Handler that records the agent session each call carries.
type sessionRecorder struct {
	mu       sync.Mutex
	sessions []string
}

func (s *sessionRecorder) Handle(ctx context.Context, req *agents.Request) *agents.Response {
	s.mu.Lock()
	s.sessions = append(s.sessions, receipts.AgentSessionFrom(ctx))
	s.mu.Unlock()
	return agents.NewResponse(req.ID, map[string]any{"ok": true})
}

func (s *sessionRecorder) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sessions...)
}

func callRunQuery(t *testing.T, c *client.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0"}
	_, err := c.Initialize(ctx, init)
	require.NoError(t, err)
	req := mcp.CallToolRequest{}
	req.Params.Name = "run_query"
	req.Params.Arguments = map[string]any{"sql": "SELECT 1"}
	res, err := c.CallTool(ctx, req)
	require.NoError(t, err)
	require.False(t, res.IsError)
}

// Two SSE clients on one server (one handler) are two agent sessions: a
// receipt names the session that asked, so one client cannot be mistaken for
// another, and a client's calls share its session.
func TestAgentSession_TwoSSEClientsAreTwoSessions(t *testing.T) {
	rec := &sessionRecorder{}
	srv := New(rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, srv.Start("127.0.0.1:0"))
	t.Cleanup(func() { _ = srv.Stop() })

	connect := func() *client.Client {
		c, err := client.NewSSEMCPClient("http://"+srv.Addr()+"/sse", transport.WithHeaders(map[string]string{"Authorization": "Bearer " + srv.Token()}))
		require.NoError(t, err)
		require.NoError(t, c.Start(context.Background()))
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	a, b := connect(), connect()
	callRunQuery(t, a)
	callRunQuery(t, b)

	seen := rec.seen()
	require.Len(t, seen, 2)
	assert.NotEmpty(t, seen[0])
	assert.NotEmpty(t, seen[1])
	assert.NotEqual(t, seen[0], seen[1], "two SSE sessions on one handler have two ids")
	assert.NotEqual(t, srv.stdioSession, seen[0])

	// The same client keeps its session across calls.
	req := mcp.CallToolRequest{}
	req.Params.Name = "run_query"
	req.Params.Arguments = map[string]any{"sql": "SELECT 2"}
	_, err := a.CallTool(context.Background(), req)
	require.NoError(t, err)
	seen = rec.seen()
	require.Len(t, seen, 3)
	assert.Equal(t, seen[0], seen[2])
}

// On stdio there is no transport session id: one random id serves the process.
func TestAgentSession_StdioHasOneIDPerProcess(t *testing.T) {
	srv := New(&sessionRecorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	other := New(&sessionRecorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.NotEmpty(t, srv.stdioSession)
	assert.NotEqual(t, srv.stdioSession, other.stdioSession)
	// With no session on the context the call is the process's own.
	assert.Equal(t, srv.stdioSession, receipts.AgentSessionFrom(srv.withAgentSession(context.Background())))
}
