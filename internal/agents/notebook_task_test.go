package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ToolDef.Modes / ListToolsForMode / IsModeVisible (#66 part B) ---

func TestListToolsForMode_TaskOnlyToolsHiddenFromChatAndMCP(t *testing.T) {
	for _, mode := range []string{ModeChat, ModeMCP, ""} {
		names := map[string]bool{}
		for _, td := range ListToolsForMode(mode) {
			names[td.Name] = true
		}
		for _, taskOnly := range []string{"write_cell", "need_more_info"} {
			assert.False(t, names[taskOnly], "mode %q must not list %q", mode, taskOnly)
		}
		// Chat/MCP-visible tools must still be there.
		assert.True(t, names["add_query_cell"])
		assert.True(t, names["create_dashboard"])
		assert.True(t, names["preview_query"], "preview_query is a read, offered everywhere (#220)")
		assert.True(t, names["introspect_schema"])
	}
}

func TestListToolsForMode_TaskModeSeesTaskOnlyToolsPlusShared(t *testing.T) {
	names := map[string]bool{}
	for _, td := range ListToolsForMode(ModeTask) {
		names[td.Name] = true
	}
	for _, want := range []string{"preview_query", "write_cell", "need_more_info", "introspect_schema", "describe_table", "read_skill"} {
		assert.True(t, names[want], "task mode should see %q", want)
	}
}

// TestListToolsForMode_TaskModeIsExactlyTheAllowlist is the CRITICAL review
// fix (finding 1): task mode must see EXACTLY the eight tools in
// TaskAllowedToolNames — nothing else, regardless of Category or a Modes
// field left empty. Before this fix, every Modes-unrestricted tool in the
// whole registry (create_table, drop_table, bulk_insert,
// add_query_cell, create_dashboard, create_form, ...) was visible to task
// mode, and — combined with AgentTask running the file's sandbox (often
// "full") — a task turn could call drop_table directly.
func TestListToolsForMode_TaskModeIsExactlyTheAllowlist(t *testing.T) {
	got := map[string]bool{}
	for _, td := range ListToolsForMode(ModeTask) {
		got[td.Name] = true
	}
	assert.Equal(t, TaskAllowedToolNames, got, "task mode's tool list must be EXACTLY TaskAllowedToolNames")

	// Explicitly assert the exact names the reviewer's fix demands, so a
	// future rename/typo in TaskAllowedToolNames itself still fails loudly.
	for _, want := range []string{"introspect_schema", "describe_table", "run_query", "list_skills", "read_skill", "preview_query", "write_cell", "need_more_info"} {
		assert.True(t, got[want], "task mode must see %q", want)
	}
	assert.Len(t, got, 8)

	// Every write/schema/page tool in the registry must be absent.
	for _, denied := range []string{"drop_table", "create_table", "add_column", "bulk_insert", "create_record", "update_record", "delete_record", "add_query_cell", "create_dashboard", "create_page", "update_page", "delete_page", "create_form", "create_relationship", "create_report", "create_switchboard", "create_macro", "create_or_update_skill"} {
		assert.False(t, got[denied], "task mode must NOT see %q", denied)
	}
}

// TestHandler_TaskMode_DeniesEveryToolOutsideTheAllowlist is the
// dispatch-level companion to the listing test above: even if a caller
// bypasses the tool list entirely and dispatches a denied method name
// directly while mode == ModeTask, Handler.Handle must still refuse it.
// This is what actually stops a "call drop_table" prompt injection from
// working, not just what the model is advertised.
func TestHandler_TaskMode_DeniesEveryToolOutsideTheAllowlist(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeFull) // even under a permissive file sandbox
	handler.SetMode(ModeTask)

	denied := []struct {
		method string
		params map[string]any
	}{
		{"drop_table", map[string]any{"name": "t"}},
		{"create_table", map[string]any{"name": "x", "columns": []any{map[string]any{"name": "id", "type": "INTEGER"}}}},
		{"bulk_insert", map[string]any{"table": "t", "columns": []any{"id"}, "rows": []any{[]any{1}}}},
		{"add_query_cell", map[string]any{"sql": "SELECT 1"}},
		{"create_dashboard", map[string]any{"page": map[string]any{"title": "x", "components": []any{}}}},
	}
	for _, d := range denied {
		resp := handler.Handle(context.Background(), &Request{JSONRPC: "2.0", ID: "1", Method: d.method, Params: d.params})
		require.NotNil(t, resp.Error, "%q must be denied from a task handler", d.method)
		assert.Equal(t, ErrMethodNotFound, resp.Error.Code, "%q", d.method)
	}
}

