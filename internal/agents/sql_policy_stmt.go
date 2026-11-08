package agents

import (
	"fmt"
	"regexp"
	"strings"
)

// The token/statement half of the agent SQL policy: DML, DDL and COPY, the
// string net and the token screen. The overview is in sql_policy.go.

// --- DML, DDL and COPY ------------------------------------------------------

// parseName reads a dotted name starting at toks[i]: a, a.b or a.b.c, each part
// a bare word or a quoted identifier.
func parseName(toks []lexTok, i int) (parts []string, next int, ok bool) {
	for {
		if i >= len(toks) || !toks[i].IsName() {
			return nil, i, false
		}
		parts = append(parts, nameOf(toks[i]))
		i++
		if i < len(toks) && toks[i].IsPunct(".") {
			i++
			continue
		}
		return parts, i, len(parts) <= 3
	}
}

// dml checks INSERT / UPDATE / DELETE / MERGE (and WITH ... of them): the token
// screen, the target catalogs, then the plan.
func (a *analyzer) dml(toks []lexTok, args []any) *sqlVerdict {
	start := 0
	if toks[0].IsWord("WITH") {
		start = -1
		depth := 0
		for i, t := range toks {
			switch {
			case t.IsParen("("):
				depth++
			case t.IsParen(")"):
				depth--
			case depth == 0 && (t.IsWord("INSERT") || t.IsWord("UPDATE") || t.IsWord("DELETE") || t.IsWord("MERGE")):
				start = i
			}
			if start >= 0 {
				break
			}
		}
		if start < 0 {
			return deny(fmt.Sprintf("%s: it is not a statement the policy recognises", couldntVerifyMsg))
		}
	}
	if toks[start].Text == "UPDATE" {
		// UPDATE EXTENSIONS downloads extensions; a row UPDATE always has SET.
		hasSet := false
		depth := 0
		for _, t := range toks[start:] {
			switch {
			case t.IsParen("("):
				depth++
			case t.IsParen(")"):
				depth--
			case depth == 0 && t.IsWord("SET"):
				hasSet = true
			}
		}
		if !hasSet {
			return deny("UPDATE is only allowed as UPDATE <table> SET ... (UPDATE EXTENSIONS is not allowed)")
		}
	}
	nameAt := -1
	switch toks[start].Text {
	case "INSERT":
		for i := start + 1; i < len(toks) && i <= start+4; i++ {
			if toks[i].IsWord("INTO") {
				nameAt = i + 1
				break
			}
		}
	case "UPDATE":
		nameAt = start + 1
	case "DELETE":
		if start+1 < len(toks) && toks[start+1].IsWord("FROM") {
			nameAt = start + 2
		}
	case "MERGE":
		if start+1 < len(toks) && toks[start+1].IsWord("INTO") {
			nameAt = start + 2
		}
	}
	if nameAt < 0 {
		return deny(fmt.Sprintf("%s: could not tell which table this statement changes", couldntVerifyMsg))
	}
	parts, _, ok := parseName(toks, nameAt)
	if !ok {
		return deny(fmt.Sprintf("%s: could not tell which table this statement changes", couldntVerifyMsg))
	}
	targets, reason := a.targetsFor(parts, false, false)
	if reason != "" {
		return deny(reason)
	}
	readers, reason := a.screenTokens(toks, false, 0)
	if reason != "" {
		return deny(reason)
	}
	if r := a.stringNet(toks); r != "" {
		return deny(r)
	}
	// EXPLAIN cannot even be run against a read-only database; the write gate
	// refuses such a target, so there is nothing to bind.
	if !anyReadOnly(targets) {
		if r := planCheck(a.ctx, a.db, a.textOf(toks), args, readers); r != "" {
			return deny(r)
		}
	}
	return &sqlVerdict{Class: sqlClassWrite, Targets: targets}
}

var ddlKinds = nameSet("TABLE", "VIEW", "SEQUENCE", "SCHEMA", "INDEX", "MACRO", "FUNCTION", "TYPE")

