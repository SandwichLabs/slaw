package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/a2ui"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/notebooks"
)

// --- add_query_cell / create_dashboard (#67 deferred part) ---
//
// Unlike write_cell (task mode: propose, dialog shows a preview, user clicks
// Apply), these persist directly to the notebook store — the agent panel's
// chat/MCP surface has no proposal dialog, so the user's own ask ("add a
// dashboard of X to my notebook") is the confirmation. Both go through
// notebooks.Coerce (via notebooks.Save) exactly like every other notebook
// write, so a malformed cell can never be smuggled in.

// existingTarget returns the notebook a cell write targets when one exists:
// explicitID if given, else the Studio-tracked active notebook. found is false
// when neither names one (the caller then creates a notebook, or refuses).
func (h *Handler) existingTarget(ctx context.Context, store notebooks.Store, explicitID string) (nb notebooks.Notebook, found bool, err error) {
	id := strings.TrimSpace(explicitID)
	if id == "" {
		id = strings.TrimSpace(h.resolveActiveNotebook())
	}
	if id == "" {
		return notebooks.Notebook{}, false, nil
	}
	nb, warnings, err := store.Get(ctx, id)
	if err != nil {
		return notebooks.Notebook{}, false, fmt.Errorf("notebook %q not found: %w", id, err)
	}
	for _, w := range warnings {
		h.logger.Warn("notebook coercion warning", "notebook_id", id, "warning", w)
	}
	return nb, true, nil
}

// resolveTargetNotebook returns the notebook to append to: explicitID if
// given, else the Studio-tracked active notebook (h.resolveActiveNotebook()), else
// a brand-new notebook titled fallbackTitle. Mirrors the contract
// add_query_cell/create_dashboard's ToolDef descriptions promise.
func (h *Handler) resolveTargetNotebook(ctx context.Context, store notebooks.Store, explicitID, fallbackTitle string) (notebooks.Notebook, error) {
	nb, found, err := h.existingTarget(ctx, store, explicitID)
	if err != nil || found {
		return nb, err
	}
	title := strings.TrimSpace(fallbackTitle)
	if title == "" {
		title = "Untitled notebook"
	}
	return store.Create(ctx, title)
}

// notebookDryRun ends a dry run of a cell write: when the cell would go into an
// existing notebook (named, or the one open in Studio) that notebook must
// load; a new notebook is created only by the real write.
func (h *Handler) notebookDryRun(ctx context.Context, req *Request, store notebooks.Store) *Response {
	if _, _, err := h.existingTarget(ctx, store, stringParam(req.Params["notebook_id"])); err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, err.Error())
	}
	return dryRunOK(req.ID)
}

// saveWithNewCell appends cell to nb and persists it through
// notebooks.Save/Coerce (never a direct INSERT), returning the saved
// notebook and any coercion warnings.
func saveWithNewCell(ctx context.Context, store notebooks.Store, nb notebooks.Notebook, cell notebooks.Cell) (notebooks.Notebook, []string, error) {
	nb.Cells = append(nb.Cells, cell)
	return saveNotebook(ctx, store, nb)
}

// saveNotebook persists nb through notebooks.Save/Coerce. nb.Rev (the revision
// it was loaded at) rides along, so a store with compare-and-write refuses it
// when the notebook changed since.
func saveNotebook(ctx context.Context, store notebooks.Store, nb notebooks.Notebook, opts ...notebooks.SaveOption) (notebooks.Notebook, []string, error) {
	raw, err := json.Marshal(nb)
	if err != nil {
		return notebooks.Notebook{}, nil, fmt.Errorf("failed to marshal notebook: %w", err)
	}
	return store.Save(ctx, raw, opts...)
}

// sqlVetFailure is one refused notebook SQL: the JSON-RPC code and the message
// (the binder's error and candidates, or the policy reason).
type sqlVetFailure struct {
	code int
	msg  string
}

// vetNotebookSQL runs the vetting every notebook-stored SQL gets at save: the
// connection must be registered, the SQL is expanded and classified on it as it
// will run (unqualified names resolve, a bad column fails with the binder's
// candidates) and must be a read-only statement under the sandbox. what names
// the surface in the refusal ("notebook sql cells"). It returns the validated
// connection name.
func (h *Handler) vetNotebookSQL(ctx context.Context, rawConnection, sqlStr, what string) (string, *sqlVetFailure) {
	// An agent-authored cell may name the connection it runs on, validated
	// against the registered names; the cell is classified on that connection
	// so its unqualified names resolve the way they will when it runs.
	connection, cerr := h.validConnection(rawConnection)
	if cerr != nil {
		return "", &sqlVetFailure{ErrInvalidParams, cerr.Error()}
	}
	// The cell keeps its metric() call and its wildcard; it is vetted as it
	// will run, expanded.
	mx, vetParams, xerr := h.expandMetrics(ctx, connection, sqlStr, nil)
	if xerr != nil {
		return "", &sqlVetFailure{expandErrorCode(xerr), what + ": " + xerr.Error()}
	}
	connectErr := h.connectReferenced(ctx, connection, mx.SQL)
	vetSQL, vetCtx, xerr := h.expand(ctx, mx.SQL)
	if xerr != nil {
		return "", &sqlVetFailure{expandErrorCode(xerr), what + ": " + xerr.Error()}
	}
	v, verr := h.classifyIn(vetCtx, connection, vetSQL, vetParams)
	if verr != nil {
		return "", &sqlVetFailure{ErrInternal, verr.Error()}
	}
	if err := h.sandboxFor(ctx).checkSQLClassReadOnly(v); err != nil {
		// A statement-level reason can quote the statement: log only the digest.
		if v.Kind&defStatement != 0 {
			h.logger.Debug("notebook sql denied", "query_sha256", engine.QueryDigest(sqlStr))
		} else {
			h.logger.Debug("notebook sql denied", "reason", v.Reason)
		}
		msg := fmt.Sprintf("%s are read-only queries: %s", what, denialWithReason(withConnectError(err, connectErr), v.Reason))
		return "", &sqlVetFailure{ErrPermission, msg}
	}
	if v.Statements > 1 {
		return "", &sqlVetFailure{ErrInvalidParams, fmt.Sprintf("%s take a single statement, but this has %d: put each statement in its own cell (one trailing semicolon is fine)", what, v.Statements)}
	}
	return connection, nil
}