// import_csv was retired (#386): it split rows on bare commas. An agent loads
// a file with run_query over read_csv; people import through `slaw import`.
func TestRegistry_ImportCSVIsRetired(t *testing.T) {
	assert.Nil(t, GetTool("import_csv"))
	for _, td := range ListTools() {
		assert.NotEqual(t, "import_csv", td.Name)
	}
	handler, _ := newRealDuckDBHandler(t, ModeFull)
	resp := handler.Handle(context.Background(), &Request{JSONRPC: "2.0", ID: "1", Method: "import_csv", Params: map[string]any{"table": "t", "data": "a\n1"}})
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrMethodNotFound, resp.Error.Code)
}

// TestHandler_TaskMode_RunQueryDeniesWrite: run_query IS on the allow-list
// (it's how task mode samples data), but the task handler runs under a
// forced read-only SandboxConfig (studio/app.go's AgentTask), so even a
// direct run_query DML/DDL call must be denied — the allow-list alone only
// controls which METHOD NAMES are reachable, not what a reachable run_query
// call may do.
func TestHandler_TaskMode_RunQueryDeniesWrite(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly) // mirrors AgentTask's forced read-only sandbox
	handler.SetMode(ModeTask)

	for _, sqlStr := range []string{"DELETE FROM t", "DROP TABLE t", "INSERT INTO t VALUES (99, 'x')"} {
		resp := handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "run_query", Params: map[string]any{"sql": sqlStr},
		})
		require.NotNil(t, resp.Error, "run_query %q must be denied under the task handler's read-only sandbox", sqlStr)
	}

	// A read stays allowed.
	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "2", Method: "run_query", Params: map[string]any{"sql": "SELECT * FROM t"},
	})
	assert.Nil(t, resp.Error)
}

func TestIsModeVisible(t *testing.T) {
	assert.True(t, IsModeVisible("preview_query", ModeTask))
	assert.True(t, IsModeVisible("preview_query", ModeChat), "preview_query is a read, offered in chat and MCP too (#220)")
	assert.True(t, IsModeVisible("preview_query", ""))
	assert.True(t, IsModeVisible("write_cell", ModeTask))
	assert.False(t, IsModeVisible("write_cell", ModeChat))
	assert.False(t, IsModeVisible("write_cell", ""))
	assert.True(t, IsModeVisible("introspect_schema", ""), "unrestricted tools are visible in every mode, including the zero value")
	assert.False(t, IsModeVisible("no-such-tool", ModeTask))
}

// TestHandler_DenyTaskOnlyToolsOutsideTaskMode is the dispatch-level (not
// just listing-level) gate: even if a caller bypasses the advertised tool
// list and dispatches "write_cell" directly, Handler.Handle must still deny
// it unless SetMode(ModeTask) was called.
func TestHandler_DenyTaskOnlyToolsOutsideTaskMode(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeFull)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "markdown", "markdown": "hi"},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrMethodNotFound, resp.Error.Code)

	handler.SetMode(ModeTask)
	resp = handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "2", Method: "write_cell",
		Params: map[string]any{"cell_type": "markdown", "markdown": "hi"},
	})
	require.Nil(t, resp.Error)
	assert.True(t, resp.StopTurn)
}

