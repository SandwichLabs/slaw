package llm

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// longRun is a read of n rows: an id (1..n), and a few measures.
func longRun(n int, measures func(i int) []any) RunValues {
	rows := make([][]any, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, append([]any{i}, measures(i)...))
	}
	return RunValues{ReceiptID: "r_long", Columns: []string{"id", "units", "share", "price"}, Rows: rows}
}

// The review's probe: on a 300-row read with an id column 1..300 the numbers of
// "average 214 units, about 45% of capacity, $87 per order" are in the result by
// chance. None of them may get a check.
func TestMarkNumbers_ProbeAnIDColumnAndALongReadVouchForNothing(t *testing.T) {
	rv := longRun(300, func(i int) []any { return []any{i % 250, float64(i%100) / 100, 80 + i%20} })
	marks := MarkNumbers("average 214 units, about 45% of capacity, $87 per order, and 100% of ids", []RunValues{rv})
	require.NotEmpty(t, marks)
	for _, m := range marks {
		assert.False(t, m.Matched, m.Text)
	}
}

func TestMarkNumbers_ASpecificUniqueValueInALongReadMatches(t *testing.T) {
	rv := longRun(300, func(i int) []any {
		if i == 77 {
			return []any{1843210, 0.0325, 12.47}
		}
		return []any{i, 0.5, 3.5}
	})
	marks := MarkNumbers("Total 1,843,210 at 3.25% and 12.47 each.", []RunValues{rv})
	for _, tx := range []string{"1,843,210", "3.25%", "12.47"} {
		m := markOf(t, marks, tx)
		assert.True(t, m.Matched, tx)
		assert.Equal(t, "r_long", m.ReceiptID)
	}
	assert.Equal(t, "units", markOf(t, marks, "1,843,210").Column)
}

func TestMarkNumbers_ANonUniqueValueIsNotMarked(t *testing.T) {
	// 1,843,210 appears twice in a long read; 4,321 twice in a short one.
	long := longRun(300, func(i int) []any {
		if i == 10 || i == 20 {
			return []any{1843210, 0.5, 3.5}
		}
		return []any{i + 1000000, 0.5, 3.5}
	})
	assert.False(t, markOf(t, MarkNumbers("1,843,210", []RunValues{long}), "1,843,210").Matched)
	short := run("r_s", []string{"a", "b"}, []any{4321, "x"}, []any{4321, "y"}, []any{7, "z"})
	assert.False(t, markOf(t, MarkNumbers("4,321", []RunValues{short}), "4,321").Matched)
	// The same value shown once is marked.
	one := run("r_s", []string{"a"}, []any{4321}, []any{7})
	assert.True(t, markOf(t, MarkNumbers("4,321", []RunValues{one}), "4,321").Matched)
	// A read that is ambiguous does not stop a later read that is not.
	m := markOf(t, MarkNumbers("4,321", []RunValues{short, one}), "4,321")
	assert.True(t, m.Matched)
}

func TestMarkNumbers_ShortReadsKeepShortNumbers(t *testing.T) {
	rv := run("r_a", []string{"n", "share"}, []any{214, 0.45}, []any{7.5, 0.9})
	marks := MarkNumbers("214 and 45% and 90%", []RunValues{rv})
	for _, tx := range []string{"214", "45%", "90%"} {
		assert.True(t, markOf(t, marks, tx).Matched, tx)
	}
}

func TestMarkNumbers_HundredPercentIsNotACellOfOne(t *testing.T) {
	rv := run("r_a", []string{"flag", "ratio"}, []any{1, 0.5}, []any{0, 0.25})
	marks := MarkNumbers("100% and 50% and 25%", []RunValues{rv})
	assert.False(t, markOf(t, marks, "100%").Matched, "a cell of 1 is not 100%")
	assert.True(t, markOf(t, marks, "50%").Matched, "0.5 is a fraction")
	// An integer cell reads as written only: 5 is 5%, never 500%.
	assert.True(t, markOf(t, MarkNumbers("5.0%", []RunValues{run("r_b", []string{"pct"}, []any{5.0}, []any{6.5})}), "5.0%").Matched)
	assert.False(t, markOf(t, MarkNumbers("500%", []RunValues{run("r_b", []string{"v"}, []any{5}, []any{9})}), "500%").Matched)
}

func TestMarkNumbers_IdentifierColumnsAreNeverMatched(t *testing.T) {
	for _, col := range []string{"id", "ID", "order_id", "Customer_Key", "row_pk"} {
		rv := run("r_a", []string{col, "x"}, []any{482913, 1.5}, []any{19, 2.5})
		assert.False(t, markOf(t, MarkNumbers("482,913", []RunValues{rv}), "482,913").Matched, col)
	}
	// Not a name rule: "paid" and "pkg" are measures.
	rv := run("r_a", []string{"paid", "pkg"}, []any{482913, 1}, []any{19, 2})
	assert.True(t, markOf(t, MarkNumbers("482,913", []RunValues{rv}), "482,913").Matched)
	// A distinct consecutive integer column is a row number whatever it is called.
	rn := run("r_a", []string{"n", "v"}, []any{1000, 5.5}, []any{1001, 6.5}, []any{1002, 7.5})
	assert.False(t, markOf(t, MarkNumbers("1,001", []RunValues{rn}), "1,001").Matched)
	// Two rows, a gap, a repeat or a fraction is not a sequence.
	assert.False(t, consecutiveInts([][]any{{1000}, {1001}, {1003}}, 0))
	assert.False(t, consecutiveInts([][]any{{1000}, {1001}, {1001}}, 0))
	assert.False(t, consecutiveInts([][]any{{1000}, {1001}}, 0), "two rows are not a sequence")
	assert.False(t, consecutiveInts([][]any{{1.5}, {2.5}, {3.5}}, 0))
	assert.True(t, consecutiveInts([][]any{{3}, {1}, {2}}, 0))
}

// 20 reads of 1,000 rows by 4 columns against 200 distinct numbers: the cells are
// converted once per answer, not once per number.
func BenchmarkMarkNumbers(b *testing.B) {
	runs, answer := benchmarkInput()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MarkNumbers(answer, runs)
	}
}

func TestMarkNumbers_ABigAnswerIsFast(t *testing.T) {
	runs, answer := benchmarkInput()
	start := time.Now()
	marks := MarkNumbers(answer, runs)
	took := time.Since(start)
	assert.Len(t, marks, 200)
	// A loose bound so a loaded CI runner does not flake; BenchmarkMarkNumbers
	// tracks the real cost (about 30 ms).
	assert.Less(t, took, 2*time.Second)
}

func benchmarkInput() ([]RunValues, string) {
	runs := make([]RunValues, 0, 20)
	for r := 0; r < 20; r++ {
		rows := make([][]any, 0, 1000)
		for i := 0; i < 1000; i++ {
			rows = append(rows, []any{i, float64(i*7+r) / 3, int64(i * (r + 2)), fmt.Sprintf("%d.%02d", i, r)})
		}
		runs = append(runs, RunValues{ReceiptID: fmt.Sprintf("r_%020d", r), Columns: []string{"id", "a", "b", "c"}, Rows: rows})
	}
	answer := ""
	for n := 0; n < 200; n++ {
		answer += fmt.Sprintf("%d.5%d ", 90000+n, n%10)
	}
	return runs, answer
}
