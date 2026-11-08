package agents

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/queries"
)

// run_query's optional connection argument pins the catalog unqualified names
// resolve against (#352), through the same gates as an unpinned run.

func runIn(fx *policyFixture, conn, q string) *Response {
	return fx.call("run_query", map[string]any{"sql": q, "connection": conn})
}

func TestRunQueryConnection_UnqualifiedNamesResolveOnThatCatalog(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	// "users" is (id, name, email) on the default connection and (uid, email) on other.
	resp := runIn(fx, "other", "SELECT uid FROM users")
	require.Nil(t, resp.Error, "%+v", resp.Error)
	rows, ok := mapOf(t, resp.Result)["rows"].([][]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 1, rows[0][0])

	// Without it the same name is the default connection's table.
	assert.NotNil(t, fx.run("SELECT uid FROM users").Error)
	// The default catalog's own name is no pin at all.
	resp = runIn(fx, fx.sess.DefaultCatalog(), "SELECT name FROM users")
	require.Nil(t, resp.Error, "%+v", resp.Error)
}

func TestRunQueryConnection_MetricOnAPinnedSavedQuery(t *testing.T) {
	fx := metricFixture(t, ModeReadOnly)
	require.NoError(t, fx.h.access.Queries().Create(context.Background(), &queries.Query{
		ID: "other_base", Title: "other_base", Connection: "other",
		SQL: "SELECT 'east' AS region, 1 AS month, total AS amount FROM orders",
	}))
	putMetric(t, fx, "other_rev", "other_base", "sum(amount)")
	const q = "SELECT * FROM metric('other_rev')"

	resp := fx.run(q)
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "defined on connection 'other'")

	resp = runIn(fx, "other", q)
	require.Nil(t, resp.Error, "%+v", resp.Error)
	res := mapOf(t, resp.Result)
	rows, ok := res["rows"].([][]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 30, rows[0][0])
	assert.NotEmpty(t, res["metrics_used"])

	// A metric on the default connection refuses a run pinned elsewhere.
	resp = runIn(fx, "other", "SELECT * FROM metric('net_revenue')")
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "defined on connection 'memory'")
}

func TestRunQueryConnection_BadArgumentsAreRefused(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	resp := runIn(fx, "nope", "SELECT 1")
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, `no connection named "nope"`)
	assert.Contains(t, resp.Error.Message, "list_connections")

	resp = fx.call("run_query", map[string]any{"sql": "SELECT 1", "connection": 7.0})
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)

	// A blank connection is the default.
	assert.Nil(t, runIn(fx, "", "SELECT 1").Error)
}

func TestRunQueryConnection_WritesKeepEveryGate(t *testing.T) {
	// The tier still decides.
	fx := newPolicyFixture(t, ModeReadOnly)
	resp := runIn(fx, "other", "INSERT INTO orders VALUES (3, 30)")
	require.NotNil(t, resp.Error)
	assert.Equal(t, 0, fx.count("SELECT count(*) FROM other.orders WHERE id = 3"))

	// A read-only connection is refused even in the full tier.
	fx = newPolicyFixture(t, ModeFull)
	resp = runIn(fx, "ro", "INSERT INTO ledger VALUES (2, 5)")
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "read-only")
	assert.Equal(t, 1, fx.count("SELECT count(*) FROM ro.ledger"))

	// A writable connection in a tier that allows it asks first when the
	// person's setting says so; the card names the connection.
	fx = newPolicyFixture(t, ModeReadWrite, withAsk(true), withApprover())
	resp = runIn(fx, "other", "INSERT INTO orders VALUES (3, 30)")
	require.Nil(t, resp.Error, "%+v", resp.Error)
	require.Equal(t, 1, fx.approver.count())
	assert.Contains(t, fx.approver.asked[0].Summary, "other")
	assert.Equal(t, 1, fx.count("SELECT count(*) FROM other.orders WHERE id = 3"))

	// Denied by the person: nothing is written.
	fx = newPolicyFixture(t, ModeReadWrite, withAsk(true), withApprover())
	fx.approver.decision = assert.AnError
	resp = runIn(fx, "other", "INSERT INTO orders VALUES (4, 40)")
	require.NotNil(t, resp.Error)
	assert.Equal(t, 0, fx.count("SELECT count(*) FROM other.orders WHERE id = 4"))
}

func TestRunQueryConnection_EmptyResultNoteDescribesThePinnedTable(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	resp := runIn(fx, "other", "SELECT uid FROM users WHERE email = 'zzz'")
	require.Nil(t, resp.Error, "%+v", resp.Error)
	note, _ := mapOf(t, resp.Result)["note"].(string)
	assert.Contains(t, note, "a@x.io", "the note lists other.users' values")
	assert.NotContains(t, note, "alice@example.com", "not the default catalog's table")
}

func TestRunQueryConnection_NonLocalWriteNeedsApproval(t *testing.T) {
	// Headless (no approver): refused, and nothing is written.
	fx := newPolicyFixture(t, ModeFull, withRemote("other"))
	resp := runIn(fx, " OTHER ", "INSERT INTO orders VALUES (5, 50)")
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrApprovalNeeded, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "other")
	assert.Equal(t, 0, fx.count("SELECT count(*) FROM other.orders WHERE id = 5"))

	// With an approver the card lists the connection by its canonical name.
	fx = newPolicyFixture(t, ModeFull, withRemote("other"), withApprover())
	resp = runIn(fx, " OTHER ", "INSERT INTO orders VALUES (5, 50)")
	require.Nil(t, resp.Error, "%+v", resp.Error)
	require.Equal(t, 1, fx.approver.count())
	assert.Equal(t, []string{"other"}, fx.approver.asked[0].Connections)
	assert.NotContains(t, fx.approver.asked[0].Summary, "OTHER")
}

func TestRunQueryConnection_ToolDefinitionAndPreviewUnaffected(t *testing.T) {
	var found bool
	for _, p := range GetTool("run_query").Params {
		if p.Name == "connection" {
			found = true
			assert.False(t, p.Required)
		}
	}
	assert.True(t, found, "run_query advertises the connection argument")
	// preview_query has no such argument and still previews on the default connection.
	fx := newPolicyFixture(t, ModeReadOnly)
	resp := fx.call("preview_query", map[string]any{"sql": "SELECT name FROM users"})
	require.Nil(t, resp.Error, "%+v", resp.Error)
}
