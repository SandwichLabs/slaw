//go:build unix

package hub

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copyTree copies dir into a new temp folder and returns it.
func copyTree(t *testing.T, dir string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(dir))
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	}))
	return dst
}

// A committed example workspace reads its own data/ files by relative path on
// every run path, with no import step and whatever the process's working
// directory is (#289): the test's working directory is internal/hub, which has
// no data/ folder.
func TestExampleWorkspaceReadsItsDataByRelativePath(t *testing.T) {
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	root := copyTree(t, filepath.Join("..", "..", "docs", "examples", "penguins"))
	// A saved query and a metric over it that name the committed CSV.
	require.NoError(t, os.WriteFile(filepath.Join(root, "queries", "penguins-csv.sql"),
		[]byte("-- slaw: {\"title\":\"Penguins from the CSV\"}\nSELECT species FROM read_csv('data/penguins.csv')\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "semantics", "metrics", "csv_count.yaml"),
		[]byte("version: 1\nname: csv_count\ntitle: CSV count\ndescription: Rows of the committed CSV.\nquery: penguins-csv\nmeasure: count(*)\ndimensions: [species]\nformat: number\nowner: example\n"), 0o644))

	o, err := Open(t.Context(), root, OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	t.Cleanup(o.Close)
	ctx := t.Context()

	h, _, err := NewAgentHandler(o.WS, o.Sess, "", quietLogger())
	require.NoError(t, err)

	// The agent's read-only path (run_query, artifacts, watches, assertions).
	for _, q := range []string{
		"SELECT count(*) AS n FROM read_csv('data/penguins.csv')",
		"SELECT count(*) AS n FROM read_csv('./data/penguins.csv')",
		"SELECT count(*) AS n FROM read_csv('data/*.csv')",
		"SELECT count(*) AS n FROM 'data/penguins.csv'",
		"SELECT count(*) AS n FROM read_csv(['data/penguins.csv'])",
	} {
		rs, _, err := h.RunReadOnlySQLIn(ctx, "", q, nil, 10)
		require.NoError(t, err, q)
		assert.EqualValues(t, 344, rs.Rows[0][0], q)
		assert.NotEmpty(t, rs.ReceiptID, q)
	}
	rs, _, err := h.RunReadOnlySQLIn(ctx, "", "SELECT sum(csv_count) AS n FROM metric('csv_count', by => 'species')", nil, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 344, rs.Rows[0][0])

	// The receipt keeps the statement as written, never the resolved path.
	rs, _, err = h.RunReadOnlySQLIn(ctx, "", "SELECT count(*) AS n FROM read_csv('data/penguins.csv')", nil, 10)
	require.NoError(t, err)
	rec, err := o.WS.Receipts().Store().Get(rs.ReceiptID)
	require.NoError(t, err)
	assert.NotContains(t, rec.SQL+rec.RunSQL, root)
	assert.Contains(t, rec.RunSQL, "read_csv('data/penguins.csv')")

	// Still refused: an escape, an absolute path outside data/, a symlink out.
	require.NoError(t, os.WriteFile(filepath.Join(root, "outside.csv"), []byte("a\n1\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(root, "outside.csv"), filepath.Join(root, "data", "link.csv")))
	for _, q := range []string{
		"SELECT * FROM read_csv('data/../outside.csv')",
		"SELECT * FROM read_csv('../x.csv')",
		"SELECT * FROM read_csv('outside.csv')",
		"SELECT * FROM read_csv('/etc/passwd')",
		"SELECT * FROM read_csv('data/link.csv')",
	} {
		_, _, err := h.RunReadOnlySQLIn(ctx, "", q, nil, 10)
		assert.Error(t, err, q)
	}
	require.NoError(t, os.Remove(filepath.Join(root, "data", "link.csv")))

	// The person's path (editor, `slaw query`, console): no policy, but the same
	// resolution against the workspace root.
	pr, err := o.Eng.Execute(ctx, "SELECT count(*) AS n FROM read_csv('data/penguins.csv')", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 344, pr.Rows[0][0])
	pr, err = o.Eng.Execute(ctx, "SELECT count(*) AS n FROM 'data/*.csv'", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 344, pr.Rows[0][0])
	_, err = o.Eng.Execute(ctx, "SELECT * FROM read_csv('data/missing.csv')", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), root, "an error shows the path as written")
}
