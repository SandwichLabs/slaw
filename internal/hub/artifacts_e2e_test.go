//go:build unix

package hub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/bundle"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Data artifacts end to end (epic #162, step 10): hub.Open, the agent tool
// surface, the UI run path (Handler.RunArtifactQuery), files edited by hand,
// a .slaw bundle round trip, and the credential audit.

const e2eView = `{"title":"E2E","components":[{"id":"t","type":"table","query":"rows"}]}`

// e2eSQL touches every parameter type. Date and number are cast where they are
// used in an expression (docs/reference/artifacts.md, "Parameter types").
const e2eSQL = `SELECT id, status, $t AS t, $n::DOUBLE AS n, $i::BIGINT AS i, $d::DATE AS d, $b::BOOLEAN AS b, $e AS e
FROM orders WHERE id >= $i AND ($t = '' OR status = $t) AND $d::DATE <= DATE '2100-01-01' ORDER BY id`

func e2eManifest(sqlText string) map[string]any {
	return map[string]any{
		"title": "E2E",
		"parameters": []any{
			map[string]any{"name": "t", "type": "text", "default": ""},
			map[string]any{"name": "n", "type": "number", "default": float64(1.5), "min": float64(0), "max": float64(100)},
			map[string]any{"name": "i", "type": "integer", "default": float64(0), "min": float64(0)},
			map[string]any{"name": "d", "type": "date", "default": "2026-01-01"},
			map[string]any{"name": "b", "type": "boolean", "default": false},
			map[string]any{"name": "e", "type": "enum", "options": []any{"a", "b"}, "default": "a"},
		},
		"queries": []any{map[string]any{"name": "rows", "sql": sqlText}},
	}
}

// e2eWorld is an opened workspace with data in `local`.
type e2eWorld struct {
	t    *testing.T
	o    *Opened
	root string
	logs *bytes.Buffer
}

func newE2E(t *testing.T) *e2eWorld {
	t.Helper()
	ws := newWorkspace(t)
	root := ws.Root()
	require.NoError(t, ws.Close())
	seedLocalDB(t, root)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o, err := Open(t.Context(), root, OpenOptions{App: "test", Logger: logger})
	require.NoError(t, err)
	t.Cleanup(o.Close)
	return &e2eWorld{t: t, o: o, root: root, logs: logs}
}

// handler is the agent tool handler at exactly mode (NewAgentHandler can only
// tighten the workspace ceiling, so a test builds the same thing by hand to
// reach every tier). Nobody is there to approve, so approval is off.
func (w *e2eWorld) handler(mode agents.SandboxMode) *agents.Handler {
	w.t.Helper()
	logger := slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &agents.SandboxConfig{Mode: mode, MaxQueryRows: 10000}
	return agents.NewHandler(workspace.NewAgentAccess(w.o.WS, w.o.Sess), engine.NewManager(w.o.Sess, engine.WithLogger(logger)), cfg, logger)
}

func call(h *agents.Handler, method string, params map[string]any) *agents.Response {
	return h.Handle(context.Background(), &agents.Request{JSONRPC: "2.0", ID: "1", Method: method, Params: params})
}

func (w *e2eWorld) write(h *agents.Handler, name, sqlText string) {
	w.t.Helper()
	var view map[string]any
	require.NoError(w.t, json.Unmarshal([]byte(e2eView), &view))
	resp := call(h, "write_artifact", map[string]any{"name": name, "manifest": e2eManifest(sqlText), "view": view})
	require.Nil(w.t, resp.Error, "%v", resp.Error)
}

// editOnDisk replaces the artifact's SQL in artifact.yaml the way a person,
// git or a bundle could, bypassing every save-time check.
func (w *e2eWorld) editOnDisk(name, newSQL string) {
	w.t.Helper()
	p := filepath.Join(w.root, "artifacts", name, "artifact.yaml")
	b, err := os.ReadFile(p)
	require.NoError(w.t, err)
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "sql:") {
			lines[i] = "    sql: " + yamlQuote(newSQL)
			// A block scalar's continuation lines are dropped.
			j := i + 1
			for j < len(lines) && strings.HasPrefix(lines[j], "      ") {
				lines = append(lines[:j], lines[j+1:]...)
			}
		}
	}
	require.NoError(w.t, os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644))
}

func yamlQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func (w *e2eWorld) run(h *agents.Handler, name string, values map[string]any) (*engine.ResultSet, error) {
	w.t.Helper()
	rs, _, err := h.RunArtifactQuery(context.Background(), name, "rows", values)
	return rs, err
}

var allTiers = []agents.SandboxMode{agents.ModeReadOnly, agents.ModeReadWrite, agents.ModeFull}

func TestArtifactsEndToEnd_EveryParameterType(t *testing.T) {
	w := newE2E(t)
	w.write(w.handler(agents.ModeReadWrite), "e2e", e2eSQL)

	for _, tier := range allTiers {
		h := w.handler(tier)
		rs, err := w.run(h, "e2e", nil)
		require.NoError(t, err, string(tier))
		assert.Len(t, rs.Rows, 3, "defaults: all three orders (%s)", tier)

		rs, err = w.run(h, "e2e", map[string]any{
			"t": "Open", "n": float64(2.25), "i": float64(2), "d": "2026-03-04", "b": true, "e": "b",
		})
		require.NoError(t, err, string(tier))
		require.Len(t, rs.Rows, 1, "id >= 2 and status = Open leaves order 3")
		row := map[string]any{}
		for i, c := range rs.Columns {
			row[c.Name] = rs.Rows[0][i]
		}
		assert.EqualValues(t, 3, row["id"])
		assert.Equal(t, "Open", row["t"])
		assert.EqualValues(t, 2.25, row["n"])
		assert.EqualValues(t, 2, row["i"])
		assert.Equal(t, true, row["b"])
		assert.Equal(t, "b", row["e"])
		assert.Contains(t, toString(row["d"]), "2026-03-04")
	}
}

func toString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestArtifactsEndToEnd_HostileValues(t *testing.T) {
	w := newE2E(t)
	h := w.handler(agents.ModeFull)
	w.write(h, "e2e", e2eSQL)

	// Text that would be SQL is only ever a value: it matches nothing and
	// nothing is dropped.
	for _, hostile := range []string{"'; DROP TABLE orders; --", "x' OR '1'='1", "$$; DELETE FROM orders; $$", "${HOME}", "Open'; SELECT 1 --"} {
		rs, err := w.run(h, "e2e", map[string]any{"t": hostile})
		require.NoError(t, err, hostile)
		assert.Empty(t, rs.Rows, hostile)
	}
	rs, err := w.run(h, "e2e", nil)
	require.NoError(t, err)
	assert.Len(t, rs.Rows, 3, "the table is intact")

	refused := []map[string]any{
		{"d": "2026-02-30"},                     // not a date
		{"d": "2026-01-01'; DROP TABLE orders"}, // not a date
		{"d": "today"},                          // a default, not a value
		{"e": "c"},                              // outside the enum
		{"e": "a' OR '1'='1"},                   // outside the enum
		{"i": "1; DROP TABLE orders"},           // not an integer
		{"n": float64(1000)},                    // past max
		{"b": "yes"},                            // not a boolean
		{"nope": "x"},                           // not a parameter
	}
	for _, v := range refused {
		_, err := w.run(h, "e2e", v)
		assert.Error(t, err, "%v must be refused", v)
	}
	rs, err = w.run(h, "e2e", nil)
	require.NoError(t, err)
	assert.Len(t, rs.Rows, 3, "the table is intact after the refusals")
}

