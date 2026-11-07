//go:build integration

package agents_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// writeDeltaTable writes a minimal, valid Delta Lake table (one Parquet file and
// a one-commit log) at dir. The Delta extension reads it with delta_scan.
func writeDeltaTable(t *testing.T, sess session.Session, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "_delta_log"), 0o755))
	part := filepath.Join(dir, "part-0000.parquet")
	_, err := sess.DB().Exec(fmt.Sprintf(`COPY (SELECT * FROM (VALUES (1, 'ada'), (2, 'bob')) t(id, name)) TO '%s' (FORMAT PARQUET)`, part))
	require.NoError(t, err)
	fi, err := os.Stat(part)
	require.NoError(t, err)
	schema := `{"type":"struct","fields":[{"name":"id","type":"integer","nullable":true,"metadata":{}},{"name":"name","type":"string","nullable":true,"metadata":{}}]}`
	log := strings.Join([]string{
		`{"protocol":{"minReaderVersion":1,"minWriterVersion":2}}`,
		fmt.Sprintf(`{"metaData":{"id":"11111111-2222-3333-4444-555555555555","format":{"provider":"parquet","options":{}},"schemaString":%q,"partitionColumns":[],"configuration":{},"createdTime":1700000000000}}`, schema),
		fmt.Sprintf(`{"add":{"path":"part-0000.parquet","partitionValues":{},"size":%d,"modificationTime":1700000000000,"dataChange":true}}`, fi.Size()),
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "_delta_log", "00000000000000000000.json"), []byte(log), 0o644))
}

// The agent may call delta_scan / iceberg_* (Zac, 2026-11-07), under exactly the
// rules read_parquet has: a table in data/ reads; a path outside data/, a
// non-storage URL, a URL outside every storage scope, a computed path and the
// options that redirect metadata lookup are refused by the policy before
// anything is fetched. A table's own metadata can still name files beyond the
// vetted path: that is the documented, warned-about risk, so it is not asserted
// away here (see docs/reference/agent-policy.md).
func TestDeltaAndIcebergAreAgentReadersUnderTheReadRules(t *testing.T) {
	ctx := context.Background()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "sales_delta", Type: "delta", Path: "data/sales"}))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "sales_ice", Type: "iceberg", Path: "data/ice"}))
	sess, issues, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.Empty(t, issues)
	defer sess.Close()
	for _, ext := range []string{"delta", "avro", "iceberg"} {
		require.NoError(t, sess.InstallExtension(ctx, ext))
	}
	sess.Preflight(ctx, "sales_delta")
	sess.Preflight(ctx, "sales_ice")
	assert.Equal(t, session.StatusOK, statusOf(sess, "sales_delta"))
	assert.Equal(t, session.StatusOK, statusOf(sess, "sales_ice"))
	for _, ci := range sess.ConnectionInfos() {
		assert.Empty(t, ci.Scope, "%s: a table connection exports no scope", ci.Name)
	}

	data := filepath.Join(ws.Root(), "data")
	require.NoError(t, os.MkdirAll(data, 0o755))
	writeDeltaTable(t, sess, filepath.Join(data, "sales"))
	outside := t.TempDir()
	writeDeltaTable(t, sess, filepath.Join(outside, "secret"))

	// The person's own SQL reads their table.
	rs, err := engine.NewManager(sess).Execute(ctx, fmt.Sprintf("SELECT name FROM delta_scan('%s') ORDER BY id", filepath.Join(data, "sales")), nil)
	require.NoError(t, err)
	require.Len(t, rs.Rows, 2)
	assert.Equal(t, "ada", rs.Rows[0][0])

	for _, mode := range []agents.SandboxMode{agents.ModeReadOnly, agents.ModeReadWrite, agents.ModeFull} {
		h := agentOn(t, ws, sess, mode, true)
		// A table in data/ reads, through the agent.
		resp := sqlCall(h, fmt.Sprintf("SELECT name FROM delta_scan('%s') ORDER BY id", filepath.Join(data, "sales")))
		require.Nil(t, resp.Error, "%s: %v", mode, resp.Error)
		assert.Contains(t, fmt.Sprint(resp.Result), "ada", mode)
		resp = sqlCall(h, fmt.Sprintf("SELECT count(*) FROM delta_scan(['%s'])", filepath.Join(data, "sales")))
		require.Nil(t, resp.Error, "%s: %v", mode, resp.Error)

		var qs []string
		for _, fn := range []string{"delta_scan", "iceberg_scan", "iceberg_metadata", "iceberg_snapshots"} {
			for _, p := range []string{
				filepath.Join(outside, "secret"), "relative/sales", filepath.Join(data, "..", "x"), srv.URL + "/x", "file:///etc/hostname", "s3://bucket/t",
			} {
				qs = append(qs, fmt.Sprintf("SELECT * FROM %s('%s')", fn, p))
			}
		}
		qs = append(qs,
			fmt.Sprintf("SELECT * FROM iceberg_snapshots('%s', version='7', version_name_format='../../../../../../..%s/metadata/v%%s%%s.metadata.json')", filepath.Join(data, "ice"), outside),
			fmt.Sprintf("SELECT * FROM iceberg_snapshots('%s', version='1', version_name_format='%s/abs.json')", filepath.Join(data, "nonexistent"), srv.URL),
			fmt.Sprintf("SELECT * FROM iceberg_scan('%s', allow_moved_paths=true)", filepath.Join(data, "ice")),
			fmt.Sprintf("SELECT * FROM iceberg_scan('%s', allow_moved_paths := true)", filepath.Join(data, "ice")),
			"SELECT * FROM delta_scan(getvariable('p'))",
		)
		for _, q := range qs {
			resp := sqlCall(h, q)
			require.NotNil(t, resp.Error, q)
			assert.Equal(t, agents.ErrPermission, resp.Error.Code, q+": "+resp.Error.Message)
			assert.NotContains(t, resp.Error.Message, "TOP-SECRET")
		}
	}
	assert.EqualValues(t, 0, atomic.LoadInt32(&hits), "nothing was fetched while the policy decided")

	// COPY still refuses the file names of a table's own metadata, so the agent
	// does not write the metadata of a table it then reads.
	full := agentOn(t, ws, sess, agents.ModeFull, false)
	for _, p := range []string{
		filepath.Join(data, "evil2", "_delta_log", "00000000000000000000.json"), filepath.Join(data, "evil2", "metadata", "v1.metadata.json"),
		filepath.Join(data, "evil2", "metadata", "v1.METADATA.JSON"), filepath.Join(data, "evil2", "_DELTA_LOG", "0.json"), filepath.Join(data, "evil2", "m.avro"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		resp := sqlCall(full, fmt.Sprintf("COPY (SELECT 1) TO '%s'", p))
		require.NotNil(t, resp.Error, p)
	}
	assert.EqualValues(t, 0, atomic.LoadInt32(&hits))
}

