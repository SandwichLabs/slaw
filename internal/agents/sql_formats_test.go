package agents

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

// #119: Iceberg and Delta readers, DuckLake's functions and catalogs, and the
// MySQL passthroughs. The extensions are not loaded in these tests, so a
// statement that passes the syntactic layer ends at DuckDB's "function does not
// exist" (a denial of its own); what is checked here is that the policy's own
// decision (path, scope, allow-list) is the one that fires, in the order the
// policy decides it.

// iceberg_scan, iceberg_metadata, iceberg_snapshots and delta_scan are agent
// readers (Zac, 2026-11-07: run what DuckDB runs, warn where needed) and take
// exactly the rules read_parquet takes: the data/ folder, storage scopes, plain
// URLs, one string literal, the safe-option list and the reader count. In these
// tests the extensions are not loaded, so a statement the policy lets through
// ends at DuckDB's own "not in the catalog" (a "could not validate" reason);
// the policy's own refusals carry their own words.
func TestPolicy_TableFormatReadersFollowTheReaderRules(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		fx := newPolicyFixture(t, mode, withExtraConns(
			session.ConnectionInfo{Name: "lake", Type: "s3", Kind: session.StatusOK, Remote: true, Catalogless: true, Scope: "s3://lake/raw/"},
			session.ConnectionInfo{Name: "tbl", Type: "delta", Kind: session.StatusOK, Remote: true, Catalogless: true},
		))
		for _, fn := range []string{"delta_scan", "iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "ICEBERG_SCAN", `"delta_scan"`} {
			// Allowed by the policy: in data/, or inside a ready storage scope.
			for _, arg := range []string{"'%DATA%/tbl'", "'%DATA%/sub/tbl'", "'s3://lake/raw/tbl'", "['%DATA%/a', 's3://lake/raw/b']"} {
				q := fx.expand("SELECT * FROM " + fn + "(" + arg + ")")
				v := fx.classify(q)
				if v.Class != sqlClassRead {
					assert.Contains(t, v.Reason, "could not validate query", "%s %s: only DuckDB's own missing-extension error may stop it (%s)", mode, q, v.Reason)
				}
			}
			// Refused by the path rules, like read_parquet.
			for arg, why := range map[string]string{
				"'%OUT%/tbl'":             "outside the workspace's data/",
				"'/etc/hostname'":         "outside the workspace's data/",
				"'relative/tbl'":          "relative path",
				"'%DATA%/../x'":           "..",
				"'s3://other/tbl'":        "outside every storage connection's scope",
				"'s3://lake/raw/../x'":    "not a plain storage url",
				"'s3://lake/raw/t?x=1'":   "not a plain storage url",
				"'" + srv.URL + "/x'":     "not a plain storage url",
				"'file:///etc/hostname'":  "not a plain storage url",
				"getvariable('p')":        "plain string literal",
				"'%DATA%/a' || '/b'":      "plain string literal",
				"['%DATA%/a', '%OUT%/b']": "outside the workspace's data/",
			} {
				v := fx.classify(fx.expand("SELECT * FROM " + fn + "(" + arg + ")"))
				assert.Equal(t, sqlClassOther, v.Class, "%s %s %s", mode, fn, arg)
				assert.Contains(t, strings.ToLower(v.Reason), strings.ToLower(why), "%s %s(%s)", mode, fn, arg)
			}
		}
		runPolicyCases(t, fx, []policyCase{
			{"named version_name_format", "SELECT * FROM iceberg_snapshots('%DATA%/tbl', version='1', version_name_format='../../../../etc/hostname')", sqlClassOther, "safe reader options"},
			{"named allow_moved_paths", "SELECT * FROM iceberg_scan('%DATA%/tbl', allow_moved_paths = true)", sqlClassOther, "safe reader options"},
			{"walrus allow_moved_paths", "SELECT * FROM iceberg_scan('%DATA%/tbl', allow_moved_paths := true)", sqlClassOther, "safe reader options"},
			{"CALL form", "CALL delta_scan('%DATA%/tbl')", sqlClassOther, "call"},
			{"as a scalar", "SELECT iceberg_scan('%DATA%/tbl')", sqlClassOther, "can only be used as a table"},
			{"in a view body, refused path", "CREATE VIEW dv AS SELECT * FROM delta_scan('%OUT%/tbl')", sqlClassOther, ""},
			{"replacement scan of a table folder", "SELECT * FROM '%DATA%/tbl.delta'", sqlClassOther, ""},
		})
	}
	assert.EqualValues(t, 0, atomic.LoadInt32(&hits), "the policy refused them before anything was fetched")
}