func TestArtifactsEndToEnd_EditedOnDiskIsRefusedAtRunInEveryTier(t *testing.T) {
	w := newE2E(t)
	w.write(w.handler(agents.ModeReadWrite), "e2e", e2eSQL)
	outFile := filepath.Join(t.TempDir(), "copied.csv")

	evil := []string{
		"DELETE FROM orders",
		"INSERT INTO orders VALUES (9, 'Hacked')",
		"UPDATE orders SET status = 'Hacked'",
		"DROP TABLE orders",
		"CREATE TABLE pwned AS SELECT 1",
		"SELECT * FROM read_text('/etc/passwd')",
		"SELECT * FROM read_csv('/etc/passwd')",
		"SELECT * FROM read_text($t)",
		"SELECT content FROM read_text('/etc/hostname')",
		"COPY orders TO '" + outFile + "'",
		"SELECT 1; DELETE FROM orders",
		"ATTACH '/etc/passwd' AS x",
		"INSTALL httpfs",
		"SELECT * FROM duckdb_secrets()",
	}
	for _, sqlText := range evil {
		w.editOnDisk("e2e", sqlText)
		for _, tier := range allTiers {
			rs, err := w.run(w.handler(tier), "e2e", nil)
			require.Error(t, err, "%q must be refused in %s", sqlText, tier)
			assertRefusal(t, err, sqlText)
			assert.Nil(t, rs, sqlText)
			assert.NotContains(t, err.Error(), "root:", "no file content in the error")
			assert.NotContains(t, err.Error(), w.root, "no workspace path in the error: %q", sqlText)
		}
	}
	// Nothing ran: the data is as seeded and nothing was written.
	h := w.handler(agents.ModeFull)
	resp := call(h, "run_query", map[string]any{"sql": "SELECT count(*) FROM orders WHERE status = 'Hacked'"})
	require.Nil(t, resp.Error)
	assert.Equal(t, [][]any{{int64(0)}}, e2eRows(t, resp))
	resp = call(h, "run_query", map[string]any{"sql": "SELECT count(*) FROM orders"})
	require.Nil(t, resp.Error)
	assert.Equal(t, [][]any{{int64(3)}}, e2eRows(t, resp))
	assert.NoFileExists(t, outFile)
	// The reason is in the debug log, never in what the caller sees.
	assert.Contains(t, w.logs.String(), "artifact query refused")
}

