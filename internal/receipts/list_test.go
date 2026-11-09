package receipts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putAt keeps a sealed receipt of surface s and sets its file's modification
// time, which is the order List reads.
func putAt(t *testing.T, st *FileStore, n int, s Surface, mod time.Time) Receipt {
	t.Helper()
	r := sample(n)
	r.Surface = s
	r, err := Seal(r)
	require.NoError(t, err)
	require.NoError(t, st.Put(r))
	require.NoError(t, os.Chtimes(st.path(r.ID), mod, mod))
	return r
}

func TestList_NewestFirstFilteredAndLimited(t *testing.T) {
	st, err := NewFileStore(t.TempDir(), DefaultPolicy())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	old := putAt(t, st, 1, SurfaceQueryEditor, now.Add(-3*time.Hour))
	mid := putAt(t, st, 2, SurfaceNotebookCell, now.Add(-2*time.Hour))
	top := putAt(t, st, 3, SurfaceQueryEditor, now.Add(-1*time.Hour))

	all, err := st.List(ListFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	// Newest first, by the receipt's own time (sample(n) puts n in the nanoseconds).
	assert.Equal(t, []string{top.ID, mid.ID, old.ID}, []string{all[0].ID, all[1].ID, all[2].ID})

	editor, err := st.List(ListFilter{Surface: SurfaceQueryEditor})
	require.NoError(t, err)
	require.Len(t, editor, 2)
	assert.Equal(t, top.ID, editor[0].ID)
	assert.Equal(t, old.ID, editor[1].ID)

	one, err := st.List(ListFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, top.ID, one[0].ID)
}

func TestList_HoldsNoValueAndCutsTheStatement(t *testing.T) {
	st, err := NewFileStore(t.TempDir(), DefaultPolicy())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	r := sample(5)
	r.SQL = "\n  SELECT " + strings.Repeat("é", 200) + "\nFROM t"
	r.SQLHash = HashText(testKey, r.SQL)
	r, err = Seal(r)
	require.NoError(t, err)
	require.NoError(t, st.Put(r))

	rows, err := st.List(ListFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, strings.HasPrefix(rows[0].SQL, "SELECT "))
	assert.NotContains(t, rows[0].SQL, "FROM t", "only the first line")
	assert.True(t, strings.HasSuffix(rows[0].SQL, "…"))
	assert.LessOrEqual(t, len(rows[0].SQL), previewBytes+len("…"))
	assert.Equal(t, []string{"warehouse"}, rows[0].Connections)
}

func TestList_SkipsAFileThatIsNotAReceiptAndIsBounded(t *testing.T) {
	root := t.TempDir()
	st, err := NewFileStore(root, DefaultPolicy())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	good := putAt(t, st, 1, SurfaceCLIQuery, time.Now())
	// A file with a receipt's name but another content, and a stray file.
	bad := sealed(t, 2)
	bogus := filepath.Join(st.Dir(), bad.ID[2:4], bad.ID+".json")
	require.NoError(t, os.MkdirAll(filepath.Dir(bogus), 0o700))
	require.NoError(t, os.WriteFile(bogus, []byte(`{"not":"a receipt"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(st.Dir(), "note.txt"), []byte("x"), 0o600))

	rows, err := st.List(ListFilter{Limit: 10_000}) // over MaxListLimit: clamped
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, good.ID, rows[0].ID)
	assert.Equal(t, MaxListLimit, ListFilter{Limit: 10_000}.limit())
	assert.Equal(t, DefaultListLimit, ListFilter{}.limit())
}

func TestList_MockMatchesTheFileStore(t *testing.T) {
	m := NewMock()
	a, b := sealed(t, 1), sealed(t, 2)
	b.Surface = SurfaceA2UI
	b, err := Seal(b)
	require.NoError(t, err)
	require.NoError(t, m.Put(a))
	require.NoError(t, m.Put(b))
	rows, err := m.List(ListFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, b.ID, rows[0].ID, "newer first")
	rows, err = m.List(ListFilter{Surface: SurfaceA2UI, Limit: 1})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, b.ID, rows[0].ID)
}

func TestNoteFor(t *testing.T) {
	rec := NewRecorder(NewMock(), testKey, DefaultPolicy(), Checks{}, nil)
	assert.Equal(t, NoteNone, NoteFor(rec, "r_x", nil), "a kept receipt has no note")
	assert.Equal(t, NoteNotKept, NoteFor(rec, "", errors.New("disk /secret/path full")))
	assert.Equal(t, NoteUnavailable, NoteFor(nil, "", nil))
	assert.Equal(t, NoteNone, NoteFor(rec, "", nil), "a run no receipt is due for")
}

func TestSurfacesForRenderSources(t *testing.T) {
	assert.True(t, SurfaceNotebookCell.Valid())
	assert.True(t, SurfaceA2UI.Valid())
	assert.True(t, SourceComponent.Valid())
}
