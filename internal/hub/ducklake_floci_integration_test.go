//go:build unix && integration

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/miniotest"
	"github.com/SandwichLabs/slaw/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the end-to-end proof of #118 for the case it was opened for: a
// SQLite DuckLake catalog and its Parquet files in an S3-compatible store with
// no TLS and path-style URLs, tables in a schema other than main, the storage
// credential in the keychain, the lake read-only.

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// flociWorkspace writes the two connections the way a person adds them (own
// scope, through the workspace API) and returns the workspace root and the
// config dir. signIn "" stores the floci credential in the (in-memory)
// keychain of r; "env" binds it to FLOCI_* variables instead; "aws-sso" signs in
// with the profile "dev" (r must be a workspace.NewResolver with a fake portal).
func flociWorkspace(t *testing.T, ep, signIn string, r *secrets.Resolver) (root, cfg string) {
	t.Helper()
	cfg = t.TempDir()
	t.Setenv("SLAW_CONFIG_DIR", cfg)
	res, err := workspace.Init(filepath.Join(t.TempDir(), "acme"), workspace.WithInitLogger(quietLogger()))
	require.NoError(t, err)
	require.NoError(t, res.Workspace.Close())
	opts := []workspace.Option{workspace.WithLogger(quietLogger()), workspace.WithConfigDir(cfg),
		workspace.WithUserConfig(workspace.UserConfig{Version: workspace.ConfigVersion})}
	if r != nil {
		opts = append(opts, workspace.WithResolver(r))
	}
	ws, err := workspace.Open(res.Workspace.Root(), opts...)
	require.NoError(t, err)
	defer ws.Close() //nolint:errcheck
	bucket := miniotest.Bucket
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "floci", Type: "s3", Scope: "s3://" + bucket + "/", Region: "us-east-1",
		Options: map[string]string{"endpoint": ep, "url_style": "path", "use_ssl": "false"}}))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{
		Name: "lake", Type: "ducklake", Path: "s3://" + bucket + "/warehouse/ducklake/catalog.sqlite", ReadOnly: workspace.Bool(true),
		Options: map[string]string{"catalog": "sqlite", "data_path": "s3://" + bucket + "/warehouse/ducklake/"}}))
	if signIn == "env" {
		require.NoError(t, ws.SetSignIn("floci", "env:FLOCI"))
	} else if signIn == "aws-sso" {
		// Nothing is stored: the profile is resolved when it is used.
		require.NoError(t, ws.SetSignIn("floci", "aws-sso:dev"))
	} else {
		require.NoError(t, ws.SetCredentials(context.Background(), "floci", secrets.Credential{KeyID: miniotest.KeyID, Secret: miniotest.Secret}, false))
	}
	return res.Workspace.Root(), cfg
}

func rowCount(t *testing.T, resp *agents.Response) any {
	t.Helper()
	require.Nil(t, resp.Error, fmt.Sprint(resp.Error))
	rows := rowsOfResp(t, resp)
	require.Len(t, rows, 1)
	return rows[0][0]
}

func rowsOfResp(t *testing.T, r *agents.Response) [][]any {
	t.Helper()
	m, ok := r.Result.(map[string]any)
	require.True(t, ok, "%v", r.Result)
	rows, ok := m["rows"].([][]any)
	require.True(t, ok, "%v", m)
	return rows
}

func agentCall(h *agents.Handler, q string) *agents.Response {
	return h.Handle(context.Background(), &agents.Request{JSONRPC: "2.0", ID: "1", Method: "run_query", Params: map[string]any{"sql": q}})
}

func statusLine(s session.Session, name string) session.Status {
	for _, st := range s.Connections() {
		if st.Name == name {
			return st
		}
	}
	return session.Status{}
}

// noNeedle fails when the MinIO secret is in any file under dirs.
func noNeedle(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		_ = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Size() > 64<<20 {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil {
				assert.NotContains(t, string(b), miniotest.Secret, "credential written to %s", p)
			}
			return nil
		})
	}
}

