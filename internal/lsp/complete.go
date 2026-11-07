package lsp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
)

// ItemKind is what a completion names.
type ItemKind int

// Completion item kinds, mapped to LSP CompletionItemKind by the server.
const (
	KindKeyword ItemKind = iota
	KindConnection
	KindSchema
	KindTable
	KindView
	KindColumn
	KindFunction
	KindAlias
	KindMetric
)

// Rank orders kinds in a completion list: what the statement names first,
// then the catalog, functions, and keywords last.
func (k ItemKind) Rank() int {
	switch k {
	case KindColumn, KindAlias, KindMetric:
		return 0
	case KindTable, KindView:
		return 1
	case KindSchema, KindConnection:
		return 2
	case KindFunction:
		return 3
	default:
		return 4
	}
}

// Item is one completion candidate.
type Item struct {
	Label  string
	Kind   ItemKind
	Detail string
	// Insert is the text to insert when it differs from Label (a name that
	// needs double quotes).
	Insert string
}

// maxItems caps one completion list; editors filter further as you type.
const maxItems = 500

// clause is the grammatical slot the cursor is in.
type clause int

const (
	clauseStart    clause = iota // nothing to go on: offer keywords
	clauseRelation               // after FROM / JOIN / INTO / UPDATE ...
	clauseColumn                 // SELECT list, WHERE, ON, GROUP BY ...
)

// tableRef is one relation a statement names, with its alias.
type tableRef struct {
	path  []string
	alias string
	rel   *Relation
	where string // conn.schema.table, for details
}

// cursor is what the completer knows about the position being completed.
type cursor struct {
	qual   []string // dotted qualifiers before the word: pg.public.or -> [pg public]
	prefix string   // the partial word under the cursor
	start  int      // byte offset the word (or its opening quote) starts at; the cursor when there is none
	quoted bool     // the word under the cursor is a "quoted" name
	clause clause
	refs   []tableRef
}

// completeAt returns completion candidates for the SQL in text at byte offset,
// and the byte offset they replace from (the start of the word, or the opening
// quote of a quoted name, under the cursor). cat may be nil (no snapshot yet):
// then only keywords are offered. incomplete reports that the list was cut at
// maxItems, so a client must ask again as the word grows instead of filtering
// the cut list itself.
func completeAt(text string, offset int, cat *Catalog) (items []Item, from int, incomplete bool) {
	if items, from, ok := metricItems(text, offset, cat); ok {
		return items, from, false
	}
	cur, ok := analyze(text, offset, cat)
	if !ok {
		return nil, min(max(offset, 0), len(text)), false
	}
	if len(cur.qual) > 0 {
		items = qualifiedItems(cur, cat)
	} else {
		items = bareItems(cur, cat)
	}
	items, incomplete = filterItems(items, cur.prefix, cur.quoted)
	return items, cur.start, incomplete
}

// String is the kind's lower-case name, for callers outside the LSP wire.
func (k ItemKind) String() string {
	names := [...]string{"keyword", "connection", "schema", "table", "view", "column", "function", "alias", "metric"}
	if k < 0 || int(k) >= len(names) {
		return "keyword"
	}
	return names[k]
}

// analyze finds the statement, the dotted path being typed, the clause and the
// tables the statement names. ok is false inside a string or comment.
func analyze(text string, offset int, cat *Catalog) (cursor, bool) {
	if offset > len(text) {
		offset = len(text)
	}
	toks, ok := statementTokens(text, offset)
	if !ok {
		return cursor{}, false
	}
	before := 0
	for before < len(toks) && toks[before].end <= offset {
		before++
	}
	// toks[:before] end at or before the cursor.
	head := toks[:before]
	cur := cursor{start: offset}
	pathStart := len(head)
	if n := len(head); n > 0 && head[n-1].end == offset && (head[n-1].kind == tokWord || head[n-1].kind == tokQuoted) {
		cur.prefix = head[n-1].text
		cur.start = head[n-1].start
		cur.quoted = head[n-1].kind == tokQuoted
		pathStart = n - 1
	}
	for pathStart >= 2 && isDot(head[pathStart-1]) && head[pathStart-2].isName() && head[pathStart-2].end == head[pathStart-1].start {
		cur.qual = append([]string{head[pathStart-2].text}, cur.qual...)
		pathStart -= 2
	}
	cur.clause = clauseAt(head[:pathStart])
	cur.refs = tableRefs(toks, cat)
	return cur, true
}

// statementTokens returns the tokens (comments dropped) of the statement that
// holds offset. ok is false when offset is inside a string or comment.
func statementTokens(text string, offset int) ([]token, bool) {
	all := lex(text)
	s, e := statementBounds(all, offset, len(text))
	var toks []token
	for _, t := range all {
		if inOpaque(t, offset) {
			return nil, false
		}
		if t.start >= s && t.end <= e && t.kind != tokComment {
			toks = append(toks, t)
		}
	}
	return toks, true
}

