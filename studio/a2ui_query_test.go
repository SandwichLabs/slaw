package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setAgentSandbox is a small test helper mirroring SetAgentConfig's write
// path, without going through guardStudioOnly/Wails plumbing.
func setAgentSandbox(t *testing.T, app *App, mode string) {
	t.Helper()
	require.NoError(t, app.SetAgentConfig(AgentConfig{Sandbox: mode, AskBeforeWrites: true}))
}

// TestRunA2UIQuery_NoFileOpen is the #3 review requirement: RunA2UIQuery
// must error, not panic, with no workspace open.
func TestRunA2UIQuery_NoFileOpen(t *testing.T) {
	app := NewApp(testLogger())
	app.ctx = context.Background()
	_, err := app.RunA2UIQuery("SELECT 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace open")
}

// TestRunA2UIQuery_ReadAllowedInEveryMode proves an ordinary SELECT succeeds
// under read-only, read-write, and full — a live A2UI component's whole
// point is that reads always work regardless of sandbox.
func TestRunA2UIQuery_ReadAllowedInEveryMode(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec("CREATE TABLE t (id INTEGER, name TEXT)")
	require.NoError(t, err)
	_, err = app.sess.DB().Exec("INSERT INTO t VALUES (1, 'a'), (2, 'b')")
	require.NoError(t, err)

	for _, mode := range []string{"read-only", "read-write", "full"} {
		t.Run(mode, func(t *testing.T) {
			setAgentSandbox(t, app, mode)
			res, err := app.RunA2UIQuery("SELECT id, name FROM t ORDER BY id")
			require.NoError(t, err)
			require.Len(t, res.Rows, 2)
			assert.Equal(t, []string{"id", "name"}, res.Columns)
		})
	}
}

// TestRunA2UIQuery_WriteDeniedInFullMode is the #2 review requirement (the
// render-time-writes blocker): a live "sql" binding must never execute a
// write, even under the full sandbox, since it re-runs on every render.
func TestRunA2UIQuery_WriteDeniedInFullMode(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec("CREATE TABLE junk (id INTEGER)")
	require.NoError(t, err)
	_, err = app.sess.DB().Exec("INSERT INTO junk VALUES (1)")
	require.NoError(t, err)
	setAgentSandbox(t, app, "full")

	t.Run("DROP TABLE", func(t *testing.T) {
		_, err := app.RunA2UIQuery("DROP TABLE junk")
		require.Error(t, err)
		var n int
		require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'junk'").Scan(&n))
		assert.Equal(t, 1, n, "DROP must not have executed")
	})

	t.Run("INSERT", func(t *testing.T) {
		_, err := app.RunA2UIQuery("INSERT INTO junk VALUES (2)")
		require.Error(t, err)
		var n int
		require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM junk").Scan(&n))
		assert.Equal(t, 1, n, "INSERT must not have executed")
	})

	t.Run("multi-statement", func(t *testing.T) {
		_, err := app.RunA2UIQuery("SELECT 1; DROP TABLE junk;")
		require.Error(t, err)
		var n int
		require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'junk'").Scan(&n))
		assert.Equal(t, 1, n, "smuggled DROP must not have executed")
	})
}

// TestRunA2UIQuery_WriteDeniedInReadWriteMode: read-write is even more
// permissive than full for handleRunQuery's own DML allowance — must still
// deny a write here.
func TestRunA2UIQuery_WriteDeniedInReadWriteMode(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec("CREATE TABLE junk (id INTEGER)")
	require.NoError(t, err)
	setAgentSandbox(t, app, "read-write")

	_, err = app.RunA2UIQuery("INSERT INTO junk VALUES (1)")
	require.Error(t, err)
	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM junk").Scan(&n))
	assert.Equal(t, 0, n)
}

// TestRunA2UIQuery_ReadOnlyDeniesDropAndMultiStatement is the #3 review
// requirement's read-only case.
func TestRunA2UIQuery_ReadOnlyDeniesDropAndMultiStatement(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec("CREATE TABLE junk (id INTEGER)")
	require.NoError(t, err)
	setAgentSandbox(t, app, "read-only")

	_, err = app.RunA2UIQuery("DROP TABLE junk")
	require.Error(t, err)

	_, err = app.RunA2UIQuery("SELECT 1; DROP TABLE junk;")
	require.Error(t, err)

	var n int
	require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'junk'").Scan(&n))
	assert.Equal(t, 1, n)
}

