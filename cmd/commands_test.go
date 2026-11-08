package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetFlags puts every flag of cmd and its subcommands back to its default,
// so one test's flags never leak into the next on the shared command tree.
func resetFlags(c *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	// Cobra keeps the context of the first run on each subcommand it reaches, so a
	// later run would see the earlier one's (a test's fake endpoints, a cancelled
	// context).
	c.SetContext(nil) //nolint:staticcheck // clearing, not passing, a context
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

// runSlaw runs the root command in-process and returns stdout, stderr and the
// error.
func runSlaw(args ...string) (stdout, stderr string, err error) {
	return runSlawIn(nil, args...)
}

func runSlawIn(in io.Reader, args ...string) (stdout, stderr string, err error) {
	var out, errb bytes.Buffer
	resetFlags(rootCmd)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	if in == nil {
		in = strings.NewReader("")
	}
	rootCmd.SetIn(in)
	rootCmd.SetArgs(args)
	err = rootCmd.Execute()
	return out.String(), errb.String(), err
}

// executeCommand runs the root command and returns stdout and the error.
func executeCommand(args ...string) (string, error) {
	out, _, err := runSlaw(args...)
	return out, err
}

// newWS creates a workspace through the CLI in a temp dir (with an isolated
// config dir) and returns its root.
func newWS(t *testing.T, extra ...string) string {
	t.Helper()
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	useMemoryKeychain(t)
	root := filepath.Join(t.TempDir(), "acme")
	args := append([]string{"workspace", "init", root}, extra...)
	_, _, err := runSlaw(args...)
	require.NoError(t, err)
	r, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	return r
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestWorkspaceInit_CreatesWorkspaceAndPrintsNextSteps(t *testing.T) {
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "acme-analytics")
	out, stderr, err := runSlaw("workspace", "init", root, "--name", "Acme Analytics")
	require.NoError(t, err, stderr)
	assert.Contains(t, out, `Created workspace "Acme Analytics"`)
	assert.Contains(t, out, "Next steps")
	assert.Contains(t, out, "slaw query")
	assert.Contains(t, out, "slaw import")

	for _, p := range []string{"slaw.yaml", ".gitignore", ".gitattributes"} {
		assert.FileExists(t, filepath.Join(root, p), p)
	}
}

func TestWorkspaceInit_Details(t *testing.T) {
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "w")
	_, _, err := runSlaw("workspace", "init", root, "--name", "Named")
	require.NoError(t, err)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	assert.Equal(t, "Named", ws.Name())
	assert.Equal(t, "local", ws.DefaultConnection())
	for _, d := range []string{"notebooks", "queries", "skills", "data"} {
		fi, err := os.Stat(filepath.Join(root, d))
		require.NoError(t, err, d)
		assert.True(t, fi.IsDir(), d)
	}
}

func TestWorkspaceInit_Git(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "g")
	out, _, err := runSlaw("workspace", "init", root, "--git")
	require.NoError(t, err)
	assert.Contains(t, out, "Initialized a git repository")
	assert.DirExists(t, filepath.Join(root, ".git"))

	plain := filepath.Join(t.TempDir(), "nogit")
	_, _, err = runSlaw("workspace", "init", plain)
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(plain, ".git"))
}

func TestWorkspaceInit_Template(t *testing.T) {
	root := newWS(t, "--template", "order-entry")
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck

	nbs, err := ws.Notebooks().List(context.Background())
	require.NoError(t, err)
	require.Len(t, nbs, 1)
	assert.Equal(t, "Order Entry", nbs[0].Title)
	files, _ := filepath.Glob(filepath.Join(root, "notebooks", "*.slnb"))
	assert.Len(t, files, 1, "the notebook is a .slnb file")

	out, stderr, err := runSlaw("query", "--workspace", root, "SELECT count(*) AS n FROM order_items")
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "3")

	// The seeded data is in the workspace's local connection, visible to a
	// fresh session.
	sess, issues, err := workspace.OpenSession(context.Background(), ws, testCmdLogger())
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck
	require.Empty(t, issues)
	var n int
	require.NoError(t, sess.DB().QueryRow("SELECT count(*) FROM customers").Scan(&n))
	assert.Equal(t, 2, n)
}

func TestWorkspaceInit_UnknownTemplateListsTemplates(t *testing.T) {
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "x")
	_, _, err := runSlaw("workspace", "init", root, "--template", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "order-entry")
	assert.NoFileExists(t, filepath.Join(root, "slaw.yaml"), "nothing is created for an unknown template")
}

func TestWorkspaceInit_RefusesExistingWorkspaceAndSlawFiles(t *testing.T) {
	root := newWS(t)
	_, _, err := runSlaw("workspace", "init", root)
	require.Error(t, err)
	assert.ErrorIs(t, err, workspace.ErrAlreadyWorkspace)

	for _, args := range [][]string{{"workspace", "init", "old.slaw"}, {"init", "old.slaw"}} {
		_, _, err = runSlaw(args...)
		require.Error(t, err, "%v", args)
		assert.Contains(t, err.Error(), "bundles, not workspaces")
		assert.Contains(t, err.Error(), "slaw workspace init")
	}
}

