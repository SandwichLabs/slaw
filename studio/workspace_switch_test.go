package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SandwichLabs/slaw/internal/hub"
	"github.com/SandwichLabs/slaw/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertAlphaBack checks the open workspace is Alpha again: usable, holding
// its hub lock and still releasable.
func assertAlphaBack(t *testing.T, app *App, root string) {
	t.Helper()
	info, err := app.GetWorkspaceInfo()
	require.NoError(t, err)
	assert.Equal(t, "Alpha", info.Name)
	assert.False(t, info.ReadOnly)
	h, err := hub.ReadHolder(root)
	require.NoError(t, err, "Alpha holds its hub")
	assert.Equal(t, os.Getpid(), h.PID)
	assert.Equal(t, hub.ControlSigusr1, h.Control, "Alpha can still be asked to let go")
	_, err = app.ExecuteQuery("SELECT 1")
	require.NoError(t, err, "Alpha's session works")
}

// #64: a switch that fails at the hub step reopens the workspace it let go of
// and says why.
func TestSwitch_RefusedTakeOverReopensTheCurrentWorkspace(t *testing.T) {
	app := newBareApp(t)
	a := newWorkspaceDir(t, "Alpha")
	b := newWorkspaceDir(t, "Beta")
	resA, err := app.OpenWorkspace(a)
	require.NoError(t, err)

	// Beta's hub is held by a process that cannot be asked to let go.
	ws, err := workspace.Open(b)
	require.NoError(t, err)
	other, err := hub.AcquireHub(ws, "slaw mcp serve")
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Release(); _ = ws.Close() })

	_, err = app.open(b, openTakeOver)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Couldn't take over the workspace")
	assert.Contains(t, err.Error(), "Alpha was reopened")
	assertAlphaBack(t, app, resA.Info.Root)
}

// failAfterLock makes the hub open for root take its lock and its session,
// then fail at the session step, releasing everything as hub.Open does when a
// step fails. after runs once the failure is decided.
func failAfterLock(t *testing.T, app *App, root string, after func()) {
	t.Helper()
	app.openHub = func(ctx context.Context, r string, o hub.OpenOptions) (*hub.Opened, error) {
		if r != root {
			return hub.Open(ctx, r, o)
		}
		o.BeforeSession = func(workspace.Workspace) {
			h, err := hub.ReadHolder(root)
			assert.NoError(t, err, "the new lock is held when the session step is reached")
			if err == nil {
				assert.Equal(t, os.Getpid(), h.PID)
			}
		}
		opened, err := hub.Open(ctx, r, o)
		if err == nil {
			opened.Close() // the lock, session and workspace go back
		}
		if after != nil {
			after()
		}
		return nil, &hub.OpenError{Step: hub.StepSession, Err: errors.New("failed to open the workspace session: no scratch space")}
	}
}

// A failure after Beta's lock was taken (the session step) releases Beta's
// lock and reopens Alpha.
func TestSwitch_SessionFailureReleasesTheNewLockAndReopensTheOld(t *testing.T) {
	app := newBareApp(t)
	a := newWorkspaceDir(t, "Alpha")
	b := newWorkspaceDir(t, "Beta")
	resA, err := app.OpenWorkspace(a)
	require.NoError(t, err)
	failAfterLock(t, app, b, nil)

	_, err = app.OpenWorkspace(b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no scratch space", "the notice names the cause")
	assert.Contains(t, err.Error(), "Alpha was reopened")
	_, herr := hub.ReadHolder(b)
	assert.ErrorIs(t, herr, hub.ErrNotHeld, "Beta's lock is released")
	assertAlphaBack(t, app, resA.Info.Root)
}

// When another program takes Alpha's database in the gap, Alpha comes back
// read-only and the notice says who has it.
func TestSwitch_ReopenedReadOnlyWhenAnotherProgramTookTheHub(t *testing.T) {
	app := newBareApp(t)
	a := newWorkspaceDir(t, "Alpha")
	b := newWorkspaceDir(t, "Beta")
	_, err := app.OpenWorkspace(a)
	require.NoError(t, err)
	var other *hub.Hold
	var aws workspace.Workspace
	t.Cleanup(func() {
		if other != nil {
			_ = other.Release()
			_ = aws.Close()
		}
	})
	failAfterLock(t, app, b, func() {
		var oerr error
		aws, oerr = workspace.Open(a)
		require.NoError(t, oerr)
		other, oerr = hub.AcquireHub(aws, "slaw mcp serve")
		require.NoError(t, oerr)
	})

	_, err = app.OpenWorkspace(b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Alpha was reopened read-only")
	assert.Contains(t, err.Error(), "slaw mcp serve")
	info, err := app.GetWorkspaceInfo()
	require.NoError(t, err)
	assert.True(t, info.ReadOnly)
}

// When the old workspace cannot be reopened either, the error says so and
// nothing is left open or locked.
func TestSwitch_NothingReopenableSaysSoAndLeavesNothingOpen(t *testing.T) {
	app := newBareApp(t)
	resA, err := app.OpenWorkspace(newWorkspaceDir(t, "Alpha"))
	require.NoError(t, err)
	b := newWorkspaceDir(t, "Beta")

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	t.Setenv("TMPDIR", filepath.Join(file, "sub"))

	_, err = app.OpenWorkspace(b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Alpha could not be reopened either")
	_, err = app.GetWorkspaceInfo()
	assert.Error(t, err, "nothing is open")
	for _, root := range []string{b, resA.Info.Root} {
		_, herr := hub.ReadHolder(root)
		assert.ErrorIs(t, herr, hub.ErrNotHeld)
	}
}

// A good switch closes the old workspace exactly once and hands its lock over.
func TestSwitch_SuccessClosesTheOldWorkspaceOnceAndHoldsTheNewLock(t *testing.T) {
	app := newBareApp(t)
	var ev eventLog
	app.emit = ev.sink
	resA, err := app.OpenWorkspace(newWorkspaceDir(t, "Alpha"))
	require.NoError(t, err)
	resB, err := app.OpenWorkspace(newWorkspaceDir(t, "Beta"))
	require.NoError(t, err)
	assert.Equal(t, 1, ev.count("workspace:closed"))
	_, err = hub.ReadHolder(resA.Info.Root)
	assert.ErrorIs(t, err, hub.ErrNotHeld)
	h, err := hub.ReadHolder(resB.Info.Root)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), h.PID)
}
