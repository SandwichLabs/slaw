package agents

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

// This file is the agent SQL policy (#116, plan §4.5). It inverts the old
// deny-list into an allow-list per tier and runs in layers, cheapest and most
// side-effect-free first:
//
//  1. Statement count and lexing. DuckDB's duckdb_extract_statements is the
//     authority on how many statements there are; the policy's own lexer must
//     agree, or the query is denied.
//  2. A syntactic check BEFORE any EXPLAIN (spike #107 finding B: binding
//     sniffs files and sends HTTP requests, so the classifier's own EXPLAIN
//     could exfiltrate). SELECT-shaped statements are parsed by DuckDB
//     (json_serialize_sql, on a separate hardened connection with external
//     access off) and the AST is walked; everything else is checked on its
//     tokens. Both enforce the never-list, the table-function allow-list and
//     the literal-path rule.
//  3. Only then EXPLAIN (FORMAT JSON) on the real connection, and a plan-
//     operator allow-list: the second layer, which sees through views and
//     macros that wrap a denied function.
//
// Writes also need the target connection to be writable and the person to
// approve; those two gates live in Handler.gateWrite (this file only reports
// the targets).

// sqlEnv is what the policy needs to know about the workspace. The zero value
// (and nil) means no data/ folder, no storage scopes and no connection
// registry: no file or URL read classifies as a read.
type sqlEnv struct {
	dataDir string                   // absolute, symlink-resolved; "" = none
	conns   []session.ConnectionInfo // registered connections
	// protected: files and folders of attached connections (symlink-resolved);
	// COPY ... TO never writes into them.
	protected []string
}

// newSQLEnv builds the environment from the workspace's data/ folder (may be
// "") and its registered connections.
func newSQLEnv(dataDir string, conns []session.ConnectionInfo, protected ...string) *sqlEnv {
	e := &sqlEnv{conns: conns}
	for _, p := range protected {
		if p == "" {
			continue
		}
		if real, err := realPath(p); err == nil {
			e.protected = append(e.protected, real)
		}
	}
	if dataDir != "" {
		if abs, err := filepath.Abs(dataDir); err == nil {
			if real, err := filepath.EvalSymlinks(abs); err == nil {
				e.dataDir = real
			}
		}
	}
	return e
}

func (e *sqlEnv) conn(name string) (session.ConnectionInfo, bool) {
	if e == nil {
		return session.ConnectionInfo{}, false
	}
	for _, c := range e.conns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return session.ConnectionInfo{}, false
}

func (e *sqlEnv) pathPolicy() *pathPolicy {
	p := &pathPolicy{}
	if e == nil {
		return p
	}
	p.dataDir = e.dataDir
	p.protected = e.protected
	for _, c := range e.conns {
		// Only a storage connection widens what is readable (defence in depth:
		// the session exports a scope for no other type).
		if !c.Catalogless || c.Scope == "" || !storageConnTypes[c.Type] {
			continue
		}
		// A storage connection is ready when it is connected or can connect;
		// one that needs credentials or failed does not widen what is readable.
		s := normalizeScope(c.Scope)
		if s == "" {
			continue
		}
		if c.Kind != session.StatusOK && c.Kind != session.StatusNotConnected {
			p.blocked = append(p.blocked, blockedScope{scope: s, name: c.Name, why: notReadyWhy(c)})
			continue
		}
		p.scopes = append(p.scopes, s)
	}
	return p
}

// analyzeSQL classifies raw (the query text with $name placeholders) plus
// params. Everything downstream classifies the BOUND form: placeholders become
// positional "?" first, so what is classified is what runs.
func analyzeSQL(ctx context.Context, userDB queryDB, env *sqlEnv, raw string, params map[string]any) *sqlVerdict {
	if touchesSecrets(raw) {
		return deny(secretsDeniedMsg)
	}
	bound, args, err := engine.BindParams(raw, params)
	if err != nil {
		return deny(fmt.Sprintf("could not validate query: %v", err))
	}
	a := &analyzer{ctx: ctx, db: userDB, env: env, paths: env.pathPolicy(), vetted: map[string]bool{}}
	v := a.script(bound, args)
	if v.Class == sqlClassOther {
		v.Reason += untypedParamHint(v.Reason, raw)
	}
	return v
}

