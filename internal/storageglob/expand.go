// Package storageglob expands wildcards in S3, GCS and R2 URLs for the SQL
// Slaw runs (#138 follow-up).
//
// Every hub session reads those URLs as literal keys (s3_url_compatibility_mode,
// so that no "?s3_endpoint=" in a URL can send a connection's signed request to
// another host), and DuckDB then does not glob them. So Slaw globs them itself:
// it finds the pattern literals in a statement with DuckDB's own parser, lists
// the matching objects in a private session that holds only the covering
// connection's secret (session.hub.ListMatching), and rewrites exactly those
// literals into an explicit list of quoted keys, which the sealed hub reads
// literally. A hostile key such as "x.parquet?s3_endpoint=evil" is only a name.
//
// What is a pattern: a plain '...' string literal that is an s3, s3a, s3n,
// gcs, gs or r2 URL holding "*" (or "**"), and that sits where DuckDB would
// glob it: the first argument (one string, or a list of strings) of a file
// reader (read_parquet, read_csv, read_json, read_text, read_blob, ..., and
// glob()), or a table in FROM ('s3://b/*.parquet'). A string anywhere else
// (SELECT 's3://b/*') is a value and is never touched. Only "*" and "**" are
// wildcards: "?" and "[" are refused in a pattern (storageurl.ValidatePattern).
//
// The listing is a point in time: the objects that matched when the statement
// started. Nothing is cached between runs. A statement that matches more than
// Max objects, or none, is refused with an error that names the pattern.
package storageglob

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// DefaultMax is the most objects one statement may expand to.
const DefaultMax = 10000

// Lister lists the objects an S3-like pattern matches, up to limit (it may
// return one more than limit to say there are more). The hub implements it:
// it picks the connection whose scope covers the pattern and lists in a
// private session that holds that connection's secret alone.
type Lister interface {
	ListMatching(ctx context.Context, pattern string, limit int) ([]string, error)
}

// Options configures Expand. Lister is required when a pattern is present.
type Options struct {
	Lister Lister
	Parser Parser // nil = DefaultParser()
	// Vet, when set, is asked about every pattern before anything is listed
	// (the agent's scope check). A non-nil error refuses the statement.
	Vet func(pattern string) error
	Max int // 0 = DefaultMax
	// RequirePlain refuses a matching object whose name is not a plain storage
	// URL (a space, "?", "#", "%" or "*" in it), which the agent never reads.
	RequirePlain bool
}

// Rewrite describes what an expansion did. The zero value and nil mean
// "nothing was rewritten".
type Rewrite struct {
	Patterns int // distinct patterns expanded
	Keys     int // keys they expanded to
	subs     []sub
}

// sub is one replaced literal: the list that took its place, the literal as
// written, and the start of the first quoted key (a fragment of the list that
// DuckDB's "LINE n:" context may show on its own).
type sub struct {
	repl, orig, probe string
	n                 int // objects the pattern expanded to
}

// Changed reports whether the text was rewritten.
func (r *Rewrite) Changed() bool { return r != nil && len(r.subs) > 0 }

// maxMessage bounds an error message that carried the rewritten SQL.
const maxMessage = 4000

// Redact puts the pattern back wherever an error message quotes the rewritten
// list, replaces the "LINE n:" excerpt DuckDB shows of a statement whose list it
// cut short, and clips what is left, so an error never holds keys.
func (r *Rewrite) Redact(msg string) string {
	if !r.Changed() {
		return msg
	}
	for _, s := range r.subs {
		msg = strings.ReplaceAll(msg, s.repl, s.orig)
	}
	lines := strings.Split(msg, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if head, _, ok := strings.Cut(lines[i], ":"); ok && strings.HasPrefix(lines[i], "LINE ") && r.carriesKeys(lines[i]) {
			out = append(out, head+": (the statement, with each wildcard expanded to the files it matched)")
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "^" {
				i++ // the caret pointed into the excerpt
			}
			continue
		}
		out = append(out, lines[i])
	}
	msg = strings.Join(out, "\n")
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "... (message shortened)"
	}
	return msg
}

func (r *Rewrite) carriesKeys(line string) bool {
	for _, s := range r.subs {
		if s.probe != "" && strings.Contains(line, s.probe) {
			return true
		}
	}
	return false
}

// Summarize is the statement as a record may keep it: text with each expanded
// key list put back as the pattern the person wrote, followed by a comment
// saying how many objects it expanded to, so 10,000 keys never land in a
// receipt. n is the number of objects the patterns expanded to. Unlike Redact it
// never clips and never rewrites an error's "LINE n:" excerpt. sql is the text
// the rewrite produced (what ran); text with nothing rewritten comes back
// unchanged with n 0. The keyed hash of what ran (run_sql_hash) is the caller's.
func (r *Rewrite) Summarize(sql string) (text string, n int) {
	if !r.Changed() {
		return sql, 0
	}
	text = sql
	changed := false
	for _, s := range r.subs {
		if !strings.Contains(text, s.repl) {
			continue
		}
		changed = true
		n += s.n
		text = strings.Replace(text, s.repl, s.orig+fmt.Sprintf(" /* expanded to %d objects */", s.n), 1)
	}
	if !changed {
		return sql, 0
	}
	return text, n
}

