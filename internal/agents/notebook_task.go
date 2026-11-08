package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/semantics"
)

// --- preview_query (#66 part B, task mode) ---

// previewCharBudget bounds the JSON size of a preview_query result — a
// character-budgeted sample, not the full result — mirroring
// schoolwork-workspace's TASK_ROW_BUDGET_CHARS via boundEnvelope. Simpler
// than the JS original (no separate columns-trim step): Slaw's engine
// already carries real column types (no DESCRIBE round trip needed), so a
// preview's "columns" list is one name+type pair per column rather than the
// JS version's per-view-fit-explanation duplication of the whole column
// list — the dominant cost here is almost always `rows`.
const previewCharBudget = 6000

// previewQueryRowCap / previewQueryTimeout bound preview_query's actual
// execution — review fix (finding 3): the original implementation ran
// h.engine.Execute directly with no LIMIT push-down and no per-call
// deadline, so `SELECT * FROM range(1000000000)` would materialize a
// billion rows before budgetedSample ever got a chance to trim anything.
// previewQueryRowCap matches the row cap RunA2UIQuery already pushes down
// for a live A2UI binding; previewQueryTimeout matches RunA2UIQuery's own
// 30s ceiling (see engineAgentAdapter/RunReadOnlySQL callers in
// studio/app.go) — preview_query reuses RunReadOnlySQL itself, so both
// call sites share one row-cap/timeout policy rather than drifting.
const previewQueryRowCap = 1000
const previewQueryTimeout = 30 * time.Second

func (h *Handler) handlePreviewQuery(ctx context.Context, req *Request) *Response {
	sqlStr, _ := req.Params["sql"].(string)
	if strings.TrimSpace(sqlStr) == "" {
		return NewErrorResponse(req.ID, ErrInvalidParams, "sql is required")
	}

	ctx, cancel := context.WithTimeout(ctx, previewQueryTimeout)
	defer cancel()

	// RunReadOnlySQL is the SAME read-only classifier + row-cap/LIMIT
	// push-down path RunA2UIQuery uses for a live A2UI binding (#67) — one
	// policy, not a second hand-rolled gate. ErrReadOnlyBindingDenied is
	// returned unwrapped on a permission denial (see RunReadOnlySQL), so
	// errors.Is distinguishes "not a read" (ErrPermission) from a genuine
	// execution failure (ErrInternal) — the pre-fix code classified this
	// itself just to get ErrPermission right; this preserves that distinction.
	connection, cerr := h.validConnection(stringParam(req.Params["connection"]))
	if cerr != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, cerr.Error())
	}
	rs, rowsTruncated, err := h.RunReadOnlySQLIn(ctx, connection, sqlStr, nil, previewQueryRowCap)
	if err != nil {
		var dr *definitionRefusal
		if errors.As(err, &dr) {
			// The statement itself is wrong (empty, unparseable, unbindable):
			// not a permission problem, and the reason is the useful part.
			return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("query failed: %v", err))
		}
		if errors.Is(err, ErrReadOnlyBindingDenied) {
			return NewErrorResponse(req.ID, ErrPermission, fmt.Sprintf("preview_query only runs read-only statements: %v", err))
		}
		if errors.Is(err, semantics.ErrInvalid) {
			return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("query failed: %v", err))
		}
		return NewErrorResponse(req.ID, ErrInternal, fmt.Sprintf("query failed: %v", err))
	}

	colNames := make([]string, len(rs.Columns))
	colTypes := make([]string, len(rs.Columns))
	columns := make([]map[string]any, len(rs.Columns))
	for i, c := range rs.Columns {
		colNames[i] = c.Name
		colTypes[i] = c.Type
		columns[i] = map[string]any{"name": c.Name, "type": c.Type}
	}

	// suggestedView/viewFit are judged on rs.Rows as returned — which is
	// already capped at previewQueryRowCap by RunReadOnlySQL above. This is
	// a deliberate change from judging the "full" rowset: the whole point of
	// pushing the cap into the query is to never materialize the full result
	// for something like range(1e9), so there is no full rowset left to
	// judge — the capped set is judged, and rowCount/truncated say so
	// honestly rather than implying a view verdict computed on data that was
	// never actually fetched.
	shape := describeViewShape(colNames, rs.Rows, colTypes)
	suggestedView := bestViewFor(shape)
	viewFit := map[string]any{}
	for _, v := range notebooks.Views {
		fit := checkViewFit(v, shape)
		if fit.OK {
			viewFit[v] = true
		} else {
			viewFit[v] = fit.Why
		}
	}

	previewRows, budgetTruncated, note := budgetedSample(colNames, rs.Rows, columns, suggestedView, viewFit)
	truncated := rowsTruncated || budgetTruncated
	if rowsTruncated && !budgetTruncated {
		note = fmt.Sprintf("Row cap: this query returns more than %d rows; showing the first %d. Add a LIMIT or aggregate if you need an exact count.", previewQueryRowCap, len(rs.Rows))
	}

	// Success: record the exact (trimmed) sql string as "previewed this
	// turn" so write_cell can require it — see wasPreviewed/markPreviewed
	// and handleWriteCell's check (review fix, finding 4).
	h.markPreviewed(connection, sqlStr)

	return NewResponse(req.ID, map[string]any{
		"rowCount":      len(rs.Rows),
		"columns":       columns,
		"rows":          previewRows,
		"truncated":     truncated,
		"suggestedView": suggestedView,
		"viewFit":       viewFit,
		"note":          note,
	})
}

