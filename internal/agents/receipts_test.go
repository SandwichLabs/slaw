package agents

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

var receiptKey = bytes.Repeat([]byte{7}, receipts.KeySize)

// recordTo wires a recorder over a mock store into the handler, as
// hub.NewAgentHandler does with the workspace's, and returns the store.
func recordTo(h *Handler) *receipts.Mock {
	m := receipts.NewMock()
	h.SetReceipts(receipts.NewRecorder(m, receiptKey, receipts.DefaultPolicy(), receipts.Checks{Statement: engine.RecordableStatement, Value: engine.Recordable}, nil))
	return m
}

func (fx *policyFixture) callCtx(ctx context.Context, method string, params map[string]any) *Response {
	fx.t.Helper()
	return fx.h.Handle(ctx, &Request{JSONRPC: "2.0", ID: "1", Method: method, Params: params})
}

func receiptID(t *testing.T, resp *Response) string {
	t.Helper()
	require.Nil(t, resp.Error, errMsg(resp))
	id, _ := mapOf(t, resp.Result)["receipt_id"].(string)
	return id
}

func only(t *testing.T, m *receipts.Mock) receipts.Receipt {
	t.Helper()
	all := m.All()
	require.Len(t, all, 1)
	return all[0]
}

func TestReceipts_RunQueryMintsOneForTheAgent(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	ctx := receipts.WithAgentSession(context.Background(), "chat-1")

	resp := fx.callCtx(ctx, "run_query", map[string]any{"sql": "SELECT name FROM users WHERE id = $id", "params": map[string]any{"id": 1}})
	id := receiptID(t, resp)
	require.NotEmpty(t, id)

	r := only(t, m)
	assert.Equal(t, id, r.ID)
	assert.Equal(t, receipts.SurfaceAgentRunQuery, r.Surface)
	assert.Equal(t, receipts.ActorAgent, r.Actor)
	assert.Equal(t, "chat-1", r.AgentSession)
	assert.Equal(t, receipts.Source{Kind: receipts.SourceSQL}, r.Source)
	assert.Equal(t, receipts.VerdictRead, r.Verdict)
	assert.Equal(t, "SELECT name FROM users WHERE id = $id", r.SQL)
	assert.Equal(t, "SELECT name FROM users WHERE id = $id", r.RunSQL)
	assert.Equal(t, map[string]receipts.ParamValue{"id": {Value: "1"}}, r.Params)
	assert.Equal(t, receipts.Rows{Returned: 1, Total: 1}, r.Rows)
	assert.Equal(t, []receipts.Column{{Name: "name", Type: "VARCHAR"}}, r.Columns)
	// The default catalog is a connection the statement resolved to.
	require.NotEmpty(t, r.Connections)
	assert.Equal(t, receipts.StatusConnected, r.Connections[0].Status)

	// The result carries no value in the receipt; the rows are only fingerprinted.
	b, err := receipts.Encode(r)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "Alice")
}

func TestReceipts_RunQueryResultMatchesWhatWasShown(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	// MaxQueryRows is 100: a 150-row result is cut to 100 in the reply.
	resp := fx.call("run_query", map[string]any{"sql": "SELECT i, 'row ' || i AS s FROM range(150) t(i) ORDER BY i"})
	require.NotEmpty(t, receiptID(t, resp))
	shown, ok := mapOf(t, resp.Result)["rows"].([][]any)
	require.True(t, ok)
	require.Len(t, shown, 100)

	r := only(t, m)
	assert.EqualValues(t, 100, r.Rows.Returned)
	assert.EqualValues(t, 150, r.Rows.Total)
	assert.True(t, r.Rows.Truncated)
	h := receipts.NewResultHasher(receiptKey, r.Columns)
	for _, row := range shown {
		require.NoError(t, h.AddRow(row))
	}
	assert.Equal(t, h.Sum(), r.ResultHMAC, "result_hmac fingerprints the 100 rows shown, not the 150 fetched")
}

func TestReceipts_RunQueryUnderTheResultBudgetFingerprintsTheBudgetedRows(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	// 100 rows of 400 characters is over the 15,000 character result budget.
	resp := fx.call("run_query", map[string]any{"sql": "SELECT repeat('x', 400) AS s FROM range(100)"})
	require.NotEmpty(t, receiptID(t, resp))
	res := mapOf(t, resp.Result)
	require.NotNil(t, res["truncated"], "the budget cut the rows")
	shown, ok := res["rows"].([][]any)
	require.True(t, ok)
	require.Less(t, len(shown), 100)

	r := only(t, m)
	assert.EqualValues(t, len(shown), r.Rows.Returned)
	assert.EqualValues(t, 100, r.Rows.Total)
	assert.True(t, r.Rows.Truncated)
	h := receipts.NewResultHasher(receiptKey, r.Columns)
	for _, row := range shown {
		require.NoError(t, h.AddRow(row))
	}
	assert.Equal(t, h.Sum(), r.ResultHMAC)
}