func statusOf(s session.Session, name string) session.StatusKind {
	for _, st := range s.Connections() {
		if st.Name == name {
			return st.Kind
		}
	}
	return ""
}

func TestDuckLakeThroughTheAgent(t *testing.T) {
	ctx := context.Background()
	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/lake.ducklake", ReadOnly: ptr(false),
		Options: map[string]string{"data_path": "data/lake_files"}}))
	sess, issues, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.Empty(t, issues)
	defer sess.Close()
	require.NoError(t, sess.InstallExtension(ctx, "ducklake"))
	sess.Preflight(ctx, "lake")
	require.NoError(t, sess.Connect(ctx, "lake"))
	// Make a lake the way the person would.
	_, err = sess.DB().Exec("CREATE TABLE lake.people (id INTEGER, name VARCHAR); INSERT INTO lake.people VALUES (1,'ada'),(2,'bob')")
	require.NoError(t, err)

	h := agentOn(t, ws, sess, agents.ModeFull, false)
	t.Run("reads, joins and the read-only metadata functions are allowed", func(t *testing.T) {
		for _, q := range []string{
			"SELECT * FROM lake.main.people ORDER BY id",
			"SELECT count(*) FROM lake.main.people WHERE id = 2",
			"SELECT p.name, l.k FROM lake.main.people p JOIN (SELECT 1 AS id, 'x' AS k) l USING (id)",
			"DESCRIBE lake.main.people",
			"EXPLAIN SELECT * FROM lake.main.people",
			"SELECT count(*) FROM ducklake_snapshots('lake')",
			"SELECT * FROM ducklake_current_snapshot('lake')",
			"SELECT * FROM ducklake_last_committed_snapshot('lake')",
			"SELECT * FROM ducklake_table_info('lake')",
		} {
			resp := sqlCall(h, q)
			require.Nil(t, resp.Error, q+": "+errText(resp))
		}
	})

	t.Run("the metadata catalog never shows in what the agent reads", func(t *testing.T) {
		for _, q := range []string{
			"SELECT database_name, table_name FROM duckdb_tables()", "SELECT * FROM duckdb_tables()", "SELECT * FROM duckdb_columns()",
			"SELECT * FROM duckdb_schemas()", "SHOW ALL TABLES", "SELECT * FROM duckdb_views()", "SELECT * FROM duckdb_constraints()",
		} {
			resp := sqlCall(h, q)
			require.Nil(t, resp.Error, q+": "+errText(resp))
			for _, r := range rowsOf(t, resp) {
				for _, c := range r {
					if s, ok := c.(string); ok {
						assert.NotContains(t, strings.ToLower(s), "__ducklake_metadata_", q)
					}
				}
			}
		}
		resp := sqlCall(h, "SELECT database_name FROM duckdb_tables() WHERE database_name = 'lake'")
		require.Nil(t, resp.Error)
		assert.NotEmpty(t, rowsOf(t, resp), "the lake's own tables still show")
	})

	t.Run("render-time reads (preview_query, A2UI) hide it too", func(t *testing.T) {
		rs, _, err := h.RunReadOnlySQLIn(ctx, "", "SELECT database_name, table_name FROM duckdb_tables()", nil, 1000)
		require.NoError(t, err)
		require.NotEmpty(t, rs.Rows)
		for _, r := range rs.Rows {
			assert.NotContains(t, strings.ToLower(fmt.Sprint(r...)), "__ducklake_metadata_")
		}
		rs, _, err = h.RunReadOnlySQLIn(ctx, "", "SELECT database_name FROM duckdb_tables()", nil, 0)
		require.NoError(t, err)
		for _, r := range rs.Rows {
			assert.NotContains(t, strings.ToLower(fmt.Sprint(r...)), "__ducklake_metadata_")
		}
	})

	t.Run("COPY never truncates the lake's catalog or writes into its data folder", func(t *testing.T) {
		for _, f := range []string{"lake.ducklake", "lake_files/main/people/evil.parquet", "x.ducklake"} {
			resp := sqlCall(h, fmt.Sprintf("COPY (SELECT 1) TO '%s'", filepath.Join(ws.Root(), "data", f)))
			require.NotNil(t, resp.Error, f)
		}
		resp := sqlCall(h, "SELECT count(*) FROM lake.main.people")
		require.Nil(t, resp.Error, errText(resp))
	})

	t.Run("every other ducklake function and the internal catalog are refused", func(t *testing.T) {
		rows, err := sess.DB().Query(`SELECT DISTINCT lower(function_name), function_type FROM duckdb_functions() WHERE function_name ILIKE 'ducklake%' OR function_name ILIKE 'iceberg_to_ducklake'`)
		require.NoError(t, err)
		allowed := map[string]bool{}
		for _, n := range agents.PolicyTableFunctions() {
			allowed[n] = true
		}
		var checked int
		type fn struct{ name, typ string }
		var fns []fn
		for rows.Next() {
			var f fn
			require.NoError(t, rows.Scan(&f.name, &f.typ))
			fns = append(fns, f)
		}
		require.NoError(t, rows.Close())
		require.NotEmpty(t, fns)
		for _, f := range fns {
			if allowed[f.name] {
				continue
			}
			checked++
			for _, q := range []string{"SELECT * FROM " + f.name + "('lake')", "CALL " + f.name + "('lake')", "SELECT * FROM " + f.name + "('lake', 'main', 'people')"} {
				resp := sqlCall(h, q)
				require.NotNil(t, resp.Error, q)
				assert.Equal(t, agents.ErrPermission, resp.Error.Code, q+": "+resp.Error.Message)
			}
		}
		assert.Greater(t, checked, 8, "the extension's maintenance and file functions were all exercised")
		for _, q := range []string{
			"SELECT * FROM __ducklake_metadata_lake.main.ducklake_table",
			"SELECT * FROM __ducklake_metadata_lake.ducklake_data_file",
			"DELETE FROM __ducklake_metadata_lake.main.ducklake_snapshot",
			"DROP TABLE __ducklake_metadata_lake.main.ducklake_table",
			"SELECT * FROM lake.snapshots()",
			"CALL lake.set_option('x', 'y')",
			"CALL lake.merge_adjacent_files()",
		} {
			resp := sqlCall(h, q)
			require.NotNil(t, resp.Error, q)
		}
		// Nothing changed: the lake still has its data, and its metadata is intact.
		resp := sqlCall(h, "SELECT count(*) FROM lake.main.people")
		require.Nil(t, resp.Error, errText(resp))
		var n int
		require.NoError(t, sess.DB().QueryRow("SELECT count(*) FROM __ducklake_metadata_lake.main.ducklake_snapshot").Scan(&n))
		assert.Greater(t, n, 0)
	})

	t.Run("a write needs the three gates: approval is forced on", func(t *testing.T) {
		resp := sqlCall(h, "INSERT INTO lake.main.people VALUES (3, 'cy')")
		require.NotNil(t, resp.Error)
		assert.Equal(t, agents.ErrApprovalNeeded, resp.Error.Code, "no approver over MCP: "+resp.Error.Message)
		var n int
		require.NoError(t, sess.DB().QueryRow("SELECT count(*) FROM lake.main.people").Scan(&n))
		assert.Equal(t, 2, n)

		ap := &stubApprover{decision: agents.ErrApprovalDeclined}
		h.SetApprover(ap.fn)
		resp = sqlCall(h, "INSERT INTO lake.main.people VALUES (3, 'cy')")
		require.NotNil(t, resp.Error)
		require.Equal(t, 1, ap.count())
		assert.Equal(t, []string{"lake"}, ap.asked[0].Connections)
		ap.decision = nil
		for _, q := range []string{
			"INSERT INTO lake.main.people VALUES (3, 'cy')",
			"UPDATE lake.main.people SET name = 'cyrus' WHERE id = 3",
			"DELETE FROM lake.main.people WHERE id = 3",
			"CREATE TABLE lake.main.made AS SELECT 1 AS a",
			"DROP TABLE lake.main.made",
		} {
			resp = sqlCall(h, q)
			require.Nil(t, resp.Error, q+": "+errText(resp))
		}
		require.NoError(t, sess.DB().QueryRow("SELECT count(*) FROM lake.main.people").Scan(&n))
		assert.Equal(t, 2, n)
		h.SetApprover(nil)
	})
}

