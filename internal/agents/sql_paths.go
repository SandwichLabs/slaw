package agents

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// pathPolicy decides which files and URLs agent SQL may read or write (#116).
//
//   - A file is readable only when it is written out as a plain string literal,
//     is an absolute path, and (after resolving symlinks) lies inside the
//     workspace's data/ folder.
//   - A URL is readable only when it lies inside the scope of a ready storage
//     connection (s3, gcs, r2, azure): a literal prefix match, with no "..", query or
//     fragment, so it cannot wander to another bucket or object.
//   - COPY ... TO may only write inside data/.
//
// Relative paths are refused: DuckDB resolves them against the process's
// working directory, which is not the workspace, so a path vetted as
// "data/x.csv" could read a different file when run.
type pathPolicy struct {
	dataDir string   // symlink-resolved absolute path of the workspace data/ folder; "" = no file access
	scopes  []string // normalised URL prefixes of ready storage connections
	// protected are files and folders COPY ... TO never writes into: the files
	// of attached connections (a DuckLake's catalog and its data folder, a duckdb
	// or sqlite file), symlink-resolved.
	protected []string
	// blocked are storage connections that are not ready (needs sign-in, error):
	// they widen nothing, but a refusal inside their scope names them, so the
	// person (or an MCP client) learns what to fix instead of "no connection".
	blocked []blockedScope
}

// blockedScope is a storage connection that is not ready, with why.
type blockedScope struct{ scope, name, why string }

// notReadyFor returns " (covered by the connection X, which is not ready: why)"
// when a connection that is not ready covers the URL or pattern, else "".
func (p *pathPolicy) notReadyFor(u string, pattern bool) string {
	for _, b := range p.blocked {
		if (pattern && storageurl.PatternWithin(u, b.scope)) || (!pattern && storageurl.Within(u, b.scope)) {
			return fmt.Sprintf(" (the storage connection %q covers it but is not ready: %s)", b.name, b.why)
		}
	}
	return ""
}

var urlSchemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)

// replacementScanExts are the file extensions DuckDB (and the extensions this
// app loads) turn into a table scan when they appear where a table name goes.
var replacementScanExts = []string{
	".csv", ".tsv", ".txt", ".parquet", ".pq", ".json", ".jsonl", ".ndjson", ".gz", ".zst",
	".db", ".sqlite", ".sqlite3", ".duckdb", ".xlsx", ".xls", ".arrow", ".feather", ".avro",
	".orc", ".shp", ".geojson", ".gpkg", ".lance", ".delta",
}