func TestReceipts_RunQueryDropsDuckLakeInternalRowsBeforeCounting(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	resp := fx.call("run_query", map[string]any{"sql": "SELECT s FROM (VALUES ('a'), ('__ducklake_metadata_lake'), ('b')) v(s) ORDER BY s"})
	require.NotEmpty(t, receiptID(t, resp))
	shown, ok := mapOf(t, resp.Result)["rows"].([][]any)
	require.True(t, ok)
	require.Len(t, shown, 2)

	r := only(t, m)
	assert.EqualValues(t, 2, r.Rows.Returned)
	assert.EqualValues(t, 2, r.Rows.Total)
	assert.False(t, r.Rows.Truncated)
	h := receipts.NewResultHasher(receiptKey, r.Columns)
	for _, row := range shown {
		require.NoError(t, h.AddRow(row))
	}
	assert.Equal(t, h.Sum(), r.ResultHMAC)
}

func TestReceipts_RunSavedQuery(t *testing.T) {
	fx := newQueryFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	q := &queries.Query{ID: "min_id", Title: "Min id", SQL: "SELECT id FROM t WHERE id >= $min", Parameters: []queries.Param{{Name: "min", Type: "integer", Default: "1"}}}
	require.NoError(t, queries.NewFileStore(fx.root).Create(context.Background(), q))

	resp := fx.callCtx(receipts.WithAgentSession(context.Background(), "mcp-9"), "run_saved_query", map[string]any{"id": "min_id", "params": map[string]any{"min": 1}})
	require.NotEmpty(t, receiptID(t, resp))
	r := only(t, m)
	assert.Equal(t, receipts.SurfaceAgentSavedQuery, r.Surface)
	assert.Equal(t, receipts.Source{Kind: receipts.SourceSavedQuery, Ref: "queries/min_id.sql"}, r.Source)
	assert.Equal(t, "SELECT id FROM t WHERE id >= $min", r.SQL)
	assert.Equal(t, "1", r.Params["min"].Value)
	assert.Equal(t, "mcp-9", r.AgentSession)
}

func TestReceipts_PreviewQueryFingerprintsTheSampleItReturns(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	resp := fx.call("preview_query", map[string]any{"sql": "SELECT i FROM range(5) t(i) ORDER BY i"})
	require.NotEmpty(t, receiptID(t, resp))
	r := only(t, m)
	assert.Equal(t, receipts.SurfaceAgentPreview, r.Surface)
	assert.Equal(t, receipts.ActorAgent, r.Actor)
	assert.EqualValues(t, 5, r.Rows.Returned)
	assert.False(t, r.Rows.Truncated)

	// A result over the preview's character budget returns a smaller sample: the
	// receipt covers that sample, and says it was cut.
	m2 := recordTo(fx.h)
	resp = fx.call("preview_query", map[string]any{"sql": "SELECT repeat('y', 300) AS s FROM range(200)"})
	require.NotEmpty(t, receiptID(t, resp))
	res := mapOf(t, resp.Result)
	sample, ok := res["rows"].([]map[string]any)
	require.True(t, ok)
	require.Less(t, len(sample), 200)
	r = only(t, m2)
	assert.EqualValues(t, len(sample), r.Rows.Returned)
	assert.EqualValues(t, 200, r.Rows.Total)
	assert.True(t, r.Rows.Truncated)
}

func TestReceipts_RenderSurfaceThroughRunReadOnlySQLIn(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)

	// maxRows+1 rows come back from the run: the receipt covers the maxRows shown.
	rs, truncated, err := fx.h.RunReadOnlySQLIn(context.Background(), "", "SELECT i FROM range(10) t(i) ORDER BY i", nil, 4)
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, rs.Rows, 4)
	require.NotEmpty(t, rs.ReceiptID)

	r := only(t, m)
	assert.Equal(t, rs.ReceiptID, r.ID)
	assert.Equal(t, receipts.SurfaceRender, r.Surface)
	assert.Equal(t, receipts.ActorPerson, r.Actor)
	assert.Empty(t, r.AgentSession)
	assert.Equal(t, receipts.Source{}, r.Source, "a render has no source until a later slice passes one")
	assert.Equal(t, receipts.VerdictRead, r.Verdict)
	assert.EqualValues(t, 4, r.Rows.Returned)
	assert.True(t, r.Rows.Truncated, "one probe row more than shown")
	// What ran is the capped wrapper; what was written is the cell's text.
	assert.Equal(t, "SELECT i FROM range(10) t(i) ORDER BY i", r.SQL)
	assert.Contains(t, r.RunSQL, "_a2ui_limit LIMIT 5")
	h := receipts.NewResultHasher(receiptKey, r.Columns)
	for _, row := range rs.Rows {
		require.NoError(t, h.AddRow(row))
	}
	assert.Equal(t, h.Sum(), r.ResultHMAC, "the probe row is not in the fingerprint")

	// Without the cap the run is not wrapped and nothing is truncated.
	m2 := recordTo(fx.h)
	rs, truncated, err = fx.h.RunReadOnlySQLIn(context.Background(), "", "SELECT 1 AS a", nil, 0)
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.NotEmpty(t, rs.ReceiptID)
	assert.Len(t, m2.All(), 1)
}

