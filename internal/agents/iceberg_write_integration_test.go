//go:build integration

package agents_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
)

func countRows(t *testing.T, h *agents.Handler, q string) int {
	t.Helper()
	resp := sqlCall(h, q)
	require.Nil(t, resp.Error, errText(resp))
	rows, ok := resp.Result.(map[string]any)["rows"].([][]any)
	require.True(t, ok)
	n, ok := rows[0][0].(int64)
	require.True(t, ok, "%T", rows[0][0])
	return int(n)
}

// A catalog defined with read_only: false is writable through the agent under
// the usual rules: the full-write tier, a writable connection, and the person's
// approval (always asked for a remote connection). Declining changes nothing, a
// read_only catalog refuses the same statement, and a lower tier refuses it too.
func TestAgentWritesToAWritableIcebergCatalog(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	ws, sess := icebergWorkspace(t, f)
	ap := &stubApprover{}
	h := agentOn(t, ws, sess, agents.ModeReadWrite, false) // approval is asked for a remote connection whatever the setting
	h.SetApprover(ap.fn)

	const ins = "INSERT INTO catw.ns.events VALUES (100, 'x')"
	before := countRows(t, h, "SELECT count(*) FROM catw.ns.events")

	// Declined: nothing changes.
	ap.decision = errors.New("declined")
	resp := sqlCall(h, ins)
	require.NotNil(t, resp.Error)
	require.Len(t, ap.asked, 1, "the person was asked")
	assert.Equal(t, before, countRows(t, h, "SELECT count(*) FROM catw.ns.events"))

	// Approved: the row is written (the ICEBERG_INSERT plan operator is known).
	ap.decision = nil
	resp = sqlCall(h, ins)
	require.Nil(t, resp.Error, errText(resp))
	require.Len(t, ap.asked, 2)
	assert.Equal(t, before+1, countRows(t, h, "SELECT count(*) FROM catw.ns.events"))
	resp = sqlCall(h, "DELETE FROM catw.ns.events WHERE id = 100")
	require.Nil(t, resp.Error, errText(resp))
	assert.Equal(t, before, countRows(t, h, "SELECT count(*) FROM catw.ns.events"))

	// The read_only catalog refuses the write, asking nobody.
	asked := len(ap.asked)
	resp = sqlCall(h, "INSERT INTO cat.ns.events VALUES (101, 'y')")
	require.NotNil(t, resp.Error)
	assert.Len(t, ap.asked, asked)

	// The read-only tier refuses the writable catalog too.
	ro := agentOn(t, ws, sess, agents.ModeReadOnly, true)
	resp = sqlCall(ro, ins)
	require.NotNil(t, resp.Error)
}
