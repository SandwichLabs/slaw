package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/filestore"
	"github.com/SandwichLabs/slaw/internal/session"
)

// Catalog is the schema snapshot completion reads: connections, their schemas,
// tables and views, and columns, plus DuckDB's function names. It holds names
// and types only: no rows, no DSN, no credential.
type Catalog struct {
	Version           int          `json:"version"`
	Generated         time.Time    `json:"generated"`
	DefaultConnection string       `json:"default_connection,omitempty"`
	Connections       []Connection `json:"connections"`
	Functions         []string     `json:"functions,omitempty"`
	// Metrics are the workspace's metrics (names, titles, dimensions; no SQL).
	// nil means the writer did not look (Merge then keeps the saved ones);
	// empty means there are none.
	Metrics []MetricDoc `json:"metrics"`
}

// Connection is one attached catalog.
type Connection struct {
	Name          string    `json:"name"`
	Type          string    `json:"type,omitempty"`
	DefaultSchema string    `json:"default_schema,omitempty"`
	Schemas       []Schema  `json:"schemas"`
	Updated       time.Time `json:"updated"`
	// Truncated is set when a cap (Caps) cut relations or columns off.
	Truncated bool `json:"truncated,omitempty"`
	// Stale marks an entry served from the saved snapshot because the
	// connection is not connected now. It is in-memory only.
	Stale bool `json:"-"`
}

// Caps bounds what one connection contributes to a catalog, so a database with
// tens of thousands of tables does not stream every column into memory.
type Caps struct {
	RelationsPerSchema int // tables and views kept per schema
	ColumnsPerRelation int // columns kept per table or view
	Rows               int // information_schema rows read per connection
}

// DefaultCaps are the caps Build and Studio use.
func DefaultCaps() Caps {
	return Caps{RelationsPerSchema: 2000, ColumnsPerRelation: 512, Rows: 250_000}
}

func (c Caps) orDefault() Caps {
	if c.RelationsPerSchema <= 0 {
		c.RelationsPerSchema = DefaultCaps().RelationsPerSchema
	}
	if c.ColumnsPerRelation <= 0 {
		c.ColumnsPerRelation = DefaultCaps().ColumnsPerRelation
	}
	if c.Rows <= 0 {
		c.Rows = DefaultCaps().Rows
	}
	return c
}

// Schema is one schema of a connection.
type Schema struct {
	Name   string     `json:"name"`
	Tables []Relation `json:"tables"`
}

// Relation is a table or view.
type Relation struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"` // table | view
	Columns []Column `json:"columns"`
}

// Column is one column of a relation.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

const catalogVersion = 1

// SnapshotPath is where a workspace's catalog snapshot lives. It is machine
// local (.slaw-local is git-ignored): it reflects which connections this
// person has connected, and table names are not for the repository.
func SnapshotPath(root string) string {
	return filepath.Join(root, ".slaw-local", "catalog.json")
}

// Conn returns the connection called name (case-insensitive), or nil.
func (c *Catalog) Conn(name string) *Connection {
	if c == nil {
		return nil
	}
	for i := range c.Connections {
		if strings.EqualFold(c.Connections[i].Name, name) {
			return &c.Connections[i]
		}
	}
	return nil
}

// Schema returns the schema called name (case-insensitive), or nil.
func (c *Connection) Schema(name string) *Schema {
	if c == nil {
		return nil
	}
	for i := range c.Schemas {
		if strings.EqualFold(c.Schemas[i].Name, name) {
			return &c.Schemas[i]
		}
	}
	return nil
}

// Relation returns the table or view called name (case-insensitive), or nil.
func (s *Schema) Relation(name string) *Relation {
	if s == nil {
		return nil
	}
	for i := range s.Tables {
		if strings.EqualFold(s.Tables[i].Name, name) {
			return &s.Tables[i]
		}
	}
	return nil
}

// Load reads a snapshot. A missing file is an error satisfying
// errors.Is(err, os.ErrNotExist).
func Load(path string) (*Catalog, error) {
	b, err := readSnapshotFile(path)
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("failed to read the catalog snapshot: %w", err)
	}
	if c.Version != catalogVersion {
		return nil, fmt.Errorf("catalog snapshot version %d is not %d", c.Version, catalogVersion)
	}
	return &c, nil
}

