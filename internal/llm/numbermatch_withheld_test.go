package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// verdictText is the three-way state of a mark.
func verdictText(m NumberMark) string {
	switch {
	case m.Matched:
		return "matched"
	case m.Withheld:
		return "withheld"
	}
	return "unmatched"
}

// A number a shown cell holds, whose check a guard keeps back, is left unmarked;
// only a number in no shown cell is "not from a query".
func TestMarkNumbers_WithheldIsNotUnmatched(t *testing.T) {
	// 20 rows: region number 121 and 233 are shown but not specific.
	rows := make([][]any, 0, 20)
	for i := 0; i < 20; i++ {
		rows = append(rows, []any{i + 1000, 100 + i*7})
	}
	rows[3][1], rows[9][1] = 121, 233
	long := RunValues{ReceiptID: "r_long", Columns: []string{"k", "units"}, Rows: rows}
	marks := MarkNumbers("R3 sold 121 units; the top region was 233, not 4,567.", []RunValues{long})
	assert.Equal(t, "withheld", verdictText(markOf(t, marks, "121")), "long read, not specific")
	assert.Equal(t, "withheld", verdictText(markOf(t, marks, "233")))
	assert.Equal(t, "unmatched", verdictText(markOf(t, marks, "4,567")), "in no cell")

	// A duplicated value in a short read.
	dup := run("r_s", []string{"a", "b"}, []any{4321, "x"}, []any{4321, "y"}, []any{7, "z"})
	assert.Equal(t, "withheld", verdictText(markOf(t, MarkNumbers("4,321", []RunValues{dup}), "4,321")))

	// An identifier column's value.
	ident := run("r_i", []string{"order_id", "total"}, []any{482913, 1.5}, []any{19, 2.5})
	assert.Equal(t, "withheld", verdictText(markOf(t, MarkNumbers("482,913", []RunValues{ident}), "482,913")))

	// The probe: an id column 1..300 and a long read. Nothing is matched; 100%
	// (no cell is 100 or a fraction of 1) is the only number in no cell.
	probe := longRun(300, func(i int) []any { return []any{i % 250, float64(i%100) / 100, 80 + i%20} })
	pm := MarkNumbers("average 214 units, about 45% of capacity, $87 per order, 750% done", []RunValues{probe})
	for _, m := range pm {
		assert.False(t, m.Matched, m.Text)
	}
	assert.Equal(t, "unmatched", verdictText(markOf(t, pm, "750%")))
	assert.Equal(t, "withheld", verdictText(markOf(t, pm, "214")))
	assert.Equal(t, "withheld", verdictText(markOf(t, pm, "45%")))

	// A vouching read still wins over a withheld one, in either order.
	one := run("r_ok", []string{"a"}, []any{4321}, []any{7})
	assert.Equal(t, "matched", verdictText(markOf(t, MarkNumbers("4,321", []RunValues{dup, one}), "4,321")))
	assert.Equal(t, "matched", verdictText(markOf(t, MarkNumbers("4,321", []RunValues{one, dup}), "4,321")))
}