type analyzer struct {
	ctx   context.Context
	db    queryDB
	env   *sqlEnv
	paths *pathPolicy
	src   string // the source the tokens being checked index into
	// vetted holds the file and URL literals the path policy accepted. The
	// string net (stringNet) lets only these, and inert strings, reach EXPLAIN.
	vetted map[string]bool
	depth  int // nesting of views, macros and query('...')

	cat    *catalog
	catErr error
	dbs    map[string]dbRow
	dbsErr error
	funcs  *funcInfo
	fnErr  error
}

func (a *analyzer) catalog() *catalog {
	if a.cat == nil && a.catErr == nil {
		a.cat, a.catErr = loadCatalog(a.ctx, a.db)
		if a.cat == nil {
			a.cat = &catalog{}
		}
	}
	return a.cat
}

func (a *analyzer) databases() map[string]dbRow {
	if a.dbs == nil && a.dbsErr == nil {
		a.dbs, a.dbsErr = loadDatabases(a.ctx, a.db)
	}
	return a.dbs
}

func (a *analyzer) functions() *funcInfo {
	if a.funcs == nil && a.fnErr == nil {
		a.funcs, a.fnErr = loadFunctions(a.ctx, a.db)
		if a.funcs == nil {
			a.funcs = &funcInfo{table: map[string]bool{}, macros: map[string]string{}, tmacro: map[string]bool{}, internal: map[string]bool{}}
		}
	}
	return a.funcs
}

// defaultCatalog is the catalog unqualified names resolve against.
func (a *analyzer) defaultCatalog() string { return strings.ToLower(a.catalog().defaultCatalog) }

// --- scripts and statements -------------------------------------------------

// script checks every statement of a (bound) script. The verdict is the
// highest class among them with all targets; the first denial wins.
func (a *analyzer) script(bound string, args []any) *sqlVerdict {
	stmts := lexStatements(bound)
	if len(stmts) == 0 {
		return denyDefinition("the statement is empty")
	}
	// Heads first, before DuckDB's parser sees the text: some statements do
	// work at parse time (IMPORT DATABASE reads its schema file), and the
	// never-list needs no parser to be refused.
	for _, toks := range stmts {
		if v := preScreen(toks); v != nil {
			return v
		}
	}
	n, err := countStatements(bound)
	if err != nil {
		return parseDenial(err, bound)
	}
	if n != len(stmts) {
		for _, toks := range stmts {
			for _, t := range toks {
				if t.IsWord("PIVOT") || t.IsWord("UNPIVOT") {
					return deny(pivotStatementMsg)
				}
			}
		}
		return deny(fmt.Sprintf("%s: it could not be split into statements reliably", couldntVerifyMsg))
	}
	a.src = bound
	if r := placeholderMismatch(stmts, len(args)); r != "" {
		return deny(r)
	}
	out := &sqlVerdict{Class: sqlClassRead}
	argPos := 0
	for i, toks := range stmts {
		nq := countPlaceholders(toks)
		stmtArgs := args[argPos : argPos+nq]
		argPos += nq
		v := a.statement(toks, stmtArgs, 0)
		if v.Class == sqlClassOther {
			return v
		}
		if v.Class == sqlClassRead && i < len(stmts)-1 {
			out.discardedReads++
		}
		if v.Class > out.Class {
			out.Class = v.Class
		}
		out.Targets = append(out.Targets, v.Targets...)
		out.Temp = out.Temp || v.Temp
	}
	out.Statements = len(stmts)
	return out
}

func countPlaceholders(toks []lexTok) int {
	n := 0
	for _, t := range toks {
		if t.IsPunct("?") {
			n++
		}
	}
	return n
}

