package usersql

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/session/sessiontest"
)

func TestCheck_Refuses(t *testing.T) {
	for _, s := range []string{
		"ATTACH 'x.duckdb' AS x",
		"  -- hi\n detach local",
		"INSTALL httpfs",
		"LOAD httpfs",
		"SET enable_external_access = true",
		"CREATE SECRET s (TYPE s3, KEY_ID 'a')",
		"CREATE OR REPLACE PERSISTENT SECRET s (TYPE s3)",
		"DROP SECRET s",
		"SELECT 1; ATTACH 'x' AS y",
		"/* c */ SET threads = 1",
		"SELECT $$;$$; ATTACH 'x' AS y",
		"SELECT $t$ ' $t$; ATTACH 'x' AS y",
		`SELECT E'a\'b'; ATTACH 'x' AS y`,
	} {
		assert.Error(t, Check(s, false), s)
	}
}

func TestCheck_AllowsLookalikes(t *testing.T) {
	for _, s := range []string{
		"SELECT 'ATTACH x; SET y' AS s",
		`SELECT "set" FROM t`,
		"SELECT 1 -- ; ATTACH",
		"CREATE TABLE secret_agents (id INT)",
		"SELECT * FROM load_events",
		"INSERT INTO t VALUES ('; LOAD x')",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"SELECT $$ ; ATTACH 'x' $$",
		"SELECT $tag$ ; SET x $tag$",
		`SELECT E'it\'s; SET x'`,
		"SELECT $1 FROM t",
		"COPY t TO 'out.csv'",
	} {
		assert.NoError(t, Check(s, false), s)
	}
}

func TestCheck_ReadOnlyRefusesFileWrites(t *testing.T) {
	for _, s := range []string{"COPY t TO 'out.csv'", "COPY (SELECT 1) TO 'o.parquet' (FORMAT parquet)", "EXPORT DATABASE 'dir'"} {
		assert.Error(t, Check(s, true), s)
	}
	assert.NoError(t, Check("COPY t FROM 'in.csv'", true))
	assert.NoError(t, Check("SELECT 'to' AS x", true))
}

func TestRequireSelect(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	noExtensionFetch(t, db)
	defer db.Close() //nolint:errcheck
	ctx := context.Background()
	for _, s := range []string{"SELECT 1", "WITH x AS (SELECT 1 AS a) SELECT * FROM x", "FROM range(3)", "VALUES (1),(2)", "SELECT 1;"} {
		assert.NoError(t, RequireSelect(ctx, db, s), s)
	}
	for _, s := range []string{
		"DROP TABLE t",
		"INSERT INTO t VALUES (1)",
		"SELECT 1; DROP TABLE t",
		"SELECT 1) TO 'x.csv'; DROP TABLE t; COPY (SELECT 1",
		"PRAGMA version",
		"not sql at all",
	} {
		assert.Error(t, RequireSelect(ctx, db, s), s)
	}
}

func TestCheckAllowsReadPragmasAndRefusesAssignments(t *testing.T) {
	for _, ok := range []string{"PRAGMA table_info('t')", "pragma show_tables", "PRAGMA version", "PRAGMA database_list"} {
		assert.NoError(t, Check(ok, false), ok)
		assert.NoError(t, CheckManaged(ok), ok)
	}
	for _, bad := range []string{"PRAGMA threads=1", "PRAGMA threads = 1", "PRAGMA s3_url_compatibility_mode", "PRAGMA \"x\"='y'"} {
		assert.Error(t, Check(bad, false), bad)
	}
}

func TestCheckManagedRefusesOnlyLoadAndSecretDDL(t *testing.T) {
	for _, bad := range []string{
		"LOAD httpfs", "install httpfs", "DROP TEMPORARY SECRET \"slaw-deny-s3\"",
		"select 1; drop secret if exists x", "CREATE OR REPLACE SECRET s (TYPE s3)", "-- c\nLOAD aws", "FORCE INSTALL httpfs", "force install httpfs FROM core", "UPDATE EXTENSIONS", "update extensions (httpfs)",
		"SET s3_url_compatibility_mode = false", "set global S3_URL_COMPATIBILITY_MODE=false", "RESET s3_url_compatibility_mode",
		`SET "s3_url_compatibility_mode" = false`, "select 1; set session s3_url_compatibility_mode to false",
		"PRAGMA s3_url_compatibility_mode=false", "pragma S3_URL_COMPATIBILITY_MODE=false", `PRAGMA "s3_url_compatibility_mode"=false`,
		"PRAGMA s3_url_compatibility_mode = 'false'", "SELECT 1; PRAGMA s3_url_compatibility_mode=false",
		"SET http_timeout = 600", "set global HTTP_RETRIES=9", `PRAGMA "http_timeout"=0`,
	} {
		assert.Error(t, CheckManaged(bad), bad)
	}
	for _, ok := range []string{
		"SELECT 'LOAD x'", "ATTACH 'a.duckdb' AS a", "SET threads=1", "COPY t TO 'f.csv'",
		"DROP TABLE secrets", "UPDATE t SET x = 1", "CREATE TABLE t AS SELECT 1",
	} {
		assert.NoError(t, CheckManaged(ok), ok)
	}
}

