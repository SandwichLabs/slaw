package agents

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SandwichLabs/slaw/internal/session/sessiontest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
)

// newClassifierTestDB creates a fresh, real, in-memory DuckDB with a small
// fixed schema (table t, table t2, table users) that classifySQL's catalog
// resolution (round 3, item B) can look up against. Every TestClassifySQL
// case gets its own fresh instance via the classify()/classifyReason()
// helpers below, so cases are independent and t/t2 always start intact.
func newClassifierTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
		CREATE TABLE t (id INTEGER, name VARCHAR);
		INSERT INTO t VALUES (1, 'Ada');
		CREATE TABLE t2 (id INTEGER);
		INSERT INTO t2 VALUES (1);
		CREATE TABLE users (id INTEGER, name VARCHAR, email VARCHAR);
		INSERT INTO users VALUES (1, 'Alice', 'alice@example.com');
	`)
	require.NoError(t, err)
	return db
}

func classify(t *testing.T, sql string) sqlClass {
	t.Helper()
	class, _ := classifySQL(context.Background(), newClassifierTestDB(t), sql, nil)
	return class
}

func classifyReason(t *testing.T, sql string) (sqlClass, string) {
	t.Helper()
	return classifySQL(context.Background(), newClassifierTestDB(t), sql, nil)
}

func TestClassifySQL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want sqlClass
	}{
		{"select", "SELECT * FROM t", sqlClassRead},
		{"select lowercase", "select * from t", sqlClassRead},
		{"with select", "WITH x AS (SELECT 1) SELECT * FROM x", sqlClassRead},
		{"show", "SHOW TABLES", sqlClassRead},
		{"describe existing table", "DESCRIBE t", sqlClassRead},
		{"show existing table", "SHOW t", sqlClassRead},
		{"explain", "EXPLAIN SELECT * FROM t", sqlClassRead},
		{"summarize existing table", "SUMMARIZE t", sqlClassRead},
		{"pragma call-style read", "PRAGMA table_info('t')", sqlClassRead},
		// database_list exposes the decrypted temp DB file's on-disk path
		// (round 6 review fix) and must never classify as a read, even
		// though it's a harmless-looking introspection PRAGMA.
		{"pragma database_list denied", "PRAGMA database_list", sqlClassOther},
		{"line comment then select", "-- list rows\nSELECT * FROM t", sqlClassRead},
		{"block comment then select", "/* list rows */ SELECT * FROM t", sqlClassRead},
		{"select with trailing semicolon", "SELECT 1;", sqlClassRead},
		{"select with inline comment mentioning drop", "SELECT 1 -- DROP TABLE t\n", sqlClassRead},
		{"select with string literal mentioning drop", "SELECT 'DROP TABLE t' AS note", sqlClassRead},
		{"select from safe table function: range", "SELECT * FROM range(10)", sqlClassRead},
		{"select from safe table function: generate_series", "SELECT * FROM generate_series(1, 10)", sqlClassRead},
		{"select from catalog function: duckdb_tables", "SELECT * FROM duckdb_tables()", sqlClassRead},

		{"insert", "INSERT INTO t VALUES (1, 'x')", sqlClassWrite},
		// Regression: reEscapeStringPrefix's E'/e' detector originally
		// matched ANY word ending in "e" immediately followed by a string's
		// own closing quote — e.g. the "e'" at the end of "...'Alice',..."
		// below is just an ordinary name butting up against its closing
		// quote, not an E'-prefixed escape string. That false positive
		// denied this exact, completely ordinary INSERT live (caught via
		// Studio chat under read-write) before the fix required a
		// non-identifier character (or start of text) immediately before
		// the e/E.
		{"insert with a value ending in 'e' before its closing quote", "INSERT INTO t VALUES (1, 'Alice')", sqlClassWrite},
		{"update with a value ending in 'e' before its closing quote", "UPDATE t SET name = 'Alice' WHERE id = 1", sqlClassWrite},
		{"insert lowercase", "insert into t values (1, 'x')", sqlClassWrite},
		{"update", "UPDATE t SET name = 'x'", sqlClassWrite},
		{"delete", "DELETE FROM t WHERE id = 1", sqlClassWrite},
		{"merge", "MERGE INTO t USING t2 ON t.id = t2.id WHEN MATCHED THEN UPDATE SET name = 'x'", sqlClassWrite},
		{"cte with delete", "WITH x AS (SELECT id FROM t WHERE id > 100) DELETE FROM t WHERE id IN (SELECT id FROM x)", sqlClassWrite},

		{"create table", "CREATE TABLE u (id INTEGER)", sqlClassDDL},
		{"drop table", "DROP TABLE t", sqlClassDDL},
		{"alter table", "ALTER TABLE t ADD COLUMN x INTEGER", sqlClassDDL},
		{"attach", "ATTACH 'foo.db' AS foo", sqlClassOther},
		{"detach", "DETACH foo", sqlClassOther},
		{"copy", "COPY t TO 'file.csv'", sqlClassOther},
		{"export", "EXPORT DATABASE 'dir'", sqlClassOther},
		{"install", "INSTALL httpfs", sqlClassOther},
		{"load", "LOAD httpfs", sqlClassOther},
		{"pragma assignment", "PRAGMA memory_limit='1GB'", sqlClassOther},
		{"pragma not on allow-list", "PRAGMA force_checkpoint", sqlClassOther},
		{"pragma not on allow-list: enable_profiling", "PRAGMA enable_profiling", sqlClassOther},
		{"set", "SET memory_limit='1GB'", sqlClassOther},
		{"vacuum", "VACUUM", sqlClassOther},
		{"explain analyze", "EXPLAIN ANALYZE SELECT 1", sqlClassOther},
		{"multi-statement select+drop", "SELECT 1; DROP TABLE t;", sqlClassDDL},
		{"multi-statement lowercase", "select 1; drop table t", sqlClassDDL},
		{"multi-statement selects only: every statement is checked and all are reads", "SELECT 1; SELECT 2;", sqlClassRead},
		{"mixed dml and ddl", "INSERT INTO t VALUES (1,'x'); CREATE TABLE u (id INTEGER)", sqlClassDDL},
		{"empty", "", sqlClassOther},
		{"whitespace only", "   \n\t", sqlClassOther},
		{"unrecognized shape", "frobnicate t", sqlClassOther},

		// --- Round 2 review: the real-world proven bypasses. Both use
		// DuckDB dollar-quoting / E'' escape strings to hide a second,
		// smuggled statement from a hand-rolled comment/string stripper.
		// The statement count now comes from DuckDB's own
		// duckdb_extract_statements (round 3 item A), which correctly
		// counts both as 2 statements regardless of what the fallback
		// lexer would have guessed.
		{"bypass: dollar-quote smuggled DROP", `SELECT $$'$$ AS probe; DROP TABLE t; --'`, sqlClassDDL},
		{"bypass: E'' escape smuggled DROP", `SELECT E'\'' AS probe; DROP TABLE t; --'`, sqlClassDDL},
		{"bypass: tagged dollar-quote smuggled DROP", `SELECT $tag$'$tag$ AS probe; DROP TABLE t; --'`, sqlClassOther},

		// --- Round 3 review, item 1: the same class of bypass, but through
		// the Write tier (DELETE/INSERT hiding a smuggled DROP), which
		// round 2 missed because it only fixed the Read decision.
		{"bypass write: DELETE hides smuggled DROP via dollar-quote", `DELETE FROM t WHERE name = $$'$$; DROP TABLE t2; --'`, sqlClassDDL},
		{"bypass write: INSERT hides smuggled DROP via E'' escape", `INSERT INTO t VALUES (1, E'\''); DROP TABLE t2; --'`, sqlClassDDL},

		// --- Round 2 review: external file/network access (item B). A
		// syntactically pure SELECT that still reaches outside the
		// database must not be sqlClassRead. Table resolution is now via
		// the real catalog (round 3), not extension-guessing, so these are
		// denied because 't'/'/etc/passwd'/etc. simply aren't real tables
		// — not because of what their name looks like.
		{"external: read_csv file", `SELECT * FROM read_csv('/etc/passwd')`, sqlClassOther},
		{"external: read_csv scalar in select list", `SELECT read_csv('/etc/passwd')`, sqlClassOther},
		{"external: read_text", `SELECT read_text('/etc/passwd')`, sqlClassOther},
		{"external: read_blob", `SELECT read_blob('/etc/shadow')`, sqlClassOther},
		{"external: getenv", `SELECT getenv('HOME')`, sqlClassOther},
		{"external: glob", `SELECT * FROM glob('/home/*')`, sqlClassOther},
		{"external: replacement scan path", `SELECT * FROM '/tmp/x.csv'`, sqlClassOther},
		{"external: replacement scan bare FROM", `FROM '/tmp/x.csv'`, sqlClassOther},
		{"external: read_csv over https", `SELECT * FROM read_csv('https://example.com/x.csv')`, sqlClassOther},
		{"external: relative path with extension", `SELECT * FROM 'data/sales.parquet'`, sqlClassOther},

		// --- Round 3 review, item 3: replacement scans the extension-list
		// approach missed entirely (no ".tsv"/".jsonl" in the old list;
		// "s.csv" parses as schema "s", table "csv" — no extension in the
		// table_name field at all), now denied purely because none of
		// these resolve in the real catalog.
		{"external: .tsv file", `SELECT * FROM 's.tsv'`, sqlClassOther},
		{"external: .tsv file bare FROM", `FROM 's.tsv'`, sqlClassOther},
		{"external: .jsonl file", `SELECT * FROM 's.jsonl'`, sqlClassOther},
		{"external: schema.table disguise", `FROM s.csv`, sqlClassOther},
		{"external: schema.table disguise explicit select", `SELECT * FROM s.csv`, sqlClassOther},
		{"external: SUMMARIZE of a file", `SUMMARIZE 's.tsv'`, sqlClassOther},
		{"external: DESCRIBE of a SELECT over a file", `DESCRIBE SELECT * FROM 's.tsv'`, sqlClassOther},
		{"external: SHOW of a file", `SHOW 's.tsv'`, sqlClassOther},
		{"external: DESCRIBE of an unresolved bare identifier", `DESCRIBE nonexistent_table`, sqlClassOther},

		// --- Round 3 review, item D: current_setting can expose configured
		// secrets (e.g. S3 credentials) — denied in every sandbox,
		// including read-only, even though it's syntactically just a
		// SELECT.
		{"external: current_setting", `SELECT current_setting('s3_access_key_id')`, sqlClassOther},

		// --- Round 3 review, item 2/C: read-write DML reading the
		// filesystem via INSERT ... SELECT / a CTE feeding an INSERT.
		// json_serialize_sql can't parse DML, so these go through the
		// Write-tier raw-text scan (writeStatementTouchesExternalAccess).
		{"write external: INSERT SELECT FROM read_csv", `INSERT INTO t SELECT * FROM read_csv('/etc/passwd')`, sqlClassOther},
		{"write external: CTE over read_text feeding INSERT", `WITH x AS (SELECT * FROM read_text('/etc/passwd')) INSERT INTO t SELECT * FROM x`, sqlClassOther},
		{"write external: quoted table ref after FROM", `INSERT INTO t SELECT * FROM 's.csv'`, sqlClassOther},
		{"write external: quoted table ref after JOIN", `UPDATE t SET name = x.name FROM t JOIN 's.csv' AS x ON true`, sqlClassOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(t, tc.sql), "classifySQL(%q)", tc.sql)
		})
	}
}