// placeholderMismatch explains a script whose `?` tokens do not match the
// $name parameters that were bound. Every bound $name became one `?`, so a
// surplus `?` is one the caller wrote, and a deficit is a $name that sat inside
// a string literal or a comment (BindParams rewrites it all the same).
func placeholderMismatch(stmts [][]lexTok, bound int) string {
	total := 0
	for _, toks := range stmts {
		total += countPlaceholders(toks)
	}
	switch {
	case total > bound:
		return "run_query parameters are named: write $name in the SQL and pass {\"name\": value} in params; a bare ? placeholder is not supported"
	case total < bound:
		return fmt.Sprintf("%s: a $name inside a string literal or a comment is not a parameter; write it outside the quotes", couldntVerifyMsg)
	}
	return ""
}

// preScreen refuses the never-list statement kinds by their first token.
func preScreen(toks []lexTok) *sqlVerdict {
	// U&'...' and U&"..." decode escapes the lexer does not, so a name or a
	// path could be spelled in a way no token check recognises.
	for i := 0; i+2 < len(toks); i++ {
		if toks[i].IsWord("U") && toks[i+1].IsPunct("&") && toks[i].End == toks[i+1].Start && toks[i+1].End == toks[i+2].Start &&
			(toks[i+2].Kind == lkString || toks[i+2].Kind == lkIdent) {
			return deny("Unicode escape strings and identifiers (U&'...') are not allowed")
		}
	}
	head := toks[0]
	if head.Kind == lkWord {
		if why, ok := deniedHeads[head.Text]; ok {
			return deny(why)
		}
		if head.Text == "COPY" {
			depth := 0
			for _, t := range toks[1:] {
				switch {
				case t.IsParen("("):
					depth++
				case t.IsParen(")"):
					depth--
				case depth == 0 && t.IsWord("FROM"):
					return deny("COPY ... FROM is not allowed (COPY FROM DATABASE and file imports): load a file with INSERT INTO ... SELECT * FROM read_csv('<path in data/>')")
				}
			}
		}
	}
	return nil
}

// parseFailure is the reason for a statement DuckDB's parser rejected. A name
// with four or more parts (motherduck.newsroom.raw.t) fails deep in the
// serializer with "NameListToString NOT IMPLEMENTED", which says nothing to
// the agent, so it is explained (#111). A keyword used as a bare alias
// (SELECT count(*) days) gets a fixed hint (#222). text is the SQL that failed.
func parseFailure(err error, text string) string {
	msg := firstLine(err.Error())
	if strings.Contains(msg, "NameListToString") {
		return "a table name has at most three parts (connection.schema.table); " + oneDatabasePerConnectionHint
	}
	return msg + keywordAliasHint(msg, text)
}

var syntaxNearRe = regexp.MustCompile(`syntax error at or near "([A-Za-z_][A-Za-z0-9_]*)"`)

// maxAliasWords bounds how many keyword aliases one hint names, and
// maxAliasProbes how many occurrences of a failing word are retried.
const (
	maxAliasWords  = 4
	maxAliasProbes = 8
	// maxAliasHintBytes: a longer statement gets no hint.
	maxAliasHintBytes = 64 << 10
)

// keywordAliasHint explains "syntax error at or near <word>" when the word is a
// DuckDB keyword or type name (days, value, year, ...) used as a bare alias.
// It does not guess from a keyword list: it asks DuckDB's parser, which only
// parses the text, whether writing AS before that word gets past the error. A
// word counts only when the statement then parses, or fails further on at a
// different word (a second bare alias, SELECT 1 hours, 2 minutes). A failure at
// the AS itself means the word was not an alias, so any other syntax error gets
// no hint.
func keywordAliasHint(msg, text string) string {
	if len(text) > maxAliasHintBytes {
		return "" // every probe holds the shared parser connection
	}
	var words []string
	for len(words) < maxAliasWords {
		m := syntaxNearRe.FindStringSubmatch(msg)
		if m == nil || slices.Contains(words, m[1]) {
			break
		}
		next, nextMsg, ok := insertAlias(text, m[1])
		if !ok {
			break
		}
		words = append(words, m[1])
		text, msg = next, nextMsg
	}
	if len(words) == 0 {
		return ""
	}
	verb, subj, noun, obj := "is a DuckDB keyword", "it", "a bare alias", "it"
	if len(words) > 1 {
		verb, subj, noun, obj = "are DuckDB keywords", "they", "bare aliases", "them"
	}
	var parts []string
	for _, w := range words {
		parts = append(parts, "AS "+w)
	}
	return fmt.Sprintf(" (hint: %s %s, so %s cannot be %s: write %s, or quote %s as \"%s\")",
		quotedList(words), verb, subj, noun, strings.Join(parts, ", "), obj, words[0])
}