func TestInit_IsAnAliasOfWorkspaceInit(t *testing.T) {
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "alias")
	out, _, err := runSlaw("init", root, "--name", "Aliased")
	require.NoError(t, err)
	assert.Contains(t, out, `Created workspace "Aliased"`)
	assert.FileExists(t, filepath.Join(root, "slaw.yaml"))
}

func TestWorkspaceInfo_ShowsEverything(t *testing.T) {
	root := newWS(t, "--template", "contacts")
	writeFile(t, filepath.Join(root, "queries"), "q1.sql", "SELECT 1\n")

	out, stderr, err := runSlaw("workspace", "info", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Root:        "+root)
	assert.Contains(t, out, "acme")
	assert.Regexp(t, `local\s+ok\s+\[duckdb, shared, read-write, default\]\s+\d+ table\(s\)`, out)
	assert.Contains(t, out, "Notebooks:   1")
	assert.Contains(t, out, "Queries:     1")
	assert.Contains(t, out, "Skills:      0")
	assert.Contains(t, out, "Agent:       sandbox read-write, ask before writes on")
	assert.Contains(t, out, "Hub owner:   none")
	assert.Regexp(t, `DuckDB:\s+v\d+\.\d+`, out)

	js, _, err := runSlaw("workspace", "info", "--workspace", root, "--json")
	require.NoError(t, err)
	var rep infoReport
	require.NoError(t, json.Unmarshal([]byte(js), &rep))
	assert.Equal(t, root, rep.Root)
	assert.Regexp(t, `^v\d+\.\d+`, rep.DuckDB)
	assert.NotNil(t, rep.Extensions)
	require.Len(t, rep.Connections, 1)
	assert.Equal(t, "ok", rep.Connections[0].Status)
	require.NotNil(t, rep.Connections[0].Tables)
	assert.Equal(t, 2, *rep.Connections[0].Tables)
	assert.Equal(t, 1, rep.Notebooks)
}

func TestWorkspaceInfo_FreshWorkspaceDoesNotCreateDataFile(t *testing.T) {
	root := newWS(t)
	out, _, err := runSlaw("workspace", "info", "--workspace", root)
	require.NoError(t, err)
	assert.Contains(t, out, "no data file yet")
	assert.NoFileExists(t, filepath.Join(root, "data", "local.duckdb"), "info must not create anything")
}

func TestWorkspaceInfo_NamesTheHubHolder(t *testing.T) {
	root := newWS(t)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	hold, err := hub.AcquireHub(ws, "Slaw")
	require.NoError(t, err)
	defer hold.Release() //nolint:errcheck

	out, _, err := runSlaw("workspace", "info", "--workspace", root)
	require.NoError(t, err)
	assert.Contains(t, out, "Hub owner:   Slaw (pid ")
}

func TestWorkspaceInfo_FindsWorkspaceFromSubfolder(t *testing.T) {
	root := newWS(t)
	t.Chdir(filepath.Join(root, "notebooks"))
	out, _, err := runSlaw("workspace", "info")
	require.NoError(t, err)
	assert.Contains(t, out, root)
}

func TestWorkspaceInfo_NoWorkspace(t *testing.T) {
	t.Setenv("SLAW_CONFIG_DIR", t.TempDir())
	_, _, err := runSlaw("workspace", "info", "--workspace", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Slaw workspace found")
}

func TestQuery_Formats(t *testing.T) {
	root := newWS(t)
	q := func(args ...string) string {
		t.Helper()
		out, stderr, err := runSlaw(append([]string{"query", "--workspace", root}, args...)...)
		require.NoError(t, err, stderr)
		return out
	}
	table := q("SELECT 1 AS a, 'x' AS b")
	assert.Contains(t, table, "a  b")
	assert.Contains(t, table, "1  x")
	assert.Contains(t, table, "(1 row)")

	assert.Equal(t, "a,b\n1,x\n", q("--format", "csv", "SELECT 1 AS a, 'x' AS b"))

	js := q("--format", "json", "SELECT 1 AS a, 'x' AS b UNION ALL SELECT 2, NULL")
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(js), &rows), js)
	require.Len(t, rows, 2)
	assert.Equal(t, "x", rows[0]["b"])
	assert.Nil(t, rows[1]["b"])
	assert.Less(t, strings.Index(js, `"a"`), strings.Index(js, `"b"`), "column order is preserved")
}

func TestQuery_WritesLandInTheDefaultConnection(t *testing.T) {
	root := newWS(t)
	_, stderr, err := runSlaw("query", "--workspace", root, "CREATE TABLE t AS SELECT 1 AS id")
	require.NoError(t, err, stderr)
	assert.FileExists(t, filepath.Join(root, "data", "local.duckdb"))
	out, _, err := runSlaw("query", "--workspace", root, "--format", "csv", "SELECT * FROM local.main.t")
	require.NoError(t, err)
	assert.Equal(t, "id\n1\n", out)
}

func TestQuery_Limit(t *testing.T) {
	root := newWS(t)
	out, stderr, err := runSlaw("query", "--workspace", root, "--limit", "3", "--format", "csv", "SELECT * FROM range(10)")
	require.NoError(t, err)
	assert.Equal(t, 4, strings.Count(out, "\n"), "header plus 3 rows")
	assert.Contains(t, stderr, "showing the first 3 of 10 rows")

	out, stderr, err = runSlaw("query", "--workspace", root, "--limit", "0", "--format", "csv", "SELECT * FROM range(10)")
	require.NoError(t, err)
	assert.Equal(t, 11, strings.Count(out, "\n"))
	assert.NotContains(t, stderr, "showing the first")
}

