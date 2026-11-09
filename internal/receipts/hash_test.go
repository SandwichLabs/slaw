package receipts

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var cols2 = []Column{{Name: "a", Type: "INTEGER"}, {Name: "b", Type: "VARCHAR"}}

func sum(t *testing.T, key []byte, cols []Column, rows ...[]any) string {
	t.Helper()
	h := NewResultHasher(key, cols)
	for _, r := range rows {
		require.NoError(t, h.AddRow(r))
	}
	return h.Sum()
}

func TestResultHasher_StableAndKeyed(t *testing.T) {
	rows := [][]any{{int64(1), "x"}, {int64(2), "y"}}
	a := sum(t, testKey, cols2, rows...)
	assert.Equal(t, a, sum(t, testKey, cols2, rows...))
	assert.True(t, IsHash(a))
	other := []byte("fedcba9876543210fedcba9876543210")
	assert.NotEqual(t, a, sum(t, other, cols2, rows...), "different keys must give different digests")
}

func TestResultHasher_RowOrderColumnsAndTypesMatter(t *testing.T) {
	r1, r2 := []any{int64(1), "x"}, []any{int64(2), "y"}
	base := sum(t, testKey, cols2, r1, r2)
	assert.NotEqual(t, base, sum(t, testKey, cols2, r2, r1), "row order")
	assert.NotEqual(t, base, sum(t, testKey, cols2, r1), "a missing row")
	assert.NotEqual(t, base, sum(t, testKey, []Column{{Name: "a", Type: "BIGINT"}, {Name: "b", Type: "VARCHAR"}}, r1, r2), "column type")
	assert.NotEqual(t, base, sum(t, testKey, []Column{{Name: "A", Type: "INTEGER"}, {Name: "b", Type: "VARCHAR"}}, r1, r2), "column name")
	assert.NotEqual(t, sum(t, testKey, cols2, []any{int64(1), "x"}), sum(t, testKey, cols2, []any{float64(1), "x"}), "int vs float")
	assert.NotEqual(t, sum(t, testKey, cols2, []any{int64(1), "x"}), sum(t, testKey, cols2, []any{int64(1), []byte("x")}), "string vs bytes")
	assert.NotEqual(t, sum(t, testKey, cols2, []any{nil, "x"}), sum(t, testKey, cols2, []any{"", "x"}), "null vs empty")
}

func TestResultHasher_IsUnambiguous(t *testing.T) {
	two := []Column{{Name: "a", Type: "T"}, {Name: "b", Type: "T"}}
	// Moving a character across a value boundary changes the digest.
	assert.NotEqual(t, sum(t, testKey, two, []any{"ab", "c"}), sum(t, testKey, two, []any{"a", "bc"}))
	// A column boundary is not a row boundary.
	one := []Column{{Name: "a", Type: "T"}}
	assert.NotEqual(t, sum(t, testKey, two, []any{"x", "y"}), sum(t, testKey, one, []any{"x"}, []any{"y"}))
	// Column names do not run into types.
	assert.NotEqual(t,
		sum(t, testKey, []Column{{Name: "ab", Type: "c"}}, []any{"x"}),
		sum(t, testKey, []Column{{Name: "a", Type: "bc"}}, []any{"x"}))
	// Lists and maps.
	assert.NotEqual(t, sum(t, testKey, one, []any{[]any{"a", "b"}}), sum(t, testKey, one, []any{[]any{"ab"}}))
	assert.Equal(t, sum(t, testKey, one, []any{map[string]any{"a": 1, "b": 2}}), sum(t, testKey, one, []any{map[string]any{"b": 2, "a": 1}}))
	assert.Equal(t, sum(t, testKey, one, []any{math.NaN()}), sum(t, testKey, one, []any{math.NaN()}))
}

func TestResultHasher_ValueTypes(t *testing.T) {
	one := []Column{{Name: "v", Type: "T"}}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 7200))
	for _, v := range []any{nil, true, false, int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1),
		uint64(1), float32(1.5), 1.5, "s", []byte("b"), ts, []any{1, "a"}, map[string]any{"k": 1}, struct{ A int }{1}} {
		assert.True(t, IsHash(sum(t, testKey, one, []any{v})), fmt.Sprintf("%T", v))
	}
	assert.Equal(t, sum(t, testKey, one, []any{ts}), sum(t, testKey, one, []any{ts.UTC()}), "the same instant in another zone")
	assert.Equal(t, sum(t, testKey, one, []any{int32(7)}), sum(t, testKey, one, []any{int64(7)}), "integer widths are one type")
}

func TestResultHasher_RefusesARowOfTheWrongWidth(t *testing.T) {
	h := NewResultHasher(testKey, cols2)
	require.Error(t, h.AddRow([]any{int64(1)}))
}

// BenchmarkResultHasher_10kRows20Cols: the target is 10k rows x 20 columns in
// under 15 ms (about 5 ms on a laptop). It does not gate CI.
func BenchmarkResultHasher_10kRows20Cols(b *testing.B) {
	cols := make([]Column, 20)
	for i := range cols {
		cols[i] = Column{Name: fmt.Sprintf("c%d", i), Type: "VARCHAR"}
	}
	rows := make([][]any, 10000)
	ts := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for i := range rows {
		row := make([]any, 20)
		for j := range row {
			switch j % 4 {
			case 0:
				row[j] = int64(i*j + 1)
			case 1:
				row[j] = fmt.Sprintf("value-%d", i)
			case 2:
				row[j] = float64(i) / 7
			default:
				row[j] = ts
			}
		}
		rows[i] = row
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		h := NewResultHasher(testKey, cols)
		for _, r := range rows {
			_ = h.AddRow(r)
		}
		_ = h.Sum()
	}
}