func TestReceipts_ArtifactAndMetricAndWatchOrigins(t *testing.T) {
	fx := newArtifactFixture(t, ModeReadWrite)
	m := recordTo(fx.h)
	fx.mustWrite("sales", artifactManifest(rowsQuery(okSQL)))
	assert.Empty(t, m.All(), "saving an artifact vets its queries with LIMIT 0 and mints nothing")

	rs, _, err := fx.h.RunArtifactQuery(context.Background(), "sales", "rows", map[string]any{"who": "Ada"})
	require.NoError(t, err)
	require.NotEmpty(t, rs.ReceiptID)
	r := only(t, m)
	assert.Equal(t, receipts.SurfaceArtifact, r.Surface)
	assert.Equal(t, receipts.Source{Kind: receipts.SourceArtifactQuery, Ref: "artifacts/sales#rows"}, r.Source)
	assert.Equal(t, "Ada", r.Params["who"].Value)

	// Another caller's origin is what its receipt says.
	m2 := recordTo(fx.h)
	ctx := receipts.WithOrigin(context.Background(), receipts.Origin{Surface: receipts.SurfaceWatch, Source: receipts.Source{Kind: receipts.SourceSavedQuery, Ref: "queries/x.sql"}})
	_, _, err = fx.h.RunReadOnlySQLIn(ctx, "", "SELECT 1 AS a", nil, 10)
	require.NoError(t, err)
	r = only(t, m2)
	assert.Equal(t, receipts.SurfaceWatch, r.Surface)
	assert.Equal(t, "queries/x.sql", r.Source.Ref)

	// A skipped origin (a join profile) makes none.
	m3 := recordTo(fx.h)
	_, _, err = fx.h.RunReadOnlySQLIn(receipts.WithOrigin(context.Background(), receipts.Origin{Skip: true}), "", "SELECT 1 AS a", nil, 10)
	require.NoError(t, err)
	assert.Empty(t, m3.All())
}

func TestReceipts_MintNothingForTheInternalQueries(t *testing.T) {
	fx := newArtifactFixture(t, ModeFull, withAsk(false))
	m := recordTo(fx.h)
	ctx := context.Background()

	// Classification (EXPLAIN), the policy's vet and a catalog listing.
	require.Equal(t, sqlClassRead, fx.classify("SELECT * FROM users").Class)
	require.NoError(t, fx.h.VetReadOnlySQLIn(ctx, "", "SELECT * FROM users", nil))
	require.Nil(t, fx.call("introspect_schema", map[string]any{}).Error)
	require.Nil(t, fx.call("list_connections", map[string]any{}).Error)
	assert.Empty(t, m.All())

	// A write is not a read: no receipt, no receipt_id.
	resp := fx.call("run_query", map[string]any{"sql": "INSERT INTO t2 VALUES (2)"})
	require.Nil(t, resp.Error, errMsg(resp))
	assert.Empty(t, mapOf(t, resp.Result)["receipt_id"])
	// A refused statement is not a run.
	resp = fx.call("run_query", map[string]any{"sql": "SELECT * FROM read_text('" + filepath.Join(fx.outside, "secret.txt") + "')"})
	require.NotNil(t, resp.Error)
	assert.Empty(t, m.All())

	// The artifact vet at save, which runs the queries with LIMIT 0.
	fx.mustWrite("sales", artifactManifest(rowsQuery(okSQL)))
	assert.Empty(t, m.All())

	// The same handler does mint a read.
	require.NotEmpty(t, receiptID(t, fx.call("run_query", map[string]any{"sql": "SELECT 1 AS a"})))
	assert.Len(t, m.All(), 1)
}

func TestReceipts_AFailingStoreStillReturnsTheResult(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	m.PutErr = fmt.Errorf("disk full")

	resp := fx.call("run_query", map[string]any{"sql": "SELECT name FROM users"})
	require.Nil(t, resp.Error, errMsg(resp))
	res := mapOf(t, resp.Result)
	assert.Empty(t, res["receipt_id"], "no receipt was kept, so none is cited")
	rows, ok := res["rows"].([][]any)
	require.True(t, ok)
	assert.Len(t, rows, 1)

	rs, _, err := fx.h.RunReadOnlySQLIn(context.Background(), "", "SELECT 1 AS a", nil, 10)
	require.NoError(t, err)
	assert.Empty(t, rs.ReceiptID)
	assert.Len(t, rs.Rows, 1)
}

