package a2ui

import (
	"fmt"
	"strings"
)

// paramTypeNames lists the manifest parameter types a kindParam field accepts.
func paramTypeNames(spec fieldSpec) []string {
	out := make([]string, len(spec.values))
	for i, v := range spec.values {
		out[i] = fmt.Sprint(v)
	}
	return out
}

// ViewTypeNames returns the view-only component types (controls and pivot) in
// reading order (a copy), so docs are generated from the registry.
func ViewTypeNames() []string { return append([]string(nil), viewTypeOrder...) }

// SkillBody renders the markdown body of the built-in a2ui skill for MCP
// clients, which never see the in-app <a2ui> prompt block. It is generated
// from the same registries Validate and ValidateView check against (the
// PromptBlock text, Components, ViewComponents and the view variants), so it
// cannot drift; only the prose around them is hand-written.
func SkillBody() string {
	var b strings.Builder
	b.WriteString("# A2UI components\n\n")
	b.WriteString("A2UI is the closed, validated component schema for dashboards. A page is\n")
	b.WriteString("`{\"title\": ..., \"components\": [...]}`. It goes to one of three places, and the set of\n")
	b.WriteString("valid component types differs:\n\n")
	b.WriteString("- `present_data` (an inline reply, Studio chat only) and `create_dashboard` (a dashboard cell in a notebook)\n")
	b.WriteString("  take the **notebook dashboard** types below: " + codeNames(typeOrder) + ".\n")
	b.WriteString("  Data comes from inline values or a live `sql` binding.\n")
	b.WriteString("- `write_artifact` (a data artifact's `view`, view: a2ui) takes the same types with `sql` and\n")
	b.WriteString("  `connection` replaced by `query` (the name of one of the manifest's named queries), plus the\n")
	b.WriteString("  **data artifact view** types " + codeNames(viewTypeOrder) + " and a `drill` field on\n")
	b.WriteString("  " + codeNames(drillable) + ". Read the `data-artifacts` skill for the manifest.\n")
	b.WriteString("- The view-only types (controls, `pivot`) and `query`/`drill` are refused in a notebook dashboard;\n")
	b.WriteString("  `sql` and `connection` are refused in an artifact view.\n\n")
	b.WriteString("Every field is checked; an unknown type or field is refused with the list of allowed ones.\n\n")
	b.WriteString("## Notebook dashboard components (present_data, create_dashboard)\n\n")
	b.WriteString(PromptBlock())
	b.WriteString("\n\n`create_dashboard` vets every `sql` binding like `add_query_cell` does: the SQL must be a\n")
	b.WriteString("single read-only statement that binds (a bad column is refused with the binder's candidates),\n")
	b.WriteString("and every failure is reported by component path.\n\n")
	b.WriteString("## Data artifact view components (write_artifact)\n\n")
	b.WriteString("Types and fields (required marked), as written in a `view.a2ui.json`:\n\n")
	for _, t := range viewTypeNames {
		fmt.Fprintf(&b, "  %-12s %s\n", t, fieldList(viewSpecs[t]))
	}
	b.WriteString("\nIn a view, a data-bound component names a manifest query with `query`; it has no `sql`,\n")
	b.WriteString("`connection` or `refresh`. A control's `param` (or a `drill`'s `param`) names a manifest\n")
	b.WriteString("parameter of a fitting type: `select` takes an enum or boolean parameter, `numberInput` a number\n")
	b.WriteString("or integer, `textInput` text, and `dateRange` two different date parameters (`start`, `end`).\n")
	b.WriteString("`pivot` lays out a query's result: `rows` (up to " + fmt.Sprint(maxPivotRows) + " column names), one `columns` column and a\n")
	b.WriteString("`value` column, each a different column. `drill` is `{param, column}`: clicking a row or bar\n")
	b.WriteString("sets that parameter to the clicked row's value of that column; it needs a `query`.\n\n")
	b.WriteString(annotationsSection())
	return b.String()
}

func codeNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "`" + n + "`"
	}
	return strings.Join(out, ", ")
}
