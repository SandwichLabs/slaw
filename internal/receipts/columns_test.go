package receipts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func finishWithColumns(t *testing.T, in Intent, cols []Column, fromData bool) Receipt {
	t.Helper()
	m := NewMock()
	ctx := ran(in, StmtSelect)
	RunFrom(ctx).Record(func(f *Facts) { f.Columns, f.NamesFromData = cols, fromData })
	row := make([]any, len(cols))
	id, err := Finish(ctx, newRec(m), [][]any{row}, false, 1)
	require.NoError(t, err)
	r, err := m.Get(id)
	require.NoError(t, err)
	return r
}

func TestColumns_ANameThatFailsTheCheckIsWithheldAlone(t *testing.T) {
	r := finishWithColumns(t, intent(), []Column{{Name: "a", Type: "INTEGER"}, {Name: "SECRET-expr", Type: "VARCHAR"}, {Name: "", Type: "BIGINT"}}, false)
	assert.Equal(t, []Column{
		{Name: "a", Type: "INTEGER"},
		{Type: "VARCHAR", Withheld: true},
		{Type: "BIGINT"},
	}, r.Columns)
}

func TestColumns_AWithheldTextWithholdsEveryName(t *testing.T) {
	in := intent()
	in.SQL = "SELECT 'SECRET'"
	r := finishWithColumns(t, in, []Column{{Name: "a", Type: "INTEGER"}, {Name: "b", Type: "VARCHAR"}}, false)
	assert.True(t, r.SQLWithheld)
	assert.Equal(t, []Column{{Type: "INTEGER", Withheld: true}, {Type: "VARCHAR", Withheld: true}}, r.Columns)
}

func TestColumns_NamesFromDataAreNeverKept(t *testing.T) {
	r := finishWithColumns(t, intent(), []Column{{Name: "EMEA", Type: "HUGEINT"}, {Name: "APAC", Type: "HUGEINT"}}, true)
	assert.Equal(t, []Column{{Type: "HUGEINT", Withheld: true}, {Type: "HUGEINT", Withheld: true}}, r.Columns, "the count and types stay")
	b, err := Encode(r)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "EMEA")
}

func TestColumns_TheFingerprintStillCoversTheRealNames(t *testing.T) {
	a := finishWithColumns(t, intent(), []Column{{Name: "EMEA", Type: "INTEGER"}}, true)
	b := finishWithColumns(t, intent(), []Column{{Name: "APAC", Type: "INTEGER"}}, true)
	assert.Equal(t, a.Columns, b.Columns)
	assert.NotEqual(t, a.ResultHMAC, b.ResultHMAC)
}

func TestColumns_ValidationAcceptsAWithheldColumnAndRefusesALeak(t *testing.T) {
	r := sample(3)
	r.Columns = []Column{{Type: "INTEGER", Withheld: true}}
	_, err := Seal(r)
	require.NoError(t, err)
	r.Columns = []Column{{Name: "leaked", Type: "INTEGER", Withheld: true}}
	_, err = Seal(r)
	require.ErrorIs(t, err, ErrInvalid)
}