func TestClassifySQL_ExternalAccessReason(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM read_csv('/etc/passwd')`,
		`SELECT * FROM 's.tsv'`,
		`SHOW 's.tsv'`,
		`INSERT INTO t SELECT * FROM read_csv('/etc/passwd')`,
	} {
		class, reason := classifyReason(t, sql)
		assert.Equal(t, sqlClassOther, class, "classifySQL(%q)", sql)
		assert.Contains(t, reason, "file reads are not available here", "classifySQL(%q) reason", sql)
	}
}

// TestClassifySQL_UnresolvedPlainTableReason is the fix from the #67 step
// review: a plain, unqualified table name that simply doesn't exist (a typo,
// or a table the agent hasn't imported yet) previously got the generic
// externalAccessDeniedMsg, which reads as "this is a security violation"
// rather than "this table isn't there" — misleading a model into thinking a
// missing table required elevating to the full sandbox rather than calling
// introspect_schema. A genuinely path-like or schema-qualified unresolved
// reference (the real external-access disguises) must still get the generic
// message, since those are the shapes that can actually smuggle file access.
func TestClassifySQL_UnresolvedPlainTableReason(t *testing.T) {
	class, reason := classifyReason(t, "SELECT * FROM customers")
	assert.Equal(t, sqlClassOther, class)
	// Round 2 review fix: leads with the plain fact, no escalation nudge —
	// a read-only user hitting an ordinary typo should not be told to
	// switch to the full sandbox.
	assert.Equal(t, `table "customers" not found`, reason)

	class, reason = classifyReason(t, "DESCRIBE nonexistent_table")
	assert.Equal(t, sqlClassOther, class)
	assert.Contains(t, reason, `table "nonexistent_table" not found`)

	// Path-like / schema-qualified unresolved refs keep the generic message.
	for _, sql := range []string{
		`SELECT * FROM 's.tsv'`,
		`FROM s.csv`,
	} {
		class, reason := classifyReason(t, sql)
		assert.Equal(t, sqlClassOther, class, "classifySQL(%q)", sql)
		assert.Contains(t, reason, "file reads are not available here", "classifySQL(%q) reason", sql)
	}
}

func TestSandboxConfig_CheckSQLClass(t *testing.T) {
	ro := &SandboxConfig{Mode: ModeReadOnly}
	assert.NoError(t, ro.checkSQLClass(sqlClassRead, ""))
	assert.Error(t, ro.checkSQLClass(sqlClassWrite, ""))
	assert.Error(t, ro.checkSQLClass(sqlClassDDL, ""))
	assert.Error(t, ro.checkSQLClass(sqlClassOther, ""))

	rw := &SandboxConfig{Mode: ModeReadWrite}
	assert.NoError(t, rw.checkSQLClass(sqlClassRead, ""))
	assert.NoError(t, rw.checkSQLClass(sqlClassWrite, ""))
	assert.Error(t, rw.checkSQLClass(sqlClassDDL, ""))
	assert.Error(t, rw.checkSQLClass(sqlClassOther, ""))

	full := &SandboxConfig{Mode: ModeFull}
	assert.NoError(t, full.checkSQLClass(sqlClassRead, ""))
	assert.NoError(t, full.checkSQLClass(sqlClassWrite, ""))
	assert.NoError(t, full.checkSQLClass(sqlClassDDL, ""))
	// The never-list is denied in EVERY tier, full included (#116).
	err := full.checkSQLClass(sqlClassOther, "ATTACH is not allowed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ATTACH is not allowed")

	// A denial's reason is surfaced verbatim instead of the generic per-tier message.
	err = ro.checkSQLClass(sqlClassOther, externalAccessDeniedMsg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), externalAccessDeniedMsg)
}

// TestSandboxConfig_CheckSQLClassReadOnly is the #67 step-2 review fix
// (render-time writes), round 3: RunA2UIQuery's gate must deny Write and
// EVERY flavor of Other — including one whose reason names external-file
// access — in every sandbox mode, including full, with NO carve-out. Round
// 2 had a full-mode carve-out keyed on the reason string, but
// classifyFallback returns the exact same externalAccessDeniedMsg reason
// for a write statement whose TEXT merely contains "read_"/"glob"/a quoted
// table ref (writeStatementSuspiciousText) — the string can't tell a read
// that touched a file apart from a write that merely mentions one, so the
// carve-out let real writes through under full mode. It's gone; class ==
// sqlClassRead is the only path to nil.
func TestSandboxConfig_CheckSQLClassReadOnly(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			sc := &SandboxConfig{Mode: mode}
			assert.NoError(t, sc.checkSQLClassReadOnly(&sqlVerdict{Class: sqlClassRead}))
			assert.Error(t, sc.checkSQLClassReadOnly(&sqlVerdict{Class: sqlClassWrite}), "a write must never be allowed through a live sql binding")
			// DDL/multi-statement Other (empty reason, the shape DROP TABLE
			// and multi-statement SQL classify as) must be denied in every
			// mode, full included.
			assert.Error(t, sc.checkSQLClassReadOnly(&sqlVerdict{Class: sqlClassOther}))
			// An Other whose reason names external-file access must ALSO be
			// denied in every mode, full included — no carve-out.
			assert.Error(t, sc.checkSQLClassReadOnly(deny(externalAccessDeniedMsg)))
		})
	}
}

// newRealDuckDBHandler builds a Handler wired to a REAL DuckDB connection
// (via internal/engine.Manager), not the SQLite-mocked engine
// agents_test.go's newTestHandler uses. classifySQL's catalog resolution
// (round 3) queries duckdb_tables()/duckdb_views()/current_database()/
// current_schema() against the handler's real DuckDB connection, which a
// bare fixture can't satisfy — proving the round-2/3 bypass strings
// are actually denied (not just that classifySQL says so in isolation)
// requires dispatching through the real Handler -> real DuckDB path, the
// same one Studio chat and MCP use in production.
func newRealDuckDBHandler(t *testing.T, mode SandboxMode) (*Handler, *sql.DB) {
	t.Helper()

	duckDB, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = duckDB.Close() })

	_, err = duckDB.Exec(`
		CREATE TABLE t (id INTEGER, name VARCHAR);
		INSERT INTO t VALUES (1, 'Ada');
		CREATE TABLE t2 (id INTEGER);
		INSERT INTO t2 VALUES (1);
		CREATE TABLE users (id INTEGER, name VARCHAR, email VARCHAR);
		INSERT INTO users VALUES (1, 'Alice', 'alice@example.com');
	`)
	require.NoError(t, err)

	sandbox := &SandboxConfig{Mode: mode, MaxQueryRows: 100}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	container := &mockAccess{duckDB: duckDB}
	eng := engine.NewManager(sessiontest.ForTests(duckDB), engine.WithLogger(logger))
	return NewHandler(container, eng, sandbox, logger), duckDB
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow("SELECT table_name FROM information_schema.tables WHERE table_name = ?", name).Scan(&got)
	if err != nil {
		return false
	}
	return got == name
}

func runQuery(handler *Handler, sql string) *Response {
	return handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "run_query",
		Params: map[string]any{"sql": sql},
	})
}

// TestHandler_RunReadOnlySQL is the #67 step-2 review fix: RunReadOnlySQL
// (RunA2UIQuery's backing entry point) must allow an ordinary read in every
// sandbox mode and deny a write/DDL/multi-statement even under full — unlike
// handleRunQuery's own dispatch, whose checkSQLClass is deliberately
// permissive under read-write/full.
func TestHandler_RunReadOnlySQL(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		t.Run(string(mode)+"/read allowed", func(t *testing.T) {
			handler, _ := newRealDuckDBHandler(t, mode)
			rs, _, err := handler.RunReadOnlySQL(context.Background(), "SELECT * FROM t", nil, 0)
			require.NoError(t, err)
			assert.Len(t, rs.Rows, 1)
		})
		t.Run(string(mode)+"/DROP denied", func(t *testing.T) {
			handler, db := newRealDuckDBHandler(t, mode)
			_, _, err := handler.RunReadOnlySQL(context.Background(), "DROP TABLE t", nil, 0)
			require.Error(t, err)
			assert.True(t, tableExists(t, db, "t"), "DROP must not have executed")
		})
		t.Run(string(mode)+"/INSERT denied", func(t *testing.T) {
			handler, db := newRealDuckDBHandler(t, mode)
			_, _, err := handler.RunReadOnlySQL(context.Background(), "INSERT INTO t VALUES (2, 'Bob')", nil, 0)
			require.Error(t, err)
			var n int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t").Scan(&n))
			assert.Equal(t, 1, n, "INSERT must not have executed")
		})
		t.Run(string(mode)+"/multi-statement denied", func(t *testing.T) {
			handler, db := newRealDuckDBHandler(t, mode)
			_, _, err := handler.RunReadOnlySQL(context.Background(), "SELECT 1; DROP TABLE t;", nil, 0)
			require.Error(t, err)
			assert.True(t, tableExists(t, db, "t"))
		})
	}
}

// TestHandler_RunReadOnlySQL_LimitPushDown is the #67 step-2 round 4 review
// fix: the row cap is now pushed into the query itself (`SELECT * FROM
// (<sql>) AS _a2ui_limit LIMIT maxRows+1`) rather than fetched-then-sliced,
// so a query over an enormous or unbounded result set returns quickly
// instead of materializing everything first.
func TestHandler_RunReadOnlySQL_LimitPushDown(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeFull)

	t.Run("huge range returns quickly, capped, and reports truncated", func(t *testing.T) {
		done := make(chan struct{})
		var rs *engine.ResultSet
		var truncated bool
		var err error
		go func() {
			rs, truncated, err = handler.RunReadOnlySQL(context.Background(), "SELECT * FROM range(100000000) t(n)", nil, 500)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("RunReadOnlySQL did not return within 10s for range(100000000) — the row cap was not pushed down into the query")
		}
		require.NoError(t, err)
		assert.Len(t, rs.Rows, 500)
		assert.True(t, truncated)
	})

	t.Run("a result smaller than the cap is not marked truncated", func(t *testing.T) {
		rs, truncated, err := handler.RunReadOnlySQL(context.Background(), "SELECT * FROM t", nil, 500)
		require.NoError(t, err)
		assert.Len(t, rs.Rows, 1)
		assert.False(t, truncated)
	})
}

