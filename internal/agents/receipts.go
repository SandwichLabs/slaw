package agents

import (
	"context"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

// Run receipts (docs/plans/run-receipts.md, E1). The handler is a chokepoint:
// run_query, run_saved_query and preview_query, and every render-time read that
// goes through RunReadOnlySQLIn (an A2UI component, a notebook cell, an artifact
// query, a metric's Try it, an assertion, a watch). Each puts a receipts.Intent
// on the context of its final run only, never on the classification EXPLAIN, the
// save-time vet or a catalog query, and finishes the receipt after its own
// trimming of the rows, so the receipt describes what was shown.

// SetReceipts attaches the recorder receipts are kept through (the workspace's,
// Workspace.Receipts). A nil recorder, which is also the default, records
// nothing; a store that cannot be written never fails a run. Call it before the
// handler dispatches.
func (h *Handler) SetReceipts(r *receipts.Recorder) { h.receipts = r }

// agentSurface reports whether a surface is one of the agent's tools: its actor
// is the agent, whose session is the MCP transport session, the chat
// conversation or the task run on the context.
func agentSurface(s receipts.Surface) bool {
	switch s {
	case receipts.SurfaceAgentRunQuery, receipts.SurfaceAgentSavedQuery, receipts.SurfaceAgentPreview:
		return true
	}
	return false
}

// withIntent puts the intent of a run on ctx: where it came from (the surface
// and source), what was written, the values bound and the verdict. It is for
// the final run's context only. With no recorder it returns ctx unchanged.
func (h *Handler) withIntent(ctx context.Context, surface receipts.Surface, src receipts.Source, written string, params map[string]any) context.Context {
	if h.receipts == nil || surface == "" {
		return ctx
	}
	in := receipts.Intent{
		Surface: surface, Actor: receipts.ActorPerson, Source: src,
		SQL: written, Params: receipts.ParamStrings(params), Verdict: receipts.VerdictRead,
		// The policy has judged this a single read, so the engine need not.
		Kind: engine.ReadKind(written),
	}
	if agentSurface(surface) {
		in.Actor, in.AgentSession = receipts.ActorAgent, receipts.AgentSessionFrom(ctx)
	}
	return receipts.WithIntent(ctx, in)
}

// finishReceipt keeps the receipt of the run on ctx over the rows as shown and
// returns its id ("" when none was made). It never fails a run: a store that
// cannot be written was logged and the result goes out without an id.
func (h *Handler) finishReceipt(ctx context.Context, shown [][]any, truncated bool, total int) string {
	id, _ := h.finishReceiptNote(ctx, shown, truncated, total)
	return id
}

// finishReceiptNote is finishReceipt that also says why there is no receipt
// when one was due (the render-time reads' drawer shows it).
func (h *Handler) finishReceiptNote(ctx context.Context, shown [][]any, truncated bool, total int) (string, receipts.Note) {
	id, err := receipts.Finish(ctx, h.receipts, shown, truncated, total)
	return id, receipts.NoteFor(h.receipts, id, err)
}

// ArtifactOrigin is the origin of an artifact's query run: the artifact surface
// and the file-relative id of the query (artifacts/<name>#<query>).
func ArtifactOrigin(name, query string) receipts.Origin {
	return receipts.Origin{Surface: receipts.SurfaceArtifact, Source: receipts.Source{Kind: receipts.SourceArtifactQuery, Ref: "artifacts/" + name + "#" + query}}
}

// savedQueryRef is the file-relative id of a saved query.
func savedQueryRef(id string) string { return "queries/" + strings.TrimSpace(id) + ".sql" }
