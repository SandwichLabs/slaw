package crud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

// duckdb-go cannot scan a VARIANT column (#388): the datasheet's page casts it
// to JSON in its one query (no failed run, no retry), keeps the column's VARIANT type, and the other columns page as usual.
func TestPage_VariantColumnPagesAsJSON(t *testing.T) {
	db := hubWithOther(t)
	_, err := db.Exec(`CREATE TABLE events (id INTEGER, payload VARIANT, tags VARCHAR[])`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO events VALUES (1, 42::VARIANT, ['a']), (2, {'a': [1, 2]}::VARIANT, NULL), (3, NULL, ['b', 'c'])`)
	require.NoError(t, err)

	res, err := Page(t.Context(), db, session.TableRef{Name: "events"}, PageRequest{SortCol: "id"})
	require.NoError(t, err)
	assert.Equal(t, []string{"id", "payload", "tags"}, res.Columns)
	assert.Equal(t, "VARIANT", res.Types[1])
	assert.Equal(t, 3, res.TotalRows)
	require.Len(t, res.Rows, 3)
	assert.EqualValues(t, 42, res.Rows[0][1])
	assert.Equal(t, map[string]any{"a": []any{float64(1), float64(2)}}, res.Rows[1][1])
	assert.Nil(t, res.Rows[2][1])
	assert.Equal(t, []any{"b", "c"}, res.Rows[2][2])
}