func quotedList(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = fmt.Sprintf("%q", w)
	}
	return strings.Join(q, ", ")
}

// insertAlias writes AS before an occurrence of word in text and reports
// whether the parser got past it: the new text, the parser's message for it
// ("" when it parses), and ok.
func insertAlias(text, word string) (string, string, bool) {
	probes := 0
	for _, stmt := range lexStatements(text) {
		for _, t := range stmt {
			if t.Kind != lkWord || !strings.EqualFold(t.Raw, word) {
				continue
			}
			if probes++; probes > maxAliasProbes {
				return "", "", false
			}
			cand := text[:t.Start] + "AS " + text[t.Start:]
			_, err := countStatements(cand)
			if err == nil {
				return cand, "", true
			}
			m := syntaxNearRe.FindStringSubmatch(firstLine(err.Error()))
			if m != nil && !strings.EqualFold(m[1], "AS") {
				return cand, firstLine(err.Error()), true
			}
		}
	}
	return "", "", false
}

// untypedParamHint adds a cast hint to a binder error caused by a bound
// parameter whose type DuckDB cannot infer (date_trunc('month', $from)): the
// value reaches the planner untyped, so a function with several overloads
// cannot choose.
func untypedParamHint(reason, raw string) string {
	names := engine.ParamNames(raw)
	if len(names) == 0 || !strings.Contains(reason, "Could not choose a best candidate function") {
		return ""
	}
	return fmt.Sprintf("\nHint: a ? above stands for one of your $name parameters. Its type is not known when the function is chosen, so cast it where it is used: $%s::DATE, $%s::TIMESTAMP, CAST($%s AS INTEGER).", names[0], names[0], names[0])
}

// pivotStatementMsg is the refusal for the PIVOT / UNPIVOT statement forms.
const pivotStatementMsg = couldntVerifyMsg + ": the PIVOT/UNPIVOT statement form expands into several statements inside DuckDB; use the FROM form instead: FROM <table> PIVOT (sum(v) FOR k IN (...)) or FROM <table> UNPIVOT (value FOR name IN (col1, col2)), or a reader on a path inside data/"

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var readHeads = nameSet("SELECT", "WITH", "FROM", "VALUES", "TABLE", "DESCRIBE", "SHOW", "SUMMARIZE")

// textOf is the source text of a statement's tokens.
func (a *analyzer) textOf(toks []lexTok) string { return a.src[toks[0].Start:toks[len(toks)-1].End] }

// statement classifies one statement (toks is its token stream into a.src).
// depth guards the recursion of EXPLAIN and query('...').
func (a *analyzer) statement(toks []lexTok, args []any, depth int) *sqlVerdict {
	if depth > 3 {
		return deny(fmt.Sprintf("%s: statements nest too deeply", couldntVerifyMsg))
	}
	head := toks[0]
	if head.Kind == lkWord {
		if why, ok := deniedHeads[head.Text]; ok {
			return deny(why)
		}
	}
	switch {
	case head.IsWord("EXPLAIN"):
		return a.explain(toks, args, depth)
	case head.IsWord("PRAGMA"):
		return a.pragma(toks)
	case head.IsWord("CALL"):
		return a.call(toks)
	case head.IsParen("(") || (head.Kind == lkWord && readHeads[head.Text]):
		return a.readOrWith(toks, args)
	case head.IsWord("INSERT"), head.IsWord("UPDATE"), head.IsWord("DELETE"), head.IsWord("MERGE"):
		return a.dml(toks, args)
	case head.IsWord("CREATE"), head.IsWord("DROP"), head.IsWord("ALTER"):
		v := a.ddl(toks, args)
		v.Temp = v.Class == sqlClassDDL && createsTemp(toks)
		return v
	case head.IsWord("COPY"):
		return a.copyStmt(toks, args)
	case head.IsWord("PIVOT"), head.IsWord("UNPIVOT"):
		return deny(pivotStatementMsg)
	}
	return deny(fmt.Sprintf("this kind of statement (%s) is not on the agent's allow-list", head.Raw))
}

