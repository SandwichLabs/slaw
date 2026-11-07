//go:build integration

package agents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
)

// How an unqualified name resolves on an Iceberg catalog, which has namespaces
// and no default schema: in default_namespace when it is set, in the only
// namespace when there is exactly one, and otherwise it is an error that says
// how to fix it (a namespace someone adds later never takes a name over).
func TestIcebergUnqualifiedNamesResolveByDefaultNamespace(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	ctx := context.Background()
	const q = "SELECT count(*) FROM orders"

	t.Run("exactly one namespace", func(t *testing.T) {
		ws, sess := icebergWorkspace(t, f)
		h := agentOn(t, ws, sess, agents.ModeReadOnly, true)
		res, _, err := h.RunReadOnlySQLIn(ctx, "cat", q, nil, 10)
		require.NoError(t, err)
		assert.EqualValues(t, 300, res.Rows[0][0])
		// introspect_schema on the catalog lists its only namespace.
		resp := call(h, "introspect_schema", map[string]any{"connection": "cat"})
		require.Nil(t, resp.Error, errText(resp))
	})

	// A second namespace with its own orders table, made through an unhardened DuckDB.
	db := f.Open(t)
	icebergtest.Exec(t, db, icebergtest.AttachSQL("seed2", f.RESTURI, ""))
	icebergtest.Exec(t, db, "CREATE SCHEMA seed2.aaa")
	icebergtest.Exec(t, db, "CREATE TABLE seed2.aaa.orders AS SELECT i AS id, 0 AS amount, 'z' AS region FROM range(7) t(i)")
	icebergtest.Exec(t, db, "DETACH seed2")

	t.Run("several namespaces and no default is a clear error", func(t *testing.T) {
		ws, sess := icebergWorkspace(t, f)
		h := agentOn(t, ws, sess, agents.ModeReadOnly, true)
		_, _, err := h.RunReadOnlySQLIn(ctx, "cat", q, nil, 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "several namespaces")
		assert.Contains(t, err.Error(), "default_namespace")
		// Qualified names are unaffected, and so is the default catalog.
		res, _, err := h.RunReadOnlySQLIn(ctx, "", "SELECT count(*) FROM cat.ns.orders", nil, 10)
		require.NoError(t, err)
		assert.EqualValues(t, 300, res.Rows[0][0])
		resp := call(h, "introspect_schema", map[string]any{"connection": "cat"})
		require.NotNil(t, resp.Error)
		assert.Contains(t, resp.Error.Message, "several namespaces")
	})

	t.Run("default_namespace decides, whatever namespaces exist", func(t *testing.T) {
		ws, sess := icebergWorkspace(t, f, map[string]string{"default_namespace": icebergtest.Namespace})
		h := agentOn(t, ws, sess, agents.ModeReadOnly, true)
		res, _, err := h.RunReadOnlySQLIn(ctx, "cat", q, nil, 10)
		require.NoError(t, err)
		assert.EqualValues(t, 300, res.Rows[0][0], "ns.orders, not aaa.orders")
		resp := call(h, "introspect_schema", map[string]any{"connection": "cat"})
		require.Nil(t, resp.Error, errText(resp))

		ws, sess = icebergWorkspace(t, f, map[string]string{"default_namespace": "aaa"})
		h = agentOn(t, ws, sess, agents.ModeReadOnly, true)
		res, _, err = h.RunReadOnlySQLIn(ctx, "cat", q, nil, 10)
		require.NoError(t, err)
		assert.EqualValues(t, 7, res.Rows[0][0])
	})
}