func TestQuery_StatementFromStdin(t *testing.T) {
	root := newWS(t)
	out, _, err := runSlawIn(strings.NewReader("SELECT 7 AS seven"), "query", "--workspace", root, "--format", "csv", "-")
	require.NoError(t, err)
	assert.Equal(t, "seven\n7\n", out)
}

func TestQuery_RefusesWhatSlawManagesForYou(t *testing.T) {
	root := newWS(t)
	for _, s := range []string{"ATTACH 'x.duckdb' AS x", "SET threads = 1", "CREATE SECRET s (TYPE s3)", "SELECT 1; INSTALL httpfs"} {
		_, _, err := runSlaw("query", "--workspace", root, s)
		require.Error(t, err, s)
		assert.Contains(t, err.Error(), "not available here", s)
	}
	assert.NoFileExists(t, filepath.Join(root, "data", "local.duckdb"), "a refusal opens nothing for writing")

	// A SQL string that merely mentions a .slaw file is just SQL.
	_, _, err := runSlaw("query", "--workspace", root, "SELECT * FROM s.slaw")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "bundles, not workspaces")
}

func TestQuery_ReadOnlyRefusesCopyTo(t *testing.T) {
	root := seededWS(t)
	dst := filepath.Join(t.TempDir(), "o.csv")
	_, _, err := runSlaw("query", "--workspace", root, "--read-only", "COPY people TO '"+dst+"'")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--read-only")
	assert.NoFileExists(t, dst)
}

func TestQuery_ConnectionFlag(t *testing.T) {
	root := newWS(t)
	archive := filepath.Join(root, "data", "archive.duckdb")
	db, err := sql.Open("duckdb", archive)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE old AS SELECT 5 AS n")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	addConnection(t, root, workspace.ConnectionDef{Name: "archive", Type: "duckdb", Path: "data/archive.duckdb"})

	out, stderr, err := runSlaw("query", "--workspace", root, "-c", "archive", "--format", "csv", "SELECT * FROM old")
	require.NoError(t, err, stderr)
	assert.Equal(t, "n\n5\n", out)

	// Writing to a read-only connection says why and how to change it.
	_, _, err = runSlaw("query", "--workspace", root, "-c", "archive", "INSERT INTO old VALUES (6)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read_only: false")

	_, _, err = runSlaw("query", "--workspace", root, "-c", "missing", "SELECT 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown connection "missing"`)
	assert.Contains(t, err.Error(), "archive")
}

// addConnection appends a shared connection to the workspace's slaw.yaml.
func addConnection(t *testing.T, root string, def workspace.ConnectionDef) {
	t.Helper()
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	cfg := ws.Config()
	cfg.Connections = append(cfg.Connections, def)
	require.NoError(t, ws.SaveConfig(cfg))
}

func TestQuery_WhenAnotherProcessOwnsTheHub(t *testing.T) {
	root := newWS(t)
	_, _, err := runSlaw("query", "--workspace", root, "CREATE TABLE t AS SELECT 1 AS id")
	require.NoError(t, err)

	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	hold, err := hub.AcquireHub(ws, "Slaw")
	require.NoError(t, err)
	defer hold.Release() //nolint:errcheck

	_, _, err = runSlaw("query", "--workspace", root, "SELECT * FROM t")
	require.Error(t, err)
	assert.ErrorIs(t, err, hub.ErrHubHeld)
	assert.Contains(t, err.Error(), "Slaw (pid ")
	assert.Contains(t, err.Error(), "--read-only")

	// Nothing in this process has the DuckDB file open (only the lock is
	// held), so a read-only reader gets through.
	out, stderr, err := runSlaw("query", "--workspace", root, "--read-only", "--format", "csv", "SELECT * FROM t")
	require.NoError(t, err, stderr)
	assert.Equal(t, "id\n1\n", out)

	_, _, err = runSlaw("query", "--workspace", root, "--read-only", "INSERT INTO t VALUES (2)")
	require.Error(t, err, "a read-only attach cannot write")
}

func TestQuery_ReadOnlyOnAFreshWorkspaceSaysThereIsNoData(t *testing.T) {
	root := newWS(t)
	_, _, err := runSlaw("query", "--workspace", root, "--read-only", "SELECT 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no data file yet")
	assert.NoFileExists(t, filepath.Join(root, "data", "local.duckdb"))
}

func TestQuery_RefusesSlawFilesAndBadFlags(t *testing.T) {
	root := newWS(t)
	_, _, err := runSlaw("query", "--workspace", "old.slaw", "SELECT 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundles, not workspaces")

	_, _, err = runSlaw("query", "--workspace", root, "--format", "xml", "SELECT 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported format")
}

func TestImport_CreatesTableWithPK(t *testing.T) {
	root := newWS(t)
	csvPath := writeFile(t, t.TempDir(), "customers.csv", "customer_id,name\n1,Ann\n2,Bob\n")

	out, stderr, err := runSlaw("import", csvPath, "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Imported 2 row(s)")
	assert.Contains(t, out, "customers")

	q, _, err := runSlaw("query", "--workspace", root, "--format", "csv", "SELECT name FROM customers ORDER BY customer_id")
	require.NoError(t, err)
	assert.Equal(t, "name\nAnn\nBob\n", q)

	q, _, err = runSlaw("query", "--workspace", root, "--format", "csv",
		`SELECT count(*) FROM duckdb_constraints() WHERE table_name='customers' AND constraint_type='PRIMARY KEY'`)
	require.NoError(t, err)
	assert.Contains(t, q, "1", "the suggested customer_id key is used")

	// The import also refreshed the workspace-<table> skill, as Studio's does.
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	sk, err := ws.Skills().List(context.Background())
	require.NoError(t, err)
	require.Len(t, sk, 1)
	assert.Contains(t, sk[0].Name, "customers")
}

// TestImport_LandsInTheWorkspaceNotAnEnvelope is the #72 regression: the CLI
// used to write into the SQLite envelope, where nothing else could see it.
// An imported table must be visible to `slaw query` and to a fresh
// workspace.OpenSession (what Studio and `mcp serve` use), and lives in the
// default connection's DuckDB file.
func TestImport_LandsInTheWorkspaceNotAnEnvelope(t *testing.T) {
	root := newWS(t)
	csvPath := writeFile(t, t.TempDir(), "sales.csv", "id,amount\n1,10.5\n2,20\n")
	_, stderr, err := runSlaw("import", csvPath, "--into", "sales", "--workspace", root)
	require.NoError(t, err, stderr)

	out, _, err := runSlaw("query", "--workspace", root, "--format", "csv", "SELECT sum(amount) AS total FROM sales")
	require.NoError(t, err)
	assert.Contains(t, out, "30.5")

	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	sess, issues, err := workspace.OpenSession(context.Background(), ws, testCmdLogger())
	require.NoError(t, err)
	require.Empty(t, issues)
	var n int
	require.NoError(t, sess.DB().QueryRow("SELECT count(*) FROM local.main.sales").Scan(&n))
	assert.Equal(t, 2, n)
	require.NoError(t, sess.Close())

	// And in the DuckDB file itself, opened on its own.
	db, err := sql.Open("duckdb", filepath.Join(root, "data", "local.duckdb")+"?access_mode=read_only")
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck
	require.NoError(t, db.QueryRow("SELECT count(*) FROM sales").Scan(&n))
	assert.Equal(t, 2, n)
	assert.Empty(t, globSlawFiles(t, root), "no .slaw envelope is ever written")
}

func globSlawFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.EqualFold(filepath.Ext(p), ".slaw") {
			found = append(found, p)
		}
		return nil
	}))
	return found
}

