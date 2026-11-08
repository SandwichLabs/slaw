package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// cacheStore is the fingerprinted schema cache (duck-tape PRD). An entry is
// valid while its fingerprint matches: for local catalogs the fingerprint
// includes a content hash of the catalog's tables, so DDL that bypassed the
// session is still seen; for remote catalogs (whose listing costs round trips)
// it is the generation counter, bumped by attach / detach / session DDL /
// InvalidateSchema.
type cacheStore struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	hits    int
	misses  int
}

type cacheEntry struct {
	fp  string
	val any
}

func (c *cacheStore) init() { c.entries = map[string]cacheEntry{} }

func (c *cacheStore) get(key, fp string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok && e.fp == fp {
		c.hits++
		return e.val, true
	}
	c.misses++
	return nil, false
}

// cached is get with a typed result; a mistyped entry counts as a miss.
func cached[T any](c *cacheStore, key, fp string) (T, bool) {
	var zero T
	v, ok := c.get(key, fp)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		return zero, false
	}
	return t, true
}

func (c *cacheStore) put(key, fp string, v any) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{fp: fp, val: v}
	c.mu.Unlock()
}

func (c *cacheStore) clear() {
	c.mu.Lock()
	c.entries = map[string]cacheEntry{}
	c.mu.Unlock()
}

func (h *hub) InvalidateSchema(name string) {
	h.gen.Add(1)
	h.cache.clear()
	h.schemaMu.Lock()
	if name == "" {
		h.defaultSchema = map[string]string{}
	} else {
		delete(h.defaultSchema, name)
	}
	h.schemaMu.Unlock()
	// A remote catalog caches its own metadata (Postgres: pg_clear_cache).
	// No remote type exists yet; #115 hooks its refresh SQL in here.
}

func (h *hub) fingerprint(ctx context.Context, catalog string) (string, error) {
	gen := fmt.Sprintf("g%d", h.gen.Load())
	if h.isRemote(catalog) {
		return gen, nil
	}
	var sum string
	err := h.db.QueryRowContext(ctx, `
		SELECT coalesce(md5(string_agg(k, ';' ORDER BY k)), '') FROM (
		  SELECT schema_name || '.' || table_name || ':' || coalesce(sql, '') AS k
		    FROM duckdb_tables() WHERE database_name = $1
		  UNION ALL
		  SELECT schema_name || '.' || view_name || ':' || coalesce(sql, '')
		    FROM duckdb_views() WHERE database_name = $1 AND NOT internal
		)`, catalog).Scan(&sum)
	if err != nil {
		return "", fmt.Errorf("failed to fingerprint catalog %q: %w", catalog, err)
	}
	return gen + ":" + sum, nil
}

func (h *hub) catalogOrDefault(catalog string) string {
	if catalog == "" {
		return h.defCat()
	}
	return catalog
}

// requireCatalog accepts only the default catalog and attached connections:
// DuckDB's own "system" and "temp" catalogs are never valid table qualifiers.
func (h *hub) requireCatalog(name string) error {
	if name == h.defCat() {
		return nil
	}
	e, eAtt := h.entryOf(name)
	if e == nil || !eAtt {
		return fmt.Errorf("unknown connection %q", name)
	}
	return nil
}

