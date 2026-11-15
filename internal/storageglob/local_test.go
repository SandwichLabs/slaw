package storageglob

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wsRoot = "/ws/acme"

func expandLocal(t *testing.T, text string, confine bool) (string, *Rewrite) {
	t.Helper()
	out, rw, err := Expand(context.Background(), text, Options{Lister: newFake(), Root: wsRoot, Confine: confine})
	require.NoError(t, err, text)
	return out, rw
}

func TestExpandResolvesRelativePathsAgainstTheRoot(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"reader", `SELECT * FROM read_csv('data/x.csv')`, `SELECT * FROM read_csv('/ws/acme/data/x.csv')`},
		{"dot slash", `SELECT * FROM read_parquet('./data/x.parquet')`, `SELECT * FROM read_parquet('/ws/acme/data/x.parquet')`},
		{"glob in data", `SELECT * FROM read_csv('data/*.csv', header=true)`, `SELECT * FROM read_csv('/ws/acme/data/*.csv', header=true)`},
		{"list", `SELECT * FROM read_csv(['data/a.csv', 'data/b.csv'])`, `SELECT * FROM read_csv(['/ws/acme/data/a.csv', '/ws/acme/data/b.csv'])`},
		{"FROM literal", `SELECT * FROM 'data/x.parquet' AS p`, `SELECT * FROM '/ws/acme/data/x.parquet' AS p`},
		{"FROM quoted name", `SELECT * FROM "data/x.csv"`, `SELECT * FROM '/ws/acme/data/x.csv'`},
		{"glob()", `SELECT * FROM glob('data/*')`, `SELECT * FROM glob('/ws/acme/data/*')`},
		{"delta", `SELECT * FROM delta_scan('data/tbl')`, `SELECT * FROM delta_scan('/ws/acme/data/tbl')`},
		{"subquery and CTE", `WITH x AS (SELECT * FROM read_csv('data/x.csv')) SELECT * FROM x`, `WITH x AS (SELECT * FROM read_csv('/ws/acme/data/x.csv')) SELECT * FROM x`},
		{"quote in path", `SELECT * FROM read_csv('data/it''s.csv')`, `SELECT * FROM read_csv('/ws/acme/data/it''s.csv')`},
		{"s3 pattern alongside", `SELECT * FROM read_csv('data/x.csv') a, read_csv('s3://b/*.csv') b`, `SELECT * FROM read_csv('/ws/acme/data/x.csv') a, read_csv(['s3://b/one.csv']) b`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, rw := expandLocal(t, c.in, true)
			assert.Equal(t, c.want, out)
			assert.True(t, rw.Changed())
		})
	}
}

func TestExpandLeavesWhatIsNotARelativeReaderPath(t *testing.T) {
	for _, in := range []string{
		`SELECT 'data/x.csv' AS v`,                         // a value, not a path
		`SELECT * FROM read_csv('/abs/data/x.csv')`,        // absolute: the policy's business
		`SELECT * FROM read_csv('~/x.csv')`,                // home directory
		`SELECT * FROM read_csv('https://h/x.csv')`,        // a URL
		`SELECT * FROM read_csv('file:///x.csv')`,          // a URL
		`SELECT * FROM my_table`,                           // a table
		`SELECT * FROM "my.table"`,                         // a table with a dot, not a file
		`SELECT * FROM s.csv`,                              // schema.table
		`WITH t AS (SELECT 1) SELECT * FROM t`,             // a CTE
		`SELECT * FROM read_csv(concat('data/', 'x.csv'))`, // an expression: left for the policy
	} {
		out, rw := expandLocal(t, in, true)
		assert.Equal(t, in, out)
		assert.False(t, rw.Changed(), in)
	}
}

func TestExpandConfineRefusesToResolveAnEscape(t *testing.T) {
	for _, in := range []string{
		`SELECT * FROM read_csv('../x.csv')`,
		`SELECT * FROM read_csv('data/../x.csv')`,
		`SELECT * FROM read_csv('data/../../x.csv')`,
		`SELECT * FROM read_csv('..\x.csv')`,
	} {
		out, rw := expandLocal(t, in, true)
		assert.Equal(t, in, out, "left as written for the policy to refuse")
		assert.False(t, rw.Changed())
	}
	// In a list the other paths resolve and the escape stays for the policy.
	out, _ := expandLocal(t, `SELECT * FROM read_csv(['data/a.csv', '../x.csv'])`, true)
	assert.Equal(t, `SELECT * FROM read_csv(['/ws/acme/data/a.csv', '../x.csv'])`, out)
	// A person's statement has no policy behind it: the same text resolves
	// against the root rather than DuckDB's working directory.
	out, _ = expandLocal(t, `SELECT * FROM read_csv('../x.csv')`, false)
	assert.Equal(t, `SELECT * FROM read_csv('/ws/x.csv')`, out)
}

func TestExpandWithoutARootLeavesRelativePathsAlone(t *testing.T) {
	in := `SELECT * FROM read_csv('data/x.csv')`
	out, rw, err := Expand(context.Background(), in, Options{Lister: newFake()})
	require.NoError(t, err)
	assert.Equal(t, in, out)
	assert.Nil(t, rw)
}

func TestExpandRootWithGlobCharactersIsNeverUsed(t *testing.T) {
	in := `SELECT * FROM read_csv('data/x.csv')`
	out, rw, err := Expand(context.Background(), in, Options{Root: "/ws/a[1]", Confine: true})
	require.NoError(t, err)
	assert.Equal(t, in, out)
	assert.False(t, rw.Changed())
}

func TestExpandLocalPathAllowedInViewsAndCopyFrom(t *testing.T) {
	out, _ := expandLocal(t, `CREATE VIEW v AS SELECT * FROM read_csv('data/x.csv')`, false)
	assert.Equal(t, `CREATE VIEW v AS SELECT * FROM read_csv('/ws/acme/data/x.csv')`, out)
	// COPY ... FROM with no s3 pattern is not the wildcard error.
	_, _, err := Expand(context.Background(), `COPY t FROM 'data/x.csv'`, Options{Root: wsRoot})
	assert.NoError(t, err)
}

func TestRewriteKeepsTheWrittenPathForRecordsAndErrors(t *testing.T) {
	in := `SELECT * FROM read_csv('data/x.csv') a, read_csv('s3://b/*.csv') b`
	out, rw := expandLocal(t, in, true)
	sum, n := rw.Summarize(out)
	assert.Equal(t, `SELECT * FROM read_csv('data/x.csv') a, read_csv('s3://b/*.csv' /* expanded to 1 objects */) b`, sum)
	assert.Equal(t, 1, n)
	assert.NotContains(t, sum, wsRoot)

	msg := rw.Redact(`IO Error: No files found that match the pattern "/ws/acme/data/x.csv"`)
	assert.Equal(t, `IO Error: No files found that match the pattern "data/x.csv"`, msg)
}
