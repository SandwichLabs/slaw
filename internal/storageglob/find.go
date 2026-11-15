package storageglob

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// readerFuncs are the table functions whose first argument is a path or a list
// of paths that DuckDB globs (httpfs-backed readers and the metadata functions
// that take the same argument). Everything else (iceberg_scan, delta_scan,
// read_xlsx, st_read, sniff_csv, ...) takes one path and never globs, so a "*"
// in its argument is part of the name and is left alone.
var readerFuncs = []string{
	"read_parquet", "parquet_scan", "parquet_metadata", "parquet_schema",
	"parquet_file_metadata", "parquet_kv_metadata", "parquet_full_metadata", "parquet_bloom_probe",
	"read_csv", "read_csv_auto",
	"read_json", "read_json_auto", "read_ndjson", "read_ndjson_auto",
	"read_json_objects", "read_json_objects_auto", "read_ndjson_objects",
	"read_text", "read_blob", "read_avro",
}

// localReaderFuncs are single-path readers that never glob on s3: a relative
// local path given to one of them is resolved against the workspace root
// (Options.Root) like the globbing readers'.
var localReaderFuncs = []string{
	"iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "delta_scan",
	"read_xlsx", "st_read", "sniff_csv",
}

// wanted says which path literals findSites reports: s3-like patterns, and,
// when root is set, relative local paths.
type wanted struct{ root string }

// ok reports whether s, the path argument of function fn ("" = a FROM name),
// is a literal to rewrite.
func (w wanted) ok(fn, s string) bool {
	if (fn == "" || fn == "glob" || slices.Contains(readerFuncs, fn)) && isPattern(s) {
		return true
	}
	if w.root == "" || !isRelPath(s) {
		return false
	}
	// A FROM name is a file only when it is written as a path: a table may be
	// called "my.table" or "q.csv".
	return fn != "" || strings.Contains(s, "/")
}

var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)

// isRelPath reports whether s names a local file relative to the workspace:
// not a URL, not absolute, not a home-directory path, and shaped like a path
// (a separator, a wildcard or an extension) so a bare value is not touched.
func isRelPath(s string) bool {
	return isRelLocal(s) && (strings.ContainsAny(s, `/\*`) || filepath.Ext(s) != "")
}

// isRelLocal reports whether s is a local path that is not absolute: not a
// URL, not a home-directory path.
func isRelLocal(s string) bool {
	return s != "" && !urlScheme.MatchString(s) && !filepath.IsAbs(s) && !strings.HasPrefix(s, "~") && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, `\\`)
}

// copyToSite is the target of COPY ... TO '<relative path>', resolved like a
// reader's path so a write and a read of data/out.csv name one file. The agent
// policy still vets the resolved target (vetWrite).
func copyToSite(toks []tok, root string, confine bool) (site, bool) {
	if root == "" || len(toks) < 3 || !toks[0].isWord("COPY") {
		return site{}, false
	}
	depth := 0
	for i, t := range toks[1:] {
		switch {
		case t.isParen("("):
			depth++
		case t.isParen(")"):
			depth--
		case depth == 0 && t.isWord("TO"):
			n := i + 2 // index in toks of the token after TO
			if n >= len(toks) || toks[n].Kind != tkString || !toks[n].Plain || !isRelLocal(toks[n].Text) {
				return site{}, false
			}
			abs := resolveRel(root, toks[n].Text, confine)
			return site{pattern: toks[n].Text, abs: abs, start: toks[n].Start, end: toks[n].End}, abs != ""
		}
	}
	return site{}, false
}

// resolveRel is the absolute path a relative literal names under root. With
// confine set, a path that holds ".." or leaves root is not resolved (""), so
// the agent policy refuses it as it is written.
func resolveRel(root, lit string, confine bool) string {
	if root == "" || strings.ContainsAny(root, "*?[") || strings.ContainsFunc(lit, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	if confine {
		for _, seg := range strings.FieldsFunc(lit, func(r rune) bool { return r == '/' || r == '\\' }) {
			if seg == ".." {
				return ""
			}
		}
	}
	abs := filepath.Join(root, filepath.FromSlash(lit))
	if confine && abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return ""
	}
	return abs
}

// hasRelToken is the cheap pre-filter for relative paths.
func hasRelToken(toks []tok) bool {
	for _, t := range toks {
		if (t.Kind == tkString || t.Kind == tkIdent) && isRelPath(t.Text) {
			return true
		}
	}
	return false
}