// TestHandler_RunReadOnlySQL_LimitPushDownForNonSelectReadShapes is the #67
// step-2 round 4 review fix's explicit requirement: check the LIMIT
// push-down works for the allowed non-SELECT reads too (DESCRIBE, SHOW
// TABLES, SUMMARIZE), and that the ones that genuinely can't be wrapped as
// a FROM-subquery (PRAGMA table_info(...), EXPLAIN — confirmed empirically,
// both are a DuckDB parser error as `SELECT * FROM (...)`) still succeed by
// falling back to unwrapped execution, rather than erroring out entirely.
func TestHandler_RunReadOnlySQL_LimitPushDownForNonSelectReadShapes(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeFull)

	for _, sql := range []string{
		"DESCRIBE t",
		"SHOW TABLES",
		"SUMMARIZE t",
		"PRAGMA table_info('t')",
	} {
		t.Run(sql, func(t *testing.T) {
			rs, _, err := handler.RunReadOnlySQL(context.Background(), sql, nil, 500)
			require.NoError(t, err, "%s must still succeed even if it can't be wrapped for LIMIT push-down", sql)
			assert.NotEmpty(t, rs.Columns)
		})
	}
}

// TestHandler_RunReadOnlySQL_DeniesDisguisedExternalAccessWrites is the #67
// step-2 round 3 review fix: the round-2 full-mode carve-out in
// checkSQLClassReadOnly matched on classifySQL's reason STRING
// (externalAccessDeniedMsg), but classifyFallback (the write path) returns
// that exact same string for a DELETE/INSERT/UPDATE whose raw text merely
// CONTAINS "read_"/"glob"/a quoted table ref
// (writeStatementSuspiciousText) — indistinguishable, by that string, from
// a genuine external-file read. Every one of these four statements is a
// live-verified write that the round-2 carve-out let through under full
// mode; all four (and the two read-side external-access statements) must
// now be denied in EVERY sandbox mode, with row counts/file content
// unchanged.
func TestHandler_RunReadOnlySQL_DeniesDisguisedExternalAccessWrites(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "x.csv")
	require.NoError(t, os.WriteFile(csvPath, []byte("n\n1\n2\n"), 0o600))
	// Backslashes in a Windows temp path would break the embedded SQL string
	// literal; forward slashes are always valid in DuckDB path arguments.
	csvSQLPath := filepath.ToSlash(csvPath)

	cases := []struct {
		name string
		sql  string
	}{
		{"DELETE disguised as a read via EXISTS(... AS read_me)", `DELETE FROM victim WHERE EXISTS (SELECT 1 AS read_me)`},
		{"INSERT ... SELECT * FROM read_csv(...)", fmt.Sprintf(`INSERT INTO victim SELECT * FROM read_csv('%s')`, csvSQLPath)},
		{"DELETE ... WHERE n IN (SELECT n FROM '<path>')", fmt.Sprintf(`DELETE FROM victim WHERE n IN (SELECT n FROM '%s')`, csvSQLPath)},
		{"UPDATE ... WHERE n IN (SELECT 1 AS glob)", `UPDATE victim SET n = 99 WHERE n IN (SELECT 1 AS glob)`},
		{"SELECT * FROM read_csv(<tmpfile>)", fmt.Sprintf(`SELECT * FROM read_csv('%s')`, csvSQLPath)},
		{"SHOW '<path>'", fmt.Sprintf(`SHOW '%s'`, csvSQLPath)},
	}

	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					handler, db := newRealDuckDBHandler(t, mode)
					_, err := db.Exec(`CREATE TABLE victim (n INTEGER); INSERT INTO victim VALUES (1), (2), (3);`)
					require.NoError(t, err)

					resp, _, err := handler.RunReadOnlySQL(context.Background(), tc.sql, nil, 500)
					require.Error(t, err, "must be denied under %s", mode)
					assert.Nil(t, resp, "no file content or row data may be returned on denial")

					var n int
					require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM victim").Scan(&n))
					assert.Equal(t, 3, n, "victim row count must be unchanged under %s: %s", mode, tc.sql)
					// COUNT(*) alone would not catch an UPDATE that rewrote
					// row VALUES without changing the row count — assert the
					// data itself (SUM) is untouched too, for every case.
					var sum int
					require.NoError(t, db.QueryRow("SELECT SUM(n) FROM victim").Scan(&sum))
					assert.Equal(t, 6, sum, "victim row VALUES must be unchanged under %s: %s", mode, tc.sql)
				})
			}
		})
	}
}