func TestArtifactsEndToEnd_BundleRoundTripRuns(t *testing.T) {
	w := newE2E(t)
	h := w.handler(agents.ModeReadWrite)
	w.write(h, "e2e", e2eSQL)

	res, err := bundle.Export(t.Context(), w.o.WS, bundle.ExportOptions{Selection: bundle.Selection{Artifacts: []string{"e2e"}}})
	require.NoError(t, err)
	pkg, err := bundle.Open(res.Data, "")
	require.NoError(t, err)

	// A fresh workspace, opened through hub.Open.
	ws2 := newWorkspace(t)
	root2 := ws2.Root()
	require.NoError(t, ws2.Close())
	seedLocalDB(t, root2)
	o2, err := Open(t.Context(), root2, OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	defer o2.Close()
	out, err := pkg.Import(t.Context(), o2.WS, bundle.ImportOptions{})
	require.NoError(t, err)
	require.Zero(t, out.Failed(), "%+v", out)

	w2 := &e2eWorld{t: t, o: o2, root: root2, logs: &bytes.Buffer{}}
	rs, err := w2.run(w2.handler(agents.ModeReadOnly), "e2e", map[string]any{"i": float64(2)})
	require.NoError(t, err)
	assert.Len(t, rs.Rows, 2)

	// A bundle carrying a hand-edited write is refused at run on the far side.
	w.editOnDisk("e2e", "DELETE FROM orders")
	res, err = bundle.Export(t.Context(), w.o.WS, bundle.ExportOptions{Selection: bundle.Selection{Artifacts: []string{"e2e"}}})
	require.NoError(t, err, "export is structural only")
	pkg, err = bundle.Open(res.Data, "")
	require.NoError(t, err)
	_, err = pkg.Import(t.Context(), o2.WS, bundle.ImportOptions{OnConflict: bundle.ActionRename})
	require.NoError(t, err)
	names := artifactNames(t, o2)
	require.Len(t, names, 2)
	for _, n := range names {
		if n == "e2e" {
			continue
		}
		_, err := w2.run(w2.handler(agents.ModeFull), n, nil)
		require.ErrorIs(t, err, agents.ErrReadOnlyBindingDenied, "the imported write is refused at run")
	}
	resp := call(w2.handler(agents.ModeFull), "run_query", map[string]any{"sql": "SELECT count(*) FROM orders"})
	require.Nil(t, resp.Error)
	assert.Equal(t, [][]any{{int64(3)}}, e2eRows(t, resp))
}

func artifactNames(t *testing.T, o *Opened) []string {
	t.Helper()
	list, err := o.WS.Artifacts().List(t.Context())
	require.NoError(t, err)
	var out []string
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

func e2eRows(t *testing.T, r *agents.Response) [][]any {
	t.Helper()
	m, ok := r.Result.(map[string]any)
	require.True(t, ok, "%v", r.Result)
	rows, ok := m["rows"].([][]any)
	require.True(t, ok, "%v", m)
	return rows
}

// A person's setting decides the tier a headless MCP server runs at, so this
// reaches all three through the real policy path (hub.Serve), not by a flag.
func setTier(t *testing.T, ws workspace.Workspace, tier string) {
	t.Helper()
	local := ws.Local()
	local.Agent = workspace.LocalAgent{Sandbox: tier, AskBeforeWrites: workspace.Bool(false)}
	require.NoError(t, ws.SaveLocal(local))
}

func TestArtifactsEndToEnd_OverMCPInEveryTier(t *testing.T) {
	for _, tier := range []string{workspace.SandboxReadOnly, workspace.SandboxReadWrite, workspace.SandboxFull} {
		t.Run(tier, func(t *testing.T) {
			ws := newWorkspace(t)
			seedLocalDB(t, ws.Root())
			setTier(t, ws, tier)
			writable := tier != workspace.SandboxReadOnly

			run := startServe(t, ServeOptions{Workspace: ws.Root()})
			run.handshake()
			list, ok := run.call("tools/list", nil)["result"].(map[string]any)
			require.True(t, ok)
			tools, _ := list["tools"].([]any)
			var names []string
			for _, tl := range tools {
				m, _ := tl.(map[string]any)
				names = append(names, toString(m["name"]))
			}
			// tools/list follows the tier (#221); the call below is still gated, so
			// a client that tries the unlisted tool anyway gets the tier refusal.
			if writable {
				assert.Contains(t, names, `"write_artifact"`)
			} else {
				assert.NotContains(t, names, `"write_artifact"`)
			}
			assert.Contains(t, names, `"get_artifact"`)
			for _, n := range names {
				assert.NotContains(t, n, "run_artifact", "MCP has no way to run an artifact query")
				assert.NotRegexp(t, `(?i)allow|deny|revoke|trust`, n, "MCP has no way to allow a custom view")
			}

			// A write statement is refused at save in every tier.
			text, isErr := run.tool("write_artifact", map[string]any{"name": "evil", "manifest": e2eManifest("DELETE FROM orders"), "view": viewMapOf(t)})
			assertSaveRefusal(t, tier, text, isErr)
			text, isErr = run.tool("write_artifact", map[string]any{"name": "evil", "manifest": e2eManifest("SELECT * FROM read_text('/etc/passwd')"), "view": viewMapOf(t)})
			assertSaveRefusal(t, tier, text, isErr)
			assert.NoDirExists(t, filepath.Join(ws.Root(), "artifacts", "evil"))

			text, isErr = run.tool("write_artifact", map[string]any{"name": "good", "manifest": e2eManifest(e2eSQL), "view": viewMapOf(t)})
			if !writable {
				assert.True(t, isErr, "the read-only tier cannot author: %s", text)
				require.NoError(t, run.finish(t))
				return
			}
			require.False(t, isErr, text)

			// What a person or git did meanwhile: the definition is readable as
			// data, and no tool returns results or file content.
			p := filepath.Join(ws.Root(), "artifacts", "good", "artifact.yaml")
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), "FROM orders", "FROM read_text('/etc/passwd')", 1)), 0o644))
			for _, tool := range []string{"get_artifact", "list_artifacts"} {
				text, isErr = run.tool(tool, map[string]any{"name": "good"})
				require.False(t, isErr, text)
				assert.NotContains(t, text, "root:")
				assert.NotContains(t, text, "Void", "no artifact tool returns rows")
			}
			// Authoring over it with the stale rev is a conflict, not an overwrite.
			text, isErr = run.tool("write_artifact", map[string]any{"name": "good", "manifest": e2eManifest(e2eSQL), "view": viewMapOf(t)})
			assert.True(t, isErr, "a write over an existing artifact needs its rev: %s", text)
			require.NoError(t, run.finish(t))
		})
	}
}

func viewMapOf(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(e2eView), &v))
	return v
}