// The workspace is opened through hub.Open (the path the CLI, Studio and MCP
// share) with floci never connected by hand: the lake is read first, then
// through the agent tool surface, then floci loses its credential.
func TestFlociLakeEndToEnd(t *testing.T) {
	ep := miniotest.Start(t)
	miniotest.BuildLake(t, ep)
	miniotest.UseExtensionCache(t)
	ctx := context.Background()

	red := secrets.NewRedactor()
	r := secrets.NewResolver(secrets.WithKeychain(secrets.NewMemoryStore()), secrets.WithRedactor(red))
	root, cfg := flociWorkspace(t, ep, "", r)

	logs := &lockedBuf{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	open := func() *Opened {
		o, err := Open(ctx, root, OpenOptions{App: "test", Logger: logger, Resolver: r, Redactor: red})
		require.NoError(t, err)
		return o
	}
	o := open()
	defer o.Close()
	require.Empty(t, o.Issues)
	want := fmt.Sprint(miniotest.Students)
	var seen []string // everything an error or result said, for the leak audit

	// 1. The engine path (the CLI): the lake is read before floci is touched.
	// (Preflight makes floci's secret at open, which dials nothing; the lake is
	// still the first connection anything connects.)
	t.Logf("at open: floci=%s lake=%s", statusLine(o.Sess, "floci").Kind, statusLine(o.Sess, "lake").Kind)
	assert.NotEqual(t, session.StatusOK, statusLine(o.Sess, "lake").Kind, "the lake is not attached until first use")
	rs, err := o.Eng.Execute(ctx, "SELECT count(*) FROM lake.warehouse.dim_students", nil)
	require.NoError(t, err)
	assert.Equal(t, want, fmt.Sprint(rs.Rows[0][0]))
	assert.Equal(t, session.StatusOK, statusLine(o.Sess, "lake").Kind)

	// 2. The agent tool surface (what MCP serves), read-only tier.
	h, _, err := NewAgentHandler(o.WS, o.Sess, "", logger)
	require.NoError(t, err)
	assert.Equal(t, want, fmt.Sprint(rowCount(t, agentCall(h, "SELECT count(*) FROM lake.warehouse.dim_students"))))
	for _, q := range []string{
		"INSERT INTO lake.warehouse.dim_students VALUES (1, 'x', 1)",
		"DELETE FROM lake.warehouse.dim_students",
		"CREATE TABLE lake.warehouse.t AS SELECT 1",
		"SELECT * FROM __ducklake_metadata_lake.main.ducklake_data_file",
		"SELECT * FROM sqlite_scan('s3://oib-data-local/warehouse/ducklake/catalog.sqlite', 'ducklake_table')",
		"CALL ducklake_flush_inlined_data('lake')",
	} {
		resp := agentCall(h, q)
		require.NotNil(t, resp.Error, q)
		seen = append(seen, resp.Error.Message)
	}
	meta := agentCall(h, "SELECT * FROM __ducklake_metadata_lake.main.ducklake_data_file")
	require.NotNil(t, meta.Error)
	assert.Equal(t, agents.ErrPermission, meta.Error.Code, meta.Error.Message)
	// The data is untouched.
	assert.Equal(t, want, fmt.Sprint(rowCount(t, agentCall(h, "SELECT count(*) FROM lake.warehouse.dim_students"))))

	// 3. floci loses its credential: the next open says what the lake waits on.
	require.NoError(t, o.WS.ClearCredentials(ctx, "floci"))
	o.Close()
	o2 := open()
	defer o2.Close()
	err = o2.Sess.Connect(ctx, "lake")
	require.Error(t, err)
	st := statusLine(o2.Sess, "lake")
	assert.Equal(t, session.StatusNeedsCredentials, st.Kind)
	assert.Equal(t, `needs the "floci" connection: it needs credentials`, st.Message)
	seen = append(seen, err.Error())
	_, err = o2.Eng.Execute(ctx, "SELECT count(*) FROM lake.warehouse.dim_students", nil)
	require.Error(t, err)
	seen = append(seen, err.Error())
	assert.Contains(t, err.Error(), `needs the "floci" connection`)
	assert.NotContains(t, err.Error(), "S3 error")

	// 4. Credential leak audit.
	for _, st := range o2.Sess.Connections() {
		seen = append(seen, st.Message)
	}
	b, _ := json.Marshal(seen)
	assert.NotContains(t, string(b), miniotest.Secret)
	assert.NotContains(t, logs.String(), miniotest.Secret)
	assert.Contains(t, logs.String(), "level=DEBUG", "the audit ran with debug logging on")
	noNeedle(t, root, cfg)
}

// `slaw mcp serve` over stdio: floci signs in from the environment (nothing is
// stored), and a client reads the lake through run_query, read-only.
func TestFlociLakeOverMCPServe(t *testing.T) {
	ep := miniotest.Start(t)
	miniotest.BuildLake(t, ep)
	miniotest.UseExtensionCache(t)
	root, cfg := flociWorkspace(t, ep, "env", nil)
	t.Setenv("FLOCI_KEY_ID", miniotest.KeyID)
	t.Setenv("FLOCI_SECRET", miniotest.Secret)

	run := startServe(t, ServeOptions{Workspace: root})
	run.handshake()
	text, isErr := run.tool("run_query", map[string]any{"sql": "SELECT count(*) AS n FROM lake.warehouse.dim_students"})
	require.False(t, isErr, text)
	assert.Contains(t, text, fmt.Sprint(miniotest.Students))
	all := []string{text}

	for _, q := range []string{
		"INSERT INTO lake.warehouse.dim_students VALUES (1, 'x', 1)",
		"SELECT * FROM __ducklake_metadata_lake.main.ducklake_data_file",
	} {
		text, isErr = run.tool("run_query", map[string]any{"sql": q})
		assert.True(t, isErr, q)
		all = append(all, text)
	}
	text, isErr = run.tool("run_query", map[string]any{"sql": "SELECT count(*) AS n FROM lake.warehouse.dim_students"})
	require.False(t, isErr, text)
	assert.Contains(t, text, fmt.Sprint(miniotest.Students))

	for _, s := range append(all, run.stderr.String()) {
		assert.False(t, strings.Contains(s, miniotest.Secret), "the secret reached the MCP client or stderr")
	}
	noNeedle(t, root, cfg)
}
