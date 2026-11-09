package llm

import "testing"

// R5 (run receipts) leaves the grounding check as it was: the set of tools that
// count as having looked at the data is exactly these three, and reading a
// receipt back (get_receipt) is not looking at the data again. The number marks
// on an answer (numbermatch.go) are drawn after the check and never feed it.
func TestGroundingTools_ArePinnedAndGetReceiptDoesNotGround(t *testing.T) {
	if len(groundingTools) != 3 || !groundingTools["run_query"] || !groundingTools["run_saved_query"] || !groundingTools["preview_query"] {
		t.Fatalf("groundingTools = %v, want exactly run_query, run_saved_query, preview_query", groundingTools)
	}
	if usedGroundingTool([]ToolStep{{Tool: "get_receipt", Output: `{"id":"r_0123456789abcdef0123"}`}}) {
		t.Error("get_receipt must not count as grounding")
	}
	if !usedGroundingTool([]ToolStep{{Tool: "run_saved_query"}}) {
		t.Error("run_saved_query grounds")
	}
	// An answer that states a number with no read this turn still asks for a recheck.
	if !assertsFact("There were 1,234 orders last month in total.") {
		t.Error("a bare number is a claim")
	}
}
