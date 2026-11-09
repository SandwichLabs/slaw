package receipts

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

// sample is a valid receipt, unsealed; n makes its text (and so its id) unique.
func sample(n int) Receipt {
	sql := "SELECT " + strings.Repeat("x", n%7) + " FROM t WHERE id = $id AND n = " + strconv.Itoa(n)
	h := NewResultHasher(testKey, []Column{{Name: "a", Type: "INTEGER"}})
	_ = h.AddRow([]any{int64(n)})
	return Receipt{
		At:         time.Date(2026, 11, 9, 10, 0, 0, n, time.UTC),
		DurationMS: 12,
		Surface:    SurfaceQueryEditor,
		Actor:      ActorPerson,
		Source:     Source{Kind: SourceSavedQuery, Ref: "queries/orders.sql"},
		SQL:        sql,
		RunSQL:     "SELECT * FROM (" + sql + ") LIMIT 1001",
		SQLHash:    HashText(testKey, sql),
		RunSQLHash: HashRunText(testKey, "run"+sql),
		Params:     map[string]ParamValue{"id": {Value: "42"}},
		ParamsHash: HashParams(testKey, map[string]string{"id": "42"}),
		Connections: []Connection{
			{Name: "warehouse", Type: "postgres", ReadOnly: true, Status: StatusConnected,
				Snapshot: &Snapshot{Fact: "wal_lsn", Value: "0/16B3748", Label: LabelLiveOnly}},
		},
		Verdict:    VerdictUnchecked,
		Rows:       Rows{Returned: 1, Total: 1},
		Columns:    []Column{{Name: "a", Type: "INTEGER"}},
		ResultHMAC: h.Sum(),
	}
}

func sealed(t *testing.T, n int) Receipt {
	t.Helper()
	r, err := Seal(sample(n))
	require.NoError(t, err)
	return r
}
