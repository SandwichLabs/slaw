package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

func callTool(t *testing.T, h *agentToolHandler, id, method string, params map[string]any, session string) *agents.Response {
	t.Helper()
	ctx := receipts.WithAgentSession(t.Context(), session)
	return h.Handle(ctx, &agents.Request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
}

// A chat conversation reads its own receipts from earlier turns (each turn builds
// its own handler), and never another conversation's or a person's.
func TestChatConversationReadsItsOwnReceiptsFromEarlierTurns(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec("CREATE TABLE sales (region TEXT, total BIGINT); INSERT INTO sales VALUES ('east', 1234567), ('west', 7654321)")
	require.NoError(t, err)
	var chat chatTurns
	conv := chat.conversation("")

	app.mu.RLock()
	turn1 := app.newAgentToolHandler(agents.ModeFull)
	turn2 := app.newAgentToolHandler(agents.ModeFull)
	app.mu.RUnlock()
	turn1.trackRuns("turn-1")

	resp := callTool(t, turn1, "call-1", "run_query", map[string]any{"sql": "SELECT region, total FROM sales ORDER BY region"}, conv)
	require.Nil(t, resp.Error)
	id, _ := resp.Result.(map[string]any)["receipt_id"].(string)
	require.NotEmpty(t, id)

	// The next turn, same conversation id (the panel sent it back).
	got := callTool(t, turn2, "call-2", "get_receipt", map[string]any{"id": id}, chat.conversation(conv))
	require.Nil(t, got.Error)
	b, err := json.Marshal(got.Result)
	require.NoError(t, err)
	assert.Contains(t, string(b), "SELECT region, total FROM sales ORDER BY region")
	assert.NotContains(t, string(b), "agent_session")
	assert.NotContains(t, string(b), conv)

	// A new chat, or an id the panel invented, is another conversation.
	for name, c := range map[string]string{"new chat": chat.conversation(""), "foreign id": chat.conversation("s-feedfacefeedfacefeedfacefeedface")} {
		resp := callTool(t, turn2, "call-3", "get_receipt", map[string]any{"id": id}, c)
		require.NotNil(t, resp.Error, name)
		assert.Equal(t, "no receipt with that id", resp.Error.Message, name)
	}

	// The cell dialog is its own session: it cannot read the chat's receipts.
	resp = callTool(t, turn2, "call-4", "get_receipt", map[string]any{"id": id}, taskSession("task-1-1"))
	require.NotNil(t, resp.Error)
	assert.Equal(t, "no receipt with that id", resp.Error.Message)

	// A person's receipt (the Query Explorer's run) is never returned to an agent.
	person, err := app.RunQuery(RunQueryRequest{SQL: "SELECT 1 AS a", TabID: "t1"})
	require.NoError(t, err)
	require.NotEmpty(t, person.ReceiptID)
	resp = callTool(t, turn2, "call-5", "get_receipt", map[string]any{"id": person.ReceiptID}, conv)
	require.NotNil(t, resp.Error)
	assert.Equal(t, "no receipt with that id", resp.Error.Message)
}

// The turn remembers each read, announces it, and marks the answer's numbers
// against that turn's rows only.
func TestChatTurnRunsAnnounceChipsAndMarkTheAnswer(t *testing.T) {
	app := newOpenApp(t)
	events := &eventLog{}
	app.emit = events.sink
	_, err := app.sess.DB().Exec("CREATE TABLE sales (region TEXT, total BIGINT); INSERT INTO sales VALUES ('east', 1234567), ('west', 7654321)")
	require.NoError(t, err)

	app.mu.RLock()
	turn := app.newAgentToolHandler(agents.ModeFull)
	app.mu.RUnlock()
	runs := turn.trackRuns("turn-9")
	resp := callTool(t, turn, "call-1", "run_query", map[string]any{"sql": "SELECT region, total FROM sales ORDER BY region"}, "conv")
	require.Nil(t, resp.Error)
	// Not a read, an error and a refused call add nothing.
	callTool(t, turn, "call-2", "list_connections", nil, "conv")
	callTool(t, turn, "call-3", "run_query", map[string]any{"sql": "DROP TABLE sales"}, "conv")
	assert.Equal(t, 1, events.count("agent:receipt"))

	marks, refs := runs.answerNumbers("East sold 1,234,567 (about 1.2M) of 8,888,888, and 55% came from the west.")
	require.Len(t, refs, 1)
	assert.Equal(t, "call-1", refs[0].CallID)
	assert.Equal(t, "run_query", refs[0].Tool)
	assert.Equal(t, 2, refs[0].Rows)
	byText := map[string]bool{}
	for _, m := range marks {
		byText[m.Text] = m.Matched
		if m.Matched {
			assert.Equal(t, refs[0].ReceiptID, m.ReceiptID)
		}
	}
	assert.Equal(t, map[string]bool{"1,234,567": true, "1.2M": true, "8,888,888": false, "55%": false}, byText)

	// Another turn has its own reads: this turn's values are not there.
	app.mu.RLock()
	next := app.newAgentToolHandler(agents.ModeFull)
	app.mu.RUnlock()
	nextRuns := next.trackRuns("turn-10")
	marks, refs = nextRuns.answerNumbers("East sold 1,234,567.")
	assert.Empty(t, refs)
	require.Len(t, marks, 1)
	assert.False(t, marks[0].Matched, "a value another turn showed is not matched")
}

// A read that showed rows but made no receipt (an unusable store) hides its
// values from the check, so the answer's numbers are never underlined as "not
// from a query"; the chips of the reads that did make one stay.
func TestTurnRuns_AnUnrecordedReadMakesNoNotFromAQueryMarks(t *testing.T) {
	read := map[string]any{"columns": []string{"total"}, "rows": [][]any{{int64(1234567)}}}

	var none turnRuns
	_, ok := none.add("c1", "run_query", read)
	assert.False(t, ok, "no receipt, no chip")
	marks, runs := none.answerNumbers("The total is 1234567 and the margin is 98765.")
	assert.Empty(t, marks)
	assert.Empty(t, runs)

	// A turn with no read at all still flags an invented number.
	var nothing turnRuns
	marks, _ = nothing.answerNumbers("The total is 1234567.")
	require.NotEmpty(t, marks)
	assert.False(t, marks[0].Matched)

	// A mixed turn keeps the tick of the recorded read and drops the underline.
	var mixed turnRuns
	_, ok = mixed.add("c1", "run_query", read)
	assert.False(t, ok)
	_, ok = mixed.add("c2", "run_query", map[string]any{"receipt_id": "r_1", "columns": []string{"n"}, "rows": [][]any{{int64(2468013)}}})
	require.True(t, ok)
	marks, runs = mixed.answerNumbers("It is 2468013, and 98765.")
	require.Len(t, marks, 1)
	assert.True(t, marks[0].Matched)
	assert.Equal(t, "r_1", marks[0].ReceiptID)
	assert.Len(t, runs, 1)
}

// A workspace whose .slaw-local cannot be written (a read-only folder) keeps no
// receipts, and a run still returns its rows, with the note that says why.
func TestRunQuery_ARunStillReturnsItsResultWhenTheReceiptsFolderIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	root := newWorkspaceDir(t, "Read Only Receipts")
	app := newBareApp(t)
	res, err := app.OpenWorkspace(root)
	require.NoError(t, err)
	require.Equal(t, "opened", res.Status)
	// The workspace keeps its lock in .slaw-local, so it is made read-only after
	// the open and before the first receipt is asked for.
	local := filepath.Join(root, workspace.LocalDir)
	require.NoError(t, os.RemoveAll(filepath.Join(local, "receipts")))
	require.NoError(t, os.Chmod(local, 0o500))
	t.Cleanup(func() { _ = os.Chmod(local, 0o700) })

	got, err := app.RunQuery(RunQueryRequest{SQL: "SELECT 41 + 1 AS answer", TabID: "t1"})
	require.NoError(t, err)
	require.Len(t, got.Rows, 1)
	assert.Empty(t, got.ReceiptID)
	assert.Equal(t, string(receipts.NoteUnavailable), got.ReceiptNote)
	_, statErr := os.Stat(filepath.Join(local, "receipts"))
	assert.Error(t, statErr, "nothing was written")
}