const (
	needleKeychain = "NEEDLE-keychain-pw-7f3a91"
	needleDotEnv   = "NEEDLE-dotenv-val-c20d44"
)

// The credential audit (as #118 step 7): a connection password and an .env
// value must not appear in an artifact error, get_artifact, list_artifacts, a
// log line or a bundle. The postgres connection's server is changed after its
// password was saved, so using it fails before anything is dialled.
func TestArtifactsEndToEnd_NoCredentialEverAppears(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("SLAW_CONFIG_DIR", cfg)
	red := secrets.NewRedactor()
	r := secrets.NewResolver(secrets.WithKeychain(secrets.NewMemoryStore()), secrets.WithRedactor(red), secrets.WithEnv(func(string) string { return "" }))
	res, err := workspace.Init(filepath.Join(t.TempDir(), "acme"), workspace.WithInitLogger(quietLogger()))
	require.NoError(t, err)
	require.NoError(t, res.Workspace.Close())
	root := res.Workspace.Root()
	seedLocalDB(t, root)

	ws, err := workspace.Open(root, workspace.WithLogger(quietLogger()), workspace.WithConfigDir(cfg),
		workspace.WithUserConfig(workspace.UserConfig{Version: workspace.ConfigVersion}), workspace.WithResolver(r))
	require.NoError(t, err)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "prod_pg", Type: "postgres", Host: "db.internal", Port: 5432, Database: "prod"}))
	require.NoError(t, ws.SetCredentials(t.Context(), "prod_pg", secrets.Credential{User: "svc", Password: needleKeychain}, false))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "env_pg", Type: "postgres", Host: "db.internal", Port: 5432, Database: "prod"}))
	require.NoError(t, ws.SetSignIn("env_pg", "env:ENVPG"))
	// An allowed .env: its secret-looking value joins the redactor, and no
	// ENVPG_* variable is in it, so env_pg still has no credential.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("OTHER_API_TOKEN="+needleDotEnv+"\nNOTE_TEXT="+needleDotEnv+"\n"), 0o600))
	require.NoError(t, ws.AllowDotEnv())
	require.NoError(t, ws.Close())
	// The server behind prod_pg changed (a pull, a hand edit): the saved
	// password is no longer usable for it.
	locals, err := filepath.Glob(filepath.Join(cfg, "workspaces", "*", workspace.LocalFile))
	require.NoError(t, err)
	require.Len(t, locals, 1)
	b, err := os.ReadFile(locals[0])
	require.NoError(t, err)
	require.Contains(t, string(b), "db.internal")
	require.NoError(t, os.WriteFile(locals[0], []byte(strings.ReplaceAll(string(b), "db.internal", "other.internal")), 0o600))

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o, err := Open(t.Context(), root, OpenOptions{App: "test", Logger: logger, Resolver: r, Redactor: red})
	require.NoError(t, err)
	defer o.Close()
	w := &e2eWorld{t: t, o: o, root: root, logs: logs}
	h := w.handler(agents.ModeReadWrite)

	var seen []string
	// Artifacts over each connection, and one that tries to print a secret.
	for _, c := range []struct{ name, sqlText, conn string }{
		{"pg", "SELECT 1 AS x", "prod_pg"},
		{"envpg", "SELECT 1 AS x", "env_pg"},
		{"peek", "SELECT * FROM duckdb_secrets()", ""},
		{"peek_env", "SELECT getenv('OTHER_API_TOKEN') AS v", ""},
	} {
		m := e2eManifest(c.sqlText)
		if c.conn != "" {
			m["queries"] = []any{map[string]any{"name": "rows", "sql": c.sqlText, "connection": c.conn}}
		}
		resp := call(h, "write_artifact", map[string]any{"name": c.name, "manifest": m, "view": viewMapOf(t)})
		if resp.Error != nil {
			seen = append(seen, resp.Error.Message) // refused at save: the message is audited too
			continue
		}
		_, err := w.run(h, c.name, nil)
		require.Error(t, err, c.name)
		seen = append(seen, err.Error())
	}
	// A hand-edited artifact too (never vetted at save).
	resp := call(h, "write_artifact", map[string]any{"name": "hand", "manifest": e2eManifest("SELECT 1 AS x"), "view": viewMapOf(t)})
	require.Nil(t, resp.Error, "%v", resp.Error)
	w.editOnDisk("hand", "SELECT * FROM read_text('/etc/passwd')")
	_, err = w.run(h, "hand", nil)
	require.ErrorIs(t, err, agents.ErrReadOnlyBindingDenied)
	seen = append(seen, err.Error())

	// ... and one hand-edited onto each credentialed connection.
	for name, conn := range map[string]string{"handpg": "prod_pg", "handenv": "env_pg"} {
		resp := call(h, "write_artifact", map[string]any{"name": name, "manifest": e2eManifest("SELECT 1 AS x"), "view": viewMapOf(t)})
		require.Nil(t, resp.Error, "%v", resp.Error)
		p := filepath.Join(root, "artifacts", name, "artifact.yaml")
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		edited := strings.Replace(string(b), "    sql:", "    connection: "+conn+"\n    sql:", 1)
		require.NotEqual(t, string(b), edited)
		require.NoError(t, os.WriteFile(p, []byte(edited), 0o644))
		_, err = w.run(h, name, nil)
		require.Error(t, err, name)
		seen = append(seen, err.Error())
	}

	seen = append(seen, toString(call(h, "list_artifacts", map[string]any{}).Result))
	for _, n := range []string{"hand", "handpg", "handenv"} {
		resp := call(h, "get_artifact", map[string]any{"name": n})
		require.Nil(t, resp.Error, "%s must exist to be audited: %v", n, resp.Error)
		seen = append(seen, toString(resp.Result), toString(resp.Error))
	}
	for _, st := range o.Sess.Connections() {
		seen = append(seen, st.Message)
	}

	// A bundle of everything, with the connection definitions.
	exp, err := bundle.Export(t.Context(), o.WS, bundle.ExportOptions{Selection: bundle.Selection{All: true, LocalConnections: []string{"prod_pg", "env_pg"}}})
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(exp.Data), int64(len(exp.Data)))
	require.NoError(t, err)
	entries := 0
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		seen = append(seen, string(body))
		entries++
	}
	require.NotZero(t, entries)
	seen = append(seen, string(exp.Data))

	all := strings.Join(seen, "\n")
	for _, needle := range []string{needleKeychain, needleDotEnv} {
		assert.NotContains(t, all, needle)
		assert.NotContains(t, logs.String(), needle, "a log line")
	}
	assert.Contains(t, logs.String(), "level=DEBUG", "the audit ran with debug logging on")
	assert.Contains(t, logs.String(), "denied", "the refusals were logged")
	// Nor on disk anywhere in the workspace or the config dir.
	noNeedleIn(t, []string{needleKeychain, needleDotEnv}, root, cfg)
}

