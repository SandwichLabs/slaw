package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2" // DuckDB driver, for the dedicated parser connection below

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// --- the plan check (second layer) ------------------------------------------

// planNode is one node of EXPLAIN (FORMAT JSON)'s physical_plan tree.
type planNode struct {
	Name      string         `json:"name"`
	Children  []planNode     `json:"children"`
	ExtraInfo map[string]any `json:"extra_info"`
}

var planDeniedFuncRe = regexp.MustCompile(`(?i)(^|[^a-z0-9_])(getenv|current_setting|duckdb_secrets|which_secret|duckdb_settings|duckdb_extensions|postgres_query|postgres_execute|postgres_scan_pushdown|mysql_query|mysql_execute|mysql_clear_cache|query_table|(md|motherduck)_[a-z0-9_]*)\s*\(`)

// explainPrefix is what planCheck writes before the agent's statement. It is the
// policy's own wrapper: an error DuckDB raises for the statement quotes it, and
// the agent should see its own SQL.
const explainPrefix = "EXPLAIN (FORMAT JSON) "

// stripExplainWrapper removes explainPrefix from a DuckDB error. The error shows
// the statement on a "LINE n: ..." line with a caret under the failing token on
// the next; the caret is moved left by the same amount so it still points at the
// token. A long statement is shown elided ("LINE 1: ...text"), which carries no
// prefix and needs nothing.
func stripExplainWrapper(msg string) string {
	lines := strings.Split(msg, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "LINE ") {
			continue
		}
		j := strings.Index(l, ": ")
		if j < 0 || !strings.HasPrefix(l[j+2:], explainPrefix) {
			continue
		}
		lines[i] = l[:j+2] + l[j+2+len(explainPrefix):]
		if i+1 < len(lines) {
			if caret := lines[i+1]; strings.HasPrefix(caret, strings.Repeat(" ", len(explainPrefix))) {
				lines[i+1] = caret[len(explainPrefix):]
			}
		}
		break
	}
	return strings.ReplaceAll(strings.Join(lines, "\n"), explainPrefix, "")
}

// planCheck runs EXPLAIN (FORMAT JSON) for stmt on the user's real connection —
// ONLY after the syntactic checks have passed, because binding has side effects
// (EXPLAIN SELECT * FROM read_csv('https://...') sniffs the file and sends an
// HTTP request; spike #107 finding B) — and walks the physical plan. The plan
// reveals what the AST cannot: a view or macro that expands to a reader or a
// table function. It fails closed: an operator not on the allow-list, a plan
// that cannot be read, or more reader operators than the statement wrote out
// (and vetted) is a denial.
//
// Plain EXPLAIN does not execute anything; EXPLAIN ANALYZE would, and is never
// used here.
func planCheck(ctx context.Context, userDB queryDB, stmt string, args []any, vettedReaders int) (reason string, kind defKind) {
	if nilDB(userDB) {
		return "could not validate query: no database connection available", defUnchecked
	}
	rows, err := userDB.QueryContext(ctx, explainPrefix+stmt, args...)
	if err != nil {
		return fmt.Sprintf("could not validate query: %s", stripExplainWrapper(secrets.RedactPatterns(err.Error()))), failureKind(err)
	}
	defer rows.Close() //nolint:errcheck

	var planJSON string
	found := false
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Sprintf("could not validate query: %v", err), defUnchecked
		}
		if key == "physical_plan" {
			planJSON = value
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("could not validate query: %s", stripExplainWrapper(secrets.RedactPatterns(err.Error()))), failureKind(err)
	}
	if !found {
		return "could not validate query: EXPLAIN (FORMAT JSON) returned no physical_plan", defUnchecked
	}
	if planDeniedFuncRe.MatchString(planJSON) {
		return externalAccessDeniedMsg, 0
	}
	var nodes []planNode
	if err := json.Unmarshal([]byte(planJSON), &nodes); err != nil {
		return externalAccessDeniedMsg, 0
	}
	readers := 0
	for i := range nodes {
		if reason := checkPlanNode(&nodes[i], &readers); reason != "" {
			return reason, 0
		}
	}
	// Two budgets. Raw readers (everything but a catalog scan) may not exceed the
	// readers the statement wrote out as literals; ICEBERG_SCAN nodes, which an
	// attached catalog's table and a raw iceberg_scan('<path>') both plan as, may
	// spend the literals not used by other readers plus one credit per catalog
	// reference. A catalog reference's credit is spendable only on ICEBERG_SCAN.
	if readersExceedBudget(readers, vettedReaders) {
		return "this query reads a file or URL that is not written out as a literal inside the statement (a view or macro that wraps a reader?), so it can't be verified", 0
	}
	return "", 0
}

// catalogScanUnit is the weight of one reference into an attached Iceberg
// catalog in a reader count (a plain reader weighs 1): the count is one int, so
// the two budgets travel through every place that sums it. splitReaders takes it
// apart: (plain readers, catalog scans).
const catalogScanUnit = 1 << 16

func splitReaders(v int) (plain, catalogScans int) {
	return v & (catalogScanUnit - 1), v >> 16
}

// readersExceedBudget is the reader-count rule over the two budgets (see
// planCheck): plain readers may not exceed the literals the statement wrote out,
// and ICEBERG_SCANs may spend the unused literals plus the catalog references.
func readersExceedBudget(planReaders, vetted int) bool {
	other, scans := splitReaders(planReaders)
	raw, catalog := splitReaders(vetted)
	return other > raw || other+scans > raw+catalog
}

// planOpName is an operator's name without MotherDuck's location marker. With
// an md: catalog attached, hybrid execution labels every operator in every
// plan with where it runs: "HASH_GROUP_BY (L)" on this computer, "SEQ_SCAN (R)"
// on MotherDuck's server (#111). The marker says nothing about what the
// operator reads, so the operator is judged by its name alone.
func planOpName(raw string) string {
	name := strings.ToUpper(strings.TrimSpace(raw))
	for _, loc := range []string{"(L)", "(R)"} {
		if base, ok := strings.CutSuffix(name, loc); ok && strings.HasSuffix(base, " ") {
			return strings.TrimSpace(base)
		}
	}
	return name
}

func checkPlanNode(n *planNode, readers *int) string {
	name := planOpName(n.Name)
	table, hasTable := n.ExtraInfo["Table"].(string)
	_, hasFunc := n.ExtraInfo["Function"]
	switch {
	case name == "ICEBERG_SCAN":
		*readers += catalogScanUnit // one scan: see planCheck for the budgets
	case readerPlanOps[name]:
		*readers++
	case hasTable && table != "" && !hasFunc && strings.HasSuffix(name, "_SCAN"):
		// A scan of a catalog table: SEQ_SCAN, TABLE_SCAN, POSTGRES_SCAN, ...
	case allowedPlanOperators[name], isRemoteWriteOp(name):
	default:
		return fmt.Sprintf("this query uses a plan operator the agent's policy does not know (%s is not on the allow-list), so it can't be verified", name)
	}
	for i := range n.Children {
		if reason := checkPlanNode(&n.Children[i], readers); reason != "" {
			return reason
		}
	}
	return ""
}
