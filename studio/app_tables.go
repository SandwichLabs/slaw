package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/SandwichLabs/slaw/internal/crud"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/session"
)

// --- Tables ---

// tableRefArg validates a table identifier arriving from the frontend: a bare
// name (default catalog) or schema.table / catalog.schema.table (an attached
// connection). Every part is validated and quoted when SQL is built.
//
// The returned reference is fully resolved (catalog, schema, table) through the
// session, so the SQL built from it never depends on the connection's current
// catalog or on a same-named temp table shadowing a bare name. An unknown
// connection ("system", "temp", a typo) is refused. The caller holds a.mu and
// has checked a.ws != nil.
func (a *App) tableRefArg(name string) (session.TableRef, error) {
	ref, err := engine.ParseTable(name)
	if err != nil {
		return ref, fmt.Errorf("invalid table name")
	}
	if sess := a.sess; sess != nil {
		resolved, rerr := sess.Resolve(a.ctxOrBackground(), ref)
		if rerr != nil {
			return ref, rerr
		}
		ref = resolved
		// Table paging, CRUD and the schema tree read the hub's pool directly,
		// not through the engine: this is where they renew a storage secret that
		// is about to expire (and dial the table's catalog on first use). A failure
		// is not fatal here; the read then reports its own.
		// Bounded: the caller holds a.mu, and a rotation can wait on a portal.
		cctx, cancel := context.WithTimeout(a.ctxOrBackground(), 10*time.Second)
		defer cancel()
		_ = sess.ConnectReferenced(cctx, ref.Catalog, "")
	}
	return ref, nil
}

// isDefaultTarget reports whether ref is in the session's default catalog and
// default schema (where a bare sequence name resolves).
func (a *App) isDefaultTarget(ref session.TableRef) bool {
	sess := a.sess
	if sess == nil {
		return false
	}
	schema, err := sess.DefaultSchema(a.ctxOrBackground(), "")
	return err == nil && ref.Catalog == sess.DefaultCatalog() && ref.Schema == schema
}

// ListTables returns all user tables of the default catalog (non _slaw_ tables).
func (a *App) ListTables() ([]string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}

	sess := a.sess
	if sess == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}
	tabs, err := sess.ListTables(a.ctxOrBackground(), "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}
	var tables []string
	for _, t := range tabs {
		tables = append(tables, t.Name)
	}
	return tables, nil
}

// ColumnInfo describes a table column.
type ColumnInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	NotNull bool   `json:"not_null"`
	PK      bool   `json:"pk"`
	// FK is read-only output from DescribeTable (see engine.ColumnInfo.FK);
	// CreateTable/AddColumn ignore it on input.
	FK bool `json:"fk"`
}

// TableInfo describes a table's schema.
type TableInfo struct {
	Name    string       `json:"name"`
	Columns []ColumnInfo `json:"columns"`
}

// DescribeTable returns schema info for a table.
func (a *App) DescribeTable(name string) (*TableInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}

	tref, err := a.tableRefArg(name)
	if err != nil {
		return nil, err
	}

	db := a.sess.DB()
	if db == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}
	// Shared introspection (internal/schema): fills real PRIMARY KEY membership
	// from duckdb_constraints() — the previous inline query always reported
	// PK=false, leaving the schema tree / ER diagram to guess by column name.
	cols, err := engine.TableColumns(db, tref)
	if err != nil {
		return nil, fmt.Errorf("failed to describe table: %w", err)
	}
	info := &TableInfo{Name: name}
	for _, c := range cols {
		info.Columns = append(info.Columns, ColumnInfo{
			Name:    c.Name,
			Type:    c.Type,
			NotNull: c.NotNull,
			PK:      c.PK,
			FK:      c.FK,
		})
	}
	return info, nil
}

// TableData holds paginated table data.
type TableData struct {
	Columns []string `json:"columns"`
	// Types is the DuckDB type name of each column, parallel to Columns
	// — the datasheet needs it to pick an editor (date input vs
	// text), align/format cells, and round-trip a DECIMAL's scale.
	Types      []string `json:"types"`
	Rows       [][]any  `json:"rows"`
	Page       int      `json:"page"`
	PageSize   int      `json:"page_size"`
	TotalRows  int      `json:"total_rows"`
	TotalPages int      `json:"total_pages"`
}

