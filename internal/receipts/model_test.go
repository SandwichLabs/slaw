package receipts

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pinnedID = "r_17fa5373805788255e4f"

func TestSeal_IDIsStableAndContentAddressed(t *testing.T) {
	a := sealed(t, 1)
	b := sealed(t, 1)
	assert.Equal(t, a.ID, b.ID)
	assert.True(t, ValidID(a.ID), a.ID)

	// The same instant in another zone is the same record.
	r := Receipt{
		At: time.Date(2026, 11, 9, 10, 0, 0, 0, time.FixedZone("x", 3600)), Surface: SurfaceCLIQuery, Actor: ActorPerson,
		Verdict: VerdictUnchecked, ResultHMAC: strings.Repeat("0", 64),
	}
	s, err := Seal(r)
	require.NoError(t, err)
	r.At = r.At.UTC()
	again, err := Seal(r)
	require.NoError(t, err)
	assert.Equal(t, s.ID, again.ID)
	// A pinned value: the id must not drift between runs, releases or machines.
	assert.Equal(t, pinnedID, s.ID, "the canonical encoding or the id rule changed")

	c := sample(1)
	c.DurationMS++
	changed, err := Seal(c)
	require.NoError(t, err)
	assert.NotEqual(t, a.ID, changed.ID)
	require.NoError(t, a.Validate())
}

func TestValidate_RefusesATamperedID(t *testing.T) {
	r := sealed(t, 2)
	r.DurationMS = 999
	require.ErrorIs(t, r.Validate(), ErrInvalid)
	r = sealed(t, 2)
	r.ID = "r_00000000000000000000"
	require.ErrorIs(t, r.Validate(), ErrInvalid)
}