// The rule: the agent never reads table metadata it could have written. The
// lake readers refuse a local path to a file (DuckDB reads any .json as Iceberg
// metadata and follows the URLs in it while binding); only a table folder or a
// storage URL is accepted.
func TestPolicy_LakeReadersRefuseMetadataFiles(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		fx := newPolicyFixture(t, mode, withExtraConns(
			session.ConnectionInfo{Name: "lake", Type: "s3", Kind: session.StatusOK, Remote: true, Catalogless: true, Scope: "s3://lake/raw/"},
		))
		for _, fn := range []string{"iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "delta_scan"} {
			for _, f := range []string{"e.json", "e.JSON", "v1.metadata.json", "e.avro", "x.json.gz", "metadata", "version-hint.text", "sub/e.json", "e.json.zst"} {
				v := fx.classify(fx.expand("SELECT * FROM " + fn + "('%DATA%/" + f + "')"))
				assert.Equal(t, sqlClassOther, v.Class, "%s %s %s", mode, fn, f)
				assert.Contains(t, v.Reason, "table", "%s %s %s: %s", mode, fn, f, v.Reason)
			}
			// A storage URL is not a local file the agent could have written.
			v := fx.classify(fx.expand("SELECT * FROM " + fn + "('s3://lake/raw/t/metadata/v1.metadata.json')"))
			if v.Class != sqlClassRead {
				assert.Contains(t, v.Reason, "could not validate query", "%s %s: %s", mode, fn, v.Reason)
			}
		}
	}
}

// The exact attack: write a JSON file that names a URL, then point the reader at
// it. The write passes the COPY rules (it is not a metadata name), so the
// reader half must refuse it, before EXPLAIN binds anything.
func TestPolicy_WriteThenReadMetadataAttackIsRefused(t *testing.T) {
	fx := newPolicyFixture(t, ModeFull)
	attack := "COPY (SELECT 'http:' || chr(47) || chr(47) || '127.0.0.1:1' || chr(47) || 'leak' AS \"manifest-list\") TO '%DATA%/e.json' (FORMAT json)"
	_ = fx.classify(fx.expand(attack)) // may pass or not; the read below must not
	for _, fn := range []string{"iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "delta_scan"} {
		v := fx.classify(fx.expand("SELECT * FROM " + fn + "('%DATA%/e.json')"))
		assert.Equal(t, sqlClassOther, v.Class, fn)
		assert.Contains(t, v.Reason, "never from a metadata file", fn)
	}
	// And the half that writes: no folder or name that reads as table metadata.
	for _, q := range []string{
		"COPY (SELECT 1) TO '%DATA%/e.metadata.json' (FORMAT json)",
		"COPY (SELECT 1) TO '%DATA%/t/metadata/e.json' (FORMAT json)",
		"COPY (SELECT 1) TO '%DATA%/t/_delta_log/0.json' (FORMAT json)",
		"EXPORT DATABASE '%DATA%/exp'",
	} {
		v := fx.classify(fx.expand(q))
		assert.Equal(t, sqlClassOther, v.Class, q)
	}
}

// The reader-count rule of the plan check counts them like read_parquet: a plan
// may hold no more readers than the statement wrote out and vetted.
func TestPolicy_TableFormatReadersCountAsReaders(t *testing.T) {
	for _, n := range []string{"ICEBERG_SCAN", "ICEBERG_METADATA", "ICEBERG_SNAPSHOTS", "DELTA_SCAN"} {
		assert.True(t, readerPlanOps[n], n)
		assert.True(t, readerFuncs[strings.ToLower(n)], n)
		var readers int
		r := planNode{Name: n + " ", ExtraInfo: map[string]any{"Function": n}}
		assert.Empty(t, checkPlanNode(&r, &readers), n)
		assert.Equal(t, 1, readers, n)
	}
}

