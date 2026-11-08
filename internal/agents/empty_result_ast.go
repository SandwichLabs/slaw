package agents

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
)

// The parse half of the empty-result note (#216): which string literals a
// statement compares which columns against, and which tables it reads from.
// Both come from DuckDB's own parser (serializeSQL, the hardened connection
// that never binds), so an alias, a three-part name or a quoted identifier is
// read the way DuckDB reads it.

// literalPredicate is a column the statement compared against string literals.
type literalPredicate struct {
	qualifier string // the alias or table name written before the column, or ""
	column    string
	values    []string
	pos       float64 // where the statement first compares it, to keep the note in reading order
}

// tableSource is one plain table the statement reads, as it was written.
type tableSource struct {
	catalog, schema, table, alias string
}

// emptyShape is what the note needs to know about a statement.
type emptyShape struct {
	preds   []literalPredicate
	sources []tableSource
	// opaque is set when the statement also reads something that is not a
	// plain table (a subquery, a table function, a CTE, a PIVOT): a bare
	// column name could belong to it, so only a qualified one is resolved.
	opaque bool
	// countOnly is set for a single ungrouped count in the select list, whose
	// 0 is as empty as a result with no rows.
	countOnly bool
}

// opaqueNodeTypes are the nodes that read something other than a plain table.
var opaqueNodeTypes = nameSet("SUBQUERY", "TABLE_FUNCTION", "EXPRESSION_LIST", "PIVOT", "SHOW_REF", "COLUMN_DATA", "DELIM_GET", "CTE")

// statementShape parses sqlText and reads its shape. ok is false when it is not
// one statement DuckDB can serialize (the note then stays silent).
func statementShape(ctx context.Context, sqlText string) (*emptyShape, bool) {
	doc, err := serializeSQL(ctx, sqlText)
	if err != nil || doc.Error || len(doc.Statements) != 1 {
		return nil, false
	}
	stmt := doc.Statements[0]
	cte := map[string]bool{}
	collectCTENames(stmt, cte)
	b := &shapeBuilder{shape: &emptyShape{}, cte: cte, byKey: map[string]int{}}
	b.visit(stmt)
	sort.SliceStable(b.shape.preds, func(i, j int) bool { return b.shape.preds[i].pos < b.shape.preds[j].pos })
	b.shape.countOnly = isUngroupedCount(stmt["node"])
	return b.shape, true
}

type shapeBuilder struct {
	shape *emptyShape
	cte   map[string]bool
	byKey map[string]int
}