// Save writes the snapshot atomically (temp file in the same directory, then
// rename over path), mode 0600. Nothing is written through a symbolic link:
// Snapshot refuses one (resolveSnapshot) before it calls Save.
func (c *Catalog) Save(path string) error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to encode the catalog snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	if err := filestore.WriteFileAtomicExact(path, b, 0o600); err != nil {
		return fmt.Errorf("failed to write the catalog snapshot: %w", err)
	}
	return nil
}

// resolveSnapshot is SnapshotPath for writing: the folder is created 0700, and
// a .slaw-local (or catalog.json) that is a symbolic link out of the workspace,
// as a cloned repository could ship, is refused.
func resolveSnapshot(root string) (string, error) {
	path, err := filestore.Resolve(root, filepath.ToSlash(filepath.Join(".slaw-local", "catalog.json")))
	if err != nil {
		return "", fmt.Errorf("refusing to write the catalog snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	return path, nil
}

// Merge returns c with every connection in fresh replacing the one of the same
// name. Connections fresh does not mention are kept, so a snapshot taken while
// only some connections are connected never forgets the others.
func (c *Catalog) Merge(fresh *Catalog) *Catalog {
	out := *fresh
	if c == nil {
		return &out
	}
	seen := map[string]bool{}
	for _, conn := range fresh.Connections {
		seen[strings.ToLower(conn.Name)] = true
	}
	out.Connections = append([]Connection{}, fresh.Connections...)
	for _, conn := range c.Connections {
		if !seen[strings.ToLower(conn.Name)] {
			out.Connections = append(out.Connections, conn)
		}
	}
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].Name < out.Connections[j].Name })
	if len(out.Functions) == 0 {
		out.Functions = c.Functions
	}
	if out.Metrics == nil {
		out.Metrics = c.Metrics
	}
	return &out
}

// Build snapshots every connection of sess that is connected now. It never
// dials: connecting is the caller's decision (connections connect on first
// use), so a connection that is not connected is left out, and Merge keeps
// whatever an earlier snapshot knew about it.
func Build(ctx context.Context, sess session.Session, opts ...BuildOption) (*Catalog, error) {
	var bo buildOpts
	for _, o := range opts {
		o(&bo)
	}
	c := &Catalog{Version: catalogVersion, Generated: time.Now().UTC(), DefaultConnection: sess.DefaultCatalog()}
	var errs []error
	if bo.metrics != nil {
		docs, err := loadMetricDocs(ctx, bo.metrics)
		if err != nil {
			errs = append(errs, err)
		}
		c.Metrics = docs
	}
	infos := catalogless(sess)
	for _, st := range sess.Connections() {
		if !completable(st, infos[st.Name], c.DefaultConnection) {
			continue
		}
		conn, err := buildConnection(ctx, sess, st, DefaultCaps())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", st.Name, err))
			continue
		}
		c.Connections = append(c.Connections, conn)
	}
	fns, err := functionNames(ctx, sess)
	if err != nil {
		errs = append(errs, err)
	}
	c.Functions = fns
	sort.Slice(c.Connections, func(i, j int) bool { return c.Connections[i].Name < c.Connections[j].Name })
	return c, errors.Join(errs...)
}

// BuildOption tunes Build and Snapshot.
type BuildOption func(*buildOpts)

type buildOpts struct{ metrics MetricLister }

// WithMetrics also snapshots the workspace's metrics (names, titles and
// dimensions; never SQL), so a language client completes metric('...').
func WithMetrics(src MetricLister) BuildOption { return func(o *buildOpts) { o.metrics = src } }

// catalogless is the set of registered connections that attach no catalog
// (s3, gcs, r2): there is no information_schema to read for them.
func catalogless(sess session.Session) map[string]bool {
	out := map[string]bool{}
	for _, info := range sess.ConnectionInfos() {
		if info.Catalogless {
			out[info.Name] = true
		}
	}
	return out
}

// completable reports whether a connection's schema may be read now: it is
// connected, has a catalog, and is not DuckDB's own scratch catalog (unless the
// workspace made it the default).
func completable(st session.Status, catalogless bool, defaultConn string) bool {
	if st.Kind != session.StatusOK || catalogless {
		return false
	}
	return !builtinCatalog(st.Name) || st.Name == defaultConn
}