// A piece that DuckDB reads as two statements (our splitter and DuckDB's
// disagreeing) cannot be prepared; it must still be refused when it names the
// setting, and passed when it does not.
func TestCheckPieceRefusesAnUnpreparablePieceThatNamesTheSetting(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	noExtensionFetch(t, db)
	defer db.Close() //nolint:errcheck
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	assert.Error(t, checkPiece(conn, "SELECT 1; PRAGMA s3_url_compatibility_mode=false"))
	assert.Error(t, checkPiece(conn, "SELECT 1; SET \"S3_URL_compatibility_mode\" = 'false'"))
	assert.NoError(t, checkPiece(conn, "SELECT 1; SELECT 2"))
}

// CheckInteractive is the editor's guard: CheckManaged, then, only while the
// session is lazy about httpfs, the parser-based CheckParsed.
func TestCheckInteractive_IsManagedPlusParsedWhenLazy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	lazy, err := session.New(
		session.WithTempDirectory(filepath.Join(dir, "tmp")),
		session.WithExtensionDirectory(filepath.Join(dir, "ext")),
		session.WithSecretDirectory(filepath.Join(dir, "sec")),
	)
	require.NoError(t, err)
	defer lazy.Close() //nolint:errcheck
	require.True(t, session.LazyStorage(lazy), "httpfs has not loaded: the parser guard applies")

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	noExtensionFetch(t, db)
	defer db.Close() //nolint:errcheck
	eager := sessiontest.ForTests(db)
	require.False(t, session.LazyStorage(eager))

	for _, stmt := range []string{
		"SELECT 1", "INSTALL httpfs", "CREATE SECRET s (TYPE s3)", "ATTACH 'x.duckdb' AS x", "SET threads=1",
		"SELECT 1; LOAD httpfs", "SET s3_url_compatibility_mode=true", "PRAGMA \"S3_URL_compat\"=1", "SELECT * FROM nope",
	} {
		managed := CheckManaged(stmt)
		assert.Equal(t, managed, CheckInteractive(ctx, eager, stmt), "eager: %s", stmt)
		want := managed
		if want == nil {
			want = CheckParsed(ctx, lazy.DB(), stmt)
		}
		assert.Equal(t, want, CheckInteractive(ctx, lazy, stmt), "lazy: %s", stmt)
	}
}

func TestParsedType(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	noExtensionFetch(t, db)
	defer db.Close() //nolint:errcheck
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck

	for text, want := range map[string]duckdb.StmtType{
		"SELECT 1":              duckdb.STATEMENT_TYPE_SELECT,
		"PRAGMA version":        duckdb.STATEMENT_TYPE_SELECT,
		"CREATE TABLE p(x INT)": duckdb.STATEMENT_TYPE_CREATE,
		"ATTACH 'postgres:host=h password=hunter2' AS pg (TYPE postgres)": duckdb.STATEMENT_TYPE_ATTACH,
		"DETACH pg":                    duckdb.STATEMENT_TYPE_DETACH,
		"SET s3_secret_access_key='x'": duckdb.STATEMENT_TYPE_SET,
		"PRAGMA threads=4":             duckdb.STATEMENT_TYPE_SET,
		"RESET threads":                duckdb.STATEMENT_TYPE_SET,
		"USE memory":                   duckdb.STATEMENT_TYPE_SET,
	} {
		got, err := ParsedType(conn, text)
		require.NoError(t, err, text)
		assert.Equal(t, want, got, text)
	}
	for _, text := range []string{"SELECT * FROM nope", "SELEC 1", "SELECT 1; SELECT 2"} {
		_, err := ParsedType(conn, text)
		assert.Error(t, err, text)
	}
}

// noExtensionFetch keeps a test database from installing or loading anything.
func noExtensionFetch(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{"SET autoinstall_known_extensions=false", "SET autoload_known_extensions=false"} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
}