// A read-only lake refuses the agent before anything is asked.
func TestDuckLakeReadOnlyRefusesAgentWrites(t *testing.T) {
	ctx := context.Background()
	ws := memWS(t)
	w := workspace.ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/lake.ducklake", ReadOnly: ptr(false), Options: map[string]string{"data_path": "data/lake_files"}}
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, w))
	sess, _, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.NoError(t, sess.InstallExtension(ctx, "ducklake"))
	sess.Preflight(ctx, "lake")
	require.NoError(t, sess.Connect(ctx, "lake"))
	_, err = sess.DB().Exec("CREATE TABLE lake.people (id INTEGER); INSERT INTO lake.people VALUES (1)")
	require.NoError(t, err)
	_ = sess.Close()

	w.ReadOnly = ptr(true)
	require.NoError(t, ws.UpdateConnection(workspace.ScopeLocal, w))
	sess, issues, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.Empty(t, issues)
	defer sess.Close()
	require.NoError(t, sess.InstallExtension(ctx, "ducklake"))
	sess.Preflight(ctx, "lake")
	ap := &stubApprover{}
	h := agentOn(t, ws, sess, agents.ModeFull, false)
	h.SetApprover(ap.fn)
	resp := sqlCall(h, "SELECT count(*) FROM lake.main.people")
	require.Nil(t, resp.Error, errText(resp))
	resp = sqlCall(h, "INSERT INTO lake.main.people VALUES (2)")
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, "read-only")
	assert.Equal(t, 0, ap.count())
	_ = secrets.Credential{}
}

