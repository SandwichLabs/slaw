package main

import (
	"fmt"
	"time"

	"github.com/SandwichLabs/slaw/internal/bundle"
	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// --- Notebooks (#66 part A) ---
//
// SQL execution for a notebook cell reuses RunA2UIQuery — a notebook's sql
// cell is a read query, the same live binding an A2UI page's data-bound
// component uses (same sandbox-gated classifier, same read-only-only gate).
// Writes belong in the Query Explorer or the table browser, not in a
// notebook cell; RunA2UIQuery already rejects a write with
// ErrReadOnlyBindingDenied, and the frontend surfaces that as "Notebook
// cells are read-only queries — use the Query editor to change data."

// NotebooksChangedEvent is the payload of every "notebooks:changed" Wails
// event (review fix, finding 2). AppendedCellIDs is the load-bearing field:
// an open NotebookView.vue merges in ONLY cell ids listed here — never "any
// remote id we don't already have locally" (the pre-fix behavior), which
// could resurrect a cell the user had just deleted locally but not yet
// saved. Every emitter that is NOT "an agent tool appended a specific new
// cell" (CreateNotebook, SaveNotebook, RenameNotebook, DeleteNotebook,
// ImportNotebook) omits AppendedCellIDs entirely — the frontend treats an
// event with no AppendedCellIDs as "not an append, nothing to merge" (this
// is also how NotebookView tells its OWN SaveNotebook echo apart from a
// remote append: its own save never sets this field).
type NotebooksChangedEvent struct {
	NotebookID      string   `json:"notebookId"`
	AppendedCellIDs []string `json:"appendedCellIds,omitempty"`
	// Changed is set when an agent tool edited, removed, moved or inserted a
	// cell of the notebook (update_cell, delete_cell, move_cell, a markdown
	// cell added mid-list): the open copy is reloaded, or the conflict banner
	// raised when it holds unsaved edits, the same as a change on disk.
	Changed bool `json:"changed,omitempty"`
}

// emitNotebooksChanged emits "notebooks:changed" with the shared payload
// shape — see NotebooksChangedEvent.
func (a *App) emitNotebooksChanged(notebookID string, appendedCellIDs []string) {
	a.emitEvent("notebooks:changed", NotebooksChangedEvent{NotebookID: notebookID, AppendedCellIDs: appendedCellIDs})
}

// emitNotebookEdited emits "notebooks:changed" for an agent edit of existing
// cells (see NotebooksChangedEvent.Changed).
func (a *App) emitNotebookEdited(notebookID string) {
	a.emitEvent("notebooks:changed", NotebooksChangedEvent{NotebookID: notebookID, Changed: true})
}

// ListNotebooks returns every notebook's summary, newest-updated first.
func (a *App) ListNotebooks() ([]notebooks.Summary, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	return a.ws.Notebooks().List(a.ctxOrBackground())
}

// GetNotebook loads one notebook, rebuilt through notebooks.Coerce.
func (a *App) GetNotebook(id string) (*notebooks.Notebook, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	nb, warnings, err := a.ws.Notebooks().Get(a.ctxOrBackground(), id)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		a.logger.Warn("notebook coercion warning", "notebook_id", id, "warning", w)
	}
	return &nb, nil
}

// SetActiveNotebook records which notebook is currently open in
// NotebookView.vue (or clears it with ""), for the chat/MCP agent's
// add_query_cell/create_dashboard tools (#67 deferred part) — "append to the
// notebook currently open in Studio" needs the frontend to say which one
// that is; there is no other way for a Go binding to know. The view calls
// this on mount, on switching notebooks, and with "" on unmount/close.
func (a *App) SetActiveNotebook(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeNotebookID = id
}

// CreateNotebook creates a new, empty notebook and returns it.
func (a *App) CreateNotebook(title string) (*notebooks.Notebook, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	nb, err := a.ws.Notebooks().Create(a.ctxOrBackground(), title)
	if err != nil {
		return nil, err
	}
	a.emitNotebooksChanged(nb.ID, nil)
	return &nb, nil
}

// NotebookSaveResult wraps a saved/imported notebook with any warnings
// notebooks.Coerce produced — a save can silently drop or clamp fields (an
// unknown cell type, an out-of-range view, an invalid a2ui page), and the
// caller needs to know rather than have it happen invisibly.
type NotebookSaveResult struct {
	Notebook notebooks.Notebook `json:"notebook"`
	Warnings []string           `json:"warnings"`
}