// ddl checks CREATE / DROP / ALTER of tables, views, sequences, schemas,
// indexes, macros and types. CREATE/DROP DATABASE, SHARE and SECRET and
// everything else are refused.
func (a *analyzer) ddl(toks []lexTok, args []any) *sqlVerdict {
	verb := toks[0].Text
	i := 1
	temp := false
	for i < len(toks) {
		w := toks[i]
		if !(w.IsWord("OR") || w.IsWord("REPLACE") || w.IsWord("TEMP") || w.IsWord("TEMPORARY") || w.IsWord("UNIQUE")) {
			break
		}
		if w.IsWord("TEMP") || w.IsWord("TEMPORARY") {
			temp = true
		}
		i++
	}
	if i >= len(toks) || toks[i].Kind != lkWord || !ddlKinds[toks[i].Text] {
		what := ""
		if i < len(toks) {
			what = toks[i].Raw
		}
		return deny(fmt.Sprintf("%s %s is not allowed (the agent may %s tables, views, sequences, schemas, indexes, macros and types)", verb, what, strings.ToLower(verb)))
	}
	kind := toks[i].Text
	if verb == "CREATE" && (kind == "MACRO" || kind == "FUNCTION") {
		if r := a.macroShadows(toks, i+1); r != "" {
			return deny(r)
		}
	}
	if verb == "ALTER" && kind != "TABLE" && kind != "VIEW" && kind != "SEQUENCE" {
		return deny("ALTER " + kind + " is not allowed")
	}
	i++
	// IF [NOT] EXISTS
	if i < len(toks) && toks[i].IsWord("IF") {
		i++
		if i < len(toks) && toks[i].IsWord("NOT") {
			i++
		}
		if i < len(toks) && toks[i].IsWord("EXISTS") {
			i++
		}
	}
	var targets []sqlTarget
	addTargets := func(parts []string, schemaKind bool) string {
		ts, reason := a.targetsFor(parts, schemaKind, temp)
		targets = append(targets, ts...)
		return reason
	}
	switch {
	case verb == "CREATE" && kind == "INDEX":
		// CREATE [UNIQUE] INDEX [IF NOT EXISTS] [name] ON table (...)
		on := -1
		for j := i; j < len(toks); j++ {
			if toks[j].IsWord("ON") {
				on = j
				break
			}
		}
		if on < 0 {
			return deny(fmt.Sprintf("%s: could not tell which table this index is on", couldntVerifyMsg))
		}
		parts, _, ok := parseName(toks, on+1)
		if !ok {
			return deny(fmt.Sprintf("%s: could not tell which table this index is on", couldntVerifyMsg))
		}
		if r := addTargets(parts, false); r != "" {
			return deny(r)
		}
	default:
		parts, _, ok := parseName(toks, i)
		if !ok {
			return deny(fmt.Sprintf("%s: could not tell which object this statement changes", couldntVerifyMsg))
		}
		if r := addTargets(parts, kind == "SCHEMA"); r != "" {
			return deny(r)
		}
	}
	readers, reason := a.screenTokens(toks, false, 0)
	if reason != "" {
		return deny(reason)
	}
	if r := a.stringNet(toks); r != "" {
		return deny(r)
	}
	if verb == "CREATE" && hasQueryPart(toks) && !anyReadOnly(targets) {
		if r := planCheck(a.ctx, a.db, a.textOf(toks), args, readers); r != "" {
			return deny(r)
		}
	}
	return &sqlVerdict{Class: sqlClassDDL, Targets: targets}
}

// macroShadows refuses a macro named like one of DuckDB's own functions: it
// would replace it for every later statement (list_value, read_text, glob ...),
// including the ones the policy has vetted by name.
func (a *analyzer) macroShadows(toks []lexTok, i int) string {
	for i < len(toks) && (toks[i].IsWord("IF") || toks[i].IsWord("NOT") || toks[i].IsWord("EXISTS")) {
		i++
	}
	parts, _, ok := parseName(toks, i)
	if !ok || len(parts) == 0 {
		return fmt.Sprintf("%s: could not tell what this macro is called", couldntVerifyMsg)
	}
	name := strings.ToLower(parts[len(parts)-1])
	if a.functions().internal[name] || hardDeniedFunc(name) != "" {
		return fmt.Sprintf("a macro cannot be named %q: that is a DuckDB function, and the macro would replace it for every later statement", name)
	}
	return ""
}