func (b *shapeBuilder) visit(node any) {
	switch v := node.(type) {
	case []any:
		for _, item := range v {
			b.visit(item)
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch {
		case typ == "BASE_TABLE":
			b.baseTable(v)
		case opaqueNodeTypes[typ]:
			b.shape.opaque = true
		case typ == "COMPARE_EQUAL" || typ == "COMPARE_NOTEQUAL":
			b.comparison(v)
		case typ == "COMPARE_IN" || typ == "COMPARE_NOT_IN":
			b.inList(v)
		}
		for _, val := range v {
			b.visit(val)
		}
	}
}

func (b *shapeBuilder) baseTable(v map[string]any) {
	src := tableSource{}
	src.catalog, _ = v["catalog_name"].(string)
	src.schema, _ = v["schema_name"].(string)
	src.table, _ = v["table_name"].(string)
	src.alias, _ = v["alias"].(string)
	if src.catalog == "" && src.schema == "" && b.cte[strings.ToLower(src.table)] {
		b.shape.opaque = true
		return
	}
	b.shape.sources = append(b.shape.sources, src)
}

// columnRef is the (qualifier, column) of a COLUMN_REF node, if it is a plain
// one- or two-part reference.
func columnRef(n any) (qualifier, column string, ok bool) {
	m, _ := n.(map[string]any)
	if m == nil || m["type"] != "COLUMN_REF" {
		return "", "", false
	}
	names, _ := m["column_names"].([]any)
	parts := make([]string, 0, len(names))
	for _, x := range names {
		s, isStr := x.(string)
		if !isStr {
			return "", "", false
		}
		parts = append(parts, s)
	}
	switch len(parts) {
	case 1:
		return "", parts[0], true
	case 2:
		return parts[0], parts[1], true
	}
	return "", "", false
}

func (b *shapeBuilder) comparison(v map[string]any) {
	if q, c, ok := columnRef(v["left"]); ok {
		if s, isLit := constString(v["right"]); isLit {
			b.add(q, c, s, location(v))
		}
		return
	}
	if q, c, ok := columnRef(v["right"]); ok {
		if s, isLit := constString(v["left"]); isLit {
			b.add(q, c, s, location(v))
		}
	}
}

func (b *shapeBuilder) inList(v map[string]any) {
	children, _ := v["children"].([]any)
	if len(children) < 2 {
		return
	}
	q, c, ok := columnRef(children[0])
	if !ok {
		return
	}
	for _, child := range children[1:] {
		if s, isLit := constString(child); isLit {
			b.add(q, c, s, location(v))
		}
	}
}

func (b *shapeBuilder) add(qualifier, column, value string, pos float64) {
	key := strings.ToLower(qualifier + "." + column)
	i, ok := b.byKey[key]
	if !ok {
		b.shape.preds = append(b.shape.preds, literalPredicate{qualifier: qualifier, column: column, pos: pos})
		i = len(b.shape.preds) - 1
		b.byKey[key] = i
	}
	p := &b.shape.preds[i]
	if pos < p.pos {
		p.pos = pos
	}
	for _, have := range p.values {
		if have == value {
			return
		}
	}
	p.values = append(p.values, value)
}

// isUngroupedCount reports whether top is a SELECT whose only output is one
// count / count(*) with no GROUP BY.
func isUngroupedCount(top any) bool {
	m, _ := top.(map[string]any)
	if m == nil || m["type"] != "SELECT_NODE" {
		return false
	}
	if g, _ := m["group_expressions"].([]any); len(g) > 0 {
		return false
	}
	if g, _ := m["group_sets"].([]any); len(g) > 0 {
		return false
	}
	list, _ := m["select_list"].([]any)
	if len(list) != 1 {
		return false
	}
	f, _ := list[0].(map[string]any)
	if f == nil || f["class"] != "FUNCTION" {
		return false
	}
	name, _ := f["function_name"].(string)
	return name == "count" || name == "count_star"
}

// probeTable is a table found in information_schema, with the column's real
// spelling.
type probeTable struct {
	catalog, schema, table, column string
}

func (t probeTable) String() string { return t.catalog + "." + t.schema + "." + t.table }

func (t probeTable) quoted() string {
	return session.QuoteIdent(t.catalog) + "." + session.QuoteIdent(t.schema) + "." + session.QuoteIdent(t.table)
}

// resolve finds the one table the predicate's column belongs to, among the
// statement's own sources. It returns ok=false when the column is ambiguous or
// cannot be found: better to say nothing than to describe the wrong table.
func (s *emptyShape) resolve(ctx context.Context, db sqlQueryer, p literalPredicate) (probeTable, bool) {
	var candidates []tableSource
	if p.qualifier != "" {
		for _, src := range s.sources {
			if sourceName(src) == strings.ToLower(p.qualifier) {
				candidates = append(candidates, src)
			}
		}
		if len(candidates) != 1 {
			return probeTable{}, false
		}
	} else {
		if s.opaque {
			return probeTable{}, false
		}
		candidates = s.sources
	}
	var found []probeTable
	for _, src := range candidates {
		tbl, ok, err := lookupColumnIn(ctx, db, src, p.column)
		if err != nil {
			return probeTable{}, false
		}
		if ok {
			found = append(found, tbl)
		}
	}
	if len(found) != 1 {
		return probeTable{}, false
	}
	return found[0], true
}

// sourceName is the name a column of src is qualified with: its alias, or the
// table name when it has none.
func sourceName(src tableSource) string {
	if src.alias != "" {
		return strings.ToLower(src.alias)
	}
	return strings.ToLower(src.table)
}

// lookupColumnIn is lookupColumn with the policy's rule for a two-part name: if
// the first part is not a schema of the default catalog but names an attached
// catalog, it is that catalog (archive.races is archive's default schema).
func lookupColumnIn(ctx context.Context, db sqlQueryer, src tableSource, column string) (probeTable, bool, error) {
	tbl, ok, err := lookupColumn(ctx, db, src, column)
	if ok || err != nil || src.catalog != "" || src.schema == "" {
		return tbl, ok, err
	}
	return lookupColumn(ctx, db, tableSource{catalog: src.schema, table: src.table, alias: src.alias}, column)
}

// lookupColumn asks information_schema whether src has the column. An
// unqualified name means the default catalog and schema; a catalog without a
// schema matches that catalog's tables of the name, and counts only when there
// is exactly one.
func lookupColumn(ctx context.Context, db sqlQueryer, src tableSource, column string) (probeTable, bool, error) {
	q := "SELECT table_catalog, table_schema, table_name, column_name FROM information_schema.columns WHERE lower(column_name) = lower($1) AND lower(table_name) = lower($2)"
	args := []any{column, src.table}
	if src.catalog == "" {
		q += " AND table_catalog = current_database()"
	} else {
		q += " AND lower(table_catalog) = lower($3)"
		args = append(args, src.catalog)
	}
	switch {
	case src.schema != "":
		q += " AND lower(table_schema) = lower($" + strconv.Itoa(len(args)+1) + ")"
		args = append(args, src.schema)
	case src.catalog == "":
		q += " AND table_schema = current_schema()"
	}
	rows, err := db.QueryContext(ctx, q+" LIMIT 2", args...)
	if err != nil {
		return probeTable{}, false, err
	}
	defer rows.Close() //nolint:errcheck
	var out []probeTable
	for rows.Next() {
		var t probeTable
		if err := rows.Scan(&t.catalog, &t.schema, &t.table, &t.column); err != nil {
			return probeTable{}, false, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil || len(out) != 1 {
		return probeTable{}, false, err
	}
	return out[0], true, nil
}

// location is a node's position in the statement text.
func location(n map[string]any) float64 {
	f, _ := n["query_location"].(float64)
	return f
}

// localCatalog reports whether a catalog is a local duckdb or sqlite file, the
// only kind the value probes read: a remote connection (Postgres, MySQL,
// MotherDuck, a lake) is not scanned for a hint the person did not ask for. An
// unknown catalog counts as remote.
func localCatalog(ctx context.Context, db sqlQueryer, name string) bool {
	var typ string
	err := db.QueryRowContext(ctx, "SELECT type FROM duckdb_databases() WHERE lower(database_name) = lower($1) LIMIT 1", name).Scan(&typ)
	return err == nil && (typ == "duckdb" || typ == "sqlite")
}