func TestImport_IntoQualifiedConnection(t *testing.T) {
	root := newWS(t)
	other := filepath.Join(root, "data", "other.duckdb")
	db, err := sql.Open("duckdb", other)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	rw := false
	addConnection(t, root, workspace.ConnectionDef{Name: "other", Type: "duckdb", Path: "data/other.duckdb", ReadOnly: &rw})

	csvPath := writeFile(t, t.TempDir(), "items.csv", "id,label\n1,a\n")
	out, stderr, err := runSlaw("import", csvPath, "--into", "other.items", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "other.main.items")

	q, _, err := runSlaw("query", "--workspace", root, "-c", "other", "--format", "csv", "SELECT label FROM items")
	require.NoError(t, err)
	assert.Equal(t, "label\na\n", q)
	_, _, err = runSlaw("query", "--workspace", root, "SELECT * FROM items")
	require.Error(t, err, "the default connection has no such table")
}

func TestImport_RefusesReadOnlyConnection(t *testing.T) {
	root := newWS(t)
	archive := filepath.Join(root, "data", "archive.duckdb")
	db, err := sql.Open("duckdb", archive)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	addConnection(t, root, workspace.ConnectionDef{Name: "archive", Type: "duckdb", Path: "data/archive.duckdb"})

	csvPath := writeFile(t, t.TempDir(), "items.csv", "id\n1\n")
	_, _, err = runSlaw("import", csvPath, "--into", "archive.items", "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
	assert.Contains(t, err.Error(), "read_only: false")
}

func TestImport_RerunNeedsAMode_UpsertAndAppend(t *testing.T) {
	root := newWS(t)
	dir := t.TempDir()
	csvPath := writeFile(t, dir, "customers.csv", "customer_id,name\n1,Ann\n")
	_, _, err := runSlaw("import", csvPath, "--workspace", root)
	require.NoError(t, err)

	out, _, err := runSlaw("import", csvPath, "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, out, "--mode")

	csv2 := writeFile(t, dir, "customers.csv", "customer_id,name\n1,Ann\n2,Bob\n")
	out, _, err = runSlaw("import", csv2, "--workspace", root, "--mode", "upsert")
	require.NoError(t, err)
	assert.Contains(t, out, "1 new, 0 changed, 1 unchanged")

	csv3 := writeFile(t, dir, "customers.csv", "customer_id,name\n3,Cy\n")
	out, _, err = runSlaw("import", csv3, "--workspace", root, "--mode", "append")
	require.NoError(t, err)
	assert.Contains(t, out, "Added 1 row(s)")

	_, _, err = runSlaw("import", csv3, "--workspace", root, "--mode", "bogus")
	require.Error(t, err)
}

func TestImport_DryRunWritesNothing(t *testing.T) {
	root := newWS(t)
	csvPath := writeFile(t, t.TempDir(), "customers.csv", "customer_id,name\n1,Ann\n")
	out, _, err := runSlaw("import", csvPath, "--workspace", root, "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "Would import 1 row(s)")

	_, _, err = runSlaw("query", "--workspace", root, "SELECT * FROM customers")
	require.Error(t, err, "a dry run must leave no table behind")

	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	sk, _ := ws.Skills().List(context.Background())
	assert.Empty(t, sk, "no skill from a dry run")
}

func TestImport_UnsupportedFileTypeAndSlawFile(t *testing.T) {
	root := newWS(t)
	p := writeFile(t, t.TempDir(), "x.parquet", "not parquet")
	_, _, err := runSlaw("import", p, "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported file type")

	// A .slaw argument is a bundle now; an older container is refused plainly.
	old := writeFile(t, t.TempDir(), "old.slaw", "SQLite format 3\x00 and then some data")
	_, _, err = runSlaw("import", old, "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "older Vibe file that holds data")
}

func TestImport_WhenAnotherProcessOwnsTheHub(t *testing.T) {
	root := newWS(t)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	hold, err := hub.AcquireHub(ws, "slaw mcp serve")
	require.NoError(t, err)
	defer hold.Release() //nolint:errcheck

	csvPath := writeFile(t, t.TempDir(), "c.csv", "id\n1\n")
	_, _, err = runSlaw("import", csvPath, "--workspace", root)
	require.Error(t, err)
	assert.ErrorIs(t, err, hub.ErrHubHeld)
	assert.Contains(t, err.Error(), "slaw mcp serve (pid ")
}

func seededWS(t *testing.T) string {
	t.Helper()
	root := newWS(t)
	csvPath := writeFile(t, t.TempDir(), "people.csv", "id,name\n1,Ann\n2,\"Bob, Jr\"\n")
	_, stderr, err := runSlaw("import", csvPath, "--workspace", root)
	require.NoError(t, err, stderr)
	return root
}

func TestExport_FilesByExtension(t *testing.T) {
	root := seededWS(t)
	dst := t.TempDir()

	csvOut := filepath.Join(dst, "people.csv")
	out, stderr, err := runSlaw("export", "people", "-o", csvOut, "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Exported to "+csvOut)
	b, _ := os.ReadFile(csvOut)
	assert.Contains(t, string(b), `"Bob, Jr"`, "a comma in a value is quoted")

	jsonOut := filepath.Join(dst, "people.json")
	_, _, err = runSlaw("export", "local.people", "-o", jsonOut, "--workspace", root)
	require.NoError(t, err)
	b, _ = os.ReadFile(jsonOut)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(b, &rows), string(b))
	assert.Len(t, rows, 2)

	pq := filepath.Join(dst, "people.parquet")
	_, _, err = runSlaw("export", "--sql", "SELECT name FROM people WHERE id = 1", "-o", pq, "--workspace", root)
	require.NoError(t, err)
	fi, err := os.Stat(pq)
	require.NoError(t, err)
	assert.Greater(t, fi.Size(), int64(0))
	got, _, err := runSlaw("query", "--workspace", root, "--format", "csv", "SELECT * FROM read_parquet('"+pq+"')")
	require.NoError(t, err)
	assert.Equal(t, "name\nAnn\n", got)
}

func TestExport_StdoutAndRefusals(t *testing.T) {
	root := seededWS(t)
	out, _, err := runSlaw("export", "people", "--workspace", root)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "id,name\n"), out)

	out, _, err = runSlaw("export", "people", "--format", "json", "--workspace", root)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "["), out)

	dst := filepath.Join(t.TempDir(), "p.csv")
	require.NoError(t, os.WriteFile(dst, []byte("keep me"), 0o644))
	_, _, err = runSlaw("export", "people", "-o", dst, "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	b, _ := os.ReadFile(dst)
	assert.Equal(t, "keep me", string(b), "an existing file is not touched without --force")
	_, _, err = runSlaw("export", "people", "-o", dst, "--force", "--workspace", root)
	require.NoError(t, err)
	b, _ = os.ReadFile(dst)
	assert.Contains(t, string(b), "Ann")

	for _, args := range [][]string{
		{"export", "--workspace", root},
		{"export", "people", "--sql", "SELECT 1", "--workspace", root},
		{"export", "people", "-o", "x.txt", "--workspace", root},
		{"export", "./old.slaw", "--workspace", root},
		{"export", "--sql", "ATTACH 'x' AS y", "-o", filepath.Join(t.TempDir(), "a.csv"), "--workspace", root},
		{"export", "nosuchtable", "-o", filepath.Join(t.TempDir(), "n.csv"), "--workspace", root},
	} {
		_, _, err = runSlaw(args...)
		assert.Error(t, err, "%v", args)
	}
}