func anyReadOnly(targets []sqlTarget) bool {
	for _, t := range targets {
		if t.ReadOnly {
			return true
		}
	}
	return false
}

// hasQueryPart reports whether a CREATE statement has a query in it (CTAS, a
// view, a macro body), so it is worth a plan check.
func hasQueryPart(toks []lexTok) bool {
	for _, t := range toks {
		if t.IsWord("SELECT") || t.IsWord("FROM") || t.IsWord("VALUES") || t.IsWord("WITH") || t.IsWord("PIVOT") || t.IsWord("UNPIVOT") {
			return true
		}
	}
	return false
}

// copyStmt allows only COPY <table | (query)> TO '<literal path in data/>' [options].
// COPY ... FROM (a file or a database) is refused: load a file with
// INSERT ... SELECT * FROM read_csv('<path in data/>') instead.
func (a *analyzer) copyStmt(toks []lexTok, args []any) *sqlVerdict {
	depth, to := 0, -1
	for i := 1; i < len(toks); i++ {
		switch {
		case toks[i].IsParen("("):
			depth++
		case toks[i].IsParen(")"):
			depth--
		case depth == 0 && toks[i].IsWord("FROM"):
			return deny("COPY ... FROM is not allowed (COPY FROM DATABASE and file imports): load a file with INSERT INTO ... SELECT * FROM read_csv('<path in data/>')")
		case depth == 0 && toks[i].IsWord("TO") && to < 0:
			to = i
		}
	}
	if to < 0 || to+1 >= len(toks) || toks[to+1].Kind != lkString || !toks[to+1].Plain {
		return deny("COPY must be COPY <table or (query)> TO '<path>' with the path written as a plain string literal")
	}
	if r := a.paths.vetWrite(toks[to+1].Text); r != "" {
		return deny(r)
	}
	a.vetted[toks[to+1].Text] = true
	// Options after the path are a parenthesised list (or WITH (...)): nothing else.
	rest := toks[to+2:]
	if len(rest) > 0 && rest[0].IsWord("WITH") {
		rest = rest[1:]
	}
	if len(rest) > 0 && !(rest[0].IsParen("(") && matchParen(rest, 0) == len(rest)-1) {
		return deny("COPY ... TO takes only a parenthesised option list after the path")
	}
	// The names of the files COPY writes are the agent's choice only through
	// these; with them it could name a file *.metadata.json or version-hint.text
	// inside a folder it is allowed to write.
	for _, t := range rest {
		if t.IsWord("FILENAME_PATTERN") || t.IsWord("FILE_EXTENSION") {
			return deny("COPY ... TO: FILENAME_PATTERN and FILE_EXTENSION are not allowed (the agent does not choose the names of the files COPY writes)")
		}
	}
	if toks[1].Kind != lkParen {
		parts, _, ok := parseName(toks, 1)
		if !ok {
			return deny(fmt.Sprintf("%s: could not tell what COPY reads", couldntVerifyMsg))
		}
		st := &walkState{cte: map[string]bool{}}
		a.checkRef(parts, st)
		if st.reason != "" {
			return deny(st.reason)
		}
	}
	readers, reason := a.screenTokens(toks[:to], false, 0)
	if reason != "" {
		return deny(reason)
	}
	if r := a.stringNet(toks); r != "" {
		return deny(r)
	}
	if r := planCheck(a.ctx, a.db, a.textOf(toks), args, readers); r != "" {
		return deny(r)
	}
	return &sqlVerdict{Class: sqlClassDDL}
}