// vetDashboardSQL vets every sql-bound component of a validated page, on its own
// connection, and returns one line per failure prefixed by the component's path
// (components[i].sql). Nothing is short-circuited: the agent sees all of them.
func (h *Handler) vetDashboardSQL(ctx context.Context, page a2ui.Page) []string {
	var failures []string
	for i, c := range page.Components {
		failures = append(failures, h.vetBinding(ctx, c, i, c.Fields, "sql")...)
		// a chart's annotations are a second live binding of the same component
		if ann, ok := c.Fields["annotations"].(map[string]any); ok {
			failures = append(failures, h.vetBinding(ctx, c, i, ann, "annotations.sql")...)
		}
	}
	return failures
}

// vetBinding vets one sql field (and its sibling connection) of component c.
func (h *Handler) vetBinding(ctx context.Context, c a2ui.Component, i int, fields map[string]any, path string) []string {
	sqlStr, _ := fields["sql"].(string)
	if strings.TrimSpace(sqlStr) == "" {
		return nil
	}
	conn, _ := fields["connection"].(string)
	if _, vf := h.vetNotebookSQL(ctx, conn, sqlStr, "dashboard sql bindings"); vf != nil {
		return []string{fmt.Sprintf("components[%d].%s (%s %q): %s", i, path, c.Type, c.ID, vf.msg)}
	}
	return nil
}

func (h *Handler) handleAddQueryCell(ctx context.Context, req *Request) *Response {
	sqlStr := stringParam(req.Params["sql"])
	if strings.TrimSpace(sqlStr) == "" {
		return NewErrorResponse(req.ID, ErrInvalidParams, "sql is required")
	}

	view, verr := optionalCellView(req.Params["view"])
	if verr != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, verr.Error())
	}
	if _, errResp := h.duckDB(req.ID); errResp != nil {
		return errResp
	}
	connection, vf := h.vetNotebookSQL(ctx, stringParam(req.Params["connection"]), sqlStr, "notebook sql cells")
	if vf != nil {
		return NewErrorResponse(req.ID, vf.code, vf.msg)
	}

	label := strings.TrimSpace(stringParam(req.Params["label"]))
	store := h.access.Notebooks()
	if isDryRun(ctx) {
		return h.notebookDryRun(ctx, req, store)
	}
	nb, err := h.resolveTargetNotebook(ctx, store, stringParam(req.Params["notebook_id"]), label)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, err.Error())
	}

	cell, err := notebooks.NewCell(notebooks.CellSQL)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}
	cell.Source = sqlStr
	cell.Connection = connection
	cell.Label = label
	if view != "" {
		cell.View = view
		cell.ViewPinned = true
	}

	saved, warnings, err := saveWithNewCell(ctx, store, nb, cell)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}
	h.logger.Info("agent added query cell", "notebook_id", saved.ID, "cell_id", cell.ID)
	return NewResponse(req.ID, map[string]any{
		"notebook_id": saved.ID,
		"cell_id":     cell.ID,
		"title":       saved.Title,
		"rev":         saved.Rev,
		"warnings":    warnings,
	})
}

func (h *Handler) handleCreateDashboard(ctx context.Context, req *Request) *Response {
	page, err := a2ui.Validate(req.Params["page"])
	if err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, err.Error())
	}
	// Vet the live sql bindings before the dry-run short-circuit, so a refusal
	// is reported on a dry run too (#215).
	if failures := h.vetDashboardSQL(ctx, page); len(failures) > 0 {
		return NewErrorResponse(req.ID, ErrInvalidParams, "create_dashboard refused: "+strings.Join(failures, "\n"))
	}

	store := h.access.Notebooks()
	if isDryRun(ctx) {
		return h.notebookDryRun(ctx, req, store)
	}
	nb, err := h.resolveTargetNotebook(ctx, store, stringParam(req.Params["notebook_id"]), page.Title)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, err.Error())
	}

	cell, err := notebooks.NewCell(notebooks.CellA2UI)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}
	cell.Page = &page

	saved, warnings, err := saveWithNewCell(ctx, store, nb, cell)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, err.Error())
	}
	h.logger.Info("agent added dashboard cell", "notebook_id", saved.ID, "cell_id", cell.ID)
	return NewResponse(req.ID, map[string]any{
		"notebook_id": saved.ID,
		"cell_id":     cell.ID,
		"title":       saved.Title,
		"rev":         saved.Rev,
		"warnings":    warnings,
	})
}
