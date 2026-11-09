package llm

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"charm.land/fantasy"

	"github.com/SandwichLabs/slaw/internal/agents"
)

// toolGroup is one labelled group in the chat prompt's tool map.
type toolGroup struct {
	label, hint string
	names       []string
}

// toolGroups returns the compact tool map the chat prompt carries. The provider
// already receives every tool's full definition (handlerTool.Info), so the
// prompt only adds what the definitions cannot: grouping and the order to use
// them in. A tool in the registry that no group names still gets listed (under
// "Other"), and a name here that the chat registry lacks is skipped, so the map
// can neither hide a tool nor promise one that does not exist.
func toolGroups() []toolGroup {
	return []toolGroup{
		{"Look first", "start here; read before you write", []string{"list_connections", "introspect_schema", "describe_table", "list_notebooks", "get_notebook", "list_queries", "list_artifacts", "get_artifact", "help_search"}},
		{"Skills", "read_skill before relying on one", []string{"list_skills", "read_skill", "create_or_update_skill"}},
		{"Query", "", []string{"run_query", "run_saved_query", "preview_query", "get_receipt"}},
		{"Rows", "", []string{"create_record", "update_record", "delete_record", "bulk_insert"}},
		{"Schema", "propose_schema first, then create_table once the user confirms", []string{"propose_schema", "create_table", "add_column", "drop_table", "import_csv"}},
		{"Build and show", "", []string{"build_query", "save_query", "present_data", "add_query_cell", "add_markdown_cell", "create_dashboard", "write_artifact"}},
		{"Meaning", "", []string{"list_definitions", "get_definition", "propose_definition"}},
		{"Watches", "", []string{"list_watches", "get_watch", "write_watch"}},
		{"Edit a notebook", "get_notebook first for the rev", []string{"update_cell", "delete_cell", "move_cell"}},
	}
}

// toolMapBlock renders the compact tool map plus the names the sandbox denies.
func toolMapBlock(sc *agents.SandboxConfig) string {
	chat := agents.ListToolsForMode(agents.ModeChat)
	have := make(map[string]bool, len(chat))
	for _, td := range chat {
		have[td.Name] = true
	}
	listed := map[string]bool{}
	var b strings.Builder
	b.WriteString("\n\nYour tools (their full definitions are supplied with this request; this is only the map):\n")
	for _, g := range toolGroups() {
		var names []string
		for _, n := range g.names {
			if have[n] {
				names = append(names, n)
				listed[n] = true
			}
		}
		writeToolGroup(&b, g.label, g.hint, names)
	}
	var other []string
	for _, td := range chat {
		if !listed[td.Name] {
			other = append(other, td.Name)
		}
	}
	writeToolGroup(&b, "Other", "", other)

	var denied []string
	for _, td := range chat {
		if sc.Check(td.Name) != nil {
			denied = append(denied, td.Name)
		}
	}
	if len(denied) > 0 {
		fmt.Fprintf(&b, "NOT PERMITTED in your current sandbox (calling these will be denied): %s.\n", strings.Join(denied, ", "))
	}
	return b.String()
}

func writeToolGroup(b *strings.Builder, label, hint string, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(b, "- %s: %s", label, strings.Join(names, ", "))
	if hint != "" {
		fmt.Fprintf(b, " (%s)", hint)
	}
	b.WriteString("\n")
}

// budgetLine tells the model its per-turn step budget (#211) so it plans for it.
func budgetLine() string {
	return fmt.Sprintf("\nSTEP BUDGET — a turn allows at most %d steps; tool calls you issue together in one step count once, so batch independent calls (several describe_table calls, say) instead of making them one at a time. If the job will not fit, finish a coherent part, say what remains, and the user can ask you to continue.\n", maxSteps)
}

// sqlStyleBlock is the DuckDB alias and parameter guidance (#212). The same
// rules sit in the built-in duckdb-sql skill.
const sqlStyleBlock = "\nSQL STYLE (DuckDB) — alias every aggregate and expression with AS, or the column comes back named like count_star(): `SELECT count(*) AS n`. Always write AS: a bare alias that is a keyword or type name is a parser error, e.g. `SELECT count(*) days` fails with 'syntax error at or near \"days\"' while `AS days` works (many keywords and type names fail bare: days, minutes, hours, year, value, name, type, time, ...; always write AS). run_query binds named parameters: write $name in the SQL and pass {\"name\": value} in params; do not use ? placeholders.\n"

// dataDirSentence says where the workspace data/ folder is, for the CONNECTIONS
// AND FILES paragraph. A path with control characters is not echoed into the
// prompt; the model is then pointed at list_connections as before.
func dataDirSentence(dir string) string {
	if !safePromptPath(dir) {
		return "list_connections returns its path"
	}
	return "it is " + dir
}

// dataDirExample is the folder to show in the read_csv example.
func dataDirExample(dir string) string {
	if !safePromptPath(dir) {
		return "/path/to/workspace/data"
	}
	return strings.TrimRight(dir, "/")
}

// safePromptPath reports whether dir is a non-empty absolute path that is safe
// to print into the prompt and into a single-quoted SQL literal.
func safePromptPath(dir string) bool {
	if dir == "" || (!strings.HasPrefix(dir, "/") && !(len(dir) > 2 && dir[1] == ':')) {
		return false
	}
	for _, r := range dir {
		if unicode.IsControl(r) || r == '\'' || r == '`' {
			return false
		}
	}
	return true
}

// dataDirFor reads the workspace data/ folder from the shared list_connections
// tool (the same data_dir an MCP client sees). Any failure yields "".
func (r *Runner) dataDirFor(ctx context.Context) string {
	resp := r.handler.Handle(ctx, &agents.Request{
		JSONRPC: "2.0", ID: "system-prompt-datadir", Method: "list_connections", Params: map[string]any{},
	})
	if resp == nil || resp.Error != nil {
		return ""
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		return ""
	}
	dir, _ := result["data_dir"].(string)
	return dir
}

// stepLimitNote is the fixed text appended when a chat turn hits maxSteps while
// the model still wanted to call tools (#211). It names no model output.
func stepLimitNote() string {
	return fmt.Sprintf("Stopped after %d steps, the limit for one turn, before finishing. Ask me to continue.", maxSteps)
}

// chatStepsExhausted reports whether the step cap, not the model, ended the
// turn: the cap was reached and the last step still ended in tool calls.
func chatStepsExhausted(result *fantasy.AgentResult) bool {
	if result == nil || len(result.Steps) < maxSteps {
		return false
	}
	return len(result.Steps[len(result.Steps)-1].Content.ToolCalls()) > 0
}

// appendStepLimitNote adds the note to whatever text the turn produced.
func appendStepLimitNote(text string) string {
	if strings.TrimSpace(text) == "" {
		return stepLimitNote()
	}
	return strings.TrimRight(text, "\n ") + "\n\n" + stepLimitNote()
}