func runQueryWithParams(handler *Handler, sql string, params map[string]any) *Response {
	return handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "run_query",
		Params: map[string]any{"sql": sql, "params": params},
	})
}

// TestHandleRunQuery_SandboxGating drives run_query through the real
// Handler.Handle path — the same one Studio chat and MCP use — against a
// real DuckDB engine, to prove the sandbox bypass is closed: under
// read-write, a DDL statement via run_query is denied exactly like
// create_table already was; under read-only, a plain SELECT still works.
func TestHandleRunQuery_SandboxGating(t *testing.T) {
	t.Run("read-write denies CREATE TABLE via run_query", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)

		resp := runQuery(handler, "CREATE TABLE notes2 (id INTEGER)")
		require.NotNil(t, resp.Error, "CREATE TABLE via run_query must be denied under read-write")
		assert.Equal(t, ErrPermission, resp.Error.Code)
		assert.False(t, tableExists(t, duckDB, "notes2"), "the table must not have been created")
	})

	t.Run("read-write allows INSERT via run_query", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadWrite)
		resp := runQuery(handler, "INSERT INTO users (id, name, email) VALUES (2, 'Bob', 'bob@example.com')")
		assert.Nil(t, resp.Error)
	})

	t.Run("read-only allows a plain SELECT", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQuery(handler, "SELECT * FROM users")
		require.Nil(t, resp.Error)
		result, _ := resp.Result.(map[string]any)
		assert.NotNil(t, result["rows"])
	})

	t.Run("read-only denies DROP TABLE", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQuery(handler, "DROP TABLE users")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrPermission, resp.Error.Code)
	})

	t.Run("read-only denies INSERT", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQuery(handler, "INSERT INTO users (id, name, email) VALUES (3, 'C', 'c@example.com')")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrPermission, resp.Error.Code)
	})
}