func TestExport_ReadOnlyDoesNotNeedTheHub(t *testing.T) {
	root := seededWS(t)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	hold, err := hub.AcquireHub(ws, "Slaw")
	require.NoError(t, err)
	defer hold.Release() //nolint:errcheck

	_, _, err = runSlaw("export", "people", "--workspace", root)
	require.ErrorIs(t, err, hub.ErrHubHeld)
	out, stderr, err := runSlaw("export", "people", "--read-only", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Ann")
}

func TestRemovedContainerCommandsPointToWorkspaces(t *testing.T) {
	for _, name := range []string{"info", "verify", "compact", "rekey", "dump", "passphrase"} {
		_, _, err := runSlaw(name, "old.slaw")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "old .vibe container files", name)
		assert.Contains(t, err.Error(), "slaw workspace init", name)
	}
}

// slowKeys feeds keystrokes one at a time with a pause, so the TUI renders a
// frame between them (its renderer is frame-throttled).
func slowKeys(keys ...string) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		time.Sleep(300 * time.Millisecond)
		for _, k := range keys {
			_, _ = io.WriteString(pw, k)
			time.Sleep(120 * time.Millisecond)
		}
		_ = pw.Close()
	}()
	return pr
}

func TestStudio_RefusesSlawFiles(t *testing.T) {
	_, _, err := runSlaw("studio", "old.slaw")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundles, not workspaces")
}