// Named options on a reader could name another file or change how files are
// found; only an explicit list of parsing options is accepted.
func TestPolicy_ReaderNamedOptionsAreAnAllowList(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	runPolicyCases(t, fx, []policyCase{
		{"header ok", "SELECT * FROM read_csv('%DATA%/x.csv', header = true)", sqlClassRead, ""},
		{"several ok", "SELECT * FROM read_csv('%DATA%/x.csv', header = true, delim = ',', union_by_name = true)", sqlClassRead, ""},
		{"columns ok", "SELECT * FROM read_csv('%DATA%/x.csv', columns = {'a': 'INT', 'b': 'INT'}, header = true)", sqlClassRead, ""},
		{"rejects_table writes a table", "SELECT * FROM read_csv('%DATA%/x.csv', store_rejects = true, rejects_table = 'r')", sqlClassOther, "safe reader options"},
		{"unknown option", "SELECT * FROM read_csv('%DATA%/x.csv', nope = 1)", sqlClassOther, "safe reader options"},
		{"version_name_format", "SELECT * FROM read_parquet('%DATA%/x.csv', version_name_format = 'a')", sqlClassOther, "safe reader options"},
		{"upper case name", "SELECT * FROM read_csv('%DATA%/x.csv', HEADER = true, Rejects_Table = 'r')", sqlClassOther, "safe reader options"},
		{"walrus form in a statement checked on tokens", "CREATE VIEW vv AS SELECT * FROM read_csv('%DATA%/x.csv', rejects_table := 'r')", sqlClassOther, "safe reader options"},
		{"walrus form ok", "CREATE VIEW vv2 AS SELECT * FROM read_csv('%DATA%/x.csv', header := true)", sqlClassDDL, ""},
	})
}

func TestPolicy_TableFormatPathPolicy(t *testing.T) {
	// The decision itself, with the extensions out of the picture.
	p := &pathPolicy{dataDir: "/ws/data", scopes: []string{"az://events/2024/", "abfss://c@acct.dfs.core.windows.net/", "s3://lake/raw/"}}
	for _, u := range []string{
		"az://events/2024/t", "az://events/2024/t/_delta_log/0.json", "abfss://c@acct.dfs.core.windows.net/a/b", "s3://lake/raw/t/metadata/v1.metadata.json",
	} {
		assert.Empty(t, p.vetRead(u), u)
	}
	for _, u := range []string{
		"az://events/2024", "az://events/2025/t", "azure://events/2024/t", "abfss://c@acct.dfs.core.windows.net/../x",
		"abfss://c@acct.dfs.core.windows.net.evil.example/x", "az://events/2024/t?sas=1", "AZ://events/2024/t",
	} {
		assert.NotEmpty(t, p.vetRead(u), u)
	}
	assert.Equal(t, "az://events/", normalizeScope("az://events"))
	assert.Equal(t, "abfss://c@acct.dfs.core.windows.net/", normalizeScope("abfss://c@acct.dfs.core.windows.net"))
	assert.Equal(t, "azure://c/p/", normalizeScope("azure://c/p/"))
}

func TestPolicy_PathPolicyScopesIncludeAzure(t *testing.T) {
	env := newSQLEnv("", []session.ConnectionInfo{
		{Name: "blob", Type: "azure", Kind: session.StatusOK, Catalogless: true, Scope: "az://events/2024"},
		{Name: "tbl", Type: "delta", Kind: session.StatusOK, Catalogless: true}, // a table source: no scope, widens nothing
		{Name: "bad", Type: "azure", Kind: session.StatusNeedsCredentials, Catalogless: true, Scope: "az://bad/"},
	})
	assert.Equal(t, []string{"az://events/2024/"}, env.pathPolicy().scopes)
}

func TestPolicy_MySQLPassthroughsDeniedInEveryTier(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		fx := newPolicyFixture(t, mode)
		runPolicyCases(t, fx, []policyCase{
			{"mysql_query", "SELECT * FROM mysql_query('my', 'SELECT 1')", sqlClassOther, "mysql_query"},
			{"mysql_query upper", "SELECT * FROM MYSQL_QUERY('my', 'SELECT 1')", sqlClassOther, "mysql_query"},
			{"mysql_query quoted", `SELECT * FROM "mysql_query"('my', 'SELECT 1')`, sqlClassOther, "mysql_query"},
			{"mysql_execute table fn", "SELECT * FROM mysql_execute('my', 'DROP TABLE x')", sqlClassOther, "mysql_execute"},
			{"mysql_execute CALL", "CALL mysql_execute('my', 'DROP TABLE x')", sqlClassOther, "mysql_execute"},
			{"mysql_clear_cache", "CALL mysql_clear_cache()", sqlClassOther, "mysql_clear_cache"},
			{"mysql_clear_cache table fn", "SELECT * FROM mysql_clear_cache()", sqlClassOther, "mysql_clear_cache"},
			{"mysql_scan", "SELECT * FROM mysql_scan('host=h', 'db', 't')", sqlClassOther, "mysql_scan"},
			{"mysql_attach", "CALL mysql_attach('host=h')", sqlClassOther, "mysql_attach"},
			{"inside a CTE", "WITH a AS (SELECT * FROM mysql_query('my', 'SELECT 1')) SELECT * FROM a", sqlClassOther, "mysql_query"},
			{"inside a scalar subquery", "SELECT (SELECT count(*) FROM mysql_query('my', 'SELECT 1'))", sqlClassOther, "mysql_query"},
			{"as the source of an INSERT", "INSERT INTO t SELECT * FROM mysql_query('my', 'SELECT 1, 2')", sqlClassOther, "mysql_query"},
			{"ATTACH mysql", "ATTACH '' AS m (TYPE mysql, SECRET s)", sqlClassOther, ""},
			{"ATTACH mysql dsn", "ATTACH 'host=h user=u password=p' AS m (TYPE mysql)", sqlClassOther, ""},
		})
	}
}