func TestValidate_RefusesBadEnumsAndRefs(t *testing.T) {
	cases := map[string]func(*Receipt){
		"surface":             func(r *Receipt) { r.Surface = "somewhere" },
		"actor":               func(r *Receipt) { r.Actor = "robot" },
		"verdict":             func(r *Receipt) { r.Verdict = "approved" },
		"source kind":         func(r *Receipt) { r.Source.Kind = "file" },
		"conn status":         func(r *Receipt) { r.Connections[0].Status = "connection refused: host 10.0.0.5" },
		"snapshot label":      func(r *Receipt) { r.Connections[0].Snapshot.Label = "fresh" },
		"absolute ref":        func(r *Receipt) { r.Source.Ref = "/home/zac/queries/x.sql" },
		"home ref":            func(r *Receipt) { r.Source.Ref = "~/x.sql" },
		"drive ref":           func(r *Receipt) { r.Source.Ref = `C:/x.sql` },
		"dotdot ref":          func(r *Receipt) { r.Source.Ref = "queries/../../x.sql" },
		"backslash ref":       func(r *Receipt) { r.Source.Ref = `queries\x.sql` },
		"control ref":         func(r *Receipt) { r.Source.Ref = "queries/x\n.sql" },
		"long ref":            func(r *Receipt) { r.Source.Ref = strings.Repeat("a", MaxRefBytes+1) },
		"session on a person": func(r *Receipt) { r.AgentSession = "s1" },
		"bad hmac":            func(r *Receipt) { r.ResultHMAC = "abc" },
		"upper hash":          func(r *Receipt) { r.SQLHash = strings.ToUpper(r.SQLHash) },
		"sql no hash":         func(r *Receipt) { r.SQLHash = "" },
		"withheld text":       func(r *Receipt) { r.SQLWithheld = true },
		"params no hash":      func(r *Receipt) { r.ParamsHash = "" },
		"long param":          func(r *Receipt) { r.Params["id"] = ParamValue{Value: strings.Repeat("a", MaxParamBytes+1)} },
		"rows":                func(r *Receipt) { r.Rows = Rows{Returned: 5, Total: 1} },
		"long sql":            func(r *Receipt) { r.SQL = strings.Repeat("a", MaxTextBytes+1) },
		"negative time":       func(r *Receipt) { r.DurationMS = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := sample(3)
			mutate(&r)
			_, err := Seal(r)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	for _, ref := range []string{"", "queries/x.sql", "artifacts/a#by_month", "notebooks/n.slnb#cell-3"} {
		assert.True(t, ValidRef(ref), ref)
	}
}

// The redaction test on the record (R1): what the model keeps for text and
// values that a caller withheld, and what it can never hold.
func TestRedaction_WithheldTextKeepsOnlyItsHash(t *testing.T) {
	r := sample(4)
	secret := "SELECT 'hunter2-not-a-real-secret'"
	r.SQL, r.SQLWithheld, r.SQLHash = "", true, HashText(secret)
	r.RunSQL, r.RunSQLWithheld, r.RunSQLHash = "", true, HashText(secret+" LIMIT 1")
	s, err := Seal(r)
	require.NoError(t, err)
	b, err := Encode(s)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "hunter2")
	assert.Contains(t, string(b), HashText(secret))

	// A withheld text that still carries the text is refused, not stored.
	r.SQL = secret
	_, err = Seal(r)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestRedaction_WithheldParamKeepsOnlyItsName(t *testing.T) {
	r := sample(5)
	r.Params = map[string]ParamValue{"token": {Withheld: true}, "id": {Value: "7"}}
	r.ParamsHash = HashParams(map[string]string{"token": "tok-not-real", "id": "7"})
	s, err := Seal(r)
	require.NoError(t, err)
	b, _ := Encode(s)
	assert.NotContains(t, string(b), "tok-not-real")
	assert.Contains(t, string(b), `"token":{"withheld":true}`)

	r.Params["token"] = ParamValue{Value: "tok-not-real", Withheld: true}
	_, err = Seal(r)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestRedaction_StatusIsAnEnumNeverAMessage(t *testing.T) {
	for _, st := range []ConnStatus{StatusConnected, StatusNotConnected, StatusError} {
		r := sample(6)
		r.Connections[0].Status = st
		_, err := Seal(r)
		require.NoError(t, err, st)
	}
	r := sample(6)
	r.Connections[0].Status = "could not connect to db.internal.example:5432"
	_, err := Seal(r)
	require.ErrorIs(t, err, ErrInvalid)
	// And a stored file carrying a message is refused on read as well.
	good := sealed(t, 6)
	b, err := Encode(good)
	require.NoError(t, err)
	bad := strings.Replace(string(b), `"status":"connected"`, `"status":"could not connect to db.internal.example"`, 1)
	_, err = Decode([]byte(bad))
	require.ErrorIs(t, err, ErrInvalid)
}

func TestDecode_Strict(t *testing.T) {
	good := sealed(t, 8)
	b, err := Encode(good)
	require.NoError(t, err)
	got, err := Decode(b)
	require.NoError(t, err)
	assert.Equal(t, good, got)

	_, err = Decode([]byte(strings.Replace(string(b), `"duration_ms"`, `"extra":1,"duration_ms"`, 1)))
	require.ErrorIs(t, err, ErrInvalid, "an unknown field")
	_, err = Decode(append(append([]byte{}, b...), []byte(`{"id":"x"}`)...))
	require.ErrorIs(t, err, ErrInvalid, "a second value")
	_, err = Decode(append(append([]byte{}, b...), []byte("garbage")...))
	require.ErrorIs(t, err, ErrInvalid, "trailing data")
	_, err = Decode(append(append([]byte{}, b...), make([]byte, MaxFileBytes)...))
	require.ErrorIs(t, err, ErrInvalid, "an oversize file")
	_, err = Decode([]byte(`{`))
	require.ErrorIs(t, err, ErrInvalid)
}

func TestEncode_NeverHoldsARowValue(t *testing.T) {
	r := sample(9)
	h := NewResultHasher(testKey, r.Columns)
	require.NoError(t, h.AddRow([]any{int64(987654321)}))
	r.ResultHMAC = h.Sum()
	s, err := Seal(r)
	require.NoError(t, err)
	b, _ := Encode(s)
	assert.NotContains(t, string(b), "987654321")
}