// TestHandler_SetActiveNotebook_ConcurrentSafe is review fix finding 8: a
// DATA RACE on the long-lived MCP handler. StartMCPServer builds ONE
// *agents.Handler for the server's whole lifetime, and mark3labs/mcp-go
// dispatches each tools/call on its own goroutine — concurrent
// SetActiveNotebook calls (from studio/app.go's agentToolHandler.Handle, one
// per in-flight MCP request) previously wrote the same unsynchronized field
// with no lock. Run with -race to catch it; also exercises
// resolveActiveNotebook (the read side, called from
// resolveTargetNotebook during a concurrent add_query_cell/create_dashboard
// dispatch).
func TestHandler_SetActiveNotebook_ConcurrentSafe(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeFull)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			handler.SetActiveNotebook(fmt.Sprintf("nb-%d", i))
		}(i)
		go func() {
			defer wg.Done()
			_ = handler.resolveActiveNotebook()
		}()
	}
	wg.Wait()
}

// --- write_cell ---

func TestHandleWriteCell_SQLRequiresNonEmptySQL(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "sql", "sql": "  "},
	})
	require.NotNil(t, resp.Error, "an sql cell with empty sql must be rejected")
	assert.False(t, resp.StopTurn)
}

func TestHandleWriteCell_MarkdownRequiresNonEmptyMarkdown(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "markdown", "explanation": "some prose here"},
	})
	require.NotNil(t, resp.Error, "a markdown cell with prose only in explanation (not markdown) must be rejected")
}

func TestHandleWriteCell_UnknownCellType(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "chart"},
	})
	require.NotNil(t, resp.Error)
}

func TestHandleWriteCell_SQLSuccess(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	// write_cell now requires the exact sql to have been preview_query'd
	// this turn (review fix, finding 4).
	previewResp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "0", Method: "preview_query",
		Params: map[string]any{"sql": "SELECT * FROM t"},
	})
	require.Nil(t, previewResp.Error)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "sql", "sql": "SELECT * FROM t", "view": "bogus", "label": "All rows"},
	})
	require.Nil(t, resp.Error)
	assert.True(t, resp.StopTurn)
	result, _ := resp.Result.(map[string]any)
	assert.Equal(t, "cell", result["outcome"])
	cell, _ := result["cell"].(map[string]any)
	assert.Equal(t, "sql", cell["type"])
	assert.Equal(t, "SELECT * FROM t", cell["sql"])
	assert.Equal(t, "table", cell["view"], "an invalid view falls back to table")
	assert.Equal(t, "All rows", cell["label"])
}

func TestHandleWriteCell_RejectsSQLNotPreviewedThisTurn(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "sql", "sql": "SELECT * FROM t"},
	})
	require.NotNil(t, resp.Error, "sql that was never preview_query'd this turn must be rejected")
	assert.False(t, resp.StopTurn)
}

func TestHandleWriteCell_RejectsSQLThatDiffersFromWhatWasPreviewed(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	previewResp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "0", Method: "preview_query",
		Params: map[string]any{"sql": "SELECT * FROM t"},
	})
	require.Nil(t, previewResp.Error)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "sql", "sql": "SELECT * FROM t WHERE id = 1"},
	})
	require.NotNil(t, resp.Error, "a different sql than what was previewed must be rejected even if it's read-only")
}

func TestHandleWriteCell_RejectsWriteSQLEvenIfSomehowPreviewed(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)
	// Simulate a write string having been marked previewed some other way
	// (defense in depth: write_cell must classify independently, never
	// trust the previewed-set alone).
	handler.markPreviewed("", "DELETE FROM t")

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "write_cell",
		Params: map[string]any{"cell_type": "sql", "sql": "DELETE FROM t"},
	})
	require.NotNil(t, resp.Error, "write_cell must independently classify its sql as read-only")
}

// --- need_more_info ---

func TestHandleNeedMoreInfo_RequiresAReason(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "need_more_info", Params: map[string]any{},
	})
	require.NotNil(t, resp.Error, "a bare decline with no question/notes/missing_data must be rejected")
}