func TestPolicy_PlanRegexRefusesMySQLPassthroughs(t *testing.T) {
	for _, fn := range []string{"mysql_query", "mysql_execute", "mysql_clear_cache"} {
		assert.True(t, planDeniedFuncRe.MatchString(`{"Function": "`+fn+`( 'x' )"}`), fn)
	}
}

func TestPolicy_MySQLPlanOperators(t *testing.T) {
	// The operator names a MySQL catalog's plan carries (from DuckDB 1.4.1's
	// mysql extension, checked against a real server in the integration test).
	var readers int
	scan := planNode{Name: "MYSQL_SCAN ", ExtraInfo: map[string]any{"Table": "people"}}
	assert.Empty(t, checkPlanNode(&scan, &readers), "a scan of a MySQL table is a catalog scan")
	assert.Equal(t, 0, readers)
	for _, n := range []string{"MYSQL_INSERT", "MYSQL_CREATE_TABLE_AS", "MYSQL_UPDATE", "MYSQL_DELETE"} {
		assert.True(t, isRemoteWriteOp(n) || allowedPlanOperators[n], n)
	}
	for _, n := range []string{"DUCKLAKE_INSERT", "DUCKLAKE_UPDATE", "DUCKLAKE_DELETE", "DUCKLAKE_CREATE_TABLE_AS"} {
		assert.True(t, isRemoteWriteOp(n), n)
	}
	// A table function that is not on the list is still refused by the plan.
	fn := planNode{Name: "MYSQL_QUERY", ExtraInfo: map[string]any{"Function": "MYSQL_QUERY"}}
	assert.Contains(t, checkPlanNode(&fn, &readers), "not on the allow-list")
	fn = planNode{Name: "DUCKLAKE_EXPIRE_SNAPSHOTS", ExtraInfo: map[string]any{"Function": "DUCKLAKE_EXPIRE_SNAPSHOTS"}}
	assert.Contains(t, checkPlanNode(&fn, &readers), "not on the allow-list")
}

func TestPolicy_DuckLakeFunctions(t *testing.T) {
	fx := newPolicyFixture(t, ModeFull)
	// Read-only metadata functions pass the allow-list (the extension is not
	// loaded here, so DuckDB's own binder is what is left to object).
	for _, q := range []string{
		"SELECT * FROM ducklake_snapshots('lk')",
		"SELECT * FROM ducklake_current_snapshot('lk')",
		"SELECT * FROM ducklake_last_committed_snapshot('lk')",
		"SELECT * FROM ducklake_table_info('lk')",
	} {
		v := fx.classify(q)
		assert.NotContains(t, v.Reason, "allow-list", q)
	}
	// Everything else is refused by default: maintenance changes the lake,
	// listing files reveals where they live.
	runPolicyCases(t, fx, []policyCase{
		{"set_option", "SELECT * FROM ducklake_set_option('lk', 'x', 'y')", sqlClassOther, "allow-list"},
		{"expire_snapshots", "SELECT * FROM ducklake_expire_snapshots('lk', older_than => now())", sqlClassOther, "allow-list"},
		{"cleanup_old_files", "SELECT * FROM ducklake_cleanup_old_files('lk', cleanup_all => true)", sqlClassOther, "allow-list"},
		{"delete_orphaned_files", "SELECT * FROM ducklake_delete_orphaned_files('lk', cleanup_all => true)", sqlClassOther, "allow-list"},
		{"merge_adjacent_files", "SELECT * FROM ducklake_merge_adjacent_files('lk')", sqlClassOther, "allow-list"},
		{"rewrite_data_files", "SELECT * FROM ducklake_rewrite_data_files('lk', 't')", sqlClassOther, "allow-list"},
		{"add_data_files reads a file", "SELECT * FROM ducklake_add_data_files('lk', 't', '/etc/x.parquet')", sqlClassOther, "allow-list"},
		{"flush_inlined_data", "SELECT * FROM ducklake_flush_inlined_data('lk')", sqlClassOther, "allow-list"},
		{"set_commit_message", "SELECT * FROM ducklake_set_commit_message('lk', 'a', 'b')", sqlClassOther, "allow-list"},
		{"list_files reveals paths", "SELECT * FROM ducklake_list_files('lk', 't')", sqlClassOther, "allow-list"},
		{"options", "SELECT * FROM ducklake_options('lk')", sqlClassOther, "allow-list"},
		{"table_insertions", "SELECT * FROM ducklake_table_insertions('lk', 'main', 't', 0, 1)", sqlClassOther, "allow-list"},
		{"iceberg_to_ducklake", "SELECT * FROM iceberg_to_ducklake('x', 'lk')", sqlClassOther, "allow-list"},
		{"CALL set_option", "CALL lk.set_option('x', 'y')", sqlClassOther, "call"},
		{"CALL ducklake_set_option", "CALL ducklake_set_option('lk', 'x', 'y')", sqlClassOther, "call"},
		{"catalog-qualified maintenance", "CALL lk.merge_adjacent_files()", sqlClassOther, "call"},
		{"catalog-qualified snapshots()", "SELECT * FROM lk.snapshots()", sqlClassOther, ""},
		{"ATTACH ducklake", "ATTACH 'ducklake:/tmp/x.ducklake' AS d (DATA_PATH '/tmp/x/')", sqlClassOther, "attach"},
	})
}