// noNeedleIn fails when a needle is in any file under dirs, except the .env
// the person wrote it into.
func noNeedleIn(t *testing.T, needles []string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		_ = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Size() > 64<<20 || filepath.Base(p) == ".env" {
				return nil
			}
			body, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			for _, n := range needles {
				assert.NotContains(t, string(body), n, "credential written to %s", p)
			}
			return nil
		})
	}
}

// assertRefusal checks an edited statement was refused for the right reason:
// the fixed policy denial, or the lexer's own message.
func assertRefusal(t *testing.T, err error, sqlText string) {
	t.Helper()
	switch {
	case strings.Contains(sqlText, "; DELETE"):
		assert.ErrorContains(t, err, "exactly one statement", sqlText)
	case strings.Contains(sqlText, "read_text($t)"):
		assert.ErrorContains(t, err, "cannot be an argument", sqlText)
	default:
		assert.ErrorIs(t, err, agents.ErrReadOnlyBindingDenied, sqlText)
	}
}

// assertSaveRefusal: the read-only tier refuses write_artifact itself; the
// writable tiers refuse the statement through the read policy.
func assertSaveRefusal(t *testing.T, tier, text string, isErr bool) {
	t.Helper()
	require.True(t, isErr, text)
	if tier == workspace.SandboxReadOnly {
		assert.Contains(t, text, "not allowed in read-only mode")
		return
	}
	assert.Contains(t, text, "did not pass the agent's read policy")
}