func TestHandleNeedMoreInfo_Success(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "need_more_info",
		Params: map[string]any{"questions": []any{"Which region column?"}},
	})
	require.Nil(t, resp.Error)
	assert.True(t, resp.StopTurn)
	result, _ := resp.Result.(map[string]any)
	assert.Equal(t, "needs_info", result["outcome"])
}

// --- preview_query ---

func TestHandlePreviewQuery_DeniesWrite(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeFull)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "preview_query",
		Params: map[string]any{"sql": "DELETE FROM t"},
	})
	require.NotNil(t, resp.Error, "preview_query must deny a write statement even under a full sandbox")
	assert.Equal(t, ErrPermission, resp.Error.Code)
}

func TestHandlePreviewQuery_StatShape(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "preview_query",
		Params: map[string]any{"sql": "SELECT COUNT(*) AS n FROM t"},
	})
	require.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	assert.Equal(t, "stat", result["suggestedView"])
	assert.EqualValues(t, 1, result["rowCount"])
}

func TestHandlePreviewQuery_TableShapeForSingleTextColumn(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "preview_query",
		Params: map[string]any{"sql": "SELECT name FROM t"},
	})
	require.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	assert.Equal(t, "table", result["suggestedView"])
	viewFit, _ := result["viewFit"].(map[string]any)
	assert.Equal(t, true, viewFit["table"])
	assert.NotEqual(t, true, viewFit["bar"], "bar should not fit a single all-text column")
}

// TestHandlePreviewQuery_CapsHugeResultFast is review fix finding 3:
// preview_query must push a row cap down into the query (via
// RunReadOnlySQL, the same path RunA2UIQuery uses) rather than materializing
// an enormous result before truncating client-side. range(1e9) would hang
// or OOM the old implementation; this must return promptly and report
// truncated=true with rowCount capped at previewQueryRowCap.
func TestHandlePreviewQuery_CapsHugeResultFast(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	done := make(chan *Response, 1)
	go func() {
		done <- handler.Handle(context.Background(), &Request{
			JSONRPC: "2.0", ID: "1", Method: "preview_query",
			Params: map[string]any{"sql": "SELECT * FROM range(1000000000)"},
		})
	}()

	select {
	case resp := <-done:
		require.Nil(t, resp.Error)
		result, _ := resp.Result.(map[string]any)
		rowCount, _ := result["rowCount"].(int)
		assert.LessOrEqual(t, rowCount, previewQueryRowCap+1)
		assert.Equal(t, true, result["truncated"])
	case <-time.After(10 * time.Second):
		t.Fatal("preview_query on range(1e9) did not return within 10s — row cap/LIMIT push-down is not working")
	}
}

func TestHandlePreviewQuery_ReturnsColumnsWithTypes(t *testing.T) {
	handler, _ := newRealDuckDBHandler(t, ModeReadOnly)
	handler.SetMode(ModeTask)

	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: "1", Method: "preview_query",
		Params: map[string]any{"sql": "SELECT id, name FROM t"},
	})
	require.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	cols, _ := result["columns"].([]map[string]any)
	require.Len(t, cols, 2)
	assert.Equal(t, "id", cols[0]["name"])
	assert.NotEmpty(t, cols[0]["type"])
}

// --- notebookViews Go port parity (mirrors studio/frontend/src/lib/
// notebookViews.spec.ts's key cases) ---

func TestBestViewFor_Parity(t *testing.T) {
	cases := []struct {
		name string
		cols []string
		rows [][]any
		want string
	}{
		{"single number single row -> stat", []string{"total"}, [][]any{{42}}, "stat"},
		{"single text single row -> table", []string{"name"}, [][]any{{"Ada"}}, "table"},
		{"two rows -> not stat", []string{"total"}, [][]any{{1}, {2}}, "table"},
		{"label+number distinct labels -> bar", []string{"region", "revenue"}, [][]any{{"East", 1}, {"West", 2}, {"North", 3}}, "bar"},
		{"date label+number -> line", []string{"month", "revenue"}, [][]any{{"Jan", 1}, {"Feb", 2}, {"Mar", 3}}, "line"},
		{"non-distinct first col -> table", []string{"region", "revenue"}, [][]any{{"East", 1}, {"East", 2}}, "table"},
		{"more than 25 rows -> table", manyRowsCols(), manyRows(30), "table"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shape := describeViewShape(c.cols, c.rows, nil)
			assert.Equal(t, c.want, bestViewFor(shape))
		})
	}
}