// rowObjects converts positional rows into name-keyed objects, the shape the
// model reads most naturally (and what schoolwork-workspace's queryRows/
// preview envelope already used).
func rowObjects(cols []string, rows [][]any) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		obj := make(map[string]any, len(cols))
		for ci, c := range cols {
			if ci < len(r) {
				obj[c] = r[ci]
			}
		}
		out[i] = obj
	}
	return out
}

// budgetedSample bounds the row sample to previewCharBudget total characters
// (rows + columns + viewFit + note together, matching boundEnvelope's "the
// WHOLE envelope must fit" contract) by halving the row count until it fits,
// never below one row.
func budgetedSample(cols []string, rows [][]any, columns []map[string]any, suggestedView string, viewFit map[string]any) (sample []map[string]any, truncated bool, note string) {
	sample = rowObjects(cols, rows)
	fits := func(n int) bool {
		probe := map[string]any{
			"rowCount": len(rows), "columns": columns, "rows": sample[:n],
			"truncated": true, "suggestedView": suggestedView, "viewFit": viewFit, "note": "",
		}
		b, err := json.Marshal(probe)
		return err == nil && len(b) <= previewCharBudget
	}
	n := len(sample)
	if n == 0 || fits(n) {
		return sample, false, ""
	}
	for n > 1 {
		n /= 2
		if fits(n) {
			break
		}
	}
	if n < 1 {
		n = 1
	}
	return sample[:n], true, fmt.Sprintf("Result has %d row(s); showing %d to fit the response budget. Add a LIMIT or aggregate if you need to see more.", len(rows), n)
}

// --- write_cell / need_more_info (#66 part B, task mode terminal tools) ---
//
// These are stateless, per-call validators — the "concluded" (don't let a
// second terminal call this step clobber the first) guard and the actual
// TaskEnvelope assembly live in internal/llm's task-mode recorder, which
// wraps every call to these two tools; see internal/llm/task.go. A
// validation rejection here returns a normal JSON-RPC error (no StopTurn),
// so the model gets the chance to correct itself within the same step
// budget rather than ending the turn on one malformed call. A successful
// call sets Response.StopTurn.

// normalizeCellView clamps a model-supplied view to notebooks.Views (the
// one closed list the notebook file format accepts), defaulting to table.
func normalizeCellView(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, ok := range notebooks.Views {
		if v == ok {
			return v
		}
	}
	return "table"
}