// The review's DuckLake probe: a catalog whose data-file entry was rewritten to
// a file outside the workspace. The lake is refused when it attaches, so the
// agent reads nothing from it (and the file's contents never appear).
func TestDuckLakeCraftedCatalogReadsNothingForTheAgent(t *testing.T) {
	ctx := context.Background()
	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/lake.ducklake", ReadOnly: ptr(false),
		Options: map[string]string{"data_path": "data/lake_files"}}))
	sess, _, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.NoError(t, sess.InstallExtension(ctx, "ducklake"))
	sess.Preflight(ctx, "lake")
	require.NoError(t, sess.Connect(ctx, "lake"))
	_, err = sess.DB().Exec("CREATE TABLE lake.people (id INTEGER, name VARCHAR); INSERT INTO lake.people VALUES (1,'ada'),(2,'bob'); CALL ducklake_flush_inlined_data('lake')")
	require.NoError(t, err)
	outside := t.TempDir()
	secretPq := filepath.Join(outside, "secret.parquet")
	_, err = sess.DB().Exec(fmt.Sprintf(`COPY (SELECT 42 AS id, 'TOP-SECRET-OUTSIDE' AS name) TO '%s' (FORMAT PARQUET, FIELD_IDS {id: 1, name: 2})`, secretPq))
	require.NoError(t, err)
	fi, _ := os.Stat(secretPq)
	_, err = sess.DB().Exec(fmt.Sprintf("UPDATE __ducklake_metadata_lake.main.ducklake_data_file SET path='%s', path_is_relative=false, file_size_bytes=%d, record_count=1, footer_size=NULL", secretPq, fi.Size()))
	require.NoError(t, err)
	require.NoError(t, sess.Close())

	sess2, _, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	defer sess2.Close()
	sess2.Preflight(ctx, "lake")
	require.Error(t, sess2.Connect(ctx, "lake"))
	for _, mode := range []agents.SandboxMode{agents.ModeReadOnly, agents.ModeFull} {
		h := agentOn(t, ws, sess2, mode, true)
		for _, q := range []string{
			"SELECT * FROM lake.main.people", "SELECT * FROM lake.people", "SELECT * FROM ducklake_table_info('lake')",
			"SELECT * FROM __ducklake_metadata_lake.main.ducklake_data_file", `SELECT * FROM "__DUCKLAKE_METADATA_LAKE".main.ducklake_data_file`,
			"SELECT * FROM duckdb_databases()", "USE __ducklake_metadata_lake",
		} {
			resp := sqlCall(h, q)
			if resp.Error == nil {
				assert.NotContains(t, fmt.Sprint(resp.Result), "TOP-SECRET", q)
			}
			require.NotNil(t, resp.Error, q)
			assert.NotContains(t, resp.Error.Message, "TOP-SECRET")
		}
	}
}

