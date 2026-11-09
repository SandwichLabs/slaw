package main

import (
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

// The conversation is what the panel's history says: the first turn starts one,
// the next turns continue it, and closing the workspace ends it.
func TestChatConversationIDFollowsTheHistory(t *testing.T) {
	var c chatTurns
	first := c.conversation(0)
	require.NotEmpty(t, first)
	assert.Equal(t, first, c.conversation(2), "a turn with history continues the conversation")
	assert.Equal(t, first, c.conversation(4))
	second := c.conversation(0)
	assert.NotEqual(t, first, second, "an empty history is a new conversation")
	c.endConversation()
	assert.NotEqual(t, second, c.conversation(6), "a closed workspace ends it even if the panel kept its history")
}