// columnsSQL lists one catalog's columns with their relation's kind in a
// single round trip (information_schema works for every attached catalog type).
// $2 and $3 are the relation and column caps plus one, pushed into the query so
// a huge catalog does not stream every column; the extra row tells the caller a
// cap cut something off. $4 is the connection's default schema, read first so
// the row cap can never starve it.
const columnsSQL = `
SELECT table_schema, table_name, table_type, column_name, data_type
  FROM (
    SELECT c.table_schema, c.table_name, t.table_type, c.column_name, c.data_type, c.ordinal_position,
           dense_rank() OVER (PARTITION BY c.table_schema ORDER BY c.table_name) AS rel_rank,
           row_number() OVER (PARTITION BY c.table_schema, c.table_name ORDER BY c.ordinal_position) AS col_rank
      FROM information_schema.columns c
      JOIN information_schema.tables t
        ON t.table_catalog = c.table_catalog AND t.table_schema = c.table_schema AND t.table_name = c.table_name
     WHERE c.table_catalog = $1
  )
 WHERE rel_rank <= $2 AND col_rank <= $3
 ORDER BY (table_schema = $4) DESC, table_schema, table_name, ordinal_position`

func buildConnection(ctx context.Context, sess session.Session, st session.Status, caps Caps) (Connection, error) {
	caps = caps.orDefault()
	conn := Connection{Name: st.Name, Type: st.Type, Updated: time.Now().UTC()}
	if st.Type == engine.IcebergCatalogType {
		return buildNamesOnly(ctx, sess, conn, caps)
	}
	if ds, err := sess.DefaultSchema(ctx, st.Name); err == nil {
		conn.DefaultSchema = ds
	}
	rows, err := sess.DB().QueryContext(ctx, columnsSQL, st.Name, caps.RelationsPerSchema+1, caps.ColumnsPerRelation+1, conn.DefaultSchema)
	if err != nil {
		return conn, fmt.Errorf("failed to list columns: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	b := &columnBuilder{conn: &conn, caps: caps}
	for rows.Next() {
		var schema, table, typ, col, dtype string
		if err := rows.Scan(&schema, &table, &typ, &col, &dtype); err != nil {
			return conn, fmt.Errorf("failed to read columns: %w", err)
		}
		if !b.add(schema, table, typ, col, dtype) {
			break
		}
	}
	return conn, rows.Err()
}

// columnBuilder folds the ordered column rows into schemas and relations,
// applying the caps. add returns false once the row cap is reached.
type columnBuilder struct {
	conn *Connection
	caps Caps
	rows int
	sch  *Schema
	rel  *Relation
	skip bool // the current relation is over the per-schema cap
}

func (b *columnBuilder) add(schema, table, typ, col, dtype string) bool {
	if b.rows++; b.rows > b.caps.Rows {
		b.conn.Truncated = true
		return false
	}
	if hiddenSchema(schema) {
		return true
	}
	if b.sch == nil || b.sch.Name != schema {
		b.conn.Schemas = append(b.conn.Schemas, Schema{Name: schema})
		b.sch, b.rel, b.skip = &b.conn.Schemas[len(b.conn.Schemas)-1], nil, false
	}
	if b.rel == nil || b.rel.Name != table {
		if len(b.sch.Tables) >= b.caps.RelationsPerSchema {
			b.conn.Truncated, b.skip = true, true
			return true
		}
		b.skip = false
		b.sch.Tables = append(b.sch.Tables, Relation{Name: table, Kind: engine.RelationKind(typ)})
		b.rel = &b.sch.Tables[len(b.sch.Tables)-1]
	}
	if b.skip {
		return true
	}
	if len(b.rel.Columns) >= b.caps.ColumnsPerRelation {
		b.conn.Truncated = true
		return true
	}
	b.rel.Columns = append(b.rel.Columns, Column{Name: col, Type: dtype})
	return true
}

// builtinCatalog is DuckDB's own in-memory scratch catalog, which the hub
// keeps beside the workspace's connections once one of them is the default.
func builtinCatalog(name string) bool {
	return name == "memory" || name == "temp" || name == "system"
}

// hiddenSchema is catalog plumbing that is not data.
func hiddenSchema(s string) bool {
	return s == "information_schema" || s == "pg_catalog" || s == "pg_toast" ||
		strings.HasPrefix(s, "pg_temp_") || strings.HasPrefix(s, "pg_toast_temp_")
}

func functionNames(ctx context.Context, sess session.Session) ([]string, error) {
	rows, err := sess.DB().QueryContext(ctx,
		`SELECT DISTINCT function_name FROM duckdb_functions()
		  WHERE function_type IN ('scalar', 'aggregate', 'table', 'macro', 'table_macro')
		    AND function_name NOT LIKE '%\_\_%' ESCAPE '\'
		    AND regexp_matches(function_name, '^[a-z_][a-z0-9_]*$')
		  ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("failed to list functions: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("failed to read functions: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Snapshot builds a fresh catalog from sess and merges it into the workspace's
// snapshot (SnapshotPath(root)), keeping connections that are not connected
// now. It returns how many connections it wrote; 0 means nothing was written
// (none were connected, or the save failed, and the error says which). Build errors for one connection do not stop the others; they
// are returned (with the count) after the save.
func Snapshot(ctx context.Context, sess session.Session, root string, opts ...BuildOption) (int, error) {
	fresh, buildErr := Build(ctx, sess, opts...)
	n := len(fresh.Connections)
	if n == 0 {
		return 0, buildErr
	}
	if err := writeMerged(root, fresh); err != nil {
		return 0, err
	}
	return n, buildErr
}

// writeMerged merges fresh into the workspace's saved snapshot and writes it.
func writeMerged(root string, fresh *Catalog) error {
	path, err := resolveSnapshot(root)
	if err != nil {
		return err
	}
	old, err := Load(path)
	if err != nil {
		old = nil
	}
	return old.Merge(fresh).Save(path)
}

// Source hands the server the current snapshot, reloading the file when it
// changes (the TUI, `slaw lsp snapshot` or Studio rewrote it).
type Source struct {
	path string

	mu    sync.Mutex
	cat   *Catalog
	mtime time.Time
	size  int64
}

// NewSource reads the snapshot at path on demand.
func NewSource(path string) *Source { return &Source{path: path} }

// Catalog returns the snapshot, re-reading the file when its mtime or size
// changed. It returns nil while there is no readable snapshot.
func (s *Source) Catalog() *Catalog {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Lstat(s.path)
	if err != nil || !fi.Mode().IsRegular() {
		s.cat = nil
		return nil
	}
	if s.cat != nil && fi.ModTime().Equal(s.mtime) && fi.Size() == s.size {
		return s.cat
	}
	c, err := Load(s.path)
	if err != nil {
		return s.cat // keep the last good one while a writer is mid-change
	}
	s.cat, s.mtime, s.size = c, fi.ModTime(), fi.Size()
	return s.cat
}

// buildNamesOnly lists an iceberg_catalog's namespaces (at most
// engine.MaxCatalogNamespaces) and table names, with no columns: describing a
// table is a LoadTable (a metadata.json fetch) each, so completion knows the
// tables and a table's columns are fetched only when a person opens it. Both
// reads are duckdb_schemas()/duckdb_tables(), which make no fetch.
func buildNamesOnly(ctx context.Context, sess session.Session, conn Connection, caps Caps) (Connection, error) {
	db := sess.DB()
	rows, err := db.QueryContext(ctx, `SELECT schema_name FROM duckdb_schemas() WHERE database_name = $1 AND schema_name <> 'information_schema' ORDER BY schema_name LIMIT $2`,
		conn.Name, engine.MaxCatalogNamespaces+1)
	if err != nil {
		return conn, fmt.Errorf("failed to list namespaces: %w", err)
	}
	keep := map[string]int{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return conn, fmt.Errorf("failed to read namespaces: %w", err)
		}
		if len(keep) >= engine.MaxCatalogNamespaces {
			conn.Truncated = true
			break
		}
		keep[n] = len(conn.Schemas)
		conn.Schemas = append(conn.Schemas, Schema{Name: n})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return conn, fmt.Errorf("failed to list namespaces: %w", err)
	}
	trows, err := db.QueryContext(ctx, `SELECT schema_name, table_name FROM duckdb_tables() WHERE database_name = $1 ORDER BY schema_name, table_name LIMIT $2`,
		conn.Name, caps.Rows+1)
	if err != nil {
		return conn, fmt.Errorf("failed to list tables: %w", err)
	}
	defer trows.Close() //nolint:errcheck
	n := 0
	for trows.Next() {
		var schema, table string
		if err := trows.Scan(&schema, &table); err != nil {
			return conn, fmt.Errorf("failed to read tables: %w", err)
		}
		if n++; n > caps.Rows {
			conn.Truncated = true
			break
		}
		i, ok := keep[schema]
		if !ok {
			continue
		}
		if len(conn.Schemas[i].Tables) >= caps.RelationsPerSchema {
			conn.Truncated = true
			continue
		}
		conn.Schemas[i].Tables = append(conn.Schemas[i].Tables, Relation{Name: table, Kind: "table"})
	}
	return conn, trows.Err()
}
