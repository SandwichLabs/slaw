package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/SandwichLabs/slaw/internal/session"
)

// ApprovalRequest describes one write operation waiting on the user's OK
// (#79). It is built by describeWrite from the tool call that triggered it,
// carried over the "agent:approval" Wails event to Studio's chat UI, and
// rendered inline under the matching tool chip via CallID.
type ApprovalRequest struct {
	ID          string `json:"id"`
	CallID      string `json:"callId"` // the tool-call id in chat (fmt.Sprint(req.ID)) — correlates the card to its chip
	Tool        string `json:"tool"`
	Summary     string `json:"summary"`       // plain English, from describeWrite
	SQL         string `json:"sql,omitempty"` // run_query sql, or the built statement shape
	Table       string `json:"table,omitempty"`
	Where       string `json:"where,omitempty"`
	Rows        int64  `json:"rows"` // estimated rows affected; -1 = unknown
	Mode        string `json:"mode"`
	Destructive bool   `json:"destructive"` // delete/drop/update, and any writing run_query -> red button
	// Connections names the changed connections that are not local files
	// (Postgres, MotherDuck, ...): approval is forced for them whatever the
	// "Ask before changes" setting is (#116).
	Connections []string `json:"connections,omitempty"`
}

// Approval outcomes. ErrApprovalDeclined/Expired/Cancelled are returned by
// ApprovalQueue.Wait; awaitApproval (handler.go) maps each to the tool-error
// text the model actually sees.
var (
	ErrApprovalDeclined  = errors.New("declined")
	ErrApprovalExpired   = errors.New("no answer in time")
	ErrApprovalCancelled = errors.New("cancelled") // turn cancelled, or file closed/replaced while waiting
)

// waitEntry is one registered Wait call: the channel Resolve answers on,
// and the cancel func for the internal, derived context CancelAll uses to
// distinguish "the file went away" from an ordinary decline/timeout — see
// Wait's doc comment.
type waitEntry struct {
	ch     chan bool
	cancel context.CancelFunc
}

// ApprovalQueue is a ctx-aware registry of in-flight approval requests. One
// queue lives for the process (studio/app.go's App owns one); Wait blocks
// the calling goroutine — never the request itself — until Resolve is
// called for the same id, or ctx ends.
type ApprovalQueue struct {
	mu      sync.Mutex
	waiting map[string]waitEntry
}

// NewApprovalQueue creates an empty queue.
func NewApprovalQueue() *ApprovalQueue {
	return &ApprovalQueue{waiting: make(map[string]waitEntry)}
}

// Wait registers req (assigning req.ID if empty), invokes announce(req) so
// the caller can surface it (e.g. emit a Wails event) before blocking, then
// waits for Resolve(req.ID, ...), for CancelAll, or for ctx to end. It
// always unregisters itself before returning, so a late Resolve on an id
// nobody is waiting on anymore is simply a no-op (Resolve reports false).
//
// Returns nil only when approved. Internally Wait derives its own
// context.WithCancel child of ctx and waits on THAT (not ctx directly) so
// CancelAll can end this one wait on its own, distinctly from ctx's normal
// deadline: a plain ctx deadline maps to ErrApprovalExpired, while a
// CancelAll-triggered cancellation (context.Canceled, since it was this
// derived context's own cancel func that fired, not something inherited
// from ctx) maps to ErrApprovalCancelled.
func (q *ApprovalQueue) Wait(ctx context.Context, req ApprovalRequest, announce func(ApprovalRequest)) error {
	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Buffered(1) + non-blocking send in Resolve: a Resolve that arrives
	// after Wait has already returned (ctx ended first) must never block on
	// a receiver that's no longer listening.
	ch := make(chan bool, 1)

	q.mu.Lock()
	q.waiting[req.ID] = waitEntry{ch: ch, cancel: cancel}
	q.mu.Unlock()

	defer func() {
		q.mu.Lock()
		delete(q.waiting, req.ID)
		q.mu.Unlock()
	}()

	if announce != nil {
		announce(req)
	}

	select {
	case approved := <-ch:
		if approved {
			return nil
		}
		return ErrApprovalDeclined
	case <-waitCtx.Done():
		if errors.Is(waitCtx.Err(), context.Canceled) {
			return ErrApprovalCancelled
		}
		return ErrApprovalExpired
	}
}

