//go:build unix

package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

func countReceiptFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	require.NoError(t, filepath.WalkDir(filepath.Join(root, ".slaw-local", "receipts"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			n++
		}
		return err
	}))
	return n
}

// Every handler a workspace's process builds records to the workspace's one
// store: the agent's tools and the render-time reads go through
// NewAgentHandler, the person's editor, CLI and console through Workspace.Receipts.
func TestReceipts_EveryChokepointOfAnOpenWorkspaceMintsOneFile(t *testing.T) {
	ws := newWorkspace(t)
	o, err := Open(t.Context(), ws.Root(), OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	t.Cleanup(o.Close)
	require.NotNil(t, o.WS.Receipts(), "an open workspace has a recorder")
	root := o.WS.Root()
	assert.Zero(t, countReceiptFiles(t, root))

	h, _, err := NewAgentHandler(o.WS, o.Sess, "", quietLogger())
	require.NoError(t, err)

	// The agent path: run_query over a session.
	ctx := receipts.WithAgentSession(t.Context(), "mcp-1")
	resp := h.Handle(ctx, &agents.Request{JSONRPC: "2.0", ID: "1", Method: "run_query", Params: map[string]any{"sql": "SELECT 1 AS a"}})
	require.Nil(t, resp.Error)
	agentID, _ := resp.Result.(map[string]any)["receipt_id"].(string)
	require.NotEmpty(t, agentID)
	assert.Equal(t, 1, countReceiptFiles(t, root))

	// The render path (an A2UI component or notebook cell) and the preview.
	rs, _, err := h.RunReadOnlySQLIn(t.Context(), "", "SELECT 2 AS b", nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, rs.ReceiptID)
	assert.Equal(t, 2, countReceiptFiles(t, root))
	resp = h.Handle(ctx, &agents.Request{JSONRPC: "2.0", ID: "2", Method: "preview_query", Params: map[string]any{"sql": "SELECT 3 AS c"}})
	require.Nil(t, resp.Error)
	previewID, _ := resp.Result.(map[string]any)["receipt_id"].(string)
	require.NotEmpty(t, previewID)
	assert.Equal(t, 3, countReceiptFiles(t, root))

	// The person's path: the engine with an intent, finished by the caller.
	pctx := receipts.WithIntent(t.Context(), receipts.Intent{
		Surface: receipts.SurfaceCLIQuery, Actor: receipts.ActorPerson, SQL: "SELECT 4 AS d", Verdict: receipts.VerdictUnchecked,
	})
	prs, total, err := o.Eng.ExecuteCappedIn(pctx, "", "SELECT 4 AS d", nil, 10)
	require.NoError(t, err)
	personID, err := receipts.Finish(pctx, o.WS.Receipts(), prs.Rows, false, total)
	require.NoError(t, err)
	require.NotEmpty(t, personID)
	assert.Equal(t, 4, countReceiptFiles(t, root))

	// All of them are in the one store the recorder reads, and the agent's names
	// its session.
	got, err := o.WS.Receipts().Store().Get(agentID)
	require.NoError(t, err)
	assert.Equal(t, receipts.ActorAgent, got.Actor)
	assert.Equal(t, "mcp-1", got.AgentSession)
	got, err = o.WS.Receipts().Store().Get(rs.ReceiptID)
	require.NoError(t, err)
	assert.Equal(t, receipts.SurfaceRender, got.Surface)
	got, err = o.WS.Receipts().Store().Get(personID)
	require.NoError(t, err)
	assert.Equal(t, receipts.SurfaceCLIQuery, got.Surface)
	assert.Equal(t, receipts.VerdictUnchecked, got.Verdict)

	// The folder is machine-local: the key and the receipts sit under .slaw-local.
	info, err := os.Stat(filepath.Join(root, ".slaw-local", "receipts", receipts.KeyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// A run with no intent mints nothing: the person's catalog and the vet.
	_, _, err = o.Eng.ExecuteCappedIn(t.Context(), "", "SELECT 5 AS e", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, 4, countReceiptFiles(t, root))
}

// A recorder that cannot open (the receipts folder is a symbolic link) is nil:
// the run goes on without a receipt, never fails.
func TestReceipts_AnUnusableFolderMeansRunsWithoutReceipts(t *testing.T) {
	ws := newWorkspace(t)
	elsewhere := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws.Root(), ".slaw-local"), 0o755))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(ws.Root(), ".slaw-local", "receipts")))

	o, err := Open(t.Context(), ws.Root(), OpenOptions{App: "test", Logger: quietLogger()})
	require.NoError(t, err)
	t.Cleanup(o.Close)
	assert.Nil(t, o.WS.Receipts())

	h, _, err := NewAgentHandler(o.WS, o.Sess, "", quietLogger())
	require.NoError(t, err)
	resp := h.Handle(t.Context(), &agents.Request{JSONRPC: "2.0", ID: "1", Method: "run_query", Params: map[string]any{"sql": "SELECT 1 AS a"}})
	require.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	assert.NotContains(t, result, "receipt_id")
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written through the link")
}
