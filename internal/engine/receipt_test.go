package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/storageglob"
)

func intentCtx() context.Context {
	return receipts.WithIntent(context.Background(), receipts.Intent{
		Surface: receipts.SurfaceQueryEditor, Actor: receipts.ActorPerson, SQL: "x", Verdict: receipts.VerdictUnchecked,
	})
}

func TestRunFacts_ARunFillsWhatOnlyTheEngineKnows(t *testing.T) {
	sess := lazySession(t)
	m := NewManager(sess)
	ctx := intentCtx()
	rs, _, err := m.ExecuteCappedIn(ctx, "", "SELECT x, 'a' AS s FROM other.main.t WHERE x = $v", map[string]any{"v": 7}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, rs.RowCount())

	f := receipts.RunFrom(ctx).Facts()
	assert.Equal(t, receipts.StmtSelect, f.Stmt)
	assert.True(t, f.Mintable())
	// The text as run keeps $name: values are bound, never part of the text.
	assert.Equal(t, "SELECT x, 'a' AS s FROM other.main.t WHERE x = $v", f.Executed)
	assert.Equal(t, f.Executed, f.Summary)
	assert.Equal(t, []receipts.Column{{Name: "x", Type: "INTEGER"}, {Name: "s", Type: "VARCHAR"}}, f.Columns)
	assert.False(t, f.At.IsZero())
	assert.GreaterOrEqual(t, int64(f.Duration), int64(0))
	// The default catalog (what unqualified names mean) and the connection the
	// statement named, lazily attached; each status is an enum.
	require.Len(t, f.Connections, 2)
	assert.Equal(t, receipts.Connection{Name: "memory", Type: "duckdb", Status: receipts.StatusConnected}, f.Connections[0])
	assert.Equal(t, receipts.Connection{Name: "other", Type: "duckdb", Status: receipts.StatusConnected}, f.Connections[1])
}

func TestRunFacts_AHeldConnectionRecordsToo(t *testing.T) {
	sess := lazySession(t)
	m := NewManager(sess)
	conn, err := sess.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	ctx := intentCtx()
	_, _, err = m.ExecuteOn(ctx, conn, "", "WITH a AS (SELECT 1 AS n) SELECT * FROM a", nil, 10)
	require.NoError(t, err)
	// WITH is prepared on the held connection, and DuckDB says SELECT.
	assert.Equal(t, receipts.StmtSelect, receipts.RunFrom(ctx).Facts().Stmt)
}

func TestRunFacts_NoIntentRecordsNothing(t *testing.T) {
	m := NewManager(lazySession(t))
	ctx := context.Background()
	_, _, err := m.ExecuteCappedIn(ctx, "", "SELECT 1", nil, 0)
	require.NoError(t, err)
	assert.Nil(t, receipts.RunFrom(ctx))
}

func TestRunFacts_AFailedRunLeavesNoFacts(t *testing.T) {
	m := NewManager(lazySession(t))
	ctx := intentCtx()
	_, _, err := m.ExecuteCappedIn(ctx, "", "SELECT * FROM nope", nil, 0)
	require.Error(t, err)
	assert.False(t, receipts.RunFrom(ctx).Facts().Mintable())
}

func TestStatementKind(t *testing.T) {
	m := NewManager(lazySession(t))
	ctx := context.Background()
	for text, want := range map[string]receipts.StmtKind{
		"SELECT 1":                                      receipts.StmtSelect,
		"select 1;":                                     receipts.StmtSelect,
		"-- note\nSELECT 1":                             receipts.StmtSelect,
		"FROM range(3)":                                 receipts.StmtSelect,
		"VALUES (1), (2)":                               receipts.StmtSelect,
		"TABLE other.main.t":                            receipts.StmtSelect,
		"WITH a AS (SELECT 1) SELECT * FROM a":          receipts.StmtSelect,
		"(SELECT 1) UNION ALL (SELECT 2)":               receipts.StmtSelect,
		"EXPLAIN SELECT 1":                              receipts.StmtExplain,
		"EXPLAIN ANALYZE SELECT 1":                      receipts.StmtOther,
		"explain analyse select 1":                      receipts.StmtOther,
		"INSERT INTO other.main.t VALUES (1)":           receipts.StmtOther,
		"DELETE FROM other.main.t":                      receipts.StmtOther,
		"CREATE TABLE tt AS SELECT 1":                   receipts.StmtOther,
		"WITH a AS (SELECT 1) DELETE FROM other.main.t": receipts.StmtOther,
		"SELECT 1; DROP TABLE other.main.t":             receipts.StmtOther,
		"SELECT 1; SELECT 2":                            receipts.StmtOther,
		"ATTACH 'x.db' AS x":                            receipts.StmtOther,
		"SET threads = 1":                               receipts.StmtOther,
		"":                                              receipts.StmtOther,
	} {
		assert.Equal(t, want, m.statementKind(ctx, "", nil, text), text)
	}
}

