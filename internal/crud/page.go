package crud

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/session"
)

// Paging bounds for the table browser.
const (
	DefaultPageSize = 50
	MaxPageSize     = 1000
)

// PageRequest is one table-browser read: a page of rows, optionally filtered
// and sorted. Page is 1-based; Page < 1 means 1 and a PageSize outside
// 1..MaxPageSize means DefaultPageSize. SortCol must name a real column of the
// table (an unknown one is ignored); SortDir is "DESC" (any case) or ascending.
type PageRequest struct {
	Filter   FilterSpec
	Page     int
	PageSize int
	SortCol  string
	SortDir  string
	// SkipCount leaves out the total-matching COUNT(*) (a full scan on a
	// remote table); TotalRows and TotalPages are then 0.
	SkipCount bool
}

// PageResult is one page of rows with the total matching row count.
type PageResult struct {
	Columns []string
	// Types is the DuckDB type name of each column, parallel to Columns.
	Types      []string
	Rows       [][]any
	Page       int
	PageSize   int
	TotalRows  int
	TotalPages int
}

// OrderClause renders " ORDER BY <col> ASC|DESC" for a sort column that is in
// known (the table's real columns), or "" when col is empty or unknown. The
// column goes through session.QuoteIdent, so any real column name is safe.
func OrderClause(col, dir string, known map[string]bool) string {
	if col == "" || !known[col] {
		return ""
	}
	d := "ASC"
	if strings.ToUpper(dir) == "DESC" {
		d = "DESC"
	}
	return " ORDER BY " + session.QuoteIdent(col) + " " + d
}

// Page reads one filtered, sorted page of ref and the total number of rows the
// filter matches (independent of paging). Filter columns and the sort column
// are checked against the table's real columns; values are bound parameters;
// ref is rendered with every part quoted.
func Page(ctx context.Context, db *sql.DB, ref session.TableRef, req PageRequest) (*PageResult, error) {
	page, pageSize := req.Page, req.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}

	// This lookup runs on every page. For a lazily resolved Iceberg catalog table
	// it asks the remote catalog each time; caching it per table for the
	// datasheet's lifetime needs an invalidation rule for schema changes, so it is
	// left as a known cost (see the A10 report).
	// The table's columns: the allowlist for a filter or a sort column, and the
	// VARIANT columns, which duckdb-go cannot scan and so are read as JSON.
	cols, err := engine.TableColumns(ctxQuerier{ctx, db}, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to introspect columns: %w", err)
	}
	known := make(map[string]bool, len(cols))
	var variants []string
	for _, c := range cols {
		known[c.Name] = true
		if engine.IsVariantType(c.Type) {
			variants = append(variants, c.Name)
		}
	}

	where, args, err := BuildWhere(req.Filter, known)
	if err != nil {
		return nil, err
	}

	var total int
	if !req.SkipCount {
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+ref.SQL()+where, args...).Scan(&total); err != nil {
			return nil, session.MapStorageDenyError(fmt.Errorf("failed to count rows: %w", err))
		}
	}

	tail := " FROM " + ref.SQL() + where + OrderClause(req.SortCol, req.SortDir, known) + " LIMIT ? OFFSET ?"
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rs, err := scanPage(ctx, db, engine.VariantCastProjection(variants), tail, queryArgs)
	if err != nil {
		return nil, err
	}
	// A cast column scans as a JSON value; the column is still a VARIANT.
	for i := range rs.Columns {
		if slices.Contains(variants, rs.Columns[i].Name) {
			rs.Columns[i].Type = "VARIANT"
		}
	}
	names, types := rs.NamesAndTypes()

	totalPages := 0
	if !req.SkipCount {
		totalPages = (total + pageSize - 1) / pageSize
		if totalPages < 1 {
			totalPages = 1
		}
	}
	return &PageResult{
		Columns: names, Types: types, Rows: rs.Rows,
		Page: page, PageSize: pageSize, TotalRows: total, TotalPages: totalPages,
	}, nil
}

// scanPage runs "SELECT <projection><tail>" and scans every row.
func scanPage(ctx context.Context, db *sql.DB, projection, tail string, args []any) (*engine.ResultSet, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+projection+tail, args...)
	if err != nil {
		return nil, session.MapStorageDenyError(fmt.Errorf("failed to query table: %w", err))
	}
	defer rows.Close() //nolint:errcheck
	rs, _, err := engine.ScanRows(rows, 0, 0)
	return rs, err
}

// ctxQuerier makes a *sql.DB's reads honour ctx for engine.TableColumns.
type ctxQuerier struct {
	ctx context.Context
	db  *sql.DB
}

func (q ctxQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	return q.db.QueryContext(q.ctx, query, args...)
}

// Count returns the exact number of rows in ref (one query).
func Count(ctx context.Context, db *sql.DB, ref session.TableRef) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+ref.SQL()).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count rows: %w", err)
	}
	return n, nil
}