// TestStudioTUI_RunsOnTheWorkspace drives the real TUI program on a temp
// workspace through a scripted input: tab to the notebooks view, then quit.
func TestStudioTUI_RunsOnTheWorkspace(t *testing.T) {
	root := newWS(t, "--template", "contacts")
	// \t switches to Tables, \t again to Notebooks; ctrl+c quits.
	out, stderr, err := runSlawIn(slowKeys("\t", "\t", "\x03"), "studio", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "SLAW STUDIO")
	assert.Contains(t, out, "acme")
	assert.Contains(t, out, "Contacts", "the starter notebook is listed")
	assert.NotContains(t, out, "[read-only]")

	// The TUI released the hub on exit.
	_, err = hub.ReadHolder(root)
	assert.ErrorIs(t, err, hub.ErrNotHeld)
}

func TestStudioTUI_ReadOnlyMarksTheStatusBarAndSkipsTheLock(t *testing.T) {
	root := seededWS(t)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	hold, err := hub.AcquireHub(ws, "Slaw")
	require.NoError(t, err)
	defer hold.Release() //nolint:errcheck

	_, _, err = runSlawIn(strings.NewReader("\x03"), "studio", "--tui", root)
	require.ErrorIs(t, err, hub.ErrHubHeld, "stdin is not a terminal, so there is no prompt, just advice")

	out, stderr, err := runSlawIn(slowKeys("\t", "\x03"), "studio", "--read-only", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "[read-only]")
	assert.Contains(t, out, "people", "the table browser lists the default connection's tables")
}