// bigGlob is a statement whose wildcard expands to 10,000 keys, with the rewrite.
func bigGlob(t *testing.T) (written, expanded string, rw *storageglob.Rewrite) {
	t.Helper()
	keys := make([]string, 0, storageglob.DefaultMax)
	for i := 0; i < storageglob.DefaultMax; i++ {
		keys = append(keys, fmt.Sprintf("s3://b/big/part-%05d.csv", i))
	}
	written = "SELECT count(*) AS n FROM read_csv('s3://b/big/*.csv') WHERE n > $min"
	expanded, rw, err := storageglob.Expand(context.Background(), written, storageglob.Options{Lister: cannedLister(keys)})
	require.NoError(t, err)
	require.True(t, rw.Changed())
	return written, expanded, rw
}

type cannedLister []string

func (c cannedLister) ListMatching(_ context.Context, _ string, limit int) ([]string, error) {
	if len(c) > limit+1 {
		return c[:limit+1], nil
	}
	return c, nil
}

const bigSummary = "SELECT count(*) AS n FROM read_csv('s3://b/big/*.csv' /* expanded to 10000 objects */) WHERE n > $min"

// A wildcard the agent policy expanded before it vetted the statement arrives
// expanded and with its rewrite on the context: what ran is the text as given.
func TestRecordExecuted_AVettedWildcardIsSummarised(t *testing.T) {
	_, expanded, rw := bigGlob(t)
	ctx := storageglob.WithRewrite(intentCtx(), rw)
	recordExecuted(ctx, expanded, expanded, rw, &ResultSet{Columns: []Column{{Name: "n", Type: "BIGINT"}}})
	f := receipts.RunFrom(ctx).Facts()
	assert.Equal(t, expanded, f.Executed)
	assert.Greater(t, len(f.Executed), 200000, "what ran holds every key")
	assert.Equal(t, bigSummary, f.Summary)
	assert.NotContains(t, f.Summary, "part-0")
}

// A wildcard the engine expanded itself (the editor, the CLI, the console) is in
// the bound text only: that is what ran, and what is kept is the same summary.
func TestRecordExecuted_AnEngineExpandedWildcardIsSummarised(t *testing.T) {
	written, expanded, rw := bigGlob(t)
	ctx := intentCtx()
	recordExecuted(ctx, written, expanded, rw, &ResultSet{})
	f := receipts.RunFrom(ctx).Facts()
	assert.Equal(t, expanded, f.Executed)
	assert.Equal(t, bigSummary, f.Summary)

	// No wildcard: what ran is the text as handed to the engine.
	ctx = intentCtx()
	recordExecuted(ctx, "SELECT $a", "SELECT ?", nil, &ResultSet{})
	f = receipts.RunFrom(ctx).Facts()
	assert.Equal(t, "SELECT $a", f.Executed)
	assert.Equal(t, "SELECT $a", f.Summary)
}

func TestOpenRecorder_ANilRootOrAnUnusableFolderRecordsNothing(t *testing.T) {
	assert.Nil(t, OpenRecorder("", nil))
	dir := t.TempDir()
	rec := OpenRecorder(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NotNil(t, rec)

	ctx := intentCtx()
	m := NewManager(lazySession(t))
	rs, _, err := m.ExecuteCappedIn(ctx, "", "SELECT 41 + 1 AS answer", nil, 0)
	require.NoError(t, err)
	id, err := receipts.Finish(ctx, rec, rs.Rows, false, 1)
	require.NoError(t, err)
	got, err := rec.Store().Get(id)
	require.NoError(t, err)
	assert.Equal(t, "SELECT 41 + 1 AS answer", got.RunSQL)

	// A credential-shaped statement is withheld by the same rule the console's
	// history uses; the hash stays.
	ctx = receipts.WithIntent(context.Background(), receipts.Intent{
		Surface: receipts.SurfaceCLIQuery, Actor: receipts.ActorPerson, Verdict: receipts.VerdictUnchecked,
		SQL: "SELECT 'password=hunter2' AS x",
	})
	rs, _, err = m.ExecuteCappedIn(ctx, "", "SELECT 'password=hunter2' AS x", nil, 0)
	require.NoError(t, err)
	id, err = receipts.Finish(ctx, rec, rs.Rows, false, 1)
	require.NoError(t, err)
	got, err = rec.Store().Get(id)
	require.NoError(t, err)
	assert.True(t, got.SQLWithheld)
	assert.True(t, got.RunSQLWithheld)
	assert.NotEmpty(t, got.SQLHash)
	assert.NotContains(t, fmt.Sprintf("%+v", got), "hunter2")
}
