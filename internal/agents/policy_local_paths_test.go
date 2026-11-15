package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/storageglob"
)

// #289: a relative path under the workspace root is made absolute before the
// policy sees the statement, so a committed saved query or metric can read the
// workspace's own data/ files. The fixture's workspace root is the parent of
// its data/ folder, and the test process's working directory is this package's
// folder, so a path that ran as written would find nothing.

func TestAgentResolvesRelativeDataPathsBeforeThePolicy(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	root := filepath.Dir(fx.dataDir)
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.csv"), []byte("a\n1\n"), 0o644))

	for _, q := range []string{
		`SELECT count(*) AS n FROM read_csv('data/x.csv')`,
		`SELECT count(*) AS n FROM read_csv('./data/x.csv')`,
		`SELECT count(*) AS n FROM read_csv(['data/x.csv', './data/y.csv'])`,
		`SELECT count(*) AS n FROM read_csv('data/sub/*.csv')`,
		`SELECT count(*) AS n FROM read_csv('data/sub/z.csv', header = true)`,
		`SELECT count(*) AS n FROM 'data/x.csv'`,
		`SELECT count(*) AS n FROM glob('data/sub/*.csv')`,
		`WITH t AS (SELECT * FROM read_csv('data/x.csv')) SELECT count(*) AS n FROM t`,
	} {
		resp := fx.run(q)
		require.Nil(t, resp.Error, "%s: %v", q, resp.Error)
	}

	// The statement that runs holds the resolved path, and the engine is told
	// it is final (it must not resolve against anything of its own).
	text, ectx, err := fx.h.expand(context.Background(), `SELECT * FROM read_csv('data/x.csv')`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM read_csv('`+filepath.Join(root, "data", "x.csv")+`')`, text)
	rw, ok := storageglob.FromContext(ectx)
	require.True(t, ok)
	summary, _ := rw.Summarize(text)
	assert.Equal(t, `SELECT * FROM read_csv('data/x.csv')`, summary, "a record keeps the path as written")
}

func TestAgentStillRefusesWhatLeavesData(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	root := filepath.Dir(fx.dataDir)
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.csv"), []byte("a\n1\n"), 0o644))

	for q, why := range map[string]string{
		`SELECT * FROM read_csv('../outside/secret.csv')`:         `contains ".."`,
		`SELECT * FROM read_csv('data/../outside/secret.csv')`:    `contains ".."`,
		`SELECT * FROM read_csv('data/sub/../../notes.csv')`:      `contains ".."`,
		`SELECT * FROM read_csv('notes.csv')`:                     "outside the workspace's data/ folder",
		`SELECT * FROM read_csv('./notes.csv')`:                   "outside the workspace's data/ folder",
		`SELECT * FROM read_csv('*.csv')`:                         "outside the workspace's data/ folder",
		`SELECT * FROM 'notes.csv'`:                               "relative path",
		`SELECT * FROM read_csv('data/link.csv')`:                 "outside the workspace's data/ folder",
		`SELECT * FROM read_csv('data/*.csv')`:                    "symlink that leaves",
		`SELECT * FROM read_csv('` + fx.outside + `/secret.csv')`: "outside the workspace's data/ folder",
		`SELECT * FROM read_csv('~/x.csv')`:                       "relative path",
		`SELECT * FROM read_csv(concat('data/', 'x.csv'))`:        "plain string literal",
	} {
		resp := fx.run(q)
		require.NotNil(t, resp.Error, q)
		assert.Contains(t, resp.Error.Message, why, q)
	}
}
