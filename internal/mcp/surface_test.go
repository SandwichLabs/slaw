package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
)

// okHandler answers every call as a success, so a refusal in a test can only
// have come from the MCP layer.
type okHandler struct{}

func (okHandler) Handle(_ context.Context, req *agents.Request) *agents.Response {
	return agents.NewResponse(req.ID, map[string]any{"called": req.Method})
}

func callRequest(name string) mcp.CallToolRequest {
	var r mcp.CallToolRequest
	r.Params.Name = name
	return r
}

// reply is the response with the given JSON-RPC id. The stdio server answers
// requests concurrently, so a fast refusal can arrive before the initialize
// response: never pick a response by position.
func reply(t *testing.T, res []map[string]any, id float64) map[string]any {
	t.Helper()
	for _, r := range res {
		if got, ok := r["id"].(float64); ok && got == id {
			return r
		}
	}
	t.Fatalf("no response with id %v in %v", id, res)
	return nil
}

func listNames(t *testing.T, s *Server) (names []string, byName map[string]map[string]any) {
	t.Helper()
	res := stdioRoundTrip(t, s, []string{initLine, notifLine, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`}, 2)
	byName = map[string]map[string]any{}
	raws, _ := asMap(t, reply(t, res, 2)["result"])["tools"].([]any)
	for _, raw := range raws {
		tl := asMap(t, raw)
		name, _ := tl["name"].(string)
		names = append(names, name)
		byName[name] = tl
	}
	return names, byName
}

// present_data renders in Studio's chat only: MCP does not list it and refuses a
// call to it as an unknown tool, while the chat registry still has it (#221).
func TestPresentData_IsChatOnly(t *testing.T) {
	names, _ := listNames(t, New(okHandler{}, slog.New(slog.DiscardHandler)))
	assert.NotContains(t, names, "present_data")
	assert.Contains(t, names, "create_dashboard", "a dashboard cell is a notebook write, which MCP can make")

	res := stdioRoundTrip(t, New(okHandler{}, slog.New(slog.DiscardHandler)), []string{initLine, notifLine,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"present_data","arguments":{"page":{"title":"x","components":[]}}}}`}, 2)
	require.NotNil(t, reply(t, res, 2)["error"], "an unregistered tool is a protocol error")
	assert.Contains(t, fmt.Sprint(reply(t, res, 2)["error"]), "not found")

	// The chat surface (and the handler's default mode) still has it.
	var chat []string
	for _, td := range agents.ListToolsForMode(agents.ModeChat) {
		chat = append(chat, td.Name)
	}
	assert.Contains(t, chat, "present_data")
	assert.True(t, agents.IsModeVisible("present_data", ""), "Handle's default mode serves chat")
	assert.False(t, agents.IsModeVisible("present_data", agents.ModeMCP))
	assert.NotNil(t, agents.GetTool("present_data"))
	// Studio's chat keeps its wording.
	assert.Contains(t, agents.GetTool("present_data").Description, "inline in the current reply")
}