// createsTemp reports whether toks is CREATE [OR REPLACE] TEMP[ORARY] ...
func createsTemp(toks []lexTok) bool {
	if !toks[0].IsWord("CREATE") {
		return false
	}
	for _, t := range toks[1:] {
		switch {
		case t.IsWord("TEMP"), t.IsWord("TEMPORARY"):
			return true
		case t.IsWord("OR"), t.IsWord("REPLACE"), t.IsWord("UNIQUE"):
		default:
			return false
		}
	}
	return false
}

// readOrWith handles every statement DuckDB can serialize (a pure SELECT
// batch), and WITH ... INSERT/UPDATE/DELETE which it cannot.
func (a *analyzer) readOrWith(toks []lexTok, args []any) *sqlVerdict {
	text := a.textOf(toks)
	doc, err := serializeSQL(a.ctx, text)
	if err != nil {
		return parseDenial(err, text)
	}
	if !doc.Error && len(doc.Statements) == 1 {
		return a.read(doc.Statements[0], toks, text, args)
	}
	if doc.Error && toks[0].IsWord("WITH") && doc.ErrorType == "not implemented" {
		return a.dml(toks, args)
	}
	return deny(fmt.Sprintf("%s: it did not parse as a single read statement", couldntVerifyMsg))
}

// read checks a parsed SELECT-shaped statement: the token screen, the AST walk,
// then the plan.
func (a *analyzer) read(stmt map[string]any, toks []lexTok, text string, args []any) *sqlVerdict {
	if _, reason := a.screenTokens(toks, true, 0); reason != "" {
		return deny(reason)
	}
	w := &walkState{cte: map[string]bool{}}
	collectCTENames(stmt, w.cte)
	a.walk(stmt, w)
	if w.reason != "" {
		return planDenial(w.reason, w.kind)
	}
	// No string net here: the AST is authoritative for sources (BASE_TABLE and
	// TABLE_FUNCTION nodes; a VALUE_CONSTANT is never a source), so a read-only
	// query may carry path- or URL-looking values.
	if r, kind := planCheck(a.ctx, a.db, text, args, w.readers); r != "" {
		return planDenial(r, kind)
	}
	return &sqlVerdict{Class: sqlClassRead, readers: w.readers}
}

// explain allows EXPLAIN of a read, never EXPLAIN ANALYZE (it executes the
// statement: INSERT, CTAS and COPY TO all ran under it, spike finding A).
func (a *analyzer) explain(toks []lexTok, args []any, depth int) *sqlVerdict {
	i := 1
	if i < len(toks) && toks[i].IsParen("(") {
		// Only EXPLAIN (FORMAT <word>) is accepted.
		if !(i+3 < len(toks) && toks[i+1].IsWord("FORMAT") && toks[i+2].Kind == lkWord && toks[i+3].IsParen(")")) {
			return deny("EXPLAIN options other than (FORMAT ...) are not allowed (EXPLAIN ANALYZE executes the statement)")
		}
		i += 4
	}
	if i < len(toks) && toks[i].IsWord("ANALYZE") {
		return deny("EXPLAIN ANALYZE is not allowed: it executes the statement")
	}
	if i >= len(toks) {
		return deny("EXPLAIN needs a statement")
	}
	v := a.statement(toks[i:], args, depth+1)
	if v.Class == sqlClassOther {
		return v
	}
	if v.Class != sqlClassRead {
		return deny("EXPLAIN is only allowed over a read statement")
	}
	return &sqlVerdict{Class: sqlClassRead, readers: v.readers}
}

