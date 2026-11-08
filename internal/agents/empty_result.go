package agents

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/session"
)

// explainEmptyResult is ported from schoolwork-workspace's
// src/agent/empty-result.js. The incident it fixes: a model wrote
// `Attended = 'Yes'` against a column that actually held `Y`/`N`, got an
// empty result back, and told a user "nobody attended" — a false finding
// from a wrong literal guess, not a true absence of data. A zero-row result
// carries its own diagnosis: for every column the query compared against a
// string literal, the distinct values that column actually holds, so the
// model (or a person reading the tool step) can correct the query instead
// of reporting a false negative.
//
// This must run BEFORE budgetRows (an empty result is never truncated
// anyway) and must never execute anything beyond a handful of read-only,
// internally-constructed SELECT DISTINCT probes against real
// (information_schema-verified) columns — never the caller's SQL text
// itself beyond parser-extracted identifiers, and every identifier is
// quoted via session.QuoteIdent before it reaches a query string.
const emptyResultMaxDistinctValues = 40
const emptyResultMaxValuesShown = 25

// emptyResultDeadline bounds all the probes of one note.
const emptyResultDeadline = 4 * time.Second

// explainEmptyResult returns advisory text to append to a zero-row run_query
// response, or "" when there is nothing useful to say (no literal comparisons
// found, every literal is actually present, or a column can't be resolved to
// exactly one of the statement's own tables). rows is the result the query
// returned: an empty result, or the single 0 of an ungrouped count(*), is what
// the note explains.
//
// The statement is parsed by DuckDB (the same hardened parser the SQL policy
// uses) and each compared column is resolved against the tables the statement
// itself names, catalog-qualified: through the alias it was written with, or,
// for a bare name, only when exactly one of those tables has that column
// (#216). userDB is queried read-only, using only information_schema-verified
// table/column names, each part quoted via session.QuoteIdent.
// sqlQueryer is what the empty-result probes need: a *sql.DB, or a *sql.Conn
// pinned to one catalog.
type sqlQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func explainEmptyResult(ctx context.Context, userDB sqlQueryer, sqlText string, rows [][]any) string {
	if userDB == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, emptyResultDeadline)
	defer cancel()
	shape, ok := statementShape(ctx, sqlText)
	if !ok || len(shape.preds) == 0 {
		return ""
	}
	what := "matched 0 rows"
	switch {
	case len(rows) == 0:
	case shape.countOnly && isZeroCount(rows):
		what = "counted 0 rows"
	default:
		return ""
	}

	var notes []string
	for _, pred := range shape.preds {
		tbl, ok := shape.resolve(ctx, userDB, pred)
		if !ok {
			continue
		}
		if !localCatalog(ctx, userDB, tbl.catalog) {
			continue
		}
		if note := mismatchNote(ctx, userDB, tbl, pred); note != "" {
			notes = append(notes, note)
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return fmt.Sprintf("This query %s, and at least one literal compared against does not appear in the data:\n%s\nRe-run with a value the column actually holds before drawing any conclusion. Do not report \"none\" or \"never\" as a finding based on this empty result.",
		what, strings.Join(notes, "\n"))
}

// isZeroCount reports whether rows is the single 0 an ungrouped count returns.
func isZeroCount(rows [][]any) bool {
	if len(rows) != 1 || len(rows[0]) != 1 {
		return false
	}
	switch v := rows[0][0].(type) {
	case int64:
		return v == 0
	case int:
		return v == 0
	case int32:
		return v == 0
	case uint64:
		return v == 0
	}
	return false
}

// mismatchNote is the line for one compared column: the literals it was
// compared with that the column does not hold, next to what it does hold.
func mismatchNote(ctx context.Context, db sqlQueryer, tbl probeTable, pred literalPredicate) string {
	actual, ok := distinctValues(ctx, db, tbl, tbl.column)
	if !ok || len(actual) == 0 {
		return ""
	}
	have := map[string]bool{}
	for _, v := range actual {
		have[v] = true
	}
	var missed []string
	for _, v := range pred.values {
		if !have[v] {
			missed = append(missed, fmt.Sprintf("'%s'", v))
		}
	}
	if len(missed) == 0 {
		return ""
	}
	shownN := min(len(actual), emptyResultMaxValuesShown)
	shown := make([]string, shownN)
	for i := range shown {
		shown[i] = fmt.Sprintf("'%s'", actual[i])
	}
	more := ""
	if len(actual) > emptyResultMaxValuesShown {
		more = fmt.Sprintf(", … (%d distinct)", len(actual))
	}
	return fmt.Sprintf("  %s (%s): you compared against %s, but this column holds %s%s",
		tbl.column, tbl.String(), strings.Join(missed, ", "), strings.Join(shown, ", "), more)
}

// distinctValues returns the distinct string values a column holds, or
// ok=false when the column has too many distinct values to be a useful
// hint (an id or free-text column) or the probe query fails.
func distinctValues(ctx context.Context, db sqlQueryer, tbl probeTable, column string) ([]string, bool) {
	qCol := session.QuoteIdent(column)
	qTable := tbl.quoted()

	var n int
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(DISTINCT %s) FROM %s", qCol, qTable)).Scan(&n)
	if err != nil || n == 0 || n > emptyResultMaxDistinctValues {
		return nil, false
	}

	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		"SELECT DISTINCT CAST(%s AS VARCHAR) FROM %s WHERE %s IS NOT NULL ORDER BY 1 LIMIT %d",
		qCol, qTable, qCol, emptyResultMaxDistinctValues))
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}
