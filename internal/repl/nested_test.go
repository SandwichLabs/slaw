package repl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The console draws a nested value the way the result grid does (#18): a
// MAP as {k=v}, a STRUCT in its declared order, a list element by element.
func TestDisplayCellNested(t *testing.T) {
	assert.Equal(t, "[{'foo': bar}]", displayCell([]any{map[string]any{"foo": "bar"}}, "NULL", `STRUCT("foo" VARCHAR)[]`).text)
	assert.Equal(t, "{'b': 1, 'a': 2}", displayCell(map[string]any{"a": int64(2), "b": int64(1)}, "NULL", `STRUCT("b" INTEGER, "a" INTEGER)`).text)
	assert.Equal(t, "{k=1}", displayCell(map[string]any{"k": int64(1)}, "NULL", "MAP(VARCHAR, INTEGER)").text)
	assert.Equal(t, "[1, NULL]", displayCell([]any{int64(1), nil}, "NULL", "INTEGER[2]").text)
	assert.Equal(t, `{"a":[1,2]}`, displayCell(`{"a":[1,2]}`, "NULL", "JSON").text)
}