// targetsFor names the catalogs a write to parts changes. DuckDB resolves a
// two-part name to a schema of the default catalog first and a catalog second,
// so when the first part names a catalog both are targets and both must pass
// the gates.
func (a *analyzer) targetsFor(parts []string, isSchema, temp bool) ([]sqlTarget, string) {
	dbs := a.databases()
	if a.dbsErr != nil || len(dbs) == 0 {
		return nil, couldntVerifyMsg + ": could not read the list of connections"
	}
	var names []string
	switch {
	case temp:
		names = []string{"temp"}
	case len(parts) == 3:
		names = []string{parts[0]}
	case len(parts) == 2 && isSchema:
		names = []string{parts[0]}
	case len(parts) == 2:
		names = []string{a.defaultCatalog()}
		if _, ok := dbs[strings.ToLower(parts[0])]; ok {
			names = append(names, parts[0])
		}
	case len(parts) == 1:
		names = []string{a.defaultCatalog()}
	default:
		return nil, couldntVerifyMsg + ": the name has too many parts"
	}
	var out []sqlTarget
	seen := map[string]bool{}
	for _, n := range names {
		ln := strings.ToLower(n)
		if seen[ln] {
			continue
		}
		seen[ln] = true
		if isDuckLakeInternal(ln) {
			return nil, duckLakeInternalMsg
		}
		row, ok := dbs[ln]
		if !ok {
			return nil, fmt.Sprintf("there is no connection named %q (see list_connections)", n)
		}
		if ln == "system" {
			return nil, "DuckDB's system catalog cannot be changed"
		}
		conn, registered := a.env.conn(ln)
		t := sqlTarget{Catalog: ln}
		t.ReadOnly = row.readOnly || (registered && conn.ReadOnly)
		t.Local = (row.typ == "duckdb" || row.typ == "sqlite") && !(registered && conn.Remote)
		out = append(out, t)
	}
	return out, ""
}

// --- the string net ---

var urlishRE = regexp.MustCompile(`^([A-Za-z]:[\\/]|[A-Za-z][A-Za-z0-9+.\-]*:/)`)