// inOpaque reports whether offset is inside a string literal or a comment,
// where there is nothing to complete. A -- comment runs to the end of its
// line, and an unterminated string or block comment to the end of the text, so
// the offset just past an open one is still inside it.
func inOpaque(t token, offset int) bool {
	if t.kind != tokString && t.kind != tokComment {
		return false
	}
	return offset > t.start && (offset < t.end || (offset == t.end && t.open))
}

func isDot(t token) bool { return t.kind == tokPunct && t.text == "." }

// clauseAt walks back over names, dots, commas and AS to the keyword that
// decides what may come next.
func clauseAt(toks []token) clause {
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		switch {
		case t.isName(), isDot(t), t.kind == tokPunct && t.text == ",", t.kind == tokWord && t.upper() == "AS":
			continue
		case t.kind == tokWord && relationKeywords[t.upper()]:
			return clauseRelation
		default:
			return clauseColumn
		}
	}
	return clauseStart
}

// tableRefs finds the relations a statement names after FROM, JOIN, UPDATE
// and INTO (and the comma list after FROM), with their aliases.
func tableRefs(toks []token, cat *Catalog) []tableRef {
	var refs []tableRef
	for i := 0; i < len(toks); i++ {
		kw := toks[i].upper()
		if toks[i].kind != tokWord || (kw != "FROM" && kw != "JOIN" && kw != "UPDATE" && kw != "INTO") {
			continue
		}
		j := i + 1
		for {
			ref, next, ok := parseRef(toks, j)
			if !ok {
				break
			}
			ref.rel, ref.where = cat.resolve(ref.path)
			refs = append(refs, ref)
			j = next
			if kw != "FROM" || j >= len(toks) || toks[j].text != "," {
				break
			}
			j++
		}
		i = j - 1
	}
	return refs
}

// parseRef reads `name[.name[.name]] [AS] [alias]` at toks[i]. A path followed
// by "(" is a table function call, not a relation.
func parseRef(toks []token, i int) (tableRef, int, bool) {
	var ref tableRef
	for i < len(toks) && toks[i].isName() {
		ref.path = append(ref.path, toks[i].text)
		i++
		if i < len(toks) && isDot(toks[i]) {
			i++
			continue
		}
		break
	}
	if len(ref.path) == 0 || (i < len(toks) && toks[i].text == "(") {
		return ref, i, false
	}
	if i < len(toks) && toks[i].kind == tokWord && toks[i].upper() == "AS" {
		i++
	}
	if i < len(toks) && toks[i].isName() {
		ref.alias = toks[i].text
		i++
	}
	return ref, i, true
}

// defaultSchemaOf is the schema an unqualified name in conn resolves to.
func defaultSchemaOf(conn *Connection) *Schema {
	if conn == nil {
		return nil
	}
	if s := conn.Schema(conn.DefaultSchema); s != nil {
		return s
	}
	return conn.Schema("main")
}

// resolve finds the relation a 1-3 part name means, the way DuckDB binds it:
// table; catalog.table or schema.table; catalog.schema.table.
func (c *Catalog) resolve(path []string) (*Relation, string) {
	if c == nil {
		return nil, ""
	}
	type cand struct {
		conn   *Connection
		schema *Schema
		table  string
	}
	def := c.Conn(c.DefaultConnection)
	var cands []cand
	switch len(path) {
	case 1:
		cands = []cand{{def, defaultSchemaOf(def), path[0]}}
	case 2:
		cn := c.Conn(path[0])
		cands = []cand{{cn, defaultSchemaOf(cn), path[1]}, {def, def.Schema(path[0]), path[1]}}
	case 3:
		cn := c.Conn(path[0])
		cands = []cand{{cn, cn.Schema(path[1]), path[2]}}
	}
	for _, k := range cands {
		if r := k.schema.Relation(k.table); r != nil {
			return r, k.conn.Name + "." + k.schema.Name + "." + r.Name
		}
	}
	return nil, ""
}

// qualifiedItems completes after a dot: an alias's columns, a connection's
// schemas and tables, a schema's tables, or a table's columns.
func qualifiedItems(cur cursor, cat *Catalog) []Item {
	var items []Item
	q := cur.qual
	if len(q) == 1 {
		for _, r := range cur.refs {
			if r.rel != nil && (strings.EqualFold(r.alias, q[0]) || (r.alias == "" && strings.EqualFold(r.path[len(r.path)-1], q[0]))) {
				items = append(items, columnItems(r.rel, r.where)...)
			}
		}
	}
	if cat == nil {
		return items
	}
	switch len(q) {
	case 1:
		if cn := cat.Conn(q[0]); cn != nil {
			for _, s := range cn.Schemas {
				items = append(items, Item{Label: s.Name, Kind: KindSchema, Detail: "schema in " + cn.Name})
			}
			items = append(items, relationItems(cn, defaultSchemaOf(cn))...)
		}
		def := cat.Conn(cat.DefaultConnection)
		items = append(items, relationItems(def, def.Schema(q[0]))...)
	case 2:
		cn := cat.Conn(q[0])
		items = append(items, relationItems(cn, cn.Schema(q[1]))...)
	}
	if len(items) == 0 || len(q) >= 2 {
		if rel, where := cat.resolve(q); rel != nil {
			items = append(items, columnItems(rel, where)...)
		}
	}
	return items
}