func TestReceipts_NoRecorderMeansNoReceiptID(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	resp := fx.call("run_query", map[string]any{"sql": "SELECT 1 AS a"})
	require.Nil(t, resp.Error)
	assert.NotContains(t, mapOf(t, resp.Result), "receipt_id")
}

func TestReceipts_RedactionAtTheCallers(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)

	// A credential-looking literal: the text is withheld, only its hash is kept.
	resp := fx.call("run_query", map[string]any{"sql": "SELECT 'password=hunter2' AS note"})
	require.NotEmpty(t, receiptID(t, resp))
	// A credential-looking parameter value: withheld on its own.
	resp = fx.call("run_query", map[string]any{"sql": "SELECT $v AS v, $w AS w", "params": map[string]any{"v": "api_key=sk-abc123def456", "w": "plain"}})
	require.NotEmpty(t, receiptID(t, resp))

	all := m.All()
	require.Len(t, all, 2)
	for _, r := range all {
		b, err := receipts.Encode(r)
		require.NoError(t, err)
		assert.NotContains(t, string(b), "hunter2")
		assert.NotContains(t, string(b), "sk-abc123def456")
	}
	var lit, par receipts.Receipt
	for _, r := range all {
		if r.SQLWithheld {
			lit = r
		} else {
			par = r
		}
	}
	assert.True(t, lit.SQLWithheld)
	assert.Empty(t, lit.SQL)
	assert.True(t, lit.RunSQLWithheld)
	assert.Equal(t, receipts.HashText(receiptKey, "SELECT 'password=hunter2' AS note"), lit.SQLHash)

	assert.Equal(t, receipts.ParamValue{Withheld: true}, par.Params["v"])
	assert.Equal(t, receipts.ParamValue{Value: "plain"}, par.Params["w"])
	assert.Equal(t, receipts.HashParams(receiptKey, map[string]string{"v": "api_key=sk-abc123def456", "w": "plain"}), par.ParamsHash)
}

func TestReceipts_SQLCannotReadTheReceiptsOrTheKey(t *testing.T) {
	fx := newPolicyFixture(t, ModeFull, withAsk(false))
	m := recordTo(fx.h)
	// The receipts folder of the workspace this fixture stands for.
	dir := filepath.Join(fx.dir, "ws", ".slaw-local", "receipts")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "ab"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, receipts.KeyFile), bytes.Repeat([]byte{9}, receipts.KeySize), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ab", "r_abcdef.json"), []byte(`{"id":"r_x"}`), 0o600))

	for _, q := range []string{
		"SELECT * FROM read_text('" + filepath.Join(dir, receipts.KeyFile) + "')",
		"SELECT * FROM read_blob('" + filepath.Join(dir, receipts.KeyFile) + "')",
		"SELECT * FROM read_csv('" + filepath.Join(dir, receipts.KeyFile) + "')",
		"SELECT * FROM read_json('" + filepath.Join(dir, "ab", "r_abcdef.json") + "')",
		"SELECT * FROM read_text('" + filepath.Join(dir, "*", "*.json") + "')",
		"SELECT * FROM glob('" + filepath.Join(dir, "*") + "')",
		"COPY (SELECT 1) TO '" + filepath.Join(dir, "x.csv") + "'",
	} {
		resp := fx.call("run_query", map[string]any{"sql": q})
		require.NotNil(t, resp.Error, q)
		assert.Equal(t, ErrPermission, resp.Error.Code, q)
	}
	assert.Empty(t, m.All())
	_, err := os.Stat(filepath.Join(dir, "x.csv"))
	assert.True(t, os.IsNotExist(err), "nothing was written into the receipts folder")
}

func TestReceipts_AgentSessionDiffersAcrossSessionsOnOneHandler(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	m := recordTo(fx.h)
	one := receipts.WithAgentSession(context.Background(), "sse-session-1")
	two := receipts.WithAgentSession(context.Background(), "sse-session-2")

	id1 := receiptID(t, fx.callCtx(one, "run_query", map[string]any{"sql": "SELECT 1 AS a"}))
	id2 := receiptID(t, fx.callCtx(two, "run_query", map[string]any{"sql": "SELECT 1 AS a"}))
	require.NotEqual(t, id1, id2, "the session is part of the record, so its address")

	r1, err := m.Get(id1)
	require.NoError(t, err)
	r2, err := m.Get(id2)
	require.NoError(t, err)
	assert.Equal(t, "sse-session-1", r1.AgentSession)
	assert.Equal(t, "sse-session-2", r2.AgentSession)
}