func TestPolicy_DuckLakeInternalCatalogIsOffLimits(t *testing.T) {
	fx := newPolicyFixture(t, ModeFull)
	meta := filepath.Join(fx.dir, "meta.duckdb")
	fx.exec(`ATTACH '` + meta + `' AS __ducklake_metadata_lk`)
	fx.exec(`CREATE TABLE __ducklake_metadata_lk.main.ducklake_table (id INTEGER)`)
	runPolicyCases(t, fx, []policyCase{
		{"read the metadata", "SELECT * FROM __ducklake_metadata_lk.main.ducklake_table", sqlClassOther, "internal metadata"},
		{"read, schema position", "SELECT * FROM __ducklake_metadata_lk.ducklake_table", sqlClassOther, "internal metadata"},
		{"read, upper case", "SELECT * FROM __DUCKLAKE_METADATA_LK.main.ducklake_table", sqlClassOther, "internal metadata"},
		{"read, quoted", `SELECT * FROM "__ducklake_metadata_lk".main.ducklake_table`, sqlClassOther, "internal metadata"},
		{"read in a join", "SELECT * FROM t JOIN __ducklake_metadata_lk.main.ducklake_table m ON m.id = t.id", sqlClassOther, "internal metadata"},
		{"write the metadata", "DELETE FROM __ducklake_metadata_lk.main.ducklake_table", sqlClassOther, "internal metadata"},
		{"insert into the metadata", "INSERT INTO __ducklake_metadata_lk.main.ducklake_table VALUES (1)", sqlClassOther, "internal metadata"},
		{"drop the metadata", "DROP TABLE __ducklake_metadata_lk.main.ducklake_table", sqlClassOther, "internal metadata"},
		{"describe the metadata", "DESCRIBE __ducklake_metadata_lk.main.ducklake_table", sqlClassOther, "internal metadata"},
		{"an ordinary catalog still works", "SELECT * FROM other.orders", sqlClassRead, ""},
	})
}

