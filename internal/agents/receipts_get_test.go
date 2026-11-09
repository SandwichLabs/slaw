package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

func getReceipt(fx *policyFixture, ctx context.Context, id string) *Response {
	fx.t.Helper()
	return fx.callCtx(ctx, "get_receipt", map[string]any{"id": id})
}

func requireNoSuchReceipt(t *testing.T, resp *Response) {
	t.Helper()
	require.NotNil(t, resp.Error, "expected a refusal")
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)
	assert.Equal(t, "no receipt with that id", resp.Error.Message)
}

func TestGetReceipt_ReturnsTheCallersOwnReceiptWithoutItsSession(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	recordTo(fx.h)
	ctx := receipts.WithAgentSession(context.Background(), "conv-1")
	id := receiptID(t, fx.callCtx(ctx, "run_query", map[string]any{"sql": "SELECT name FROM users WHERE id = $id", "params": map[string]any{"id": 1}}))
	require.NotEmpty(t, id)

	resp := getReceipt(fx, ctx, id)
	require.Nil(t, resp.Error, errMsg(resp))
	b, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, id, out["id"])
	assert.Equal(t, "SELECT name FROM users WHERE id = $id", out["sql"])
	assert.Equal(t, "SELECT name FROM users WHERE id = $id", out["run_sql"])
	assert.NotEmpty(t, out["params"])
	assert.NotEmpty(t, out["connections"])
	assert.NotEmpty(t, out["columns"])
	assert.NotEmpty(t, out["result_hmac"])
	assert.NotEmpty(t, out["at"])
	assert.Equal(t, map[string]any{"returned": float64(1), "total": float64(1), "truncated": false}, out["rows"])
	assert.NotContains(t, string(b), "agent_session", "the session is never in the answer")
	assert.NotContains(t, string(b), "conv-1")
	assert.NotContains(t, string(b), "Alice", "a receipt never holds a value")
}

func TestGetReceipt_RefusesEverythingElseLikeAMissingID(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	mine := receipts.WithAgentSession(context.Background(), "conv-1")
	theirs := receipts.WithAgentSession(context.Background(), "conv-2")
	id := receiptID(t, fx.callCtx(mine, "run_query", map[string]any{"sql": "SELECT 1 AS a"}))
	require.NotEmpty(t, id)

	// A person's receipt (a render-time read has no agent actor).
	rs, _, err := fx.h.RunReadOnlySQLIn(context.Background(), "", "SELECT 2 AS b", nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, rs.ReceiptID)
	require.Len(t, m.All(), 2)

	missing := "r_0123456789abcdef0123"
	for name, c := range map[string]struct {
		ctx context.Context
		id  string
	}{
		"another session":                        {theirs, id},
		"no session":                             {context.Background(), id},
		"a person receipt":                       {mine, rs.ReceiptID},
		"a person receipt, whatever the session": {theirs, rs.ReceiptID},
		"a missing id":                           {mine, missing},
		"a malformed id":                         {mine, "../../key"},
		"an empty id":                            {mine, ""},
		"upper case id":                          {mine, "R_" + id[2:]},
	} {
		t.Run(name, func(t *testing.T) { requireNoSuchReceipt(t, getReceipt(fx, c.ctx, c.id)) })
	}
	// The same words, code and shape as a missing id.
	assert.Equal(t, getReceipt(fx, mine, missing).Error, getReceipt(fx, mine, rs.ReceiptID).Error)
}

func TestGetReceipt_NoRecorderIsAMissingID(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	ctx := receipts.WithAgentSession(context.Background(), "conv-1")
	requireNoSuchReceipt(t, getReceipt(fx, ctx, "r_0123456789abcdef0123"))
}

func TestGetReceipt_ASessionReadsItsEarlierTurnsInEveryTier(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			fx := newPolicyFixture(t, mode)
			recordTo(fx.h)
			// Two turns of one conversation are two contexts with one session id.
			turn1 := receipts.WithAgentSession(context.Background(), "conv-9")
			turn2 := receipts.WithAgentSession(context.Background(), "conv-9")
			id := receiptID(t, fx.callCtx(turn1, "run_query", map[string]any{"sql": "SELECT 1 AS a"}))
			resp := getReceipt(fx, turn2, id)
			require.Nil(t, resp.Error, errMsg(resp))
		})
	}
}

func TestGetReceipt_IsAnIntrospectToolForEveryModeWithAMCPDescription(t *testing.T) {
	var def *ToolDef
	for i := range Tools {
		if Tools[i].Name == "get_receipt" {
			def = &Tools[i]
		}
	}
	require.NotNil(t, def)
	assert.Equal(t, "introspect", def.Category)
	assert.Empty(t, def.Modes)
	assert.NotEmpty(t, def.MCPDescription)
	assert.NotEqual(t, def.Description, def.MCPDescription)
	assert.NotContains(t, def.Description+def.MCPDescription, "agent_session")
	for _, mode := range []string{ModeChat, ModeMCP} {
		var has bool
		for _, td := range ListToolsForMode(mode) {
			has = has || td.Name == "get_receipt"
		}
		assert.True(t, has, mode)
	}
}