// Resolve answers the pending request with the given id. Returns false if no
// such request is waiting (unknown id, already resolved, or already timed
// out/cancelled) — the caller (studio/app.go's ResolveAgentApproval binding)
// turns that into a user-visible "no longer waiting" error.
func (q *ApprovalQueue) Resolve(id string, approved bool) bool {
	q.mu.Lock()
	entry, ok := q.waiting[id]
	if ok {
		delete(q.waiting, id)
	}
	q.mu.Unlock()
	if !ok {
		return false
	}
	// Buffered(1): this send never blocks even if Wait's select has already
	// moved on (it won't have — Wait only removes itself from q.waiting in
	// its own deferred cleanup, which runs after the select returns, so a
	// concurrent Resolve and a ctx-expiry can race here, and the buffer is
	// what makes that race harmless either way).
	entry.ch <- approved
	return true
}

// CancelAll ends every request currently waiting via its own derived
// context (see Wait), so each one returns ErrApprovalCancelled rather than
// ErrApprovalDeclined/Expired. Used by studio/workspace.go's open/close/switch
// before taking the workspace lock, so a pending approval on the workspace
// being replaced ends as "cancelled, nothing changed" rather than blocking
// the swap.
func (q *ApprovalQueue) CancelAll() {
	q.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(q.waiting))
	for _, entry := range q.waiting {
		cancels = append(cancels, entry.cancel)
	}
	q.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// describeWrite builds the plain-English ApprovalRequest for a write tool
// call, after the sandbox has already allowed it (approval never runs ahead
// of the sandbox check — see Handler.Handle/handleRunQuery). estimateRows is
// used for delete_record/update_record/drop_table only —
// everything else either has no natural row count (create_table, a saved
// query, a skill) or its count comes for free from the call itself
// (bulk_insert's row list length).
func describeWrite(ctx context.Context, req *Request, userDB *sql.DB, class sqlClass) ApprovalRequest {
	ar := ApprovalRequest{
		CallID: fmt.Sprint(req.ID),
		Tool:   req.Method,
	}

	table := stringParam(req.Params["table"])
	where := stringParam(req.Params["where"])
	ar.Table = table
	ar.Where = where

	switch req.Method {
	case "delete_record":
		ar.Rows = estimateRows(ctx, userDB, table, where)
		ar.Destructive = true
		ar.Summary = summarizeRowScope(fmt.Sprintf("Delete %s from %s", rowsPhrase(ar.Rows), table), where)
	case "update_record":
		values, _ := req.Params["values"].(map[string]any)
		cols := make([]string, 0, len(values))
		for k := range values {
			cols = append(cols, k)
		}
		ar.Rows = estimateRows(ctx, userDB, table, where)
		ar.Destructive = true
		ar.Summary = summarizeRowScope(fmt.Sprintf("Change %s on %s in %s", strings.Join(cols, ", "), rowsPhrase(ar.Rows), table), where)
	case "create_record":
		ar.Rows = 1
		ar.Summary = fmt.Sprintf("Add a row to %s", table)
	case "bulk_insert":
		n := int64(0)
		if rows, ok := req.Params["rows"].([]any); ok {
			n = int64(len(rows))
		}
		ar.Rows = n
		ar.Summary = fmt.Sprintf("Add %d rows to %s", n, table)
	case "drop_table":
		name := stringParam(req.Params["name"])
		ar.Table = name
		ar.Rows = estimateRows(ctx, userDB, name, "")
		ar.Destructive = true
		ar.Summary = fmt.Sprintf("Delete table %s and its %s", name, rowsPhrase(ar.Rows))
	case "create_table":
		ar.Table = stringParam(req.Params["name"])
		ar.Summary = fmt.Sprintf("Create table %s", ar.Table)
		// The column type is raw SQL text (it can carry constraints), so the person sees all of it.
		if cols, ok := req.Params["columns"].([]any); ok && len(cols) > 0 {
			defs := make([]string, 0, len(cols))
			for _, c := range cols {
				if m, ok := c.(map[string]any); ok {
					defs = append(defs, stringParam(m["name"])+" "+stringParam(m["type"]))
				}
			}
			ar.Summary += " with " + strings.Join(defs, ", ")
		}
	case "add_column":
		ar.Rows = -1
		ar.Summary = fmt.Sprintf("Add column %s %s to %s", stringParam(req.Params["name"]), stringParam(req.Params["type"]), table)
	case "build_query":
		ar.Rows = -1
		ar.Summary = fmt.Sprintf("Save query %q", stringParam(req.Params["name"]))
	case "save_query":
		ar.Rows = -1
		ar.Summary = fmt.Sprintf("Save query %q", stringParam(req.Params["title"]))
	case "create_or_update_skill":
		ar.Rows = -1
		ar.Summary = fmt.Sprintf("Save skill %s", stringParam(req.Params["name"]))
	case "write_watch":
		ar.Rows = -1
		verb := "Save"
		if rev, _ := req.Params["rev"].(string); rev != "" {
			verb = "Replace"
		}
		ar.Summary = fmt.Sprintf("%s watch %s%s", verb, stringParam(req.Params["name"]), watchAsks(req))
		ar.SQL = watchCardFile(req)
	case "write_artifact":
		ar.Rows = -1
		verb := "Save"
		if rev, _ := req.Params["rev"].(string); rev != "" {
			verb = "Replace"
		}
		ar.Summary = fmt.Sprintf("%s data artifact %s", verb, stringParam(req.Params["name"]))
		if mf, _ := req.Params["manifest"].(map[string]any); stringParam(mf["view"]) == "script" {
			ar.Summary += " with a script view (it stays inert until you allow it)"
		}
		if stringParam(mfView(req.Params["manifest"])) == "html" {
			ar.Summary += " with an HTML page (it stays inert until you allow it)"
		}
		ar.SQL = artifactQueriesText(req.Params["manifest"])
	case "add_query_cell", "create_dashboard", "add_markdown_cell":
		ar.Rows = -1
		ar.Summary = "Add a cell to a notebook"
	case "update_cell":
		ar.Rows = -1
		ar.Summary = fmt.Sprintf("Change cell %s in notebook %s", stringParam(req.Params["cell_id"]), stringParam(req.Params["notebook_id"]))
		ar.SQL = stringParam(req.Params["sql"])
	case "delete_cell":
		ar.Rows = -1
		ar.Destructive = true
		ar.Summary = fmt.Sprintf("Delete cell %s from notebook %s", stringParam(req.Params["cell_id"]), stringParam(req.Params["notebook_id"]))
	case "move_cell":
		ar.Rows = -1
		ar.Summary = fmt.Sprintf("Move cell %s in notebook %s", stringParam(req.Params["cell_id"]), stringParam(req.Params["notebook_id"]))
	case "run_query":
		ar.Rows = -1
		ar.SQL = stringParam(req.Params["sql"])
		ar.Destructive = true
		if class == sqlClassDDL || class == sqlClassOther {
			ar.Summary = "Run SQL that changes a connection's structure or writes a file"
		} else {
			ar.Summary = "Run SQL that changes data"
		}
	default:
		ar.Rows = -1
		ar.Summary = fmt.Sprintf("Run %s", req.Method)
	}
	return ar
}

