package main

import (
	"fmt"
	"sync"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/llm"
)

// ChatRun is one read of a chat turn that made a receipt: run_query,
// run_saved_query or preview_query. The panel draws the receipt id, the row
// count and "Open evidence" on that tool's chip. It travels as an
// "agent:receipt" event the moment the read finishes (so the chip is live) and
// again in AgentChatResult.Runs (so a missed event never loses it).
type ChatRun struct {
	CallID    string `json:"callId"`
	Tool      string `json:"tool"`
	ReceiptID string `json:"receiptId"`
	// Rows is how many rows the model was shown.
	Rows      int  `json:"rows"`
	Truncated bool `json:"truncated,omitempty"`
}

// turnRuns holds what one chat turn's reads returned to the model: the rows in
// memory (never stored: a receipt holds no value) that the answer's numbers are
// checked against, and the chip facts. One per turn, so a value from another
// turn is never matched.
type turnRuns struct {
	mu     sync.Mutex
	values []llm.RunValues
	runs   []ChatRun
	// unrecorded counts reads that showed the model rows but made no receipt (a
	// store that could not be used): their values are unknown here, so the answer
	// cannot honestly be called "not from a query".
	unrecorded int
}

// isReadTool reports whether a tool's result carries a receipt_id and rows.
func isReadTool(name string) bool {
	return name == "run_query" || name == "run_saved_query" || name == "preview_query"
}

// add notes a finished tool call. It reports false for a call that is not a
// read or that made no receipt.
func (t *turnRuns) add(callID, tool string, result any) (ChatRun, bool) {
	if !isReadTool(tool) {
		return ChatRun{}, false
	}
	rv, ok := llm.RunValuesFrom(result)
	if !ok {
		if m, isMap := result.(map[string]any); isMap && m["rows"] != nil {
			t.mu.Lock()
			t.unrecorded++
			t.mu.Unlock()
		}
		return ChatRun{}, false
	}
	run := ChatRun{CallID: callID, Tool: tool, ReceiptID: rv.ReceiptID, Rows: len(rv.Rows)}
	if m, ok := result.(map[string]any); ok && m["truncated"] != nil {
		run.Truncated = m["truncated"] != false
	}
	t.mu.Lock()
	t.values = append(t.values, rv)
	t.runs = append(t.runs, run)
	t.mu.Unlock()
	return run, true
}

// snapshot is the turn's reads so far, in the order they finished.
func (t *turnRuns) snapshot() ([]llm.RunValues, []ChatRun) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.RunValues(nil), t.values...), append([]ChatRun(nil), t.runs...)
}

// trackRuns makes the handler remember this turn's reads and announce each one
// as an "agent:receipt" event tagged with turnID. Only a chat turn does it.
func (h *agentToolHandler) trackRuns(turnID string) *turnRuns {
	h.turn = &turnRuns{}
	h.turnID = turnID
	return h.turn
}

// noteRun records a successful read of the tracked turn and tells the panel.
func (h *agentToolHandler) noteRun(req *agents.Request, resp *agents.Response) {
	if h.turn == nil || resp.Error != nil {
		return
	}
	if run, ok := h.turn.add(fmt.Sprint(req.ID), req.Method, resp.Result); ok {
		h.app.emitEvent("agent:receipt", map[string]any{"turnId": h.turnID, "run": run})
	}
}

// answerNumbers marks the numbers of a finished answer against the reads this
// turn made (llm.MarkNumbers), and returns those reads for the result.
func (t *turnRuns) answerNumbers(answer string) ([]llm.NumberMark, []ChatRun) {
	values, runs := t.snapshot()
	marks := llm.MarkNumbers(answer, values)
	t.mu.Lock()
	unrecorded := t.unrecorded
	t.mu.Unlock()
	if unrecorded == 0 {
		return marks, runs
	}
	// A read without a receipt hid its values from the check: keep the ticks and
	// the withheld numbers, drop every "not from a query" underline, and keep
	// the chips.
	kept := marks[:0]
	for _, m := range marks {
		if m.Matched || m.Withheld {
			kept = append(kept, m)
		}
	}
	return kept, runs
}