func (h *hub) ListCatalogs(ctx context.Context) ([]CatalogInfo, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT database_name, type, readonly FROM duckdb_databases() WHERE NOT internal
		   AND database_name NOT LIKE '\_\_ducklake\_metadata\_%' ESCAPE '\'
		 ORDER BY database_name`)
	if err != nil {
		return nil, fmt.Errorf("failed to list catalogs: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []CatalogInfo
	for rows.Next() {
		var c CatalogInfo
		var typ *string
		if err := rows.Scan(&c.Name, &typ, &c.ReadOnly); err != nil {
			return nil, fmt.Errorf("failed to scan catalog: %w", err)
		}
		if typ != nil {
			c.Type = *typ
		}
		c.Default = c.Name == h.defCat()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list catalogs: %w", err)
	}
	return out, nil
}

func (h *hub) ListSchemas(ctx context.Context, catalog string) ([]string, error) {
	catalog = h.catalogOrDefault(catalog)
	if err := h.requireCatalog(catalog); err != nil {
		return nil, err
	}
	fp, err := h.fingerprint(ctx, catalog)
	if err != nil {
		return nil, err
	}
	key := "schemas|" + catalog
	if v, ok := cached[[]string](&h.cache, key, fp); ok {
		return append([]string(nil), v...), nil
	}
	rows, err := h.db.QueryContext(ctx, `
		SELECT schema_name FROM duckdb_schemas()
		 WHERE database_name = $1
		   AND schema_name NOT IN ('information_schema', 'pg_catalog')
		   AND NOT (schema_name IN ('mysql', 'performance_schema', 'sys')
		            AND coalesce((SELECT type FROM duckdb_databases() WHERE database_name = $1), '') = 'mysql')
		 ORDER BY schema_name`, catalog)
	if err != nil {
		return nil, fmt.Errorf("failed to list schemas of %q: %w", catalog, err)
	}
	defer rows.Close() //nolint:errcheck
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("failed to scan schema: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list schemas of %q: %w", catalog, err)
	}
	h.cache.put(key, fp, out)
	return append([]string(nil), out...), nil
}

func (h *hub) DefaultSchema(ctx context.Context, catalog string) (string, error) {
	catalog = h.catalogOrDefault(catalog)
	h.schemaMu.Lock()
	if s, ok := h.defaultSchema[catalog]; ok {
		h.schemaMu.Unlock()
		return s, nil
	}
	h.schemaMu.Unlock()

	var schema string
	if catalog == h.defCat() {
		if err := h.db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
			return "", fmt.Errorf("failed to read default schema: %w", err)
		}
	} else {
		conn, err := h.pin(ctx, catalog, nil)
		if err != nil {
			return "", err
		}
		defer conn.Close() //nolint:errcheck
		if err := conn.c.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
			return "", fmt.Errorf("failed to read default schema of %q: %w", catalog, err)
		}
	}
	h.schemaMu.Lock()
	h.defaultSchema[catalog] = schema
	h.schemaMu.Unlock()
	return schema, nil
}

func (h *hub) Resolve(ctx context.Context, ref TableRef) (TableRef, error) {
	if ref.Name == "" {
		return ref, fmt.Errorf("table name is empty")
	}
	if ref.Catalog == "" && ref.Schema != "" {
		// "x.t": x is a catalog when one is attached under that name and
		// otherwise a schema of the default catalog. A registered but not yet
		// attached remote connection connects here (lazy attach).
		e, eAtt := h.entryOf(ref.Schema)
		if e != nil && !eAtt && !h.catalogless(e) {
			if err := h.connectImplicit(ctx, ref.Schema); err != nil {
				return ref, err
			}
			_, eAtt = h.entryOf(ref.Schema)
		}
		if e != nil && eAtt {
			// catalog.table: use that catalog's default schema.
			ref.Catalog, ref.Schema = ref.Schema, ""
		}
	}
	if ref.Catalog == "" {
		ref.Catalog = h.defCat()
	}
	ce, ceAtt := h.entryOf(ref.Catalog)
	if ce != nil && !ceAtt && ref.Catalog != h.defCat() {
		if err := h.connectImplicit(ctx, ref.Catalog); err != nil {
			return ref, err
		}
	}
	if err := h.requireCatalog(ref.Catalog); err != nil {
		return ref, err
	}
	if ref.Schema == "" {
		s, err := h.DefaultSchema(ctx, ref.Catalog)
		if err != nil {
			return ref, err
		}
		ref.Schema = s
	}
	return ref, nil
}

func (h *hub) ListTables(ctx context.Context, catalog, schema string) ([]TableInfo, error) {
	catalog = h.catalogOrDefault(catalog)
	if err := h.requireCatalog(catalog); err != nil {
		return nil, err
	}
	if schema == "" {
		s, err := h.DefaultSchema(ctx, catalog)
		if err != nil {
			return nil, err
		}
		schema = s
	}
	fp, err := h.fingerprint(ctx, catalog)
	if err != nil {
		return nil, err
	}
	key := "tables|" + catalog + "|" + schema
	if v, ok := cached[[]TableInfo](&h.cache, key, fp); ok {
		return append([]TableInfo(nil), v...), nil
	}
	rows, err := h.db.QueryContext(ctx, `
		SELECT table_catalog, table_schema, table_name, table_type
		  FROM information_schema.tables
		 WHERE table_catalog = $1 AND table_schema = $2
		 ORDER BY table_name`, catalog, schema)
	if err != nil {
		return nil, fmt.Errorf("failed to list tables of %s.%s: %w", catalog, schema, err)
	}
	defer rows.Close() //nolint:errcheck
	out := []TableInfo{}
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Catalog, &t.Schema, &t.Name, &t.Type); err != nil {
			return nil, fmt.Errorf("failed to scan table: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list tables of %s.%s: %w", catalog, schema, err)
	}
	h.cache.put(key, fp, out)
	return append([]TableInfo(nil), out...), nil
}

// ErrNoSuchTable is why DescribeTable found nothing: the table is not there,
// or has no columns.
var ErrNoSuchTable = errors.New("does not exist or has no columns")

func (h *hub) DescribeTable(ctx context.Context, qualified string) (*TableDescription, error) {
	ref, err := ParseTableRef(qualified)
	if err != nil {
		return nil, err
	}
	ref, err = h.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	fp, err := h.fingerprint(ctx, ref.Catalog)
	if err != nil {
		return nil, err
	}
	key := "describe|" + ref.String()
	if v, ok := cached[*TableDescription](&h.cache, key, fp); ok {
		d := *v
		d.Columns = append([]ColumnInfo(nil), d.Columns...)
		return &d, nil
	}
	cols, err := ColumnsOf(ctx, h.db, ref)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %q %w", ref.String(), ErrNoSuchTable)
	}
	d := &TableDescription{Ref: ref, Columns: cols}
	h.cache.put(key, fp, d)
	cp := *d
	cp.Columns = append([]ColumnInfo(nil), cols...)
	return &cp, nil
}