// looksLikeFile reports whether a name that did not resolve to a table is
// shaped like a file or URL, so DuckDB would try a replacement scan on it.
func looksLikeFile(name string) bool {
	if strings.ContainsAny(name, `/\*?`) || strings.Contains(name, "://") {
		return true
	}
	lower := strings.ToLower(name)
	for _, e := range replacementScanExts {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return false
}

func hasBadPathChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// hasDotDot reports whether any path segment (split on / and \) is "..".
func hasDotDot(s string) bool {
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// realPath resolves symlinks as far as the path exists, re-attaching the part
// that does not.
func realPath(p string) (string, error) {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, nil
	}
	dir, base := filepath.Split(p)
	dir = filepath.Clean(dir)
	if dir == p || dir == "." {
		return p, nil
	}
	rd, err := realPath(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(rd, base), nil
}

// vetReaderPath is the one check of a path given to reader function fn (lower
// case), whichever statement the call is in: the read rules, plus for the
// Iceberg and Delta readers the table-folder rule. Every place that vets a
// reader call's path goes through here, so no statement kind can miss it.
func (p *pathPolicy) vetReaderPath(fn, lit string) string {
	if r := p.vetRead(lit); r != "" {
		return r
	}
	if lakeReaders[fn] {
		return lakeReaderPathRefusal(lit)
	}
	return ""
}

const dataDirHint = "the agent can only read files inside the workspace's data/ folder, by absolute path"

// vetRead returns "" when lit may be read, else the reason it may not.
func (p *pathPolicy) vetRead(lit string) string {
	if lit == "" || hasBadPathChars(lit) {
		return "the file path is empty or contains control characters"
	}
	if urlSchemeRE.MatchString(lit) {
		return p.vetURL(lit)
	}
	if p.dataDir == "" {
		return "file reads are not available here (this file has no workspace data/ folder)"
	}
	if !filepath.IsAbs(lit) {
		return fmt.Sprintf("%q is a relative path; %s: use %s", lit, dataDirHint, filepath.Join(p.dataDir, "<file>"))
	}
	if hasDotDot(lit) {
		return fmt.Sprintf("%q contains \"..\"; %s", lit, dataDirHint)
	}
	glob := strings.IndexAny(lit, "*?[")
	if glob >= 0 {
		return p.vetGlob(lit, glob)
	}
	real, err := realPath(lit)
	if err != nil || !filestore.Within(p.dataDir, real) {
		return fmt.Sprintf("%q is outside the workspace's data/ folder (%s)", lit, p.dataDir)
	}
	return ""
}

// vetGlob accepts a pattern whose literal directory prefix is inside data/ and
// under which no symlink points out of data/ (a glob cannot be resolved one
// match at a time before DuckDB expands it).
func (p *pathPolicy) vetGlob(lit string, glob int) string {
	prefix := lit[:glob]
	if i := strings.LastIndexAny(prefix, `/\`); i >= 0 {
		prefix = prefix[:i+1]
	}
	real, err := realPath(prefix)
	if err != nil || !filestore.Within(p.dataDir, real) {
		return fmt.Sprintf("%q is outside the workspace's data/ folder (%s)", lit, p.dataDir)
	}
	const maxEntries = 20000
	seen := 0
	escaped := false
	walkErr := filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		seen++
		if seen > maxEntries {
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if target, err := filepath.EvalSymlinks(path); err != nil || !filestore.Within(p.dataDir, target) {
				escaped = true
				return fs.SkipAll
			}
		}
		return nil
	})
	_ = walkErr
	if escaped {
		return fmt.Sprintf("%q matches a symlink that leaves the workspace's data/ folder", lit)
	}
	if seen > maxEntries {
		return fmt.Sprintf("%q matches too many files to check; narrow the pattern", lit)
	}
	return ""
}

// patternRefusal is the policy refusing an s3, gcs or r2 wildcard pattern.
type patternRefusal struct{ msg string }

func (e *patternRefusal) Error() string { return e.msg }

// vetPattern is vetURL for a wildcard pattern, asked before it is expanded: a
// plain s3, gcs or r2 URL (only * and ** are wildcards) whose text starts with
// the scope of a ready storage connection, so every object it can match is
// inside that scope. The keys it expands to are vetted again as plain URLs.
func (p *pathPolicy) vetPattern(pattern string) error {
	if err := storageurl.ValidatePattern(pattern); err != nil {
		return &patternRefusal{fmt.Sprintf("the pattern %q is not a plain storage URL (%v); only * and ** are wildcards", pattern, err)}
	}
	for _, s := range p.scopes {
		if storageurl.PatternWithin(pattern, s) {
			return nil
		}
	}
	if len(p.scopes) == 0 {
		return &patternRefusal{fmt.Sprintf("the pattern %q cannot be read: this workspace has no ready storage connection (s3, gcs, r2 or azure) whose scope contains it%s", pattern, p.notReadyFor(pattern, true))}
	}
	return &patternRefusal{fmt.Sprintf("the pattern %q is outside every storage connection's scope (%s); a wildcard must start with a scope%s", pattern, strings.Join(p.scopes, ", "), p.notReadyFor(pattern, true))}
}

func (p *pathPolicy) vetURL(lit string) string {
	// One shared normaliser (internal/storageurl) decides what a plain storage
	// URL is: known scheme, lower case, no query, fragment, percent-escape,
	// backslash, "." / ".." / empty segment. Anything else is never readable.
	if err := storageurl.ValidateRead(lit); err != nil {
		hint := ""
		if strings.Contains(lit, "*") && storageurl.IsS3Like(lit) {
			hint = "; Slaw expands a wildcard on s3, gcs and r2 only as a reader function's path (read_parquet('s3://b/*.parquet')) or a FROM 's3://b/*.parquet', so this one was not expanded"
		}
		return fmt.Sprintf("the URL %q is not a plain storage URL (%v); only plain object paths inside a storage connection's scope can be read%s", lit, err, hint)
	}
	for _, s := range p.scopes {
		if storageurl.Within(lit, s) {
			return ""
		}
	}
	if len(p.scopes) == 0 {
		return fmt.Sprintf("the URL %q cannot be read: this workspace has no ready storage connection (s3, gcs, r2 or azure) whose scope contains it%s", lit, p.notReadyFor(lit, false))
	}
	return fmt.Sprintf("the URL %q is outside every storage connection's scope (%s)%s", lit, strings.Join(p.scopes, ", "), p.notReadyFor(lit, false))
}

// vetWrite returns "" when COPY ... TO lit is allowed: an absolute, glob-free
// path inside data/ whose parent exists and resolves inside data/.
func (p *pathPolicy) vetWrite(lit string) string {
	if lit == "" || hasBadPathChars(lit) || urlSchemeRE.MatchString(lit) {
		return "COPY ... TO may only write to a plain file path inside the workspace's data/ folder"
	}
	if p.dataDir == "" {
		return "file writes are not available here (this file has no workspace data/ folder)"
	}
	if !filepath.IsAbs(lit) || hasDotDot(lit) || strings.ContainsAny(lit, "*?[") {
		return fmt.Sprintf("COPY ... TO %q: the target must be an absolute path inside %s with no \"..\" or wildcards", lit, p.dataDir)
	}
	clean := filepath.Clean(lit)
	// Never write over a database file (an attached connection's file lives in
	// data/ and an overwrite would corrupt it) or something loadable.
	lower := strings.ToLower(clean)
	for _, ext := range []string{".duckdb", ".ducklake", ".db", ".wal", ".sqlite", ".sqlite3", ".duckdb_extension", ".so", ".dylib", ".dll"} {
		if strings.HasSuffix(lower, ext) {
			return fmt.Sprintf("COPY ... TO %q: the agent does not write database or extension files", lit)
		}
	}
	// Nor a table's own metadata (see metadataWriteRefusal): the agent never
	// reads table metadata it could have written.
	if rel, ok := relUnder(p.dataDir, clean); ok {
		if why := metadataWriteRefusal(rel); why != "" {
			return fmt.Sprintf("COPY ... TO %q: %s", lit, why)
		}
	}
	if fi, err := os.Lstat(clean); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Sprintf("COPY ... TO %q: the target is a symlink", lit)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil || !filestore.Within(p.dataDir, parent) {
		return fmt.Sprintf("COPY ... TO %q: the target must be inside %s", lit, p.dataDir)
	}
	// Never into a connection's own files: a DuckLake's catalog or data folder
	// (whatever the file is called), a duckdb or sqlite file.
	target := filepath.Join(parent, filepath.Base(clean))
	if rel, ok := relUnder(p.dataDir, target); ok {
		if why := metadataWriteRefusal(rel); why != "" {
			return fmt.Sprintf("COPY ... TO %q: %s", lit, why)
		}
	}
	for _, prot := range p.protected {
		if filestore.Within(prot, target) {
			return fmt.Sprintf("COPY ... TO %q: the target is part of a connection's own files; the agent does not write there", lit)
		}
	}
	return ""
}

// normalizeScope turns a storage connection's scope into the prefix reads are
// matched against: a plain storage URL that always ends in a slash, so
// "s3://bucket" never admits "s3://bucket-evil/..." and "az://events/2024" never
// admits "az://events/2024-evil/". A scope with any other scheme ("file:///",
// "http://", "hf://") or that is not a plain URL normalises to "": it widens
// nothing.
func normalizeScope(scope string) string {
	sc, err := storageurl.Scope(scope)
	if err != nil {
		return ""
	}
	return sc
}

// relUnder is path relative to dir, when path is lexically inside dir.
func relUnder(dir, path string) (string, bool) {
	if dir == "" || !filestore.Within(dir, path) {
		return "", false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(filepath.Clean(path), filepath.Clean(dir)), string(filepath.Separator))
	return filepath.ToSlash(rel), true
}

// metadataWriteRefusal is the reason the agent may not write rel (a path
// inside data/): it would be the metadata of an Iceberg or Delta table, which
// names the files, and the URLs, that iceberg_scan and delta_scan then read.
// Any `metadata` or `_delta_log` folder anywhere, a version hint, a manifest
// (.avro) and any *metadata.json are refused. The lake readers refuse a local
// file path in the first place (lakeReaderPathRefusal), so neither half alone
// is trusted.
func metadataWriteRefusal(rel string) string {
	for _, seg := range strings.Split(strings.ToLower(rel), "/") {
		if seg == "_delta_log" || seg == "metadata" || seg == "version-hint.text" ||
			strings.HasSuffix(seg, ".avro") || strings.HasSuffix(seg, "metadata.json") {
			return "the agent does not write the metadata of an Iceberg or Delta table"
		}
	}
	return ""
}

// lakeReaderPathRefusal holds the local path given to iceberg_scan,
// iceberg_metadata, iceberg_snapshots or delta_scan to a table directory. A
// path to a file that is (or reads as) table metadata is refused: DuckDB treats
// any .json path as an Iceberg metadata file and follows the URLs in it while
// binding. Storage URLs are not touched here (the agent writes only to data/).
func lakeReaderPathRefusal(lit string) string {
	if urlSchemeRE.MatchString(lit) {
		return ""
	}
	base := strings.ToLower(lit)
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	for _, suf := range []string{".json", ".avro", ".gz", ".zst", ".text", ".txt"} {
		if strings.Contains(base, suf) {
			return fmt.Sprintf("%q is a file, not a table folder: the agent reads Iceberg and Delta tables by their folder or storage URL, never from a metadata file it could have written", lit)
		}
	}
	if strings.Contains(base, "metadata") || strings.Contains(base, "version-hint") {
		return fmt.Sprintf("%q looks like table metadata: the agent reads Iceberg and Delta tables by their folder or storage URL only", lit)
	}
	return ""
}