// inertString reports whether a string literal cannot be taken for a file or
// URL: no scheme, not shaped like a path, no data-file extension.
func inertString(s string) bool {
	t := strings.TrimSpace(s)
	switch {
	case strings.Contains(t, "://"), urlishRE.MatchString(t):
		return false
	case strings.HasPrefix(t, "/"), strings.HasPrefix(t, "~"), strings.HasPrefix(t, "./"), strings.HasPrefix(t, "../"), strings.HasPrefix(t, `\`):
		return false
	}
	lower := strings.ToLower(t)
	for _, e := range replacementScanExts {
		if strings.HasSuffix(lower, e) {
			return false
		}
	}
	return true
}

// stringNet is the safety net that does not depend on knowing every place SQL
// can name a table source (FROM, JOIN, PIVOT, UNPIVOT, TABLE, ...): before any
// EXPLAIN binds a statement, every string literal and quoted identifier in it
// must be either a literal the path policy already accepted as a reader path
// or URL, or inert (it cannot be read as a file or URL). It is blunt on
// purpose: a data value such as '/etc/passwd' or 'report.csv' is refused too.
func (a *analyzer) stringNet(toks []lexTok) string {
	for _, t := range toks {
		if (t.Kind == lkString || t.Kind == lkIdent) && !a.vetted[t.Text] && !inertString(t.Text) {
			return "a file or URL here can't be checked; use a reader function (read_csv('<path>') ...) on a path inside data/"
		}
	}
	return ""
}

// --- the token screen --------------------------------------------------------

// cteNamesFromTokens collects the names a statement defines with WITH.
func cteNamesFromTokens(toks []lexTok) map[string]bool {
	names := map[string]bool{}
	for j, t := range toks {
		if !t.IsWord("AS") {
			continue
		}
		k := j + 1
		for k < len(toks) && (toks[k].IsWord("NOT") || toks[k].IsWord("MATERIALIZED")) {
			k++
		}
		if k >= len(toks) || !toks[k].IsParen("(") {
			continue
		}
		p := j - 1
		if p >= 0 && toks[p].IsParen(")") { // a column list: WITH a(x, y) AS (...)
			depth := 0
			for ; p >= 0; p-- {
				if toks[p].IsParen(")") {
					depth++
				} else if toks[p].IsParen("(") {
					depth--
					if depth == 0 {
						p--
						break
					}
				}
			}
		}
		if p >= 0 && toks[p].IsName() {
			names[strings.ToLower(nameOf(toks[p]))] = true
		}
	}
	return names
}

// screenTokens applies the policy to a statement's tokens, whatever its shape.
// astMode means an AST walk covers table functions and table references (a
// SELECT-shaped statement), so only the never-list and the literal-path rule
// are applied here; otherwise (DML, DDL, COPY, view and macro bodies) it also
// checks table functions against the allow-list and every table reference.
// It returns how many readers were vetted.
func (a *analyzer) screenTokens(toks []lexTok, astMode bool, depth int) (readers readerCount, reason string) {
	if depth > 4 {
		return readerCount{}, "views and macros nest too deeply to verify"
	}
	var cte map[string]bool
	for i, t := range toks {
		if t.IsName() && i+1 < len(toks) && toks[i+1].IsParen("(") {
			name := strings.ToLower(nameOf(t))
			if r := hardDeniedFunc(name); r != "" {
				if name == "query" || name == "query_table" {
					// A single literal argument is checked recursively.
					n, rr := a.literalQuery(name, toks, i, depth)
					if rr != "" {
						return readerCount{}, rr
					}
					if !astMode {
						readers = readers.add(n)
					}
					continue
				}
				return readerCount{}, fmt.Sprintf("%s() is not allowed: %s", name, r)
			}
			switch {
			case readerFuncs[name]:
				if r := a.vetReaderTokens(name, toks, i+1); r != "" {
					return readerCount{}, r
				}
				if !astMode {
					readers.plain++
				}
			case !astMode:
				fi := a.functions()
				if def, isMacro := fi.macros[name]; isMacro {
					n, r := a.defScreen(def, depth+1)
					if r != "" {
						return readerCount{}, r
					}
					readers = readers.add(n)
				} else if fi.table[name] && !allowedTableFunctions[name] {
					return readerCount{}, fmt.Sprintf("table function %s() is not on the agent's allow-list", name)
				}
			}
		}
		if astMode {
			continue
		}
		if cte == nil {
			cte = cteNamesFromTokens(toks)
		}
		if isTableRefStart(toks, i) {
			parts, next, ok := tableRefAt(toks, i)
			if !ok || (next < len(toks) && toks[next].IsParen("(")) {
				continue // a function call or a subquery, not a table name
			}
			if len(parts) == 1 && cte[strings.ToLower(parts[0])] {
				continue
			}
			st := &walkState{cte: cte}
			a.checkRef(parts, st)
			if st.reason != "" {
				return readerCount{}, st.reason
			}
			readers = readers.add(st.readers)
		}
	}
	return readers, ""
}

// literalQuery vets query('<sql>') / query_table('<name>') with one plain
// literal argument by checking the SQL it carries as a read of its own.
func (a *analyzer) literalQuery(name string, toks []lexTok, i, depth int) (readerCount, string) {
	if i+3 >= len(toks) || toks[i+2].Kind != lkString || !toks[i+2].Plain || !toks[i+3].IsParen(")") {
		return readerCount{}, fmt.Sprintf("%s() is not allowed unless its argument is one plain string literal", name)
	}
	inner := toks[i+2].Text
	if name == "query_table" {
		parts, next, ok := parseNameFromString(inner)
		if !ok || next != len(inner) {
			return readerCount{}, "query_table() needs one plain table name"
		}
		st := &walkState{cte: map[string]bool{}}
		a.checkRef(parts, st)
		return st.readers, st.reason
	}
	if depth >= 2 {
		return readerCount{}, "query() nests too deeply to verify"
	}
	sub := *a
	sub.src = inner
	stmts := lexStatements(inner)
	if len(stmts) != 1 {
		return readerCount{}, "query() needs exactly one statement"
	}
	if n, err := countStatements(inner); err != nil || n != 1 {
		return readerCount{}, "query() needs exactly one statement"
	}
	v := sub.statement(stmts[0], nil, depth+1)
	if v.Class != sqlClassRead {
		if v.Reason != "" {
			return readerCount{}, v.Reason
		}
		return readerCount{}, "query() may only run a read statement"
	}
	a.vetted[inner] = true
	return v.readers, ""
}

func parseNameFromString(s string) ([]string, int, bool) {
	toks := lexStatements(s)
	if len(toks) != 1 {
		return nil, 0, false
	}
	parts, next, ok := parseName(toks[0], 0)
	if !ok || next != len(toks[0]) {
		return nil, 0, false
	}
	return parts, len(s), true
}

// vetReaderTokens checks a reader call's arguments: exactly one positional
// argument, a plain string literal, that the path policy accepts. Named
// arguments (header = true, columns = {...}) are not paths and pass.
func (a *analyzer) vetReaderTokens(fn string, toks []lexTok, open int) string {
	closeIdx := matchParen(toks, open)
	const need = "%s() needs its path written out as one plain string literal, or a list of them (no expressions, variables, concatenation or E'...' strings)"
	if closeIdx < 0 || open+2 > closeIdx {
		return fmt.Sprintf(need, fn)
	}
	if _, shadowed := a.functions().macros[strings.ToLower(fn)]; shadowed {
		return fmt.Sprintf("%s() is shadowed by a macro of the same name, so it would not read the file that was checked; the agent will not use it", fn)
	}
	paths, next, ok := readerPathTokens(toks, open+1, closeIdx)
	if !ok || (toks[open+1].IsPunct("[") && !a.listFormSafe()) {
		return fmt.Sprintf(need, fn)
	}
	// A second positional string (after a comma, not after "=" or ":=") would
	// be a second path.
	depth := 0
	for j := next; j < closeIdx; j++ {
		switch {
		case toks[j].IsParen("("), toks[j].IsPunct("{"), toks[j].IsPunct("["):
			depth++
		case toks[j].IsParen(")"), toks[j].IsPunct("}"), toks[j].IsPunct("]"):
			depth--
		case depth == 0 && toks[j].IsPunct(",") && j+1 < closeIdx:
			if n := toks[j+1]; n.Kind == lkString {
				return fmt.Sprintf("%s() takes one path: more than one positional argument is not allowed", fn)
			}
		}
	}
	// Named options can name other files or change how the reader resolves
	// them (version_name_format, allow_moved_paths, rejects_table ...): only an
	// explicit list of options that cannot is accepted.
	depth = 0
	for j := next; j < closeIdx; j++ {
		switch {
		case toks[j].IsParen("("), toks[j].IsPunct("{"), toks[j].IsPunct("["):
			depth++
		case toks[j].IsParen(")"), toks[j].IsPunct("}"), toks[j].IsPunct("]"):
			depth--
		case depth == 0 && toks[j].IsName() && j+1 < closeIdx:
			eq := toks[j+1].IsPunct("=") || (toks[j+1].IsPunct(":") && j+2 < closeIdx && toks[j+2].IsPunct("="))
			if eq && !safeReaderParams[strings.ToLower(nameOf(toks[j]))] {
				return fmt.Sprintf("%s(): the option %q is not on the agent's list of safe reader options (%s)", fn, strings.ToLower(nameOf(toks[j])), safeReaderParamList())
			}
		}
	}
	for _, p := range paths {
		if r := a.paths.vetReaderPath(strings.ToLower(fn), p); r != "" {
			return r
		}
		a.vetted[p] = true
	}
	return ""
}

// readerPathTokens reads a reader's path argument starting at toks[i]: one
// plain string literal, or a bracketed list of them. next is the index after it,
// which must be a "," or the closing ")" (so 'a' 'b', 'a' || 'b' and 'a'::T
// are not paths).
func readerPathTokens(toks []lexTok, i, closeIdx int) (paths []string, next int, ok bool) {
	plain := func(t lexTok) bool { return t.Kind == lkString && t.Plain }
	switch {
	case plain(toks[i]):
		paths, next = []string{toks[i].Text}, i+1
	case toks[i].IsPunct("["):
		next = i + 1
		for {
			if next >= closeIdx || !plain(toks[next]) {
				return nil, 0, false
			}
			paths = append(paths, toks[next].Text)
			next++
			if next < closeIdx && toks[next].IsPunct(",") {
				next++
				continue
			}
			if next < closeIdx && toks[next].IsPunct("]") {
				next++
				break
			}
			return nil, 0, false
		}
	default:
		return nil, 0, false
	}
	if next > closeIdx || !(toks[next].IsPunct(",") || toks[next].IsParen(")")) {
		return nil, 0, false
	}
	return paths, next, true
}

// isTableRefStart reports whether toks[i] begins a table reference: the token
// after FROM, JOIN or USING, in a comma-separated FROM list, at the start of a
// parenthesised table group, or a string after TABLE.
func isTableRefStart(toks []lexTok, i int) bool {
	t := toks[i]
	if !(t.IsName() || t.Kind == lkString) || i == 0 {
		return false
	}
	// A keyword after "(" starts a subquery, not a table name.
	if t.IsWord("LATERAL") || t.IsWord("SELECT") || t.IsWord("WITH") || t.IsWord("VALUES") || t.IsWord("UNNEST") ||
		t.IsWord("FROM") || t.IsWord("TABLE") || t.IsWord("PIVOT") || t.IsWord("UNPIVOT") || t.IsWord("DESCRIBE") || t.IsWord("SHOW") || t.IsWord("SUMMARIZE") {
		return false
	}
	prev := toks[i-1]
	switch {
	case prev.IsWord("FROM"), prev.IsWord("JOIN"), prev.IsWord("USING"):
		return true
	case prev.IsWord("PIVOT"), prev.IsWord("UNPIVOT"):
		// UNPIVOT '<file>' ...; but FROM t UNPIVOT INCLUDE NULLS (...) is not a source.
		return !(t.IsWord("INCLUDE") || t.IsWord("EXCLUDE"))
	case prev.IsWord("TABLE"):
		return t.Kind == lkString
	case prev.IsParen("("):
		return i >= 2 && fromGroup(toks, i-2, 0)
	case prev.IsPunct(","):
		return inFromList(toks, i-1)
	}
	return false
}

// fromGroup reports whether the "(" at toks[q+1] opens a group of table refs.
func fromGroup(toks []lexTok, q, depth int) bool {
	if q < 0 || depth > 4 {
		return false
	}
	switch {
	case toks[q].IsWord("FROM"), toks[q].IsWord("JOIN"):
		return true
	case toks[q].IsPunct(","):
		return inFromList(toks, q)
	case toks[q].IsParen("("):
		return fromGroup(toks, q-1, depth+1)
	}
	return false
}

// inFromList reports whether the comma at toks[c] separates table references:
// walking back at the same depth, FROM or USING comes before any other clause.
func inFromList(toks []lexTok, c int) bool {
	depth := 0
	for j := c - 1; j >= 0; j-- {
		t := toks[j]
		switch {
		case t.IsParen(")"):
			depth++
		case t.IsParen("("):
			if depth == 0 {
				return false
			}
			depth--
		case depth > 0:
		case t.Kind == lkWord:
			switch t.Text {
			case "FROM", "USING":
				return true
			case "SELECT", "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "SET", "VALUES", "QUALIFY", "WINDOW", "RETURNING", "INTO", "BY":
				return false
			}
		}
	}
	return false
}

// tableRefAt parses the table name at toks[i]: one quoted or bare name, a
// dotted chain, or a single string literal (DuckDB reads 'x.csv' like "x.csv").
func tableRefAt(toks []lexTok, i int) (parts []string, next int, ok bool) {
	if toks[i].Kind == lkString {
		return []string{toks[i].Text}, i + 1, true
	}
	return parseName(toks, i)
}

// defScreen applies the token screen to a stored view or macro definition.
func (a *analyzer) defScreen(def string, depth int) (readerCount, string) {
	if depth > 4 {
		return readerCount{}, "views and macros nest too deeply to verify"
	}
	a.catalog()
	a.functions()
	a.databases()
	sub := *a
	sub.src = def
	var total readerCount
	for _, toks := range lexStatements(def) {
		n, r := sub.screenTokens(toks, false, depth)
		if r != "" {
			return readerCount{}, r
		}
		total = total.add(n)
	}
	return total, ""
}