// TestHandleRunQuery_RealDuckDB_BypassesAreDenied is the handler-level proof
// (review, item E) that every reported bypass string is denied by the REAL
// agents.Handler dispatching to a REAL DuckDB engine.Manager, and that
// table t2 genuinely still exists afterward.
func TestHandleRunQuery_RealDuckDB_BypassesAreDenied(t *testing.T) {
	t.Run("read-only bypasses (round 2)", func(t *testing.T) {
		bypasses := []struct {
			name string
			sql  string
		}{
			{"dollar-quote smuggled DROP", `SELECT $$'$$ AS probe; DROP TABLE t; --'`},
			{"E'' escape smuggled DROP", `SELECT E'\'' AS probe; DROP TABLE t; --'`},
			{"tagged dollar-quote smuggled DROP", `SELECT $tag$'$tag$ AS probe; DROP TABLE t; --'`},
		}
		for _, tc := range bypasses {
			t.Run(tc.name, func(t *testing.T) {
				handler, duckDB := newRealDuckDBHandler(t, ModeReadOnly)
				require.True(t, tableExists(t, duckDB, "t"), "sanity: t must exist before the attempt")

				resp := runQuery(handler, tc.sql)
				require.NotNilf(t, resp.Error, "bypass %q must be denied under read-only, got result: %+v", tc.sql, resp.Result)
				assert.Equal(t, ErrPermission, resp.Error.Code)
				assert.True(t, tableExists(t, duckDB, "t"), "table t must still exist — the bypass must not have executed the smuggled DROP")
			})
		}
	})

	t.Run("read-write DDL bypass through the Write tier (round 3, item 1)", func(t *testing.T) {
		bypasses := []struct {
			name string
			sql  string
		}{
			{"DELETE hides smuggled DROP via dollar-quote", `DELETE FROM t WHERE name = $$'$$; DROP TABLE t2; --'`},
			{"INSERT hides smuggled DROP via E'' escape", `INSERT INTO t VALUES (1, E'\''); DROP TABLE t2; --'`},
		}
		for _, tc := range bypasses {
			t.Run(tc.name, func(t *testing.T) {
				handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)
				require.True(t, tableExists(t, duckDB, "t2"), "sanity: t2 must exist before the attempt")

				resp := runQuery(handler, tc.sql)
				require.NotNilf(t, resp.Error, "bypass %q must be denied under read-write, got result: %+v", tc.sql, resp.Result)
				assert.Equal(t, ErrPermission, resp.Error.Code)
				assert.True(t, tableExists(t, duckDB, "t2"), "table t2 must still exist — the bypass must not have executed the smuggled DROP")
			})
		}
	})

	t.Run("read-write DML reading the filesystem (round 3, item 2)", func(t *testing.T) {
		dir := t.TempDir()
		leakFile := filepath.Join(dir, "leak.csv")
		require.NoError(t, os.WriteFile(leakFile, []byte("secret\nhunter2\n"), 0o600))

		cases := []string{
			`INSERT INTO t SELECT 99, name FROM read_csv('` + leakFile + `')`,
			`WITH x AS (SELECT * FROM read_text('` + leakFile + `')) INSERT INTO t SELECT 99, x.content FROM x`,
		}
		for _, sqlStr := range cases {
			t.Run(sqlStr, func(t *testing.T) {
				handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)

				resp := runQuery(handler, sqlStr)
				require.NotNilf(t, resp.Error, "query %q must be denied under read-write, got result: %+v", sqlStr, resp.Result)
				assert.Equal(t, ErrPermission, resp.Error.Code)

				var count int
				require.NoError(t, duckDB.QueryRow("SELECT count(*) FROM t WHERE id = 99").Scan(&count))
				assert.Zero(t, count, "no row from the leaked file must have been inserted")
			})
		}
	})

	t.Run("read-only replacement scans relative to the working directory (round 3, item 3)", func(t *testing.T) {
		// A relative filename in the process's actual working directory —
		// exactly the shape the review reported ('s.tsv' with no path
		// separator at all), so cwd-independent absolute-path denial isn't
		// what's under test here.
		relName := "s_round3_test.tsv"
		require.NoError(t, os.WriteFile(relName, []byte("id\tname\n1\tsecret\n"), 0o600))
		t.Cleanup(func() { _ = os.Remove(relName) })

		cases := []string{
			`SELECT * FROM '` + relName + `'`,
			`FROM '` + relName + `'`,
			`SUMMARIZE '` + relName + `'`,
			`DESCRIBE SELECT * FROM '` + relName + `'`,
			`SHOW '` + relName + `'`,
		}
		for _, sqlStr := range cases {
			t.Run(sqlStr, func(t *testing.T) {
				handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
				resp := runQuery(handler, sqlStr)
				require.NotNilf(t, resp.Error, "query %q must be denied under read-only, got result: %+v", sqlStr, resp.Result)
				assert.Equal(t, ErrPermission, resp.Error.Code)
				assert.Nil(t, resp.Result, "no column names or file content may be returned")
			})
		}
	})

	t.Run("read-only denies file access even though it's a pure SELECT", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQuery(handler, "SELECT * FROM read_csv('/etc/passwd')")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrPermission, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "file reads are not available here")
	})

	t.Run("read-only denies current_setting", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQuery(handler, "SELECT current_setting('s3_access_key_id')")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrPermission, resp.Error.Code)
	})

	t.Run("read-only still allows a genuine read afterward", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQuery(handler, "SELECT * FROM t")
		require.Nil(t, resp.Error)
		result, _ := resp.Result.(map[string]any)
		rows, _ := result["rows"].([][]any)
		assert.Len(t, rows, 1)
	})
}