// literalArgs reports whether toks (the inside of a call's parentheses) are
// only plain string literals, numbers, true/false/null and separators.
func literalArgs(toks []lexTok) bool {
	for _, t := range toks {
		switch {
		case t.Kind == lkString && t.Plain:
		case t.Kind == lkPunct && (t.Text == "," || t.Text == "-" || t.Text == "."):
		case t.Kind == lkWord && (t.Text == "TRUE" || t.Text == "FALSE" || t.Text == "NULL" || (t.Text != "" && t.Text[0] >= '0' && t.Text[0] <= '9')):
		default:
			return false
		}
	}
	return true
}

// pragma allows only the read-style introspection PRAGMAs.
func (a *analyzer) pragma(toks []lexTok) *sqlVerdict {
	if len(toks) < 2 || !toks[1].IsName() {
		return deny("PRAGMA needs a name")
	}
	name := strings.ToLower(nameOf(toks[1]))
	if !allowedPragmas[name] {
		return deny(fmt.Sprintf("PRAGMA %s is not on the allow-list (only %s)", name, sortedNames(allowedPragmas)))
	}
	if len(toks) > 2 {
		if !toks[2].IsParen("(") || matchParen(toks, 2) != len(toks)-1 || !literalArgs(toks[3:len(toks)-1]) {
			return deny("a PRAGMA may only take literal arguments")
		}
	}
	if tableArgFuncs[name] && len(toks) > 2 {
		if r := a.vetTableArgToks(name, toks[3:len(toks)-1]); r != "" {
			return deny(r)
		}
	}
	return &sqlVerdict{Class: sqlClassRead}
}

// vetTableArgToks vets the argument tokens of a PRAGMA or CALL that takes a
// table name: exactly one plain string literal.
func (a *analyzer) vetTableArgToks(fn string, args []lexTok) string {
	if len(args) != 1 || args[0].Kind != lkString || !args[0].Plain {
		return fmt.Sprintf("%s takes one table name written as a string literal", fn)
	}
	return a.vetTableArg(fn, args[0].Text)
}

// call allows only CALL of the catalog metadata functions with literal arguments.
func (a *analyzer) call(toks []lexTok) *sqlVerdict {
	if len(toks) < 4 || !toks[1].IsName() || !toks[2].IsParen("(") || matchParen(toks, 2) != len(toks)-1 {
		return deny("CALL is only allowed as CALL <catalog function>(literal arguments)")
	}
	name := strings.ToLower(nameOf(toks[1]))
	if !allowedCalls[name] {
		return deny(fmt.Sprintf("CALL %s is not on the allow-list (only %s)", name, sortedNames(allowedCalls)))
	}
	if !literalArgs(toks[3 : len(toks)-1]) {
		return deny("CALL may only take literal arguments")
	}
	if tableArgFuncs[name] {
		if r := a.vetTableArgToks(name, toks[3:len(toks)-1]); r != "" {
			return deny(r)
		}
	}
	return &sqlVerdict{Class: sqlClassRead}
}

func nameOf(t lexTok) string {
	if t.Kind == lkIdent {
		return t.Text
	}
	return t.Raw
}

func sortedNames(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// small, fixed sets: a simple insertion sort keeps messages stable.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return strings.Join(out, ", ")
}

// notReadyWhy words a storage connection's state for a refusal: its status, and
// for an AWS SSO sign-in what the person does about it (profile name only).
func notReadyWhy(c session.ConnectionInfo) string {
	if c.SignIn != nil {
		return string(c.Kind) + ": " + (&secrets.ErrNeedsCredentials{SignIn: c.SignIn}).Error()
	}
	return string(c.Kind)
}