// GetTableData returns paginated data from a table.
func (a *App) GetTableData(name string, page, pageSize int, sortCol, sortDir string) (*TableData, error) {
	return a.pageTable(name, FilterSpec{}, page, pageSize, sortCol, sortDir)
}

// FilterCondition and FilterSpec are the structured datasheet / QBE filter
// shapes (plan §2.3), defined once in internal/crud with the same JSON.
type (
	FilterCondition = crud.FilterCondition
	FilterSpec      = crud.FilterSpec
)

// GetTableDataFiltered returns paginated, structured-filter-driven table data
// plus an accurate total-row count for the record-count bar (plan §2.3, P2.5).
// Filter columns and values are checked and bound by crud.Page (identifiers
// against the table's real columns, values as parameters).
func (a *App) GetTableDataFiltered(name string, spec FilterSpec, page, pageSize int, sortCol, sortDir string) (*TableData, error) {
	return a.pageTable(name, spec, page, pageSize, sortCol, sortDir)
}

// pageTable is the shared body of the two table-read bindings.
func (a *App) pageTable(name string, spec FilterSpec, page, pageSize int, sortCol, sortDir string) (*TableData, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	tref, err := a.tableRefArg(name)
	if err != nil {
		return nil, err
	}
	db := a.sess.DB()
	if db == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}

	res, err := crud.Page(a.ctxOrBackground(), db, tref, crud.PageRequest{
		Filter: spec, Page: page, PageSize: pageSize, SortCol: sortCol, SortDir: sortDir,
	})
	if err != nil {
		// A table whose files sit outside every storage scope is explained, not echoed.
		return nil, session.ExplainStorageDeny(err, a.sess, "")
	}
	result := &TableData{
		Columns:    res.Columns,
		Types:      res.Types,
		Rows:       res.Rows,
		Page:       res.Page,
		PageSize:   res.PageSize,
		TotalRows:  res.TotalRows,
		TotalPages: res.TotalPages,
	}
	if err := jsonRoundTripGuard(result); err != nil {
		return nil, err
	}
	return result, nil
}

// InsertRow inserts a row into a table, coercing all-string form inputs to the
// column's type (via information_schema). Constraints (NOT NULL, CHECK, keys)
// come from the database itself; a violation is returned as a field-keyed
// *crud.WriteError where it can be attributed, so the UI can highlight the offending
// controls instead of surfacing a raw SQL string.
func (a *App) InsertRow(table string, values map[string]any) (*crud.WriteResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ddb, colTypes, table, err := a.prepareWrite(table)
	if err != nil {
		return nil, err
	}

	return crud.InsertRow(ddb, colTypes, table, values)
}

// UpdateRow updates a row in a table by primary key, applying the same coercion
// and validation as InsertRow. A 0-affected-row outcome (PK mismatch) is
// surfaced as an error rather than a silent no-op.
func (a *App) UpdateRow(table, pkCol string, pkVal any, values map[string]any) (*crud.WriteResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !engine.ValidIdentifier(pkCol) {
		return nil, fmt.Errorf("invalid primary key column")
	}

	ddb, colTypes, table, err := a.prepareWrite(table)
	if err != nil {
		return nil, err
	}

	return crud.UpdateRow(ddb, colTypes, table, pkCol, pkVal, values)
}

// prepareWrite validates the open container + table and loads the column types
// needed by the transactional write helpers.
func (a *App) prepareWrite(table string) (*sql.DB, map[string]string, string, error) {
	if err := a.requireWorkspace(); err != nil {
		return nil, nil, "", err
	}
	tref, err := a.tableRefArg(table)
	if err != nil {
		return nil, nil, "", err
	}
	ddb := a.sess.DB()
	if ddb == nil {
		return nil, nil, "", fmt.Errorf("DuckDB not available")
	}
	// Writes address the table by its fully resolved name (catalog.schema.table),
	// so neither the connection's current catalog nor a same-named temp table
	// can redirect them.
	resolved := tref.String()
	colTypes, err := crud.ColumnTypes(ddb, resolved)
	if err != nil {
		return nil, nil, "", err
	}
	return ddb, colTypes, resolved, nil
}