// TestCheckPlanExternalAccess_EXPLAINDoesNotExecute is the round-4 review's
// required proof that plain EXPLAIN (FORMAT JSON) — which
// checkPlanExternalAccess runs on every Write-tier statement — only binds
// the statement against the catalog and never executes it. It runs EXPLAIN
// over a DELETE and confirms the target table still has its row afterward.
// EXPLAIN ANALYZE, which DOES execute, is never used anywhere in this
// package (see the explicit exclusion in classifySQL for the read path).
func TestCheckPlanExternalAccess_EXPLAINDoesNotExecute(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE t2 (id INTEGER); INSERT INTO t2 VALUES (1), (2), (3)`)
	require.NoError(t, err)

	rows, err := db.Query("EXPLAIN (FORMAT JSON) DELETE FROM t2")
	require.NoError(t, err)
	for rows.Next() {
		var k, v string
		require.NoError(t, rows.Scan(&k, &v))
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM t2").Scan(&count))
	assert.Equal(t, 3, count, "EXPLAIN must not have executed the DELETE")
}

// newRound4LeakFixture builds a real-DuckDB-backed handler in read-write
// mode with a single empty target table t(a VARCHAR) and three real,
// relative-path leak files (s.csv, s.jsonl, s.tsv — each one column, "a"),
// written into and the process chdir'd into a fresh temp directory so the
// exact relative filenames the review reported ('s.csv', "s.tsv", ...) are
// real, readable files relative to cwd — not just plausible-looking
// strings a bind failure would reject regardless of the security check.
// t.Cleanup restores the original working directory.
func newRound4LeakFixture(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s.csv"), []byte("a\nsecret-csv\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s.tsv"), []byte("a\nsecret-tsv\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(`{"a":"secret-jsonl"}`+"\n"), 0o600))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	duckDB, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = duckDB.Close() })
	_, err = duckDB.Exec(`CREATE TABLE t (a VARCHAR)`)
	require.NoError(t, err)

	sandbox := &SandboxConfig{Mode: ModeReadWrite, MaxQueryRows: 100}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	container := &mockAccess{duckDB: duckDB}
	eng := engine.NewManager(sessiontest.ForTests(duckDB), engine.WithLogger(logger))
	return NewHandler(container, eng, sandbox, logger), duckDB
}

func assertTableEmpty(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
	assert.Zero(t, count, "table %s must stay empty — the leak must not have executed", table)
}

// TestHandleRunQuery_RealDuckDB_Round4WriteTierBypassesAreDenied is the
// round-4 regression suite: every string the review reported as still
// leaking a file through the Write tier (all confirmed live through the
// real Handler against a real DuckDB, in read-write mode), now denied by
// checkPlanExternalAccess (DuckDB's own bound plan) instead of the
// bypassable raw-text scan. Quoting the function name, splitting a keyword
// from its argument with a comment, and the bare/quoted replacement-scan
// forms all resolve to the exact same physical-plan operator once DuckDB
// binds them — there is nothing left in the plan to hide behind.
func TestHandleRunQuery_RealDuckDB_Round4WriteTierBypassesAreDenied(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{
			"quoted read_csv function name",
			`INSERT INTO t SELECT * FROM "read_csv"('/etc/passwd', header=false, columns={'a':'VARCHAR'}) LIMIT 1`,
		},
		{
			"quoted read_text function name",
			`INSERT INTO t SELECT content FROM "read_text"('s.csv')`,
		},
		{
			"unquoted replacement scan (schema.table disguise)",
			`INSERT INTO t SELECT * FROM s.csv`,
		},
		{
			"quoted relative filename replacement scan",
			`INSERT INTO t SELECT * FROM "s.tsv"`,
		},
		{
			"comment-split FROM and replacement scan",
			`INSERT INTO t SELECT * FROM /**/ 's.tsv'`,
		},
		{
			"replacement scan inside an UPDATE subquery",
			`UPDATE t SET a=(SELECT a FROM s.jsonl LIMIT 1)`,
		},
		{
			"replacement scan via DELETE ... USING",
			`DELETE FROM t USING s.tsv x WHERE t.a = x.a`,
		},
		{
			"replacement scan via UPDATE ... FROM",
			`UPDATE t SET a = x.a FROM s.tsv x WHERE t.a = x.a`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, duckDB := newRound4LeakFixture(t)
			resp := runQuery(handler, tc.sql)
			require.NotNilf(t, resp.Error, "query %q must be denied under read-write, got result: %+v", tc.sql, resp.Result)
			assert.Equal(t, ErrPermission, resp.Error.Code)
			assertTableEmpty(t, duckDB, "t")
		})
	}
}

// TestHandleRunQuery_RealDuckDB_InformationSchemaAllowed is the round-4/5
// follow-up: agents probe information_schema.* and a handful of duckdb_*
// introspection views constantly for routine schema inspection. Round 4
// allowed them via an any-catalog fallback (any system-catalog view
// resolved); round 5 replaced that with a closed allow-list (see
// allowedSystemObjects) after review flagged that the round-4 fallback also
// let duckdb_databases and pragma_database_list resolve — both list every
// attached database's file path, which for this container is the decrypted
// temp DB file.
func TestHandleRunQuery_RealDuckDB_InformationSchemaAllowed(t *testing.T) {
	allowed := []string{
		"SELECT * FROM information_schema.tables",
		"SELECT * FROM information_schema.columns",
		"SELECT * FROM information_schema.schemata",
		"SELECT * FROM information_schema.views",
		"SELECT * FROM information_schema.table_constraints",
		"SELECT * FROM information_schema.key_column_usage",
		"SELECT * FROM information_schema.referential_constraints",
		"SELECT * FROM duckdb_tables",
		"SELECT * FROM duckdb_columns",
		"SELECT * FROM duckdb_views",
		"SELECT * FROM duckdb_constraints",
		"SELECT * FROM duckdb_indexes",
		"SELECT * FROM duckdb_types",
	}
	for _, sqlStr := range allowed {
		t.Run(sqlStr, func(t *testing.T) {
			handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
			resp := runQuery(handler, sqlStr)
			assert.Nilf(t, resp.Error, "query %q should be allowed in read-only, got: %v", sqlStr, resp.Error)
		})
	}

	denied := []string{
		"SELECT * FROM pg_catalog.pg_settings",
		"SELECT * FROM duckdb_logs",
		"SELECT * FROM duckdb_logs()",
		// round 5 follow-up: leaks the decrypted temp DB's file path.
		"SELECT * FROM duckdb_databases",
		"SELECT * FROM duckdb_databases()",
		"SELECT * FROM pragma_database_list",
		"SELECT * FROM pragma_database_list()",
		// pg_catalog.* beyond pg_settings should not resolve either — only
		// the explicit information_schema/duckdb_* allow-list does.
		"SELECT * FROM pg_catalog.pg_tables",
	}
	for _, sqlStr := range denied {
		t.Run(sqlStr, func(t *testing.T) {
			handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
			resp := runQuery(handler, sqlStr)
			require.NotNilf(t, resp.Error, "query %q must stay denied in read-only, got result: %+v", sqlStr, resp.Result)
			assert.Equal(t, ErrPermission, resp.Error.Code)
		})
	}
}

// TestHandleRunQuery_RealDuckDB_PragmaDatabaseListDenied is the round-6
// hardening follow-up: PRAGMA database_list is a harmless-looking
// introspection PRAGMA, but it returns the decrypted temp DB file's on-disk
// path — the same leak duckdb_databases()/pragma_database_list() were
// already closed off for. It must be denied in read-only, not silently
// allowed via pragmaReadAllowlist.
func TestHandleRunQuery_RealDuckDB_PragmaDatabaseListDenied(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	resp := runQuery(handler, "PRAGMA database_list")
	require.NotNilf(t, resp.Error, "PRAGMA database_list must be denied in read-only, got result: %+v", resp.Result)
	assert.Equal(t, ErrPermission, resp.Error.Code)
}

// TestHandleRunQuery_RealDuckDB_ParameterizedQueries is the round-5 blocker
// fix: classifySQL used to classify the RAW, unbound SQL text — so a
// perfectly ordinary parameterized query like `SELECT * FROM customers
// WHERE id = $id` reached EXPLAIN with a bare, unbindable `$id` placeholder
// (EXPLAIN failed to bind, misreported as "external file/network access
// requires the full sandbox") and reached looksObfuscated with a literal
// `$` in the text (denied outright — looksObfuscated rejects any `$`).
// classifySQL now binds params via engine.BindParams before classifying
// anything, so both a parameterized read in read-only and a parameterized
// write in read-write must now succeed exactly as they did at HEAD, while a
// dollar-quoted bypass string — which still contains a literal `$` in its
// BOUND form, since dollar-quote delimiters are never valid $name
// placeholders — must keep failing closed even with an unrelated params
// object present.
func TestHandleRunQuery_RealDuckDB_ParameterizedQueries(t *testing.T) {
	t.Run("parameterized SELECT succeeds in read-only", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQueryWithParams(handler, "SELECT * FROM users WHERE id = $id", map[string]any{"id": 1})
		require.Nilf(t, resp.Error, "parameterized SELECT should succeed in read-only, got: %v", resp.Error)
		result, _ := resp.Result.(map[string]any)
		rows, _ := result["rows"].([][]any)
		assert.Len(t, rows, 1)
	})

	t.Run("named parameter used twice still succeeds", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQueryWithParams(handler, "SELECT * FROM users WHERE id = $id OR id = $id", map[string]any{"id": 1})
		require.Nilf(t, resp.Error, "got: %v", resp.Error)
	})

	t.Run("parameterized INSERT succeeds in read-write", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)
		resp := runQueryWithParams(handler, "INSERT INTO t2 (id) VALUES ($v)", map[string]any{"v": 42})
		require.Nilf(t, resp.Error, "parameterized INSERT should succeed in read-write, got: %v", resp.Error)
		var count int
		require.NoError(t, duckDB.QueryRow("SELECT count(*) FROM t2 WHERE id = 42").Scan(&count))
		assert.Equal(t, 1, count)
	})

	t.Run("dollar-quote bypass still denied even with a params object present", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQueryWithParams(handler, `SELECT $$'$$ AS probe; DROP TABLE t; --'`, map[string]any{"id": 1})
		require.NotNilf(t, resp.Error, "dollar-quote bypass must stay denied, got result: %+v", resp.Result)
		assert.Equal(t, ErrPermission, resp.Error.Code)
		assert.True(t, tableExists(t, duckDB, "t"))
	})

	t.Run("dollar-quote bypass through a params key matching the tag is still denied", func(t *testing.T) {
		// If the tag inside a dollar-quote happens to collide with a
		// supplied parameter name, BindParams partially (and only
		// partially) replaces it, producing syntactically broken SQL — it
		// must still end up denied (via a failed parse/bind, or a leftover
		// '$'), never accidentally "cleaned up" into something that runs.
		handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)
		resp := runQueryWithParams(handler, `DELETE FROM t WHERE name = $tag$'$tag$; DROP TABLE t2; --'`, map[string]any{"tag": "x"})
		require.NotNilf(t, resp.Error, "got result: %+v", resp.Result)
		assert.Equal(t, ErrPermission, resp.Error.Code)
		assert.True(t, tableExists(t, duckDB, "t2"))
	})

	t.Run("missing referenced parameter fails closed with a clear message, not a security message", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		resp := runQueryWithParams(handler, "SELECT * FROM users WHERE id = $id", map[string]any{})
		require.NotNil(t, resp.Error)
		assert.Contains(t, resp.Error.Message, "could not validate query")
		assert.NotContains(t, resp.Error.Message, externalAccessDeniedMsg)
	})

	t.Run("EXPLAIN bind failure is reported honestly, not as external access", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
		// A syntactically fine, single, pure-SELECT statement that will
		// fail to bind (no such column) — must be denied with an honest
		// "could not validate query" message, not the external-access one.
		resp := runQuery(handler, "SELECT * FROM users WHERE nonexistent_column = 1")
		require.NotNil(t, resp.Error)
		assert.Contains(t, resp.Error.Message, "could not validate query")
		assert.NotContains(t, resp.Error.Message, externalAccessDeniedMsg)
	})
}