// rowsPhrase renders an estimated row count for a summary sentence: "N rows"
// when known, "matching rows" when estimateRows couldn't determine it (-1).
func rowsPhrase(n int64) string {
	if n < 0 {
		return "matching rows"
	}
	return fmt.Sprintf("%d rows", n)
}

// summarizeRowScope appends " where <clause>" to base when where is
// non-empty, matching the "Delete N rows from T where W" shape.
func summarizeRowScope(base, where string) string {
	if strings.TrimSpace(where) == "" {
		return base
	}
	return fmt.Sprintf("%s where %s", base, where)
}

// estimateRows returns COUNT(*) FROM table [WHERE where], or -1 on any
// failure (no db, bad table/where, timeout, or — the security-relevant case
// — where classifying as anything but a plain read, e.g. a `where` smuggling
// a second statement like "1=0; DROP TABLE orders"). It reuses classifySQL,
// the exact same classifier handleRunQuery/requireWriteStatement use, rather
// than hand-rolling a second SQL-shape check — a where clause this function
// can't safely turn into a read-only COUNT(*) still lets the approval card
// through (with Rows == -1, "matching rows" in the summary), it just can't
// tell the user how many.
func estimateRows(ctx context.Context, db *sql.DB, table, where string) int64 {
	if db == nil || strings.TrimSpace(table) == "" {
		return -1
	}
	query := "SELECT count(*) FROM " + session.QuoteIdent(table)
	if strings.TrimSpace(where) != "" {
		query += " WHERE " + where
	}
	if class, _ := classifySQL(ctx, db, query, nil); class != sqlClassRead {
		return -1
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var n int64
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return -1
	}
	return n
}

// mfView is a write_artifact manifest's view kind (any), for the approval card.
func mfView(manifest any) any {
	mf, _ := manifest.(map[string]any)
	return mf["view"]
}