func TestPolicy_NeverWritesTableMetadata(t *testing.T) {
	fx := newPolicyFixture(t, ModeFull)
	runPolicyCases(t, fx, []policyCase{
		{"delta log commit", "COPY (SELECT 1) TO '%DATA%/tbl/_delta_log/00000000000000000000.json'", sqlClassOther, "metadata"},
		{"iceberg metadata json", "COPY (SELECT 1) TO '%DATA%/tbl/metadata/v1.metadata.json'", sqlClassOther, "metadata"},
		{"iceberg manifest", "COPY (SELECT 1) TO '%DATA%/tbl/metadata/snap-1.avro'", sqlClassOther, "metadata"},
		{"iceberg version hint", "COPY (SELECT 1) TO '%DATA%/tbl/metadata/version-hint.text'", sqlClassOther, "metadata"},
		{"delta log upper case", "COPY (SELECT 1) TO '%DATA%/tbl/_DELTA_LOG/0.json'", sqlClassOther, "metadata"},
		{"a metadata folder anywhere", "COPY (SELECT 1) TO '%DATA%/t/metadata/x.json'", sqlClassOther, "metadata"},
		{"a metadata folder in a deep path", "COPY (SELECT 1) TO '%DATA%/a/b/Metadata/c/x.json'", sqlClassOther, "metadata"},
		{"a delta log folder deep", "COPY (SELECT 1) TO '%DATA%/a/b/_delta_log/x.json'", sqlClassOther, "metadata"},
		{"any metadata.json suffix", "COPY (SELECT 1) TO '%DATA%/v2.metadata.json'", sqlClassOther, "metadata"},
		{"a file name pattern could name a manifest", "COPY (SELECT 1) TO '%DATA%/out' (FORMAT JSON, PARTITION_BY (a), FILENAME_PATTERN 'v1.metadata')", sqlClassOther, "filename_pattern"},
		{"a file extension could name a version hint", "COPY (SELECT 1 AS a) TO '%DATA%/out' (FORMAT CSV, PARTITION_BY (a), FILENAME_PATTERN 'version-hint', FILE_EXTENSION 'text')", sqlClassOther, "filename_pattern"},
		{"a file extension alone", "COPY (SELECT 1 AS a) TO '%DATA%/out2' (FORMAT CSV, FILE_EXTENSION 'json')", sqlClassOther, "file_extension"},
		{"an ordinary export still works", "COPY (SELECT 1) TO '%DATA%/out.csv'", sqlClassDDL, ""},
		{"a partitioned export still works", "COPY (SELECT 1 AS a, 2 AS b) TO '%DATA%/part' (FORMAT PARQUET, PARTITION_BY (a))", sqlClassDDL, ""},
		{"a folder called metadata still works", "COPY (SELECT 1) TO '%DATA%/sub/out.parquet' (FORMAT PARQUET)", sqlClassDDL, ""},
	})
}

// A catalog-less connection that is not a storage connection widens nothing,
// whatever scope its definition carries, and a scope with any scheme outside
// the storage schemes never becomes one.
func TestPolicy_OnlyStorageSchemesAndTypesWidenReads(t *testing.T) {
	env := newSQLEnv("", []session.ConnectionInfo{
		{Name: "a", Type: "delta", Kind: session.StatusOK, Catalogless: true, Scope: "file:///"},
		{Name: "b", Type: "iceberg", Kind: session.StatusOK, Catalogless: true, Scope: "http://"},
		{Name: "c", Type: "s3", Kind: session.StatusOK, Catalogless: true, Scope: "file:///"},
		{Name: "d", Type: "s3", Kind: session.StatusOK, Catalogless: true, Scope: "https://bucket.s3.amazonaws.com/"},
		{Name: "e", Type: "s3", Kind: session.StatusOK, Catalogless: true, Scope: "hf://datasets/x/"},
		{Name: "f", Type: "s3", Kind: session.StatusOK, Catalogless: true, Scope: "s3://ok/p"},
		{Name: "g", Type: "s3", Kind: session.StatusOK, Catalogless: true, Scope: "s3://b/%2e%2e/"},
		{Name: "h", Type: "s3", Kind: session.StatusOK, Catalogless: true, Scope: "s3://b/../c/"},
		// Defence in depth: even a well-formed storage scope on a type that is
		// not a storage connection widens nothing.
		{Name: "i", Type: "delta", Kind: session.StatusOK, Catalogless: true, Scope: "s3://widened/"},
		{Name: "j", Type: "ducklake", Kind: session.StatusOK, Catalogless: true, Scope: "az://widened/"},
	})
	assert.Equal(t, []string{"s3://ok/p/"}, env.pathPolicy().scopes)
	p := env.pathPolicy()
	for _, u := range []string{"file:///etc/hostname", "http://127.0.0.1/x", "https://bucket.s3.amazonaws.com/x", "hf://datasets/x/y"} {
		assert.NotEmpty(t, p.vetRead(u), u)
	}
	// Every spelling of "up one folder" is refused as a read inside a good scope.
	for _, u := range []string{"s3://ok/p/../q", "s3://ok/p/%2e%2e/q", "s3://ok/p/%2E%2E/q", "s3://ok/p//q", "s3://ok/p/./q", "s3://ok/p/q?x", "s3://ok/p/q#f", "S3://ok/p/q", "s3a://ok/p/q", "s3://OK/p/q"} {
		assert.NotEmpty(t, p.vetRead(u), u)
	}
	assert.NotEmpty(t, p.vetRead("s3://ok/p/q/*.parquet"), "an S3-like URL is read literally, so a wildcard is refused (#138)")
}