type ctxKey struct{}

// WithRewrite marks ctx as carrying SQL that has already been through Expand
// (rw may be nil or empty): the engine then runs it as written and does not
// expand it again, and shortens errors with rw.Redact.
func WithRewrite(ctx context.Context, rw *Rewrite) context.Context {
	if rw == nil {
		rw = &Rewrite{}
	}
	return context.WithValue(ctx, ctxKey{}, rw)
}

// FromContext returns the rewrite WithRewrite stored.
func FromContext(ctx context.Context) (*Rewrite, bool) {
	rw, ok := ctx.Value(ctxKey{}).(*Rewrite)
	return rw, ok
}

// Expand rewrites every wildcard pattern in text to an explicit list of the
// objects it matches. It returns text unchanged (and a nil Rewrite) when there
// is nothing to expand, which costs one substring scan and no parse.
func Expand(ctx context.Context, text string, o Options) (string, *Rewrite, error) {
	if !mayHavePattern(text) {
		return text, nil, nil
	}
	if o.Parser == nil {
		o.Parser = DefaultParser()
	}
	if o.Max <= 0 {
		o.Max = DefaultMax
	}
	var sites []site
	for _, toks := range statements(text) {
		if !hasPatternToken(toks) {
			continue
		}
		if copiesFrom(toks) {
			return "", nil, fmt.Errorf("COPY ... FROM does not take a wildcard on s3, gcs or r2: load the files with INSERT INTO t SELECT * FROM read_csv('s3://bucket/dir/*.csv') (or read_parquet, read_json)")
		}
		ss, err := findSites(ctx, o.Parser, text, toks)
		if err != nil {
			return "", nil, err
		}
		if len(ss) > 0 && persistsSQL(toks) {
			return "", nil, fmt.Errorf("a view or macro cannot hold the wildcard %q: Slaw expands wildcards on s3, gcs and r2 when a query runs, so it would keep today's list of files; create a table from the query, or name the files", ss[0].pattern)
		}
		sites = append(sites, ss...)
	}
	if len(sites) == 0 {
		return text, nil, nil
	}
	return rewrite(ctx, text, sites, o)
}