// SaveNotebook rebuilds raw (the frontend's in-memory notebook, JSON-decoded
// into a plain map by Wails) through notebooks.Coerce and upserts it. This is
// the ONE write path for a notebook's title/cells — the frontend's add/move/
// delete/collapse cell operations all mutate its local copy and call this
// with the whole document, matching the "outputs are never persisted, only
// source/view/page are" contract (Coerce only ever keeps those fields).
// sessionID must be the value GetSessionID() returned at the time
// the caller LOADED the notebook it is now saving (see App.sessionID's doc
// comment) — never re-fetched at save time, or this check couldn't catch
// the bug it exists for (a save that fires after the file underneath it was
// already switched). A mismatch means the file was closed/reopened/switched
// since this notebook was loaded, so the write is rejected rather than
// silently landing in the wrong container.
func (a *App) SaveNotebook(raw map[string]any, sessionID string) (*NotebookSaveResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	if sessionID != a.sessionID {
		return nil, fmt.Errorf("the workspace changed since this notebook was loaded; reopen it and try again")
	}
	nb, warnings, err := a.ws.Notebooks().Save(a.ctxOrBackground(), raw)
	if err != nil {
		return nil, conflictErr(err)
	}
	a.emitNotebooksChanged(nb.ID, nil)
	return &NotebookSaveResult{Notebook: nb, Warnings: warnings}, nil
}

// DeleteNotebook removes a notebook by id.
func (a *App) DeleteNotebook(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return err
	}
	if err := a.ws.Notebooks().Delete(a.ctxOrBackground(), id); err != nil {
		return err
	}
	a.emitNotebooksChanged(id, nil)
	return nil
}

// ExportNotebook writes one notebook to a JSON file. If dst is empty, a save
// dialog picks the destination; dst == "" and a cancelled dialog both return
// ("", nil), matching ExportQueryResults's cancel semantics.
func (a *App) ExportNotebook(id, dst string) (string, error) {
	a.mu.RLock()
	if err := a.requireWorkspace(); err != nil {
		a.mu.RUnlock()
		return "", err
	}
	nb, warnings, err := a.ws.Notebooks().Get(a.ctxOrBackground(), id)
	a.mu.RUnlock()
	if err != nil {
		return "", err
	}
	for _, w := range warnings {
		a.logger.Warn("notebook coercion warning on export", "notebook_id", id, "warning", w)
	}

	path := dst
	if path == "" {
		path, err = runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
			Title:           "Export Notebook",
			DefaultFilename: nb.Title + ".json",
			Filters: []runtime.FileFilter{
				{DisplayName: "Slaw Notebook (*.json)", Pattern: "*.json"},
			},
		})
		if err != nil {
			return "", fmt.Errorf("failed to open save dialog: %w", err)
		}
		if path == "" {
			return "", nil
		}
	}

	if err := bundle.WriteNotebookFile(path, nb, time.Now()); err != nil {
		return "", err
	}
	return path, nil
}

// SelectNotebookFile opens a file dialog for choosing an exported notebook
// JSON file — ImportNotebook's picker step.
func (a *App) SelectNotebookFile() (string, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import Notebook",
		Filters: []runtime.FileFilter{
			{DisplayName: "Slaw Notebook (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to open file dialog: %w", err)
	}
	return path, nil
}

// ImportNotebook reads path (an ExportNotebook file) and imports it as a
// brand-new notebook, through notebooks.Coerce like every other load. Import
// never overwrites a notebook already in this workspace: the file's own id and
// updatedAt are stripped (bundle.ReadNotebookFile, which also holds the file to
// a bundle's size limit) so a fresh id is always assigned, matching model.js's
// importFile ("Import always lands as a new notebook").
func (a *App) ImportNotebook(path string) (*NotebookSaveResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	doc, err := bundle.ReadNotebookFile(path)
	if err != nil {
		return nil, err
	}
	nb, warnings, err := a.ws.Notebooks().Save(a.ctxOrBackground(), doc)
	if err != nil {
		return nil, err
	}
	a.emitNotebooksChanged(nb.ID, nil)
	return &NotebookSaveResult{Notebook: nb, Warnings: warnings}, nil
}