// The makeToolHandler gate is the second line: even if a chat-only tool were
// registered on the server, the call is refused by name.
func TestChatOnlyTool_RefusedByTheCallHandler(t *testing.T) {
	s := New(okHandler{}, slog.New(slog.DiscardHandler))
	res, err := s.makeToolHandler("present_data")(context.Background(), callRequest("present_data"))
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

// No MCP description or parameter text names the notebook "open in Studio": a
// headless client has no such thing.
func TestMCPDescriptions_NoStudioOnlyWording(t *testing.T) {
	_, byName := listNames(t, New(okHandler{}, slog.New(slog.DiscardHandler)))
	require.NotEmpty(t, byName)
	for name, tl := range byName {
		blob := fmt.Sprint(tl)
		assert.NotContains(t, blob, "open in Studio", name)
		assert.NotContains(t, blob, "currently open", name)
	}
	for _, name := range []string{"add_query_cell", "add_markdown_cell", "create_dashboard"} {
		assert.Contains(t, fmt.Sprint(byName[name]["description"]), "new notebook", name)
	}
	// The in-app agent keeps the Studio wording.
	assert.Contains(t, agents.GetTool("add_query_cell").Description, "currently open in Studio")
}

// tools/list follows the session's tier: read-only lists no write tool, the
// higher tiers list them, and a call to an unlisted tool is still the handler's
// to refuse (nothing here changes dispatch).
func TestToolsList_FollowsTheTier(t *testing.T) {
	tierServer := func(mode agents.SandboxMode) *Server {
		return New(okHandler{}, slog.New(slog.DiscardHandler),
			WithTier(func(context.Context) agents.SandboxMode { return mode }))
	}
	writeTools := map[string]string{ // tool -> lowest tier that lists it
		"create_record": "read-write", "bulk_insert": "read-write", "save_query": "read-write",
		"write_artifact": "read-write", "add_query_cell": "read-write", "create_or_update_skill": "read-write",
		"create_table": "full", "drop_table": "full", "add_column": "full", "import_csv": "full",
	}
	rank := map[string]int{"read-only": 0, "read-write": 1, "full": 2}
	for _, mode := range []agents.SandboxMode{agents.ModeReadOnly, agents.ModeReadWrite, agents.ModeFull} {
		names, _ := listNames(t, tierServer(mode))
		for tool, lowest := range writeTools {
			if rank[string(mode)] >= rank[lowest] {
				assert.Contains(t, names, tool, "%s lists %s", mode, tool)
			} else {
				assert.NotContains(t, names, tool, "%s lists %s", mode, tool)
			}
		}
		// Reads and run_query (classified per statement) are listed at every tier.
		for _, tool := range []string{"list_connections", "describe_table", "run_query", "get_notebook", "read_skill"} {
			assert.Contains(t, names, tool, "%s lists %s", mode, tool)
		}
	}

	// Calls are not shaped by the listing: an unlisted write tool reaches the
	// handler, which owns the tier refusal.
	res := stdioRoundTrip(t, tierServer(agents.ModeReadOnly), []string{initLine, notifLine,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_table","arguments":{}}}`}, 2)
	require.Nil(t, reply(t, res, 2)["error"])
	assert.Contains(t, fmt.Sprint(reply(t, res, 2)["result"]), "create_table", "the call was dispatched to the handler")

	// Without a tier source the full registry is listed, as before.
	names, _ := listNames(t, New(okHandler{}, slog.New(slog.DiscardHandler)))
	assert.Contains(t, names, "create_table")
}

// With the real handler a read-only tier refuses a write tool called while it is
// unlisted, with the message that says how to raise the tier.
func TestToolsList_UnlistedWriteToolIsStillGated(t *testing.T) {
	h := agents.NewHandler(&mockAccess{}, &mockEngine{}, &agents.SandboxConfig{Mode: agents.ModeReadOnly, MaxQueryRows: 10}, slog.New(slog.DiscardHandler))
	s := New(h, slog.New(slog.DiscardHandler), WithTier(func(ctx context.Context) agents.SandboxMode { return h.Policy(ctx).Mode }))
	names, _ := listNames(t, s)
	assert.NotContains(t, names, "drop_table")
	res := stdioRoundTrip(t, s, []string{initLine, notifLine,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"drop_table","arguments":{"name":"t"}}}`}, 2)
	result := asMap(t, reply(t, res, 2)["result"])
	assert.Equal(t, true, result["isError"])
	assert.True(t, strings.Contains(fmt.Sprint(result["content"]), "not allowed in read-only mode"), fmt.Sprint(result))
}

// The tier a session is at when it lists follows the handler live, and a
// ceiling bound to the session (a proxy's --sandbox) tightens it.
func TestToolsList_SessionCeilingTightens(t *testing.T) {
	h := agents.NewHandler(&mockAccess{}, &mockEngine{}, &agents.SandboxConfig{Mode: agents.ModeFull, MaxQueryRows: 10}, slog.New(slog.DiscardHandler))
	s := New(h, slog.New(slog.DiscardHandler), WithTier(func(ctx context.Context) agents.SandboxMode { return h.Policy(ctx).Mode }))
	names, _ := listNames(t, s)
	assert.Contains(t, names, "drop_table")

	h.SetFilePolicy(agents.ModeReadOnly, true) // Studio: the person lowered the tier
	names, _ = listNames(t, s)
	assert.NotContains(t, names, "drop_table")
}