func (h *Handler) handleWriteCell(ctx context.Context, req *Request) *Response {
	cellType := strings.ToLower(strings.TrimSpace(stringParam(req.Params["cell_type"])))
	explanation := strings.TrimSpace(stringParam(req.Params["explanation"]))

	switch cellType {
	case "sql":
		sqlStr := stringParam(req.Params["sql"])
		if strings.TrimSpace(sqlStr) == "" {
			return NewErrorResponse(req.ID, ErrInvalidParams, "a sql cell needs a non-empty \"sql\" field. Preview the query with preview_query, then call write_cell again including that sql.")
		}
		// review fix (finding 4): write_cell must independently verify its
		// own sql is read-only — never trust that a model-supplied "sql"
		// field is the same text preview_query classified, even though the
		// next check also requires it to have been previewed verbatim this
		// turn. userDB may be unavailable in a test double with no DuckDB;
		// treat that as an internal error rather than silently skipping the
		// gate.
		if _, errResp := h.duckDB(req.ID); errResp != nil {
			return errResp
		}
		connection, cerr := h.validConnection(stringParam(req.Params["connection"]))
		if cerr != nil {
			return NewErrorResponse(req.ID, ErrInvalidParams, cerr.Error())
		}
		mx, vetParams, xerr := h.expandMetrics(ctx, connection, sqlStr, nil)
		if xerr != nil {
			return NewErrorResponse(req.ID, expandErrorCode(xerr), "write_cell: "+xerr.Error())
		}
		connectErr := h.connectReferenced(ctx, connection, mx.SQL)
		vetSQL, vetCtx, xerr := h.expand(ctx, mx.SQL)
		if xerr != nil {
			return NewErrorResponse(req.ID, expandErrorCode(xerr), "write_cell: "+xerr.Error())
		}
		v, verr := h.classifyIn(vetCtx, connection, vetSQL, vetParams)
		if verr != nil {
			return NewErrorResponse(req.ID, ErrInternal, verr.Error())
		}
		if err := h.sandboxFor(ctx).checkSQLClassReadOnly(v); err != nil {
			return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("write_cell only accepts read-only sql: %s", denialWithReason(withConnectError(err, connectErr), v.Reason)))
		}
		// review fix (finding 4): require this EXACT (trimmed) sql to have
		// been run through preview_query earlier in this same task turn —
		// "a query that merely looks right" is exactly the failure mode task
		// mode exists to prevent (see the task-notebook-cells skill). This
		// is a validation rejection (no StopTurn), so the model can preview
		// and retry within the same step budget.
		if !h.wasPreviewed(connection, sqlStr) {
			return NewErrorResponse(req.ID, ErrInvalidParams, "this sql has not been checked with preview_query this turn (must match exactly, including whitespace after trimming) — call preview_query with this sql first, then write_cell again.")
		}
		resp := NewResponse(req.ID, map[string]any{
			"outcome": "cell",
			"cell": map[string]any{
				"type":  "sql",
				"sql":   sqlStr,
				"view":  normalizeCellView(stringParam(req.Params["view"])),
				"label": strings.TrimSpace(stringParam(req.Params["label"])),
				// connection is set only when the agent named one (validated
				// against the registered connections above).
				"connection": connection,
			},
			"explanation": explanation,
		})
		resp.StopTurn = true
		return resp
	case "markdown":
		md := stringParam(req.Params["markdown"])
		if strings.TrimSpace(md) == "" {
			return NewErrorResponse(req.ID, ErrInvalidParams, "a markdown cell needs a non-empty \"markdown\" field — \"explanation\" is not shown as the cell's content. Call write_cell again with the text in \"markdown\".")
		}
		resp := NewResponse(req.ID, map[string]any{
			"outcome": "cell",
			"cell": map[string]any{
				"type":     "markdown",
				"markdown": md,
				"label":    strings.TrimSpace(stringParam(req.Params["label"])),
			},
			"explanation": explanation,
		})
		resp.StopTurn = true
		return resp
	default:
		return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("unknown cell_type %q — use \"sql\" or \"markdown\".", cellType))
	}
}

func stringSliceParam(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func (h *Handler) handleNeedMoreInfo(_ context.Context, req *Request) *Response {
	questions := stringSliceParam(req.Params["questions"])
	missingData := stringSliceParam(req.Params["missing_data"])
	notes := strings.TrimSpace(stringParam(req.Params["notes"]))

	if len(questions) == 0 && len(missingData) == 0 && notes == "" {
		return NewErrorResponse(req.ID, ErrInvalidParams, "need_more_info needs at least one specific question, or non-empty notes/missing_data explaining what's missing and why. Call again with a real reason, or call write_cell if you can answer after all.")
	}

	resp := NewResponse(req.ID, map[string]any{
		"outcome":     "needs_info",
		"questions":   questions,
		"missingData": missingData,
		"notes":       notes,
	})
	resp.StopTurn = true
	return resp
}