// bareItems completes a word with no qualifier, by clause.
func bareItems(cur cursor, cat *Catalog) []Item {
	var items []Item
	switch cur.clause {
	case clauseRelation:
		items = relationClauseItems(cat)
	case clauseColumn:
		items = columnClauseItems(cur.refs, cat)
	}
	for _, k := range keywords {
		items = append(items, Item{Label: k, Kind: KindKeyword})
	}
	return items
}

// relationClauseItems is what may follow FROM: the default schema's tables and
// views, every connection, and the default connection's schemas.
func relationClauseItems(cat *Catalog) []Item {
	if cat == nil {
		return nil
	}
	def := cat.Conn(cat.DefaultConnection)
	items := relationItems(def, defaultSchemaOf(def))
	for _, cn := range cat.Connections {
		items = append(items, Item{Label: cn.Name, Kind: KindConnection, Detail: strings.TrimSpace(cn.Type + " connection")})
	}
	items = append(items, metricFunctionItem(cat)...)
	if def != nil {
		for _, s := range def.Schemas {
			items = append(items, Item{Label: s.Name, Kind: KindSchema, Detail: "schema in " + def.Name})
		}
	}
	return items
}

// columnClauseItems is what may appear in an expression: the aliases and
// columns of the tables the statement names, then functions.
func columnClauseItems(refs []tableRef, cat *Catalog) []Item {
	var items []Item
	for _, r := range refs {
		if r.alias != "" {
			items = append(items, Item{Label: r.alias, Kind: KindAlias, Detail: "alias for " + strings.Join(r.path, ".")})
		}
		if r.rel != nil {
			items = append(items, columnItems(r.rel, r.where)...)
		}
	}
	if cat != nil {
		for _, f := range cat.Functions {
			items = append(items, Item{Label: f, Kind: KindFunction, Detail: "function"})
		}
		items = append(items, metricFunctionItem(cat)...)
	}
	return items
}

func relationItems(cn *Connection, s *Schema) []Item {
	if cn == nil || s == nil {
		return nil
	}
	items := make([]Item, 0, len(s.Tables))
	for _, t := range s.Tables {
		kind := KindTable
		if t.Kind == "view" {
			kind = KindView
		}
		detail := fmt.Sprintf("%s in %s.%s (%d columns)", t.Kind, cn.Name, s.Name, len(t.Columns))
		if len(t.Columns) == 0 && cn.Type == engine.IcebergCatalogType {
			detail = fmt.Sprintf("%s in %s.%s (columns load when opened)", t.Kind, cn.Name, s.Name) // names-only listing
		}
		items = append(items, Item{Label: t.Name, Kind: kind, Detail: detail})
	}
	return items
}

func columnItems(rel *Relation, where string) []Item {
	items := make([]Item, 0, len(rel.Columns))
	for _, c := range rel.Columns {
		items = append(items, Item{Label: c.Name, Kind: KindColumn, Detail: c.Type + " — " + where})
	}
	return items
}

// filterItems keeps the items that start with prefix (case-insensitive), drops
// duplicates, quotes names that need it, and caps the list.
//
// quoted means the replaced range starts at an opening quote, so every name
// is inserted in its quoted form.
func filterItems(items []Item, prefix string, quoted bool) ([]Item, bool) {
	lp := strings.ToLower(prefix)
	seen := map[string]bool{}
	out := []Item{}
	for _, it := range items {
		key := fmt.Sprintf("%d/%s", it.Kind, strings.ToLower(it.Label))
		if seen[key] || !strings.HasPrefix(strings.ToLower(it.Label), lp) {
			continue
		}
		isSyntax := it.Kind == KindKeyword || it.Kind == KindFunction
		if quoted && isSyntax {
			continue // a "quoted" word is a name, never a keyword or function
		}
		seen[key] = true
		if !isSyntax && (quoted || needsQuotes(it.Label)) {
			it.Insert = `"` + strings.ReplaceAll(it.Label, `"`, `""`) + `"`
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind.Rank() < out[j].Kind.Rank() })
	if len(out) > maxItems {
		return out[:maxItems], true
	}
	return out, false
}

// needsQuotes reports whether name must be written "quoted" in SQL.
func needsQuotes(name string) bool {
	if name == "" || isKeyword(strings.ToUpper(name)) || !isWordStart(name[0]) {
		return true
	}
	for i := 0; i < len(name); i++ {
		if !isWordPart(name[i]) || name[i] >= 0x80 {
			return true
		}
	}
	return false
}
