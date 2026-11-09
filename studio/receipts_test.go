package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

// The Query Explorer's run and a live A2UI binding each forward the run's
// receipt id on their wire type; the ids name receipts in the workspace's store.
func TestReceipts_QueryResultsCarryTheReceiptID(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec("CREATE TABLE r (id INTEGER, name TEXT); INSERT INTO r VALUES (1, 'a'), (2, 'b'), (3, 'c')")
	require.NoError(t, err)
	store := app.ws.Receipts().Store()
	require.NotNil(t, store)

	res, err := app.RunQuery(RunQueryRequest{SQL: "SELECT id, name FROM r ORDER BY id", TabID: "t1"})
	require.NoError(t, err)
	require.NotEmpty(t, res.ReceiptID)
	got, err := store.Get(res.ReceiptID)
	require.NoError(t, err)
	assert.Equal(t, receipts.SurfaceQueryEditor, got.Surface)
	assert.Equal(t, receipts.ActorPerson, got.Actor)
	assert.Equal(t, receipts.VerdictUnchecked, got.Verdict)
	assert.Equal(t, "SELECT id, name FROM r ORDER BY id", got.SQL)
	assert.EqualValues(t, 3, got.Rows.Returned)
	assert.EqualValues(t, 3, got.Rows.Total)
	assert.False(t, got.Rows.Truncated)

	// A write is not a read: no receipt, and the result still comes back.
	res, err = app.RunQuery(RunQueryRequest{SQL: "INSERT INTO r VALUES (4, 'd')", TabID: "t1"})
	require.NoError(t, err)
	assert.Empty(t, res.ReceiptID)

	// A live binding (an A2UI component or a notebook cell) is a render.
	a, err := app.RunA2UIQuery("SELECT id FROM r ORDER BY id")
	require.NoError(t, err)
	require.NotEmpty(t, a.ReceiptID)
	got, err = store.Get(a.ReceiptID)
	require.NoError(t, err)
	assert.Equal(t, receipts.SurfaceRender, got.Surface)
	assert.Equal(t, receipts.VerdictRead, got.Verdict)
	assert.Equal(t, receipts.Source{}, got.Source)
	assert.EqualValues(t, 4, got.Rows.Returned)
}

// Go mints the conversation id and takes back only an id it issued in this
// workspace: an unknown or foreign id starts a new conversation, an empty one
// (a new chat) does too, and closing the workspace forgets every id.
func TestChatConversationIDRoundTripsAndOnlyIssuedIDsAreAccepted(t *testing.T) {
	var c chatTurns
	first := c.conversation("")
	require.NotEmpty(t, first)
	assert.Equal(t, first, c.conversation(first), "the id the panel sends back continues the conversation")
	assert.Equal(t, first, c.conversation(first))

	foreign := "s-0123456789abcdef0123456789abcdef"
	got := c.conversation(foreign)
	assert.NotEqual(t, foreign, got, "an id Go did not issue starts a new conversation")
	assert.NotEqual(t, first, got)
	assert.NotEqual(t, got, c.conversation(foreign), "and is not remembered as issued")

	other := c.conversation("")
	assert.NotEqual(t, first, other, "a new chat clears the id the panel sends")
	assert.Equal(t, first, c.conversation(first), "an earlier conversation is still the person's")

	c.endConversation()
	assert.NotEqual(t, first, c.conversation(first), "a closed workspace forgets every id it issued")
}

func TestChatConversationIDsKeptAreBounded(t *testing.T) {
	var c chatTurns
	first := c.conversation("")
	for i := 0; i < maxIssuedConversations; i++ {
		c.conversation("")
	}
	assert.NotEqual(t, first, c.conversation(first), "the oldest id has been dropped")
}

// The AI cell dialog is one session: its follow-ups all send its one taskId.
func TestTaskSessionIsOneStablePrefixedIDPerDialog(t *testing.T) {
	a := taskSession("task-1-1700000000000")
	assert.Equal(t, a, taskSession("task-1-1700000000000"), "a follow-up of the same dialog is the same session")
	assert.NotEqual(t, a, taskSession("task-2-1700000000000"), "another dialog is another session")
	assert.Equal(t, "task:task-1-1700000000000", a)
	var c chatTurns
	assert.NotEqual(t, a, c.conversation(a), "a task id is never a chat conversation")
	long := taskSession(strings.Repeat("x", 500))
	assert.LessOrEqual(t, len(long), receipts.MaxSessionBytes, "a long id still fits a receipt")
	assert.Equal(t, long, taskSession(strings.Repeat("x", 500)))
}