func manyRowsCols() []string { return []string{"label", "n"} }

func manyRows(n int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = []any{i, i}
	}
	return rows
}

func TestCheckViewFit_Parity(t *testing.T) {
	textOnly := describeViewShape([]string{"name"}, [][]any{{"Ada"}, {"Bo"}}, nil)
	assert.False(t, checkViewFit("bar", textOnly).OK)
	assert.True(t, checkViewFit("table", textOnly).OK)

	oneCol := describeViewShape([]string{"n"}, [][]any{{1}}, nil)
	assert.True(t, checkViewFit("stat", oneCol).OK)
	assert.False(t, checkViewFit("bar", oneCol).OK, "bar needs at least two columns")

	empty := describeViewShape([]string{}, nil, nil)
	assert.False(t, checkViewFit("table", empty).OK)
}

// TestBestViewFor_JSONNumberDecimal proves that engine.NormalizeValue
// hands a small-magnitude DECIMAL back as json.Number, not a Go float,
// so a (label, DECIMAL) shape — the common case for a money SUM — must
// still be judged numeric and suggest "bar", exactly like a plain int/float
// column already does above.
func TestBestViewFor_JSONNumberDecimal(t *testing.T) {
	cols := []string{"region", "revenue"}
	rows := [][]any{
		{"East", json.Number("1500.30")},
		{"West", json.Number("2000.00")},
		{"North", json.Number("999.99")},
	}
	shape := describeViewShape(cols, rows, nil)
	assert.True(t, shape.numeric["revenue"], "a json.Number column must be judged numeric")
	assert.Equal(t, "bar", bestViewFor(shape))
	assert.True(t, checkViewFit("bar", shape).OK)
}

// The KINETIC views: label-then-numbers charts share bar/line's rule (plus
// radar's axis minimum and the rings' maximum); sankey and heatmap want three
// columns with a numeric third.
func TestCheckViewFit_KineticViews(t *testing.T) {
	series := describeViewShape([]string{"region", "a", "b"}, [][]any{{"x", 1, 2}, {"y", 3, 4}}, nil)
	for _, v := range []string{"area", "columns", "rings"} {
		assert.True(t, checkViewFit(v, series).OK, v)
	}
	assert.False(t, checkViewFit("radar", series).OK, "radar needs three axes")

	textOnly := describeViewShape([]string{"name"}, [][]any{{"Ada"}}, nil)
	assert.False(t, checkViewFit("area", textOnly).OK)

	manyRows := describeViewShape([]string{"k", "v"}, seq(7), nil)
	assert.False(t, checkViewFit("rings", manyRows).OK)
	assert.True(t, checkViewFit("radar", manyRows).OK)

	flows := describeViewShape([]string{"src", "dst", "w"}, [][]any{{"a", "b", 3}}, nil)
	assert.True(t, checkViewFit("sankey", flows).OK)
	assert.True(t, checkViewFit("heatmap", flows).OK)
	textWeight := describeViewShape([]string{"src", "dst", "note"}, [][]any{{"a", "b", "c"}}, nil)
	assert.False(t, checkViewFit("sankey", textWeight).OK, "third column must be numeric")
	assert.False(t, checkViewFit("heatmap", manyRows).OK, "needs three columns")

	for _, v := range []string{"area", "columns", "rings", "radar", "sankey", "heatmap"} {
		assert.Equal(t, v, normalizeCellView(" "+strings.ToUpper(v)+" "))
	}
	assert.Equal(t, "table", normalizeCellView("pie"))
}

func seq(n int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = []any{fmt.Sprintf("k%d", i), i}
	}
	return rows
}
