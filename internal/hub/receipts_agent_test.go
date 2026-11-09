//go:build unix

package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	slawmcp "github.com/SandwichLabs/slaw/internal/mcp"
)

// A receipt minted by one MCP SSE session is refused to another session on the
// same handler, in the same words as an id that does not exist; the session that
// made it reads it back, without its own session id in the answer (R5).
func TestReceipts_GetReceiptIsOwnSessionOnlyOverSSE(t *testing.T) {
	ws := newWorkspace(t)
	o, err := Open(t.Context(), ws.Root(), OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	t.Cleanup(o.Close)
	h, _, err := NewAgentHandler(o.WS, o.Sess, "", quietLogger())
	require.NoError(t, err)

	srv := slawmcp.New(h, quietLogger())
	require.NoError(t, srv.Start("127.0.0.1:0"))
	t.Cleanup(func() { _ = srv.Stop() })

	connect := func() *client.Client {
		c, err := client.NewSSEMCPClient("http://"+srv.Addr()+"/sse", transport.WithHeaders(map[string]string{"Authorization": "Bearer " + srv.Token()}))
		require.NoError(t, err)
		require.NoError(t, c.Start(context.Background()))
		t.Cleanup(func() { _ = c.Close() })
		init := mcp.InitializeRequest{}
		init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		init.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0"}
		_, err = c.Initialize(context.Background(), init)
		require.NoError(t, err)
		return c
	}
	call := func(c *client.Client, tool string, args map[string]any) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		req := mcp.CallToolRequest{}
		req.Params.Name = tool
		req.Params.Arguments = args
		res, err := c.CallTool(ctx, req)
		require.NoError(t, err)
		require.NotEmpty(t, res.Content)
		txt, ok := res.Content[0].(mcp.TextContent)
		require.True(t, ok)
		return txt.Text, res.IsError
	}

	// tools/list offers get_receipt.
	a, b := connect(), connect()
	tools, err := a.ListTools(context.Background(), mcp.ListToolsRequest{})
	require.NoError(t, err)
	found := false
	for _, tl := range tools.Tools {
		found = found || tl.Name == "get_receipt"
	}
	assert.True(t, found, "get_receipt is in tools/list")

	text, isErr := call(a, "run_query", map[string]any{"sql": "SELECT 41 + 1 AS answer"})
	require.False(t, isErr, text)
	var ran map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &ran))
	id, _ := ran["receipt_id"].(string)
	require.NotEmpty(t, id)

	own, isErr := call(a, "get_receipt", map[string]any{"id": id})
	require.False(t, isErr, own)
	assert.Contains(t, own, id)
	assert.Contains(t, own, "SELECT 41 + 1 AS answer")
	assert.NotContains(t, own, "agent_session")

	other, otherErr := call(b, "get_receipt", map[string]any{"id": id})
	missing, missingErr := call(b, "get_receipt", map[string]any{"id": "r_0123456789abcdef0123"})
	assert.True(t, otherErr)
	assert.True(t, missingErr)
	assert.Equal(t, missing, other, "another session's receipt reads exactly like a missing id")
	assert.Contains(t, other, "no receipt with that id")
	assert.NotContains(t, other, "SELECT 41")

	// A reconnect is a new session: it cannot read the earlier session's receipts.
	require.NoError(t, a.Close())
	a2 := connect()
	again, againErr := call(a2, "get_receipt", map[string]any{"id": id})
	assert.True(t, againErr)
	assert.Equal(t, missing, again)
}