// --- Finding #1 (round 2 review): record/DDL tool handlers used to build
// SQL by raw concatenation (zero identifier quoting, and a raw `where`
// param appended straight onto the statement) — the reviewer proved
// delete_record{table:"users", where:"1=0; COPY (SELECT 'x') TO '<path>'"}
// wrote a file, and where:"1=0; DROP TABLE users" dropped the table, under
// read-write. The fix: every identifier goes through session.QuoteIdent, and
// create/update/delete/bulk_insert now build the FULL statement (quoted
// identifiers + the raw `where`/whatever) and run it through classifySQL
// (requireWriteStatement) before ever calling ExecContext — the exact same
// real-DuckDB classifier run_query uses. A `where` that smuggles a second
// statement makes countStatements return >1, so classifySQL returns
// sqlClassOther and requireWriteStatement denies it — no hand-rolled
// semicolon scanning. ---

// TestHandler_DeleteRecord_MultiStatementWhere_Denied reproduces
// a reviewer's read-write bypass proof of concept exactly: under
// read-write, a `where` smuggling a second statement (COPY ... TO or DROP
// TABLE) must be denied, no file written, and the table must survive.
func TestHandler_DeleteRecord_MultiStatementWhere_Denied(t *testing.T) {
	t.Run("where smuggles COPY ... TO", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)
		pwned := filepath.Join(t.TempDir(), "rw-bypass.txt")
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "delete_record",
			Params: map[string]any{"table": "users", "where": "1=0; COPY (SELECT 'rw-bypass') TO '" + pwned + "'"},
		})
		require.NotNil(t, resp.Error, "must be denied")
		assert.Equal(t, ErrPermission, resp.Error.Code)
		_, statErr := os.Stat(pwned)
		assert.Error(t, statErr, "the injected COPY ... TO must never have run")
		assert.True(t, tableExists(t, duckDB, "users"))
	})

	t.Run("where smuggles DROP TABLE", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "delete_record",
			Params: map[string]any{"table": "users", "where": "1=0; DROP TABLE users"},
		})
		require.NotNil(t, resp.Error, "must be denied")
		assert.Equal(t, ErrPermission, resp.Error.Code)
		assert.True(t, tableExists(t, duckDB, "users"), "the users table must still exist")
	})

	t.Run("update_record: same where-based multi-statement smuggle is denied", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeReadWrite)
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "update_record",
			Params: map[string]any{"table": "users", "values": map[string]any{"name": "pwned"}, "where": "1=0; DROP TABLE users"},
		})
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrPermission, resp.Error.Code)
		assert.True(t, tableExists(t, duckDB, "users"))
	})
}

// TestHandler_DeleteRecord_LegitimateWhere_StillWorks proves the fix isn't
// just "deny everything" — an ordinary single-condition where clause must
// still delete exactly the matched row.
func TestHandler_DeleteRecord_LegitimateWhere_StillWorks(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadWrite)
	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "delete_record",
		Params: map[string]any{"table": "users", "where": "name = 'Alice'"},
	})
	require.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	assert.Equal(t, int64(1), result["rows_affected"])
}