// A lake with a SQLite catalog is hidden and protected like one with a DuckDB
// catalog: the internal metadata catalog is off limits, sqlite_scan stays off
// the allow-list, and COPY TO cannot touch the local catalog file (#118).
func TestDuckLakeSQLiteCatalogThroughTheAgent(t *testing.T) {
	ctx := context.Background()
	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/lake.sqlite", ReadOnly: ptr(false),
		Options: map[string]string{"catalog": "sqlite", "data_path": "data/lake_files"}}))
	sess, issues, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.Empty(t, issues)
	defer sess.Close()
	for _, e := range []string{"ducklake", "sqlite"} {
		require.NoError(t, sess.InstallExtension(ctx, e))
	}
	sess.Preflight(ctx, "lake")
	require.NoError(t, sess.Connect(ctx, "lake"))
	_, err = sess.DB().Exec("CREATE TABLE lake.people (id INTEGER, name VARCHAR); INSERT INTO lake.people VALUES (1,'ada')")
	require.NoError(t, err)

	h := agentOn(t, ws, sess, agents.ModeFull, false)
	resp := sqlCall(h, "SELECT count(*) FROM lake.main.people")
	require.Nil(t, resp.Error, errText(resp))
	for _, q := range []string{
		"SELECT * FROM __ducklake_metadata_lake.main.ducklake_table",
		"SELECT * FROM __ducklake_metadata_lake.ducklake_data_file",
		"DELETE FROM __ducklake_metadata_lake.main.ducklake_snapshot",
		fmt.Sprintf("SELECT * FROM sqlite_scan('%s', 'ducklake_table')", filepath.Join(ws.Root(), "data", "lake.sqlite")),
		fmt.Sprintf("CALL sqlite_attach('%s')", filepath.Join(ws.Root(), "data", "lake.sqlite")),
		fmt.Sprintf("COPY (SELECT 1) TO '%s'", filepath.Join(ws.Root(), "data", "lake.sqlite")),
	} {
		resp := sqlCall(h, q)
		require.NotNil(t, resp.Error, q)
	}
	resp = sqlCall(h, "SELECT count(*) FROM lake.main.people")
	require.Nil(t, resp.Error, errText(resp))
}