// TestRunA2UIQuery_RowCapEnforcedServerSide is the #3 review requirement:
// the 500-row cap must be enforced in RunA2UIQuery itself, not left to the
// frontend.
func TestRunA2UIQuery_RowCapEnforcedServerSide(t *testing.T) {
	app := newOpenApp(t)
	setAgentSandbox(t, app, "full")

	res, err := app.RunA2UIQuery("SELECT * FROM range(0, 10000) t(n)")
	require.NoError(t, err)
	assert.Equal(t, a2uiQueryRowCap, len(res.Rows))
	assert.True(t, res.Truncated)
}

// TestRunA2UIQuery_HugeRangeReturnsQuickly is the #67 step-2 round 4 review
// requirement: the row cap is pushed down into the query itself
// (RunReadOnlySQL's LIMIT push-down), so a query over an unbounded/huge
// result set — SELECT * FROM range(100000000), 100 million rows — returns
// quickly with exactly the cap's worth of rows instead of materializing
// the whole thing first and slicing it down afterward.
func TestRunA2UIQuery_HugeRangeReturnsQuickly(t *testing.T) {
	app := newOpenApp(t)
	setAgentSandbox(t, app, "full")

	done := make(chan struct{})
	var res *A2UIQueryResult
	var err error
	go func() {
		res, err = app.RunA2UIQuery("SELECT * FROM range(100000000) t(n)")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunA2UIQuery did not return within 10s for range(100000000) — the row cap was not pushed down into the query")
	}
	require.NoError(t, err)
	assert.Equal(t, a2uiQueryRowCap, len(res.Rows))
	assert.True(t, res.Truncated)
}

// TestRunA2UIQuery_NestedNonFiniteFloatDoesNotCrash is the #67 step-2 round
// 3 review fix (blocker 2): a NaN/Inf nested inside a LIST/STRUCT/MAP
// crashed the app exactly like a bare top-level one — proven live with
// `SELECT ['nan'::DOUBLE, 1.0] AS l`. NormalizeValue now recurses through
// composite shapes (internal/engine/normalize.go); this proves it end to
// end through the actual binding the live crash was reported against.
func TestRunA2UIQuery_NestedNonFiniteFloatDoesNotCrash(t *testing.T) {
	app := newOpenApp(t)
	setAgentSandbox(t, app, "full")

	for _, sql := range []string{
		`SELECT ['nan'::DOUBLE, 1.0] AS l`,
		`SELECT {'a': 'inf'::DOUBLE} AS s`,
		`SELECT MAP {'k': 'nan'::DOUBLE} AS m`,
		`SELECT [{'x':'nan'::DOUBLE}] AS ls`,
	} {
		t.Run(sql, func(t *testing.T) {
			require.NotPanics(t, func() {
				res, err := app.RunA2UIQuery(sql)
				require.NoError(t, err)
				require.Len(t, res.Rows, 1)
				// Confirm the returned value is actually JSON-marshalable —
				// the literal failure mode the live crash hit.
				_, jsonErr := json.Marshal(res)
				require.NoError(t, jsonErr)
			})
		})
	}
}