// TestHandler_RecordTools_HostileIdentifiers_CannotWriteFile proves
// create_record/update_record/bulk_insert/create_table/add_column/
// drop_table all quote their table/column identifiers correctly (via
// session.QuoteIdent) instead of concatenating them raw — a crafted
// table/column name can't break out and run an attacker statement.
func TestHandler_RecordTools_HostileIdentifiers_CannotWriteFile(t *testing.T) {
	evilTable := `x") ; COPY (SELECT 'pwned') TO '`

	t.Run("create_record: hostile table name is treated as a literal (nonexistent) table", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeFull)
		pwned := filepath.Join(t.TempDir(), "create-record.txt")
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "create_record",
			Params: map[string]any{"table": evilTable + pwned + `'; --`, "values": map[string]any{"a": 1}},
		})
		require.NotNil(t, resp.Error)
		_, statErr := os.Stat(pwned)
		assert.Error(t, statErr)
	})

	t.Run("create_table: hostile column name cannot break out", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeFull)
		pwned := filepath.Join(t.TempDir(), "create-table.txt")
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "create_table",
			Params: map[string]any{
				"name": "evil_ct",
				"columns": []any{
					map[string]any{"name": `a") ; COPY (SELECT 'pwned') TO '` + pwned + `'; --`, "type": "INTEGER"},
				},
			},
		})
		// Whether it succeeds (creating a table with that literal weird
		// column name) or fails, the COPY must never have run.
		_ = resp
		_, statErr := os.Stat(pwned)
		assert.Error(t, statErr, "the injected COPY ... TO must never have run")
		if resp.Error == nil {
			assert.True(t, tableExists(t, duckDB, "evil_ct"))
		}
	})

	t.Run("add_column: hostile column name cannot break out", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeFull)
		pwned := filepath.Join(t.TempDir(), "add-column.txt")
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "add_column",
			Params: map[string]any{"table": "users", "name": `a") ; COPY (SELECT 'pwned') TO '` + pwned + `'; --`, "type": "INTEGER"},
		})
		_ = resp
		_, statErr := os.Stat(pwned)
		assert.Error(t, statErr)
	})

	t.Run("drop_table: hostile table name cannot break out", func(t *testing.T) {
		handler, duckDB := newRealDuckDBHandler(t, ModeFull)
		pwned := filepath.Join(t.TempDir(), "drop-table.txt")
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "drop_table",
			Params: map[string]any{"name": `x") ; COPY (SELECT 'pwned') TO '` + pwned + `'; --`},
		})
		_ = resp
		_, statErr := os.Stat(pwned)
		assert.Error(t, statErr)
		assert.True(t, tableExists(t, duckDB, "users"), "the real users table must be untouched")
	})

	t.Run("bulk_insert: hostile column name cannot break out", func(t *testing.T) {
		handler, _ := newRealDuckDBHandler(t, ModeFull)
		pwned := filepath.Join(t.TempDir(), "bulk-insert.txt")
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "bulk_insert",
			Params: map[string]any{
				"table":   "t",
				"columns": []any{`a") FROM t; COPY (SELECT 42) TO '` + pwned + `'; --`},
				"rows":    []any{[]any{1}},
			},
		})
		require.NotNil(t, resp.Error, "hostile column doesn't exist on t, so the insert itself must fail — but never via the injected COPY")
		_, statErr := os.Stat(pwned)
		assert.Error(t, statErr)
	})
}

// TestHandler_CreateTable_InvalidColumnType_Rejected proves the column-type
// text allow-list (crud.ValidColumnType) rejects an attempt to smuggle SQL
// via the type field.
func TestHandler_CreateTable_InvalidColumnType_Rejected(t *testing.T) {
	handler, duckDB := newRealDuckDBHandler(t, ModeFull)
	pwned := filepath.Join(t.TempDir(), "coltype.txt")
	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "create_table",
		Params: map[string]any{
			"name": "evil_ct2",
			"columns": []any{
				map[string]any{"name": "a", "type": `INTEGER); COPY (SELECT 'pwned') TO '` + pwned + `'; --`},
			},
		},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)
	_, statErr := os.Stat(pwned)
	assert.Error(t, statErr)
	assert.False(t, tableExists(t, duckDB, "evil_ct2"))
}

// TestClassifySQL_NextvalIsNeverRead is a regression guard (live review
// finding, step 7 round 3): `SELECT nextval('s')` parses as an ordinary
// single-statement SELECT with no BASE_TABLE write and no external-access
// concern, so it used to fall all the way through to sqlClassRead — despite
// nextval() mutating the sequence's counter as a real side effect every time
// it's evaluated. A "read" classification (used directly by run_query's
// sandbox gating AND by internal/tui's IsReadOnlyStatement-based
// unsaved-changes tracker) must never let this through as a plain read.
func TestClassifySQL_NextvalIsNeverRead(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE SEQUENCE s`)
	require.NoError(t, err)

	class, reason := classifySQL(context.Background(), db, "SELECT nextval('s')", nil)
	assert.NotEqual(t, sqlClassRead, class, "SELECT nextval('s') must never classify as a pure read")
	assert.NotEmpty(t, reason)
	assert.False(t, IsReadOnlyStatement(context.Background(), db, "SELECT nextval('s')", nil))

	// setval is the same kind of mutation and must be caught the same way.
	class2, _ := classifySQL(context.Background(), db, "SELECT setval('s', 5)", nil)
	assert.NotEqual(t, sqlClassRead, class2)

	// An ordinary read must be unaffected.
	assert.Equal(t, sqlClassRead, classify(t, "SELECT * FROM t"))
}

// Credential DDL and secret listings are never agent SQL (#114): the session
// creates secrets itself, on its own non-logging path. Denied in every mode.
func TestClassifyDeniesSecretStatements(t *testing.T) {
	for _, q := range []string{
		`CREATE SECRET s (TYPE http, BEARER_TOKEN 'x')`,
		`CREATE OR REPLACE TEMPORARY SECRET s (TYPE http)`,
		`DROP SECRET s`,
		`SELECT * FROM duckdb_secrets()`,
		`SELECT which_secret('s3://b/x', 's3')`,
		`ATTACH '' AS pg (TYPE postgres, SECRET mysecret)`,
		`SET s3_secret_access_key='x'`,
		`SELECT 1; CREATE SECRET s (TYPE http)`,
		`select 1; attach 'x' as y (secret z)`,
		"CREATE/**/SECRET s (TYPE http)",
		"CREATE -- c\nSECRET s (TYPE http)",
		"/* c */ ATTACH '' AS pg (TYPE postgres, SECRET pg)",
		`/* x */ SELECT * FROM "duckdb_secrets"()`,
		`SELECT * FROM query('SELECT * FROM duckdb_' || 'secrets()')`,
		`SELECT * FROM query_table(some_expr)`,
		`SELECT * FROM query('SELECT * FROM duckdb_secrets()')`,
		`SELECT * FROM query($$ create secret s (type http) $$)`,
		`CREATE OR REPLACE PERSISTENT SECRET s (TYPE http)`,
		`DROP SECRET IF EXISTS s`,
		`SELECT * FROM QUERY ( 'x' || 'y' )`,
	} {
		assert.Equal(t, sqlClassOther, classify(t, q), q)
		for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
			class, reason := classifyReason(t, q)
			err := (&SandboxConfig{Mode: mode}).checkSQLClass(class, reason)
			require.Error(t, err, "%s in %s", q, mode)
		}
	}
	// A column or table merely named like a secret is still just SQL.
	assert.Equal(t, sqlClassRead, classify(t, `SELECT 1 AS secret`))
	assert.Equal(t, sqlClassRead, classify(t, `SELECT 'create secret' AS note -- create secret`))
	assert.False(t, touchesSecrets(`SELECT secret FROM t`))
	assert.False(t, touchesSecrets(`SELECT * FROM query('SELECT 1')`))
}