// mayHavePattern is the cheap pre-filter.
func mayHavePattern(text string) bool {
	if !strings.Contains(text, "*") {
		return false
	}
	for _, s := range storageurl.S3Like {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// copiesFrom reports whether the statement is COPY <table> FROM '...'.
func copiesFrom(toks []tok) bool {
	if len(toks) == 0 || !toks[0].isWord("COPY") {
		return false
	}
	depth := 0
	for _, t := range toks[1:] {
		switch {
		case t.isParen("("):
			depth++
		case t.isParen(")"):
			depth--
		case depth == 0 && t.isWord("FROM"):
			return true
		case depth == 0 && t.isWord("TO"):
			return false
		}
	}
	return false
}

// persistsSQL reports whether the statement stores its SQL (CREATE VIEW,
// MACRO or FUNCTION).
func persistsSQL(toks []tok) bool {
	if len(toks) == 0 || !toks[0].isWord("CREATE") {
		return false
	}
	for _, t := range toks[1:min(len(toks), 8)] {
		switch t.Text {
		case "OR", "REPLACE", "TEMP", "TEMPORARY", "RECURSIVE":
			continue
		case "VIEW", "MACRO", "FUNCTION":
			return t.Kind == tkWord
		}
		return false
	}
	return false
}

func rewrite(ctx context.Context, text string, sites []site, o Options) (string, *Rewrite, error) {
	keys := map[string][]string{}
	total := 0
	rw := &Rewrite{}
	for _, s := range sites {
		if _, done := keys[s.pattern]; done {
			continue
		}
		ks, err := listPattern(ctx, s.pattern, o, o.Max-total)
		if err != nil {
			return "", nil, err
		}
		keys[s.pattern] = ks
		total += len(ks)
		rw.Patterns++
	}
	rw.Keys = total
	sort.Slice(sites, func(i, j int) bool { return sites[i].start < sites[j].start })
	var b strings.Builder
	prev := 0
	for _, s := range sites {
		if s.start < prev {
			return "", nil, fmt.Errorf("could not rewrite the wildcard pattern %q: it overlaps another", s.pattern)
		}
		if len(keys[s.pattern]) == 0 && (s.role != roleGlob || o.RequirePlain) {
			return "", nil, fmt.Errorf("no files found that match the pattern %q", s.pattern)
		}
		repl, err := replacement(s, keys[s.pattern])
		if err != nil {
			return "", nil, err
		}
		b.WriteString(text[prev:s.start])
		b.WriteString(repl)
		rw.subs = append(rw.subs, sub{repl: repl, orig: text[s.start:s.end], probe: probe(keys[s.pattern]), n: len(keys[s.pattern])})
		prev = s.end
	}
	b.WriteString(text[prev:])
	return b.String(), rw, nil
}

// probe is the start of the first quoted key, enough to recognise a fragment
// of the list in an error message.
func probe(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	q := session.QuoteLiteral(keys[0])
	return q[:min(len(q), 14)]
}

// listPattern vets and lists one pattern and checks what comes back.
func listPattern(ctx context.Context, pattern string, o Options, room int) ([]string, error) {
	if err := storageurl.ValidatePattern(pattern); err != nil {
		return nil, fmt.Errorf("the pattern %q cannot be expanded: %w (only * and ** are wildcards on s3, gcs and r2)", pattern, err)
	}
	if o.Vet != nil {
		if err := o.Vet(pattern); err != nil {
			return nil, err
		}
	}
	if o.Lister == nil {
		return nil, fmt.Errorf("the pattern %q cannot be expanded: no storage connection is available to list it", pattern)
	}
	if room < 0 {
		room = 0
	}
	ks, err := o.Lister.ListMatching(ctx, pattern, room)
	if err != nil {
		if errors.Is(err, ctx.Err()) {
			return nil, err
		}
		return nil, fmt.Errorf("could not list %q: %w", pattern, err)
	}
	if len(ks) > room {
		if room < o.Max {
			return nil, fmt.Errorf("the pattern %q and the others in this statement match more than %d objects in all, the most one query reads; narrow the patterns", pattern, o.Max)
		}
		return nil, fmt.Errorf("the pattern %q matches more than %d objects, the most one query reads; narrow the pattern", pattern, o.Max)
	}
	prefix := storageurl.PatternPrefix(pattern)
	bad, odd := 0, 0
	for _, k := range ks {
		if !strings.HasPrefix(k, prefix) {
			return nil, fmt.Errorf("could not list %q: the listing returned an object outside the pattern's folder", pattern)
		}
		if strings.ContainsFunc(k, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '?' || r == '#' }) {
			odd++
		} else if o.RequirePlain && storageurl.Validate(k) != nil {
			bad++
		}
	}
	// A name with a control character, "?" or "#" is never put into a statement:
	// anyone who can write to the bucket chooses names, and "?s3_..." is the
	// shape of a URL-parameter redirect. The sealed hub would read it literally
	// anyway; this is a second layer. Such an object is still readable by its
	// exact name.
	if odd > 0 {
		return nil, fmt.Errorf("%d of the objects that match %q have a control character, ? or # in the name, which Slaw does not put into a statement; narrow the pattern (an object like that can still be read by its exact name)", odd, pattern)
	}
	if bad > 0 {
		return nil, fmt.Errorf("%d of the objects that match %q have names that are not plain URLs (a space, ?, #, %% or * in the name), which cannot be read here; narrow the pattern", bad, pattern)
	}
	sort.Strings(ks)
	return ks, nil
}

// replacement is the text that takes a pattern literal's place.
func replacement(s site, keys []string) (string, error) {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = session.QuoteLiteral(k)
	}
	list := strings.Join(q, ", ")
	switch s.role {
	case roleElem:
		return list, nil
	case roleArg:
		return "[" + list + "]", nil
	case roleGlob:
		if len(keys) == 0 {
			return "[]::VARCHAR[]", nil
		}
		return "[" + list + "]", nil
	case roleFrom:
		fn, ok := fromReader(s.pattern)
		if !ok {
			return "", fmt.Errorf("cannot tell how to read %q from its name: write read_parquet(%s), read_csv(...) or read_json(...) around it", s.pattern, session.QuoteLiteral(s.pattern))
		}
		return fn + "([" + list + "])", nil
	}
	return "", fmt.Errorf("could not rewrite the wildcard pattern %q", s.pattern)
}

// fromReader is the reader DuckDB's replacement scan would use for a name, by
// extension (a trailing .gz or .zst is looked through).
func fromReader(pattern string) (string, bool) {
	name := strings.ToLower(pattern[strings.LastIndexByte(pattern, '/')+1:])
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".zst")
	switch {
	case strings.HasSuffix(name, ".parquet"), strings.HasSuffix(name, ".pq"):
		return "read_parquet", true
	case strings.HasSuffix(name, ".csv"), strings.HasSuffix(name, ".tsv"), strings.HasSuffix(name, ".tbl"):
		return "read_csv", true
	case strings.HasSuffix(name, ".json"), strings.HasSuffix(name, ".jsonl"), strings.HasSuffix(name, ".ndjson"):
		return "read_json_auto", true
	}
	return "", false
}