// role says where a pattern literal sits, and so what replaces it.
type role int

const (
	roleArg  role = iota // the whole first argument of a reader: [keys]
	roleElem             // one element of a list first argument: 'k1', 'k2'
	roleGlob             // the whole argument of glob(): [keys], [] when nothing matches
	roleFrom             // FROM 'pattern': read_x([keys]), x chosen by the extension
)

// site is one pattern literal to replace.
type site struct {
	pattern    string
	role       role
	start, end int // byte span of the literal's token in the whole text
	// abs is set for a relative local path: the absolute path that takes its
	// place (Options.Root). "" = an s3, gcs or r2 pattern.
	abs string
}

// isPattern reports whether s is an S3-like URL holding a wildcard.
func isPattern(s string) bool { return storageurl.IsS3Like(s) && storageurl.HasWildcard(s) }

// hasPatternToken is the cheap pre-filter: a statement with no quoted S3-like
// URL holding "*" is never parsed.
func hasPatternToken(toks []tok) bool {
	for _, t := range toks {
		if (t.Kind == tkString || t.Kind == tkIdent) && isPattern(t.Text) {
			return true
		}
	}
	return false
}

// span is a byte range of the source.
type span struct{ start, end int }

const (
	maxCandidates = 64
	// maxParseBytes bounds the text the parser is shown for one statement.
	maxParseBytes = 8 << 20
)