// DeleteRow deletes a row from a table by primary key. Consistent with
// InsertRow/UpdateRow, it validates the pk column identifier, runs the DELETE
// inside a transaction with rollback on error, coerces pkVal to the PK column's
// type, and surfaces a 0-affected-row outcome (PK mismatch) as a structured
// *crud.WriteError rather than a raw SQL string.
func (a *App) DeleteRow(table, pkCol string, pkVal any) (*crud.WriteResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !engine.ValidIdentifier(pkCol) {
		return nil, fmt.Errorf("invalid primary key column")
	}

	ddb, colTypes, table, err := a.prepareWrite(table)
	if err != nil {
		return nil, err
	}

	return crud.DeleteRow(ddb, colTypes, table, pkCol, pkVal)
}

// CreateTable creates a new table with the given columns. Integer primary-key
// columns are made auto-numbering via a backing sequence (the Access AutoNumber
// behavior) so data-entry forms can omit the id.
func (a *App) CreateTable(name string, columns []ColumnInfo) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return err
	}
	tref, err := a.tableRefArg(name)
	if err != nil {
		return err
	}
	ddb := a.sess.DB()
	if ddb == nil {
		return fmt.Errorf("DuckDB not available")
	}
	defs := make([]crud.ColumnDef, len(columns))
	for i, c := range columns {
		defs[i] = crud.ColumnDef{Name: c.Name, Type: c.Type, NotNull: c.NotNull, PK: c.PK}
	}
	if err := crud.CreateTable(a.ctxOrBackground(), ddb, tref, defs, a.isDefaultTarget(tref)); err != nil {
		return err
	}
	a.emitEvent("data:changed")
	a.catalogChanged("")
	return nil
}

// DropTable drops a table.
func (a *App) DropTable(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.requireWorkspace(); err != nil {
		return err
	}
	tref, err := a.tableRefArg(name)
	if err != nil {
		return err
	}

	ddb := a.sess.DB()
	if ddb == nil {
		return fmt.Errorf("DuckDB not available")
	}
	if err := crud.DropTable(a.ctxOrBackground(), ddb, tref); err != nil {
		return err
	}
	a.emitEvent("data:changed")
	a.catalogChanged("")
	return nil
}

// SaveFilterAsQuery renders a FilterSpec into deterministic SQL and persists it
// as a named query via the existing queries store, returning the new query id
// (plan §2.4, P2.6). It reuses the same rendering path as generateFilterSQL so the
// stored SQL equals the previewed SQL.
func (a *App) SaveFilterAsQuery(name string, spec FilterSpec, columns []string, title string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	sqlStr, err := a.generateFilterSQL(name, spec, columns)
	if err != nil {
		return "", err
	}

	q := &queries.Query{
		Title:      title,
		SQL:        sqlStr,
		Engine:     "duckdb",
		Parameters: []queries.Param{},
	}
	if err := a.ws.Queries().Create(a.ctxOrBackground(), q); err != nil {
		return "", fmt.Errorf("failed to save filter as query: %w", err)
	}
	a.logger.Info("saved filter as named query", "id", q.ID, "table", name, "title", title)
	return q.ID, nil
}

// generateFilterSQL is the single, deterministic FilterSpec → SQL renderer behind
// both generateFilterSQL and SaveFilterAsQuery. Unlike GetTableDataFiltered (which
// binds parameters at execution time), a saved query is a standalone SQL string,
// so literal values are safely inlined here. Column identifiers (filter columns
// and the optional projection) are validated against the table's real columns —
// no SQL injection via identifier or value. The caller must hold a.mu.
func (a *App) generateFilterSQL(name string, spec FilterSpec, columns []string) (string, error) {
	if err := a.requireWorkspace(); err != nil {
		return "", err
	}
	tref, err := a.tableRefArg(name)
	if err != nil {
		return "", err
	}

	db := a.sess.DB()
	if db == nil {
		return "", fmt.Errorf("DuckDB not available")
	}

	cols, err := engine.TableColumns(db, tref)
	if err != nil {
		return "", fmt.Errorf("failed to introspect columns for filter: %w", err)
	}
	known := make(map[string]bool, len(cols))
	for _, c := range cols {
		known[c.Name] = true
	}

	return crud.BuildFilterSQLFrom(tref.SQL(), spec, columns, known)
}
