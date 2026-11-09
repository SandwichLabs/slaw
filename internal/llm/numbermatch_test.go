package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tokenizer is mirrored in the panel (lib/numberMarks.ts); both read this
// file, so they cannot drift apart.
func TestNumbers_SharedTokenVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/number_tokens.json")
	require.NoError(t, err)
	var cases []struct {
		Text   string   `json:"text"`
		Tokens []string `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	require.NotEmpty(t, cases)
	for _, c := range cases {
		got := Numbers(c.Text)
		if len(c.Tokens) == 0 {
			assert.Empty(t, got, c.Text)
			continue
		}
		assert.Equal(t, c.Tokens, got, c.Text)
	}
}

func TestNumbers_NeverReadsCodeLinksOrListMarkers(t *testing.T) {
	text := "Total 1,234 here.\n" +
		"1. first 55\n" +
		"```sql\nSELECT 98765 FROM t\n```\n" +
		"inline `4,321` and ``9,876`` done, see [the 8,888 docs](https://x.test/p/77777) or <https://x.test/66666> or https://x.test/55555 and 42.\n" +
		"~~~\n31,337\n~~~\n" +
		"10. ten and 11,111"
	assert.Equal(t, []string{"1,234", "55", "42", "11,111"}, Numbers(text), "link text, like link targets, is not decorated")
}

func run(id string, cols []string, rows ...[]any) RunValues {
	return RunValues{ReceiptID: id, Columns: cols, Rows: rows}
}

func markOf(t *testing.T, marks []NumberMark, text string) NumberMark {
	t.Helper()
	for _, m := range marks {
		if m.Text == text {
			return m
		}
	}
	require.Failf(t, "no mark", "no mark for %q in %v", text, marks)
	return NumberMark{}
}

func TestMarkNumbers_ExactAndFormatted(t *testing.T) {
	rv := run("r_a", []string{"revenue", "orders", "rate"}, []any{int64(1234567), int64(1234), 0.125})
	marks := MarkNumbers("Revenue 1,234,567 from 1234 orders, a rate of 12.5% and $1,234,567.", []RunValues{rv})
	for _, tx := range []string{"1,234,567", "1234", "12.5%", "$1,234,567"} {
		m := markOf(t, marks, tx)
		assert.True(t, m.Matched, tx)
		assert.Equal(t, "r_a", m.ReceiptID)
	}
	assert.Equal(t, "revenue", markOf(t, marks, "1,234,567").Column)
	assert.Equal(t, "rate", markOf(t, marks, "12.5%").Column)
}

func TestMarkNumbers_RoundedWithinTolerance(t *testing.T) {
	rv := run("r_a", []string{"v"}, []any{1234567.0}, []any{12.46}, []any{0.4549}, []any{"2500000"})
	marks := MarkNumbers("About 1.2M and 1.23M, 12.5, 45%, $2.5M.", []RunValues{rv})
	assert.True(t, markOf(t, marks, "1.2M").Matched, "1,234,567 shown as 1.2M")
	assert.True(t, markOf(t, marks, "1.23M").Matched)
	assert.True(t, markOf(t, marks, "12.5").Matched, "12.46 rounds to 12.5")
	assert.True(t, markOf(t, marks, "45%").Matched, "0.4549 as a percent rounds to 45%")
	assert.True(t, markOf(t, marks, "$2.5M").Matched, "text cell 2500000")
}

func TestMarkNumbers_OutsideToleranceIsNotMatched(t *testing.T) {
	rv := run("r_a", []string{"v"}, []any{1234567.0}, []any{12.46}, []any{0.4549}, []any{2500000})
	marks := MarkNumbers("1.3M and 12.6 and 46% and 2M and 1,235 and 12.4", []RunValues{rv})
	for _, tx := range []string{"1.3M", "12.6", "46%", "2M", "1,235", "12.4"} {
		assert.False(t, markOf(t, marks, tx).Matched, tx)
	}
}

func TestMarkNumbers_TheSuffixToleranceIsCappedAtFivePercent(t *testing.T) {
	// "1M" rounds anything from 500k up, but only a value within 5% is the figure.
	rv := run("r_a", []string{"v"}, []any{1400000}, []any{980000})
	marks := MarkNumbers("1M and 1.4M", []RunValues{rv})
	assert.True(t, markOf(t, marks, "1M").Matched, "980,000 is within 5% of 1M")
	rv2 := run("r_b", []string{"v"}, []any{1400000})
	assert.False(t, markOf(t, MarkNumbers("1M", []RunValues{rv2}), "1M").Matched, "1.4M is not 1M")
}

func TestMarkNumbers_SignMustAgree(t *testing.T) {
	rv := run("r_a", []string{"v"}, []any{-5.0}, []any{12})
	marks := MarkNumbers("-5 and 5% and -12 and -5%", []RunValues{rv})
	assert.False(t, markOf(t, marks, "5%").Matched, "positive 5% is not -5")
	assert.False(t, markOf(t, marks, "-12").Matched)
	assert.True(t, markOf(t, marks, "-5%").Matched)
}

func TestMarkNumbers_OnlyTheShownRowsOfThisTurnsRuns(t *testing.T) {
	// A value that another turn's read held is not in this turn's runs.
	mine := run("r_new", []string{"v"}, []any{111222})
	marks := MarkNumbers("987,654 and 111,222", []RunValues{mine})
	assert.False(t, markOf(t, marks, "987,654").Matched)
	assert.True(t, markOf(t, marks, "111,222").Matched)
	assert.Equal(t, "r_new", markOf(t, marks, "111,222").ReceiptID)
	// And no runs at all: nothing is matched, every number is "not from a query".
	for _, m := range MarkNumbers("987,654", nil) {
		assert.False(t, m.Matched)
	}
}

func TestMarkNumbers_FirstRunThatShowedItWins(t *testing.T) {
	a := run("r_a", []string{"x"}, []any{777777})
	b := run("r_b", []string{"y"}, []any{777777})
	m := markOf(t, MarkNumbers("777,777", []RunValues{a, b}), "777,777")
	assert.Equal(t, "r_a", m.ReceiptID)
}

func TestMarkNumbers_NumbersInCodeAreNotMarked(t *testing.T) {
	rv := run("r_a", []string{"v"}, []any{98765})
	marks := MarkNumbers("```\nSELECT 98765\n```\nand `98765` and [98765](https://x.test/98765)", []RunValues{rv})
	assert.Empty(t, marks)
}

func TestMarkNumbers_SmallTwoDigitNumbersNeedAShortResult(t *testing.T) {
	short := run("r_a", []string{"n"}, []any{42})
	rows := make([][]any, 0, 30)
	for i := 0; i < 30; i++ {
		rows = append(rows, []any{i + 20})
	}
	long := RunValues{ReceiptID: "r_b", Columns: []string{"n"}, Rows: rows}
	assert.True(t, markOf(t, MarkNumbers("42 customers", []RunValues{short}), "42").Matched)
	assert.False(t, markOf(t, MarkNumbers("42 customers", []RunValues{long}), "42").Matched, "a 30-row result always has some 42")
	// Written with formatting, the same value is matched in any result.
	assert.True(t, markOf(t, MarkNumbers("42.0 average", []RunValues{long}), "42.0").Matched)
}

func TestMarkNumbers_UnmarkedKindsAndNonNumericCells(t *testing.T) {
	rv := run("r_a", []string{"a", "b", "c", "d"}, []any{true, nil, "abc", "1/2"}, []any{"x1,234", []byte("12345"), "  7654321  ", struct{}{}})
	marks := MarkNumbers("It was 3, in 2024, 007, 12345 and 7,654,321 and 1/2", []RunValues{rv})
	// 3, 2024 and 007 are never marked; 12345 matches nothing (bytes are not a number).
	assert.Len(t, marks, 2)
	assert.False(t, markOf(t, marks, "12345").Matched)
	assert.True(t, markOf(t, marks, "7,654,321").Matched, "a text cell that is wholly a decimal number counts, trimmed")
}

func TestMarkNumbers_DistinctTextOnceAndCapped(t *testing.T) {
	marks := MarkNumbers("1,000 then 1,000 again", nil)
	assert.Len(t, marks, 1)
	var words []string
	for i := 0; i < 300; i++ {
		words = append(words, fmt.Sprintf("$%d", 10000+i))
	}
	assert.Len(t, MarkNumbers(strings.Join(words, " "), nil), maxMarks)
}

func TestRunValuesFrom(t *testing.T) {
	rv, ok := RunValuesFrom(map[string]any{"receipt_id": "r_1", "columns": []string{"a", "b"}, "rows": [][]any{{1, 2}}})
	require.True(t, ok)
	assert.Equal(t, RunValues{ReceiptID: "r_1", Columns: []string{"a", "b"}, Rows: [][]any{{1, 2}}}, rv)

	// preview_query: column objects and row objects keyed by name.
	rv, ok = RunValuesFrom(map[string]any{"receipt_id": "r_2",
		"columns": []map[string]any{{"name": "a", "type": "INTEGER"}, {"name": "b", "type": "INTEGER"}},
		"rows":    []map[string]any{{"b": 2, "a": 1}}})
	require.True(t, ok)
	assert.Equal(t, []any{1, 2}, rv.Rows[0])

	_, ok = RunValuesFrom(map[string]any{"columns": []string{"a"}, "rows": [][]any{{1}}})
	assert.False(t, ok, "no receipt, no run")
	_, ok = RunValuesFrom("text")
	assert.False(t, ok)
}
