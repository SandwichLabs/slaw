package receipts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainChecks are test text rules: nothing containing "SECRET" is kept, and a
// parameter value is kept when it is one line.
func plainChecks() Checks {
	return Checks{
		Statement: func(t string) bool { return t != "" && !strings.Contains(t, "SECRET") },
		Value: func(t string) bool {
			return t != "" && !strings.ContainsAny(t, "\n\t") && !strings.Contains(t, "SECRET")
		},
	}
}

func newRec(store Store) *Recorder {
	return NewRecorder(store, testKey, DefaultPolicy(), plainChecks(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func intent() Intent {
	return Intent{
		Surface: SurfaceAgentRunQuery, Actor: ActorAgent, AgentSession: "sess-1",
		Source: Source{Kind: SourceSQL}, SQL: "SELECT $n AS a", Params: map[string]string{"n": "7"}, Verdict: VerdictRead,
	}
}

// ran is a context whose run the engine has filled in, as it does after a read.
func ran(in Intent, stmt StmtKind) context.Context {
	ctx := WithIntent(context.Background(), in)
	RunFrom(ctx).Record(func(f *Facts) {
		f.At = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
		f.Duration = 12 * time.Millisecond
		f.Executed, f.Summary, f.Stmt = "SELECT ? AS a", "SELECT ? AS a", stmt
		f.Columns = []Column{{Name: "a", Type: "INTEGER"}}
		f.Connections = []Connection{{Name: "memory", Type: "duckdb", Status: StatusConnected}}
	})
	return ctx
}

func TestFinish_MintsOneReceiptOverTheRowsShown(t *testing.T) {
	m := NewMock()
	ctx := ran(intent(), StmtSelect)
	rows := [][]any{{int64(7)}, {int64(8)}}
	id, err := Finish(ctx, newRec(m), rows, true, 5)
	require.NoError(t, err)
	require.True(t, ValidID(id))

	r, err := m.Get(id)
	require.NoError(t, err)
	assert.Equal(t, SurfaceAgentRunQuery, r.Surface)
	assert.Equal(t, ActorAgent, r.Actor)
	assert.Equal(t, "sess-1", r.AgentSession)
	assert.Equal(t, "SELECT $n AS a", r.SQL)
	assert.Equal(t, "SELECT ? AS a", r.RunSQL)
	assert.Equal(t, HashText(testKey, "SELECT $n AS a"), r.SQLHash)
	assert.Equal(t, HashRunText(testKey, "SELECT ? AS a"), r.RunSQLHash)
	assert.Equal(t, "7", r.Params["n"].Value)
	assert.Equal(t, HashParams(testKey, map[string]string{"n": "7"}), r.ParamsHash)
	assert.Equal(t, Rows{Returned: 2, Total: 5, Truncated: true}, r.Rows)
	assert.Equal(t, VerdictRead, r.Verdict)
	assert.Equal(t, int64(12), r.DurationMS)
	assert.Equal(t, []Connection{{Name: "memory", Type: "duckdb", Status: StatusConnected}}, r.Connections)

	// result_hmac is the fingerprint of the columns and the rows as shown.
	h := NewResultHasher(testKey, []Column{{Name: "a", Type: "INTEGER"}})
	require.NoError(t, h.AddRow(rows[0]))
	require.NoError(t, h.AddRow(rows[1]))
	assert.Equal(t, h.Sum(), r.ResultHMAC)
	assert.Len(t, m.All(), 1)

	// A run is minted once: a second Finish for it makes nothing.
	id2, err := Finish(ctx, newRec(m), rows, false, 2)
	require.NoError(t, err)
	assert.Empty(t, id2)
	assert.Len(t, m.All(), 1)
}

func TestFinish_TotalIsAtLeastTheRowsShown(t *testing.T) {
	m := NewMock()
	id, err := Finish(ran(intent(), StmtSelect), newRec(m), [][]any{{int64(1)}, {int64(2)}}, false, 0)
	require.NoError(t, err)
	r, err := m.Get(id)
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.Rows.Total)
}

func TestFinish_MintsNothingWithoutAnIntentARecorderOrARead(t *testing.T) {
	m := NewMock()
	rows := [][]any{{int64(1)}}

	id, err := Finish(context.Background(), newRec(m), rows, false, 1)
	assert.Empty(t, id)
	assert.NoError(t, err)

	id, err = Finish(ran(intent(), StmtSelect), nil, rows, false, 1)
	assert.Empty(t, id)
	assert.NoError(t, err)

	// An intent the engine never filled (no run happened) and a statement that is
	// not a select or an explain mint nothing.
	id, err = Finish(WithIntent(context.Background(), intent()), newRec(m), rows, false, 1)
	assert.Empty(t, id)
	assert.NoError(t, err)
	id, err = Finish(ran(intent(), StmtOther), newRec(m), rows, false, 1)
	assert.Empty(t, id)
	assert.NoError(t, err)
	assert.Empty(t, m.All())

	// An explain is minted.
	id, err = Finish(ran(intent(), StmtExplain), newRec(m), rows, false, 1)
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestFinish_AFailingStoreIsReportedAndKeepsNothing(t *testing.T) {
	m := NewMock()
	m.PutErr = errors.New("disk full")
	id, err := Finish(ran(intent(), StmtSelect), newRec(m), [][]any{{int64(1)}}, false, 1)
	assert.Empty(t, id)
	assert.Error(t, err)
	assert.Empty(t, m.All())
}

func TestFinish_AWrongWidthRowMintsNothing(t *testing.T) {
	m := NewMock()
	id, err := Finish(ran(intent(), StmtSelect), newRec(m), [][]any{{int64(1), int64(2)}}, false, 1)
	assert.Empty(t, id)
	assert.Error(t, err)
	assert.Empty(t, m.All())
}

func TestFinish_Redaction(t *testing.T) {
	m := NewMock()
	in := intent()
	in.SQL = "SELECT 'SECRET' AS a"
	in.Params = map[string]string{"token": "SECRET-value", "multi": "a\nb", "empty": "", "ok": "fine"}
	ctx := ran(in, StmtSelect)
	RunFrom(ctx).Record(func(f *Facts) { f.Summary = "SELECT 'SECRET' AS a" })
	id, err := Finish(ctx, newRec(m), [][]any{{"x"}}, false, 1)
	require.NoError(t, err)
	r, err := m.Get(id)
	require.NoError(t, err)

	// A text that fails the check keeps only its keyed hash.
	assert.True(t, r.SQLWithheld)
	assert.Empty(t, r.SQL)
	assert.Equal(t, HashText(testKey, in.SQL), r.SQLHash)
	assert.True(t, r.RunSQLWithheld)
	assert.Empty(t, r.RunSQL)
	assert.Equal(t, HashRunText(testKey, "SELECT ? AS a"), r.RunSQLHash)

	// A value that fails its check is withheld on its own; the rest are kept.
	assert.Equal(t, ParamValue{Withheld: true}, r.Params["token"])
	assert.Equal(t, ParamValue{Withheld: true}, r.Params["multi"])
	assert.Equal(t, ParamValue{}, r.Params["empty"])
	assert.Equal(t, ParamValue{Value: "fine"}, r.Params["ok"])
	// params_hash is over the full values, withheld or not.
	assert.Equal(t, HashParams(testKey, in.Params), r.ParamsHash)

	b, err := Encode(r)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "SECRET")
}

func TestFinish_ARecorderWithoutChecksWithholdsEverything(t *testing.T) {
	m := NewMock()
	rec := NewRecorder(m, testKey, DefaultPolicy(), Checks{}, nil)
	id, err := Finish(ran(intent(), StmtSelect), rec, [][]any{{int64(1)}}, false, 1)
	require.NoError(t, err)
	r, err := m.Get(id)
	require.NoError(t, err)
	assert.True(t, r.SQLWithheld)
	assert.True(t, r.RunSQLWithheld)
	assert.Equal(t, ParamValue{Withheld: true}, r.Params["n"])
}

func TestFinish_KeepParamsOffAndPolicyChange(t *testing.T) {
	m := NewMock()
	rec := newRec(m)
	p := DefaultPolicy()
	p.KeepParams = false
	rec.SetPolicy(p)
	id, err := Finish(ran(intent(), StmtSelect), rec, [][]any{{int64(1)}}, false, 1)
	require.NoError(t, err)
	r, err := m.Get(id)
	require.NoError(t, err)
	assert.True(t, r.ParamsWithheld)
	assert.Equal(t, ParamValue{}, r.Params["n"])
	assert.NotEmpty(t, r.ParamsHash)
}

func TestFinish_AnInvalidSourceRefIsDroppedNotKept(t *testing.T) {
	m := NewMock()
	in := intent()
	in.Source = Source{Kind: SourceSavedQuery, Ref: "/home/someone/queries/x.sql"}
	id, err := Finish(ran(in, StmtSelect), newRec(m), [][]any{{int64(1)}}, false, 1)
	require.NoError(t, err)
	r, err := m.Get(id)
	require.NoError(t, err)
	assert.Equal(t, Source{Kind: SourceSavedQuery}, r.Source)
}

func TestFinish_OnlyAnAgentHasASession(t *testing.T) {
	m := NewMock()
	in := intent()
	in.Actor = ActorPerson // a person's surface never carries a session
	id, err := Finish(ran(in, StmtSelect), newRec(m), [][]any{{int64(1)}}, false, 1)
	assert.Empty(t, id)
	assert.Error(t, err)
}

func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, RunFrom(ctx))
	assert.Equal(t, Origin{Surface: SurfaceRender}, OriginFrom(ctx))
	o := Origin{Surface: SurfaceArtifact, Source: Source{Kind: SourceArtifactQuery, Ref: "artifacts/a#q"}}
	assert.Equal(t, o, OriginFrom(WithOrigin(ctx, o)))
	assert.True(t, OriginFrom(WithOrigin(ctx, Origin{Skip: true})).Skip)

	assert.Empty(t, AgentSessionFrom(ctx))
	assert.Equal(t, "s1", AgentSessionFrom(WithAgentSession(ctx, "s1")))
	assert.NotEqual(t, NewSessionID(), NewSessionID())

	// A nil Run records nothing and does not panic.
	var run *Run
	run.Record(func(f *Facts) { f.Executed = "x" })
	assert.Equal(t, Facts{}, run.Facts())
}

func TestParamStrings(t *testing.T) {
	assert.Nil(t, ParamStrings(nil))
	got := ParamStrings(map[string]any{"s": "x", "f": 1000000.0, "i": 7, "b": true, "n": nil, "d": 1.5})
	assert.Equal(t, map[string]string{"s": "x", "f": "1000000", "i": "7", "b": "true", "n": "NULL", "d": "1.5"}, got)
}