func TestStudio_GuiAcceptsAFolder(t *testing.T) {
	root := newWS(t)
	t.Setenv("PATH", t.TempDir()) // no slaw-studio to exec: we only check the folder is accepted
	_, _, err := runSlaw("studio", "--gui", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slaw-studio binary not found", "a folder argument is no longer refused")
	assert.NotContains(t, err.Error(), "#111")
}

func TestStudio_GuiPassesASlawBundleToTheApp(t *testing.T) {
	// The desktop app opens its Import dialog for a .slaw; only the terminal UI
	// (which has no import flow) refuses one.
	t.Setenv("PATH", t.TempDir()) // no slaw-studio to exec (not even one in ~/go/bin)
	_, _, err := runSlaw("studio", "--gui", filepath.Join(t.TempDir(), "x.slaw"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slaw-studio binary not found")
	assert.NotErrorIs(t, err, errLegacyFileArg)

	_, _, err = runSlaw("studio", filepath.Join(t.TempDir(), "x.slaw"))
	assert.ErrorIs(t, err, errLegacyFileArg)
}

func TestStudio_TuiFlagStillAccepted(t *testing.T) {
	root := newWS(t)
	_, stderr, err := runSlawIn(slowKeys("\x03"), "studio", "--tui", root)
	require.NoError(t, err, stderr)
}

func TestStudioTUI_REPLRefusesAttach(t *testing.T) {
	root := newWS(t)
	// Type the statement one key at a time, then Enter, then quit.
	keys := []string{}
	for _, r := range "ATTACH 'x.duckdb' AS x" {
		keys = append(keys, string(r))
	}
	keys = append(keys, "\r", "\x03")
	out, stderr, err := runSlawIn(slowKeys(keys...), "studio", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "not available here")
}

func TestIsSlawTableArg(t *testing.T) {
	dir := t.TempDir()
	real := writeFile(t, dir, "old.slaw", "x")
	assert.True(t, isSlawTableArg(real), "an existing file")
	assert.True(t, isSlawTableArg("./old.slaw"))
	assert.True(t, isSlawTableArg("dir/old.slaw"))
	assert.False(t, isSlawTableArg("s.slaw"), "schema.table shape, no such file")
	assert.False(t, isSlawTableArg("SELECT * FROM s.slaw"))
	assert.False(t, isSlawTableArg(`"my.slaw"`))
	assert.False(t, isSlawTableArg("orders"))
	assert.True(t, isSlawFile("old.slaw"), "a path argument")
}

func TestExport_TableNamedLikeASlawFileIsATable(t *testing.T) {
	root := newWS(t)
	_, _, err := runSlaw("export", "s.slaw", "--workspace", root)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "bundles, not workspaces")
}

func TestExport_SQLMustBeOneSelect(t *testing.T) {
	root := seededWS(t)
	dst := filepath.Join(t.TempDir(), "o.csv")
	for _, q := range []string{
		"DROP TABLE people",
		"SELECT 1) TO '/tmp/should-not-exist.csv'; DROP TABLE people; COPY (SELECT 1",
		"SELECT 1; DROP TABLE people",
		"INSERT INTO people VALUES (9, 'x')",
	} {
		_, _, err := runSlaw("export", "--sql", q, "-o", dst, "--workspace", root)
		require.Error(t, err, q)
		_, _, err = runSlaw("export", "--sql", q, "--workspace", root)
		require.Error(t, err, "also without -o: %s", q)
	}
	assert.NoFileExists(t, dst)
	out, _, err := runSlaw("query", "--workspace", root, "--format", "csv", "SELECT count(*) FROM people")
	require.NoError(t, err)
	assert.Equal(t, "count_star()\n2\n", out, "the table survived")
}

func TestExport_DanglingSymlinkNeedsForce(t *testing.T) {
	root := seededWS(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.csv")
	link := filepath.Join(dir, "out.csv")
	require.NoError(t, os.Symlink(target, link))
	_, _, err := runSlaw("export", "people", "-o", link, "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	assert.NoFileExists(t, target)
}

func TestExportAndImport_ReportADownConnectionBeforeAMissingTable(t *testing.T) {
	root := newWS(t)
	addConnection(t, root, workspace.ConnectionDef{Name: "gone", Type: "duckdb", Path: "data/gone.duckdb"})
	csvPath := writeFile(t, t.TempDir(), "t.csv", "id\n1\n")
	_, _, err := runSlaw("export", "gone.t", "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection gone is not available")
	_, _, err = runSlaw("import", csvPath, "--into", "gone.t", "--workspace", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection gone is not available")
}

// A second process that really holds the DuckDB file: the advice must be
// accurate, not a dead end.
func TestHeldWorkspace_AdviceIsAccurate(t *testing.T) {
	root, cfg := newCmdWorkspace(t)
	_, _, err := runSlaw("query", "--workspace", root, "CREATE TABLE t AS SELECT 1 AS id")
	require.NoError(t, err)

	_, in, out, stderr := slawProc(t, cfg, "mcp", "serve", "--workspace", root)
	_, err = io.WriteString(in, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`+"\n")
	require.NoError(t, err)
	require.True(t, out.Scan(), stderr.String())

	// A command with a --read-only flag suggests it and says what it reaches.
	_, _, err = runSlaw("query", "--workspace", root, "SELECT 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--read-only")
	assert.Contains(t, err.Error(), "other than local DuckDB files")

	// import has no such flag, so it must not suggest one.
	csvPath := writeFile(t, t.TempDir(), "c.csv", "id\n1\n")
	_, _, err = runSlaw("import", csvPath, "--workspace", root)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--read-only")
	assert.Contains(t, err.Error(), "close it first")

	// --read-only against the held local file: plain words, not DuckDB's error.
	_, _, err = runSlaw("query", "--workspace", root, "--read-only", "SELECT * FROM t")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has open for writing")
	assert.NotContains(t, err.Error(), "Conflicting lock")
	assert.Contains(t, err.Error(), "slaw mcp serve")

	js, _, err := runSlaw("workspace", "info", "--workspace", root, "--json")
	require.NoError(t, err)
	var rep infoReport
	require.NoError(t, json.Unmarshal([]byte(js), &rep))
	require.NotEmpty(t, rep.Connections)
	assert.Equal(t, "locked-by", rep.Connections[0].Status)
	assert.NotContains(t, rep.Connections[0].Message, "Conflicting lock")

	// The TUI refuses to open on a locked default database instead of showing
	// an empty screen.
	_, _, err = runSlawIn(slowKeys("\x03"), "studio", "--read-only", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has open for writing")
}

func TestGuiTarget(t *testing.T) {
	root, err := filepath.EvalSymlinks(newWS(t))
	require.NoError(t, err)
	sub := filepath.Join(root, "notebooks")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	plain := t.TempDir()

	assert.Equal(t, root, guiTarget(sub), "a folder inside a workspace opens its root")
	assert.Equal(t, root, guiTarget(root))
	assert.Equal(t, plain, guiTarget(plain), "a plain folder passes through")
	b := filepath.Join(plain, "x.slaw")
	assert.Equal(t, b, guiTarget(b), "a bundle passes through")

	t.Chdir(sub)
	assert.Equal(t, root, guiTarget(""), "no dir uses the workspace around cwd")
	t.Chdir(plain)
	assert.Equal(t, "", guiTarget(""), "outside any workspace: launcher")
}

// workspace info and conn list use the same status words as Studio's rail
// (session.StatusKind): a connection that could not even be registered reads
// "error" in both, never "unavailable".
func TestConnStatusWords_InfoAndListAgree(t *testing.T) {
	root := newWS(t)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "broken", Type: "sqlite", Path: "${NO_SUCH_SLAW_VAR}/x.sqlite"}))
	require.NoError(t, ws.Close())

	js, _, err := runSlaw("workspace", "info", "--workspace", root, "--json")
	require.NoError(t, err)
	var rep infoReport
	require.NoError(t, json.Unmarshal([]byte(js), &rep))
	infoStatus := map[string]string{}
	for _, c := range rep.Connections {
		infoStatus[c.Name] = c.Status
	}

	lj, _, err := runSlaw("conn", "list", "--json", "--workspace", root)
	require.NoError(t, err)
	var rows []connRow
	require.NoError(t, json.Unmarshal([]byte(lj), &rows))
	require.NotEmpty(t, rows)
	for _, r := range rows {
		assert.Equal(t, infoStatus[r.Name], r.Status, r.Name)
		switch session.StatusKind(r.Status) {
		case session.StatusOK, session.StatusNotConnected, session.StatusLockedBy, session.StatusError, session.StatusNeedsCredentials, session.StatusExtensionMissing:
		default:
			t.Errorf("%s: %q is not one of the status words Studio uses", r.Name, r.Status)
		}
	}
	assert.Equal(t, "error", infoStatus["broken"])
	assert.NotEqual(t, "unavailable", infoStatus["broken"])
}

func TestQuery_SavedWithParams(t *testing.T) {
	root := newWS(t)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	require.NoError(t, ws.Queries().Create(context.Background(), &queries.Query{
		ID: "over", Title: "Over", SQL: "SELECT i, $label AS label FROM range(10) t(i) WHERE i >= $min_n",
		Parameters: []queries.Param{{Name: "min_n", Type: "integer", Default: "8"}, {Name: "label", Type: "text", Default: "dflt"}},
	}))
	require.NoError(t, ws.Close())

	run := func(args ...string) (string, error) {
		out, _, err := runSlaw(append([]string{"query", "--workspace", root, "--format", "csv"}, args...)...)
		return out, err
	}
	out, err := run("--saved", "over")
	require.NoError(t, err)
	assert.Equal(t, "i,label\n8,dflt\n9,dflt\n", out, "declared defaults")

	// A value is bound, never written into the SQL.
	out, err = run("--saved", "over", "-p", "min_n=9", "-p", "label=x'; DROP TABLE t; --")
	require.NoError(t, err)
	assert.Equal(t, "i,label\n9,x'; DROP TABLE t; --\n", out)

	_, err = run("--saved", "over", "-p", "min_n=nine")
	require.ErrorContains(t, err, "whole number")
	_, err = run("--saved", "over", "-p", "other=1")
	require.ErrorContains(t, err, "no parameter named")
	_, err = run("--saved", "missing")
	require.ErrorContains(t, err, "no saved query")
	_, err = run("-p", "min_n=1", "SELECT 1")
	require.ErrorContains(t, err, "--saved")
	_, err = run("--saved", "over", "SELECT 1")
	require.ErrorContains(t, err, "--saved runs the saved query")
	_, err = run("SELECT $x")
	require.Error(t, err, "ad-hoc SQL with $name is still the binder's error")
}

func TestQuery_SavedShapeIsCheckedLikeTheAgentDoes(t *testing.T) {
	root := newWS(t)
	ws, err := workspace.Open(root, workspace.WithLogger(testCmdLogger()))
	require.NoError(t, err)
	decl := []queries.Param{{Name: "q", Type: "text", Default: "SELECT 1"}}
	cases := map[string]string{
		"reader":   "SELECT * FROM query($q)",
		"literal":  "SELECT '$q' AS s, $q",
		"dollar":   "SELECT $pé$; CREATE SECRET pwn (TYPE s3); --$pé$",
		"question": "SELECT ?, $q",
		"position": "SELECT $1, $q",
	}
	for id, sqlText := range cases {
		require.NoError(t, ws.Queries().Create(context.Background(), &queries.Query{ID: id, Title: id, SQL: sqlText, Parameters: decl}))
	}
	require.NoError(t, ws.Close())
	for id := range cases {
		_, _, err := runSlaw("query", "--workspace", root, "--saved", id)
		require.Error(t, err, id)
		assert.NotContains(t, err.Error(), "failed to execute", id, "refused before the engine ran it")
	}
}

func TestRootHelp_ListsSlawRepl(t *testing.T) {
	out, _, err := runSlaw("--help")
	require.NoError(t, err)
	assert.Regexp(t, `(?m)^\s+repl\s`, out)
	assert.False(t, replCmd.Hidden)
	help, _, err := runSlaw("repl", "--help")
	require.NoError(t, err)
	assert.Contains(t, help, "--connection")
	assert.Contains(t, help, "Console tab")
}

// useMemoryKeychain points the CLI's credential store at process memory for
// the test, so no test ever writes the person's (or CI's) OS keychain.
func useMemoryKeychain(t *testing.T) {
	t.Helper()
	m := secrets.NewMemoryStore()
	old := newCLIKeychain
	newCLIKeychain = func() secrets.Store { return m }
	t.Cleanup(func() { newCLIKeychain = old })
}

// TestMain keeps every test in this package off the OS keychain, including a
// test that never calls newWS: credentials and the LLM key go to process memory.
func TestMain(m *testing.M) {
	cli, llm := secrets.NewMemoryStore(), secrets.NewMemoryStore()
	newCLIKeychain = func() secrets.Store { return cli }
	newLLMKeyStore = func() secrets.Store { return llm }
	os.Exit(m.Run())
}