func TestDropDuckLakeInternalRows(t *testing.T) {
	rows := [][]any{{"memory", "t"}, {"__ducklake_metadata_lk", "ducklake_table"}, {"lk", "people"}, {1, "__DUCKLAKE_METADATA_x"}, {nil, 3}}
	got := dropDuckLakeInternalRows(rows)
	assert.Equal(t, [][]any{{"memory", "t"}, {"lk", "people"}, {nil, 3}}, got)
	same := [][]any{{"a"}, {1}}
	assert.Equal(t, same, dropDuckLakeInternalRows(same))
	assert.Nil(t, dropDuckLakeInternalRows(nil))
}

func TestPolicy_MySQLServerSchemasAreOffLimits(t *testing.T) {
	// The MySQL catalog is stood in for by a duckdb file renamed in the
	// analyzer's database list.
	a := &analyzer{dbs: map[string]dbRow{"shop": {name: "shop", typ: "mysql"}, "memory": {name: "memory", typ: "duckdb"}}}
	a.cat = &catalog{defaultCatalog: "memory"}
	for _, c := range [][2]string{{"shop", "mysql"}, {"shop", "performance_schema"}, {"shop", "SYS"}, {"shop", "information_schema"}, {"SHOP", "mysql"}} {
		assert.True(t, a.mysqlServerSchema(c[0], c[1]), "%v", c)
	}
	for _, c := range [][2]string{{"shop", "app"}, {"memory", "information_schema"}, {"memory", "mysql"}, {"", "information_schema"}, {"", "shop"}, {"shop", ""}, {"nope", "mysql"}} {
		assert.False(t, a.mysqlServerSchema(c[0], c[1]), "%v", c)
	}
}

// COPY ... TO never truncates a lake: not a .ducklake file, not the configured
// catalog file whatever it is called, not anything in a lake's data folder.
func TestPolicy_CopyNeverWritesIntoAConnectionsFiles(t *testing.T) {
	fx := newPolicyFixture(t, ModeFull)
	require.NoError(t, os.MkdirAll(fx.p("lake_files/main/t"), 0o755))
	require.NoError(t, os.WriteFile(fx.p("meta.bin"), []byte("x"), 0o644))
	env := newSQLEnv(fx.dataDir, nil, fx.p("meta.bin"), fx.p("lake_files"), fx.p("odd name.catalog"))
	p := env.pathPolicy()
	for _, f := range []string{
		"x.ducklake", "sub/x.DuckLake", "meta.bin", "lake_files/main/t/new.parquet", "lake_files/x.csv", "lake_files/main/t/ducklake-1.parquet", "odd name.catalog",
	} {
		assert.NotEmpty(t, p.vetWrite(fx.p(f)), f)
	}
	for _, f := range []string{"out.csv", "lake_files_other.csv", "sub/out.parquet"} {
		assert.Empty(t, p.vetWrite(fx.p(f)), f)
	}
	// Through the whole policy, with the container reporting the paths.
	pc, ok := fx.h.access.(*policyAccess)
	require.True(t, ok)
	fx.h = NewHandler(&protectedAccess{policyAccess: pc, prot: []string{fx.p("meta.bin"), fx.p("lake_files")}}, fx.h.engine,
		&SandboxConfig{Mode: ModeFull, MaxQueryRows: 100}, fx.h.logger)
	runPolicyCases(t, fx, []policyCase{
		{"copy over a lake file", "COPY (SELECT 1) TO '%DATA%/x.ducklake'", sqlClassOther, "database or extension files"},
		{"copy over the catalog under another name", "COPY (SELECT 1) TO '%DATA%/meta.bin'", sqlClassOther, "connection's own files"},
		{"copy into the data folder", "COPY (SELECT 1) TO '%DATA%/lake_files/main/t/x.parquet'", sqlClassOther, "connection's own files"},
		{"an ordinary export", "COPY (SELECT 1) TO '%DATA%/out.csv'", sqlClassDDL, ""},
	})
}

type protectedAccess struct {
	*policyAccess
	prot []string
}

func (c *protectedAccess) ProtectedPaths() []string { return c.prot }