// candidates are the pieces of a statement that might be a SELECT that
// json_serialize_sql can parse: the statement itself, every suffix that starts
// at SELECT, WITH or FROM (INSERT INTO t SELECT ..., CREATE TABLE t AS ...,
// EXPLAIN ...), and the inside of every parenthesised group that starts like a
// query (COPY (SELECT ...) TO ..., subqueries). They are tried outermost first
// and a piece that overlaps one already parsed is skipped, so a pattern is
// found once, in the SELECT that holds it.
func candidates(toks []tok) []span {
	last := toks[len(toks)-1].End
	out := []span{{toks[0].Start, last}}
	for i, t := range toks {
		switch {
		case i > 0 && (t.isWord("SELECT") || t.isWord("WITH") || t.isWord("FROM")):
			out = append(out, span{t.Start, last})
		case t.isParen("(") && i+1 < len(toks):
			c := matchParen(toks, i)
			if c < 0 || c == i+1 {
				continue
			}
			if f := toks[i+1]; f.isWord("SELECT") || f.isWord("WITH") || f.isWord("FROM") || f.isWord("VALUES") || f.isWord("TABLE") || f.isParen("(") {
				out = append(out, span{f.Start, toks[c-1].End})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b span) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// findSites returns the pattern literals of one statement, each located in toks
// and verified to be what the parser said it is.
func findSites(ctx context.Context, p Parser, text string, toks []tok, w wanted, confine bool) ([]site, error) {
	var sites []site
	covered := 0 // end of the last piece the parser accepted
	budget := maxParseBytes
	for _, c := range candidates(toks) {
		if c.start < covered {
			continue
		}
		if budget -= c.end - c.start; budget < 0 {
			break
		}
		tree, err := p.Parse(ctx, text[c.start:c.end])
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		covered = c.end
		var found []rawSite
		ctes := map[string]bool{}
		collectCTEs(tree, ctes)
		collect(tree, ctes, w, &found)
		for _, r := range found {
			s, err := locate(toks, c.start, r)
			if err != nil {
				if !isPattern(r.pattern) {
					continue // a relative path written as E'..' or $$..$$: left as written
				}
				return nil, err
			}
			if !isPattern(r.pattern) {
				// A relative local path; one that escapes the root is left as
				// written, for the policy to refuse.
				if s.abs = resolveRel(w.root, r.pattern, confine); s.abs == "" {
					continue
				}
			}
			sites = append(sites, s)
		}
	}
	return sites, nil
}

// rawSite is a pattern as the parser reported it.
type rawSite struct {
	pattern string
	role    role
	loc     int // offset into the parsed piece
}

func collect(node any, ctes map[string]bool, w wanted, out *[]rawSite) {
	switch v := node.(type) {
	case []any:
		for _, x := range v {
			collect(x, ctes, w, out)
		}
	case map[string]any:
		switch v["type"] {
		case "TABLE_FUNCTION":
			if fn, ok := v["function"].(map[string]any); ok {
				collectFunction(fn, w, out)
			}
		case "BASE_TABLE":
			name, _ := v["table_name"].(string)
			cat, _ := v["catalog_name"].(string)
			sch, _ := v["schema_name"].(string)
			if loc, ok := location(v); ok && cat == "" && sch == "" && w.ok("", name) && !ctes[name] {
				*out = append(*out, rawSite{name, roleFrom, loc})
			}
		}
		for _, x := range v {
			collect(x, ctes, w, out)
		}
	}
}

// collectCTEs gathers the names of the CTEs a statement defines: a table
// reference with one of those names is the CTE, not a file.
func collectCTEs(node any, out map[string]bool) {
	switch v := node.(type) {
	case []any:
		for _, x := range v {
			collectCTEs(x, out)
		}
	case map[string]any:
		if cm, ok := v["cte_map"].(map[string]any); ok {
			items, _ := cm["map"].([]any)
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					if k, ok := m["key"].(string); ok {
						out[k] = true
					}
				}
			}
		}
		for _, x := range v {
			collectCTEs(x, out)
		}
	}
}

func collectFunction(fn map[string]any, w wanted, out *[]rawSite) {
	name, _ := fn["function_name"].(string)
	name = strings.ToLower(name)
	isGlob := name == "glob"
	if !isGlob && !slices.Contains(readerFuncs, name) && !(w.root != "" && slices.Contains(localReaderFuncs, name)) {
		return
	}
	children, _ := fn["children"].([]any)
	for _, c := range children {
		if isNamedParam(c) {
			continue
		}
		// The first positional argument is the path.
		if s, loc, ok := constString(c); ok {
			if w.ok(name, s) {
				r := roleArg
				if isGlob {
					r = roleGlob
				}
				*out = append(*out, rawSite{s, r, loc})
			}
			return
		}
		if m, ok := c.(map[string]any); ok && m["function_name"] == "list_value" {
			elems, _ := m["children"].([]any)
			for _, e := range elems {
				if s, loc, ok := constString(e); ok && w.ok(name, s) {
					*out = append(*out, rawSite{s, roleElem, loc})
				}
			}
		}
		return
	}
}

// isNamedParam reports whether a function argument is name = value.
func isNamedParam(n any) bool {
	m, ok := n.(map[string]any)
	if !ok || m["type"] != "COMPARE_EQUAL" {
		return false
	}
	l, _ := m["left"].(map[string]any)
	return l != nil && l["type"] == "COLUMN_REF"
}

// constString returns the value and location of a VARCHAR constant.
func constString(n any) (string, int, bool) {
	m, ok := n.(map[string]any)
	if !ok || m["type"] != "VALUE_CONSTANT" {
		return "", 0, false
	}
	val, _ := m["value"].(map[string]any)
	typ, _ := val["type"].(map[string]any)
	if val == nil || typ == nil || typ["id"] != "VARCHAR" {
		return "", 0, false
	}
	if null, _ := val["is_null"].(bool); null {
		return "", 0, false
	}
	s, ok := val["value"].(string)
	if !ok {
		return "", 0, false
	}
	loc, ok := location(m)
	return s, loc, ok
}

// location reads a node's query_location; the parser's "unknown" (2^64-1)
// and anything that is not a number is not a location.
func location(m map[string]any) (int, bool) {
	n, ok := m["query_location"].(json.Number)
	if !ok {
		return 0, false
	}
	u, err := strconv.ParseUint(n.String(), 10, 32)
	if err != nil {
		return 0, false
	}
	return int(u), true
}

// locate finds the token the parser's location points at and checks it is the
// literal the parser saw: a plain '...' string with exactly the pattern as its
// value (or, in FROM, a "..." name). Anything else (an E'...' string, adjacent
// literals the parser joined, a shifted offset) is refused, never guessed at.
func locate(toks []tok, base int, r rawSite) (site, error) {
	at := base + r.loc
	for i, t := range toks {
		if t.Start != at {
			continue
		}
		okKind := t.Kind == tkString && t.Plain || (r.role == roleFrom && t.Kind == tkIdent)
		if i+1 < len(toks) && toks[i+1].Kind == tkString {
			return site{}, fmt.Errorf("could not rewrite the wildcard pattern %q: it is followed by another string literal (adjacent literals are joined); write one literal", r.pattern)
		}
		if okKind && t.Text == r.pattern {
			return site{pattern: r.pattern, role: r.role, start: t.Start, end: t.End}, nil
		}
		break
	}
	return site{}, fmt.Errorf("could not rewrite the wildcard pattern %q: write it as one plain '...' string", r.pattern)
}
