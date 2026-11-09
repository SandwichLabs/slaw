package repl

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
)

func historyPath(root string) string { return filepath.Join(root, ".slaw-local", "repl", "history") }

func TestHistoryNeverStoresCredentialStatements(t *testing.T) {
	root := t.TempDir()
	f := newSession(t, withRoot(root))
	f.waitFor(prompt)
	for _, stmt := range []string{
		"ATTACH 'postgres:host=h password=hunter2' AS pg (TYPE postgres);", // runs, fails: no extension
		"CREATE SECRET s (TYPE s3, KEY_ID 'k', SECRET 'v');",               // refused by the gates
		"SET s3_secret_access_key='v';",
		"PRAGMA threads=4;",
		"DETACH pg;",
		"INSTALL httpfs;", // refused by the gates
		"USE memory;",
		"SELECT 1;",
		"SELECT * FROM typo_table;", // cannot be prepared: recorded, a typo is not a secret
		".mode csv",
		".open md:?motherduck_token=SEKRETTOKEN1", // refused: never recorded
		".bogus password=hunter2",                 // unknown: never recorded
		"FROM read_parquet('s3://b/k?s3_secret_access_key=AKIASECRET2');",
		"SELECT 'bearer_token = abcdefgh12345678' AS tok;",
	} {
		f.run(stmt + "\r")
	}
	require.NoError(t, f.s.Close())

	data, err := os.ReadFile(historyPath(root))
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1\nSELECT * FROM typo_table\n.mode csv\n", string(data))
	for _, secret := range []string{"hunter2", "SEKRETTOKEN1", "AKIASECRET2", "abcdefgh12345678"} {
		assert.NotContains(t, string(data), secret)
	}

	// Nothing the console printed on its own (errors included) holds the secret;
	// only the line editor's echo of what was typed does.
	for _, line := range strings.Split(f.out.String(), "\n") {
		if strings.Contains(line, "Error:") {
			assert.NotContains(t, line, "hunter2", line)
		}
	}
}

func TestHistoryEligible(t *testing.T) {
	prep := errors.New("cannot prepare")
	cases := []struct {
		name string
		text string
		typ  duckdb.StmtType
		err  error
		want bool
	}{
		{"select", "SELECT 1", duckdb.STATEMENT_TYPE_SELECT, nil, true},
		{"insert", "INSERT INTO t VALUES (1)", duckdb.STATEMENT_TYPE_INSERT, nil, true},
		{"create table", "CREATE TABLE t(x INT)", duckdb.STATEMENT_TYPE_CREATE, nil, true},
		{"attach", "ATTACH 'x password=1' AS a", duckdb.STATEMENT_TYPE_ATTACH, nil, false},
		{"detach", "DETACH a", duckdb.STATEMENT_TYPE_DETACH, nil, false},
		{"set", "SET threads=4", duckdb.STATEMENT_TYPE_SET, nil, false},
		{"use is a set", "USE memory", duckdb.STATEMENT_TYPE_SET, nil, false},
		{"pragma", "PRAGMA threads=4", duckdb.STATEMENT_TYPE_PRAGMA, nil, false},
		{"load", "LOAD httpfs", duckdb.STATEMENT_TYPE_LOAD, nil, false},
		{"extension", "INSTALL httpfs", duckdb.STATEMENT_TYPE_EXTENSION, nil, false},
		{"multi", "SELECT 1; SELECT 2", duckdb.STATEMENT_TYPE_MULTI, nil, false},
		{"typo", "SELECT * FROM nope", duckdb.STATEMENT_TYPE_INVALID, prep, true},
		{"unparsed attach", "ATTACH 'x password=1' AS", duckdb.STATEMENT_TYPE_INVALID, prep, false},
		{"unparsed attach after comment", "/* hi */ attach 'x password=1'", duckdb.STATEMENT_TYPE_INVALID, prep, false},
		{"unparsed secret", "CREATE SECRET s (", duckdb.STATEMENT_TYPE_INVALID, prep, false},
		{"unparsed set", "set s3_secret_access_key=", duckdb.STATEMENT_TYPE_INVALID, prep, false},
		{"unparsed use", "USE pg", duckdb.STATEMENT_TYPE_INVALID, prep, false},
		{"string mentioning attach", "SELECT 'attach' FROM nope", duckdb.STATEMENT_TYPE_INVALID, prep, true},
		{"nothing", "   ", duckdb.STATEMENT_TYPE_INVALID, prep, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, eligible(c.text, c.typ, c.err)) })
	}
}

func TestHistoryFileModeAndCap(t *testing.T) {
	root := t.TempDir()
	h, notice := openHistory(root)
	require.Empty(t, notice)
	for i := 0; i < historyMax+50; i++ {
		h.record("SELECT 1 -- " + strconv.Itoa(i))
	}
	h.record("SELECT   2\n  FROM  t")
	h.record("SELECT 2 FROM t") // a repeat of the last (after whitespace folding)

	info, err := os.Stat(historyPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dir, err := os.Stat(filepath.Dir(historyPath(root)))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())

	reopened, notice := openHistory(root)
	require.Empty(t, notice)
	assert.Len(t, reopened.entries, historyMax, "the file is trimmed to the last 1000 on open")
	assert.Equal(t, "SELECT 2 FROM t", reopened.entries[len(reopened.entries)-1])
	data, err := os.ReadFile(historyPath(root))
	require.NoError(t, err)
	assert.Equal(t, historyMax, strings.Count(string(data), "\n"), "and rewritten")
}

func TestHistoryDisabledWithoutRoot(t *testing.T) {
	h, notice := openHistory("")
	require.Empty(t, notice)
	h.record("SELECT 1")
	assert.Equal(t, []string{"SELECT 1"}, h.entries, "kept for recall")
	assert.Empty(t, h.path)
}

func TestHistoryRecallsInTheEditor(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(historyPath(root)), 0o700))
	require.NoError(t, os.WriteFile(historyPath(root), []byte("SELECT 'from file' AS v\n"), 0o600))
	f := newSession(t, withRoot(root))
	f.waitFor(prompt)
	f.run("\x1b[A;\r") // up arrow recalls the previous statement
	assert.Contains(t, f.out.String(), "from file")
}

// The redactor's patterns grow (#276); ordinary SQL that merely mentions
// credential-looking words must stay recordable.
func TestOrdinarySQLStaysRecordable(t *testing.T) {
	for _, stmt := range []string{
		"SELECT token, password FROM users WHERE id = 5",
		"CREATE TABLE users(password VARCHAR)",
		"SELECT * FROM secrets ORDER BY key_id",
		"SELECT 'Basic Input' AS s",
		"-- bearer of bad news\nSELECT 1",
		"SELECT max_tokens FROM settings",
	} {
		assert.True(t, engine.Recordable(foldEntry(stmt)), stmt)
	}
	assert.False(t, engine.Recordable(foldEntry("SELECT 1 WHERE password = 'x'")))
}