// #111: with a MotherDuck catalog attached, hybrid execution suffixes every
// plan operator with where it runs, " (L)" or " (R)". The suffix must not turn
// an allowed operator into an unknown one, nor hide a reader.
func TestPolicy_MotherDuckLocationSuffix(t *testing.T) {
	assert.Equal(t, "DOWNLOAD_SOURCE", planOpName("DOWNLOAD_SOURCE (L)"))
	assert.Equal(t, "SEQ_SCAN", planOpName(" seq_scan (R) "))
	assert.Equal(t, "HASH_GROUP_BY", planOpName("HASH_GROUP_BY"))
	assert.Equal(t, "X(L)", planOpName("X(L)"), "only a separated marker is a location")

	var readers int
	plan := planNode{Name: "UNGROUPED_AGGREGATE (L)", Children: []planNode{
		{Name: "DOWNLOAD_SOURCE (L)", Children: []planNode{
			{Name: "DOWNLOAD_SINK (R)", Children: []planNode{
				{Name: "SEQ_SCAN  (R)", ExtraInfo: map[string]any{"Table": "pulls"}},
			}},
		}},
	}}
	for _, n := range []string{"ORDER_BY (L)", "TOP_N (L)", "HASH_GROUP_BY (R)", "BATCH_DOWNLOAD_SINK (R)", "BATCH_UPLOAD_SINK (L)"} {
		plan.Children = append(plan.Children, planNode{Name: n})
	}
	assert.Empty(t, checkPlanNode(&plan, &readers), "a MotherDuck read is allowed")
	assert.Equal(t, 0, readers)

	r := planNode{Name: "READ_CSV (R)", ExtraInfo: map[string]any{"Function": "READ_CSV"}}
	assert.Empty(t, checkPlanNode(&r, &readers))
	assert.Equal(t, 1, readers, "a suffixed reader still counts, so it must be vetted")

	unknown := planNode{Name: "MD_SOMETHING_NEW (R)"}
	reason := checkPlanNode(&unknown, &readers)
	assert.Contains(t, reason, "MD_SOMETHING_NEW is not on the allow-list")
	assert.NotContains(t, reason, "files and URLs", "an unknown operator is not blamed on file access")
}

// #111: names that reach a database no connection attaches say so.
func TestPolicy_UnattachedCatalogNames(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	v := fx.classify("SELECT count(*) FROM newsroom.raw.cameras")
	assert.Equal(t, sqlClassOther, v.Class)
	assert.Contains(t, v.Reason, `no connection named "newsroom"`)
	assert.Contains(t, v.Reason, "connection of its own")
	assert.NotContains(t, v.Reason, "files and URLs")

	v = fx.classify("SELECT count(*) FROM other.newsroom.raw.cameras")
	assert.Equal(t, sqlClassOther, v.Class)
	assert.Contains(t, v.Reason, "at most three parts")
	assert.NotContains(t, v.Reason, "NameListToString")
}

// A DuckLake catalog in object storage is bytes in a bucket: the agent can read
// them with read_blob if and only if a ready storage connection's scope (the
// person's decision) already covers the URL. The lake widens nothing, and the
// SQLite readers stay off the allow-list (#118).
func TestPolicy_URLCatalogBytesFollowTheStorageScope(t *testing.T) {
	const q = "SELECT size(content) FROM read_blob('s3://acme/lake/catalog.db')"
	withScope := newPolicyFixture(t, ModeFull, withExtraConns(
		session.ConnectionInfo{Name: "store", Type: "s3", Kind: session.StatusOK, Catalogless: true, Scope: "s3://acme/"},
		// The lake itself carries no scope, whatever its catalog is.
		session.ConnectionInfo{Name: "lake", Type: "ducklake", Kind: session.StatusOK, Catalogless: false},
	))
	// Inside the scope the URL rule lets it through; what is left to object is
	// DuckDB's binder (httpfs is not installed in this fixture).
	v := withScope.classify(q)
	assert.Contains(t, v.Reason, "could not validate query", "the URL rule passed it: %s", v.Reason)

	// The same URL outside the scope is refused by the URL rule itself.
	v = withScope.classify("SELECT size(content) FROM read_blob('s3://other/lake/catalog.db')")
	assert.Equal(t, sqlClassOther, v.Class)
	assert.NotContains(t, v.Reason, "could not validate query", v.Reason)

	// With only the lake (no storage connection) nothing covers it.
	lakeOnly := newPolicyFixture(t, ModeFull, withExtraConns(
		session.ConnectionInfo{Name: "lake", Type: "ducklake", Kind: session.StatusOK, Catalogless: false, Scope: "s3://acme/"},
	))
	v = lakeOnly.classify(q)
	assert.Equal(t, sqlClassOther, v.Class)
	assert.Contains(t, v.Reason, "storage connection")

	for _, bad := range []string{
		"SELECT * FROM sqlite_scan('/tmp/catalog.db', 'ducklake_table')",
		"CALL sqlite_attach('/tmp/catalog.db')",
		"SELECT * FROM sqlite_scan('s3://acme/lake/catalog.db', 'ducklake_table')",
	} {
		v = withScope.classify(bad)
		assert.Equal(t, sqlClassOther, v.Class, bad)
		assert.Contains(t, v.Reason, "sqlite", bad)
	}
}
