package main

// Regression test for a reviewer-caught DATA CORRUPTION bug (pre-existing
// from #66, fixed alongside #68): calling a workspace switch while a NotebookView was still mounted for the
// PREVIOUS workspace let that view's in-flight save land in the NEW container —
// the container pointer was already swapped by the time the old save
// fired. App.sessionID (see its doc comment) + SaveNotebook's token check
// close this: a save must carry the session id in effect when its caller
// loaded the notebook, and a save from a superseded session is rejected.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveNotebook_RejectsStaleSession(t *testing.T) {
	app := newOpenApp(t)

	nb, err := app.CreateNotebook("Original file's notebook")
	require.NoError(t, err)

	app.mu.RLock()
	staleSessionID := app.sessionID
	app.mu.RUnlock()
	require.NotEmpty(t, staleSessionID)

	raw := map[string]any{
		"id":    nb.ID,
		"title": "Edited after the file switched",
		"cells": []any{},
	}

	// The file underneath gets switched (a second file open in the SAME
	// App instance, exactly like a workspace switch
	// while a stale view is still mounted) — sessionID is regenerated.
	_, err = app.OpenWorkspace(newWorkspaceDir(t, "Second"))
	require.NoError(t, err)

	app.mu.RLock()
	newSessionID := app.sessionID
	app.mu.RUnlock()
	require.NotEmpty(t, newSessionID)
	require.NotEqual(t, staleSessionID, newSessionID, "opening a new file must regenerate the session id")

	// A save carrying the OLD (stale) session id must be rejected — this is
	// exactly the shape of the bug: a debounced save from the original
	// file's NotebookView, in flight when the switch happened.
	_, err = app.SaveNotebook(raw, staleSessionID)
	require.Error(t, err, "a save from a superseded file session must be rejected")
	assert.Contains(t, err.Error(), "the workspace changed since this notebook was loaded")

	// Prove it did NOT land in the new (second) file: no notebook with
	// nb.ID exists there.
	_, err = app.GetNotebook(nb.ID)
	require.Error(t, err, "the stale-session notebook must not have been written into the new container")

	// A save carrying the CURRENT session id (a notebook created/loaded
	// after the switch) must succeed.
	nb2, err := app.CreateNotebook("Second file's own notebook")
	require.NoError(t, err)
	raw2 := map[string]any{
		"id":    nb2.ID,
		"title": "Edited in the second file",
		"cells": []any{},
	}
	result, err := app.SaveNotebook(raw2, newSessionID)
	require.NoError(t, err, "a save carrying the current session id must succeed")
	require.NotNil(t, result)
	assert.Equal(t, "Edited in the second file", result.Notebook.Title)
}

// TestSaveNotebook_RejectsAfterClose asserts a save attempted after the file
// was closed (sessionID cleared back to "") is rejected the same way —
// "no workspace open" takes priority, but either way the write must not happen.
func TestSaveNotebook_RejectsAfterClose(t *testing.T) {
	app := newOpenApp(t)
	nb, err := app.CreateNotebook("Doomed notebook")
	require.NoError(t, err)

	app.mu.RLock()
	sessionID := app.sessionID
	app.mu.RUnlock()

	require.NoError(t, app.CloseWorkspace())

	raw := map[string]any{"id": nb.ID, "title": "after close", "cells": []any{}}
	_, err = app.SaveNotebook(raw, sessionID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspace open")
}