// TestJSONRoundTripGuard is a unit test for the last-resort defence-in-depth
// guard: it must accept an ordinary marshalable value and reject one that
// isn't, without panicking either way.
func TestJSONRoundTripGuard(t *testing.T) {
	assert.NoError(t, jsonRoundTripGuard(&A2UIQueryResult{Columns: []string{"a"}, Rows: [][]any{{1}}}))

	err := jsonRoundTripGuard(map[string]any{"v": math.NaN()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "couldn't encode")

	require.NotPanics(t, func() {
		_ = jsonRoundTripGuard(math.NaN())
	})
}

// TestRunA2UIQuery_DeniesDisguisedExternalAccessWrites is the #67 step-2
// round 3 review fix (blocker): the round-2 full-mode "external access"
// carve-out in checkSQLClassReadOnly matched only on classifySQL's reason
// STRING, and classifyFallback returns that same string
// (externalAccessDeniedMsg) for a write statement whose raw TEXT merely
// contains "read_"/"glob"/a quoted table reference — live-verified to let
// real writes through RunA2UIQuery under full mode. The carve-out is now
// removed entirely; every one of these must be denied in every mode, with
// row counts unchanged.
func TestRunA2UIQuery_DeniesDisguisedExternalAccessWrites(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "x.csv")
	require.NoError(t, os.WriteFile(csvPath, []byte("n\n1\n2\n"), 0o600))
	csvSQLPath := filepath.ToSlash(csvPath)

	cases := []struct {
		name string
		sql  string
	}{
		{"DELETE disguised via EXISTS(... AS read_me)", `DELETE FROM victim WHERE EXISTS (SELECT 1 AS read_me)`},
		{"INSERT ... SELECT * FROM read_csv(...)", fmt.Sprintf(`INSERT INTO victim SELECT * FROM read_csv('%s')`, csvSQLPath)},
		{"DELETE ... WHERE n IN (SELECT n FROM '<path>')", fmt.Sprintf(`DELETE FROM victim WHERE n IN (SELECT n FROM '%s')`, csvSQLPath)},
		{"UPDATE ... WHERE n IN (SELECT 1 AS glob)", `UPDATE victim SET n = 99 WHERE n IN (SELECT 1 AS glob)`},
		{"SELECT * FROM read_csv(<tmpfile>)", fmt.Sprintf(`SELECT * FROM read_csv('%s')`, csvSQLPath)},
		{"SHOW '<path>'", fmt.Sprintf(`SHOW '%s'`, csvSQLPath)},
	}

	for _, mode := range []string{"read-only", "read-write", "full"} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					app := newOpenApp(t)
					_, err := app.sess.DB().Exec(`CREATE TABLE victim (n INTEGER); INSERT INTO victim VALUES (1), (2), (3);`)
					require.NoError(t, err)
					setAgentSandbox(t, app, mode)

					res, err := app.RunA2UIQuery(tc.sql)
					require.Error(t, err, "must be denied under %s sandbox", mode)
					assert.Nil(t, res, "no file content or row data may be returned on denial")

					var n int
					require.NoError(t, app.sess.DB().QueryRow("SELECT COUNT(*) FROM victim").Scan(&n))
					assert.Equal(t, 3, n, "victim row count must be unchanged under %s: %s", mode, tc.sql)
					// COUNT(*) alone would not catch an UPDATE that rewrote
					// row VALUES without changing the row count — assert the
					// data itself (SUM) is untouched too, for every case.
					var sum int
					require.NoError(t, app.sess.DB().QueryRow("SELECT SUM(n) FROM victim").Scan(&sum))
					assert.Equal(t, 6, sum, "victim row VALUES must be unchanged under %s: %s", mode, tc.sql)
				})
			}
		})
	}
}

// TestRunA2UIQuery_NonFiniteFloatDoesNotCrash is the #1 review requirement
// (the process-killing crash): a NaN/Inf result must come back as a normal
// row, never an error that propagates as an unmarshalable value, and must
// never panic/crash the call. The value is DuckDB's own text
// ("nan"/"inf"/"-inf"), not nil — NaN/Inf are real values distinct from
// SQL NULL, so the wire rule keeps them faithful (matching CSV export)
// rather than collapsing them into NULL.
func TestRunA2UIQuery_NonFiniteFloatDoesNotCrash(t *testing.T) {
	app := newOpenApp(t)
	setAgentSandbox(t, app, "full")

	for _, tc := range []struct {
		sql  string
		want any
	}{
		{"SELECT 'nan'::DOUBLE", "nan"},
		{"SELECT 'inf'::DOUBLE", "inf"},
		{"SELECT '-inf'::DOUBLE", "-inf"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			require.NotPanics(t, func() {
				res, err := app.RunA2UIQuery(tc.sql)
				require.NoError(t, err)
				require.Len(t, res.Rows, 1)
				assert.Equal(t, tc.want, res.Rows[0][0])
			})
		})
	}
}
