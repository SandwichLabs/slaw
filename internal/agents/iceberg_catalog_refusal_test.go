package agents

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/session"
)

// icebergStub stands in for the iceberg_catalog type (same name, so the
// registered connection reads as one) without a network: it attaches an
// in-memory catalog and counts how often it is asked to.
type icebergStub struct{ attaches *atomic.Int32 }

func (icebergStub) Name() string                { return "iceberg_catalog" }
func (icebergStub) Remote() bool                { return true }
func (icebergStub) Validate(session.Spec) error { return nil }
func (s icebergStub) AttachSQL(sp session.Spec) ([]string, error) {
	s.attaches.Add(1)
	return []string{"ATTACH ':memory:' AS " + session.QuoteIdent(sp.Name)}, nil
}

func newIcebergRefusalHandler(t *testing.T) (*Handler, *atomic.Int32) {
	t.Helper()
	dir := t.TempDir()
	var attaches atomic.Int32
	sess, err := session.New(
		session.WithTempDirectory(filepath.Join(dir, "tmp")),
		session.WithExtensionDirectory(filepath.Join(dir, "ext")),
		session.WithSecretDirectory(filepath.Join(dir, "sec")),
		session.WithConnectionTypes(icebergStub{&attaches}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	_, err = sess.DB().Exec("CREATE TABLE local_t (id INTEGER); INSERT INTO local_t VALUES (1)")
	require.NoError(t, err)
	require.NoError(t, sess.Register(session.Spec{Name: "lake", Type: "iceberg_catalog", ReadOnly: true, Params: map[string]string{}}))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	eng := engine.NewManager(sess, engine.WithLogger(logger))
	acc := &sessionAccess{mockAccess: &mockAccess{}, sess: sess}
	h := NewHandler(acc, eng, &SandboxConfig{Mode: ModeFull, MaxQueryRows: 100}, logger)
	return h, &attaches
}

// Until the lake reader routes them, an agent statement that names an
// iceberg_catalog is refused with one message, in every tool, and never makes
// the main hub attach the catalog.
func TestAgentRefusesIcebergCatalogTables(t *testing.T) {
	h, attaches := newIcebergRefusalHandler(t)
	ctx := context.Background()
	const want = "not readable by the agent yet"

	for _, q := range []string{
		"SELECT * FROM lake.ns.t",
		"SELECT * FROM lake.t",
		`SELECT * FROM "lake"."ns"."t"`,
		"SELECT * FROM local_t JOIN lake.ns.t USING (id)",
		"SELECT (SELECT count(*) FROM lake.ns.t)",
		"WITH c AS (SELECT * FROM lake.ns.t) SELECT * FROM c",
		"DESCRIBE SELECT * FROM lake.ns.t",
	} {
		resp := call(h, "run_query", map[string]any{"sql": q})
		require.NotNil(t, resp.Error, q)
		assert.Contains(t, resp.Error.Message, want, q)

		err := h.VetReadOnlySQLIn(ctx, "", q, nil)
		require.Error(t, err, q)
		assert.Contains(t, err.Error(), want, q)
		_, _, err = h.RunReadOnlySQLIn(ctx, "", q, nil, 10)
		require.Error(t, err, q) // the runner words its refusal generically
		assert.Zero(t, attaches.Load(), "attached by %s", q)
	}

	// The catalog named as the run's own connection, with an unqualified table.
	_, _, err := h.RunReadOnlySQLIn(ctx, "lake", "SELECT * FROM ns.t", nil, 10)
	require.Error(t, err)

	assert.Zero(t, attaches.Load(), "before: // A table on another catalog ")
	// A table on another catalog still works.
	resp := call(h, "run_query", map[string]any{"sql": "SELECT id FROM local_t"})
	require.Nil(t, resp.Error)

	assert.Zero(t, attaches.Load(), "before: // describe_table and introspe")
	// describe_table and introspect_schema, qualified or by connection.
	for _, table := range []string{"lake.ns.t", "lake.t"} {
		resp = call(h, "describe_table", map[string]any{"table": table})
		require.NotNil(t, resp.Error, table)
		assert.Contains(t, resp.Error.Message, want)
	}
	resp = call(h, "introspect_schema", map[string]any{"connection": "lake"})
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, want)

	assert.Zero(t, attaches.Load(), "before: // run_saved_query: a stored q")
	// run_saved_query: a stored query over the catalog.
	require.NoError(t, h.access.Queries().Create(ctx, &queries.Query{ID: "over_lake", Title: "x", SQL: "SELECT * FROM lake.ns.t"}))
	resp = call(h, "run_saved_query", map[string]any{"id": "over_lake"})
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, want)

	assert.Zero(t, attaches.Load(), "before: // A metric whose saved query")
	// A metric whose saved query reads the catalog: the expanded text is refused.
	require.NoError(t, h.access.Queries().Create(ctx, &queries.Query{ID: "lake_base", Title: "b", SQL: "SELECT id, 1 AS n FROM lake.ns.t"}))
	m, err := semantics.DecodeMetric("lake_n", []byte("version: 1\nname: lake_n\nquery: lake_base\nmeasure: sum(n)\ndimensions: [id]\n"))
	require.NoError(t, err)
	sem, ok := h.access.Semantics().(*semantics.Mock)
	require.True(t, ok)
	require.NoError(t, sem.PutMetric(ctx, m))
	resp = call(h, "run_query", map[string]any{"sql": "SELECT * FROM metric('lake_n', by => 'id')"})
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, want)

	assert.Zero(t, attaches.Load(), "no agent statement attached the catalog on the main hub")
}
