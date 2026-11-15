package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
)

// ErrResultTooLarge is the fixed error of a result that went over its byte
// budget while rows were being read. It names no SQL, value or path.
var ErrResultTooLarge = errors.New("the result is too large")

type maxBytesKey struct{}

// WithMaxResultBytes returns a context that makes every query run under it
// stop reading rows once their kept cells total more than n bytes (estimated
// by cellBytes), failing with ErrResultTooLarge. n <= 0 means no cap. It is a
// context value so the cap reaches ScanRows through Execute and ExecuteIn
// without widening the engine interfaces.
func WithMaxResultBytes(ctx context.Context, n int64) context.Context {
	return context.WithValue(ctx, maxBytesKey{}, n)
}

func maxResultBytes(ctx context.Context) int64 {
	n, _ := ctx.Value(maxBytesKey{}).(int64)
	return n
}

// ResultTooLargeError is ErrResultTooLarge with the cap spelled out in plain
// words, for the person who sees it. errors.Is matches ErrResultTooLarge.
func ResultTooLargeError(maxBytes int64) error {
	return fmt.Errorf("%w: a result may hold at most %d MB of data", ErrResultTooLarge, maxBytes>>20)
}

// Cell size estimates (see cellBytes): a fixed cost per scalar or empty
// value, plus the length of text, plus per-element overhead in a nested value.
const (
	scalarCellBytes = 16
	nestedElemBytes = 16
)

// cellBytes estimates the memory a normalized cell holds: len for text, a
// fixed cost for any other scalar, and the sum of the elements (plus a fixed
// cost each) for a list or map. It is cheap and bounded by the size of the
// value itself, which the driver has already allocated.
func cellBytes(v any) int64 {
	switch t := v.(type) {
	case nil:
		return scalarCellBytes
	case string:
		return int64(len(t)) + scalarCellBytes
	case []byte:
		return int64(len(t)) + scalarCellBytes
	case []any:
		n := int64(scalarCellBytes)
		for _, e := range t {
			n += nestedElemBytes + cellBytes(e)
		}
		return n
	case map[string]any:
		n := int64(scalarCellBytes)
		for k, e := range t {
			n += nestedElemBytes + int64(len(k)) + cellBytes(e)
		}
		return n
	case map[any]any:
		n := int64(scalarCellBytes)
		for k, e := range t {
			n += nestedElemBytes + cellBytes(k) + cellBytes(e)
		}
		return n
	default:
		return scalarCellBytes
	}
}

// ScanRows reads rows from a sql.Rows into a ResultSet. It is the single
// funnel for ExecuteQuery, GetTableData*, RunA2UIQuery (notebooks and A2UI
// tables/charts), run_query/preview_query, MCP, and the TUI, so a fix here
// (like DATE formatting, or the wire contract's value normalization) covers
// all of those consumers at once. It is not the ONLY code that scans a
// *sql.Rows: the CLI export command (cmd/export.go) and the import preview
// (importer/inspect.go, which stages everything as VARCHAR text) scan
// independently and are out of this funnel's scope.
//
// maxRows <= 0 means unlimited: every row is scanned and returned. A
// positive maxRows keeps only the first maxRows rows but keeps calling
// rows.Next() (without Scan) for the remainder, so the returned total is
// still the true row count while the Go-side ResultSet (and what reaches
// the webview) stays bounded by maxRows. This does NOT bound DuckDB's own
// memory: go-duckdb runs non-streaming PendingPrepared+ExecutePending, so
// the full result is still materialized natively before scanning ever
// starts — a true streaming cap (and cancel) is #84's.
//
// maxBytes <= 0 means no byte cap. A positive maxBytes bounds the estimated
// size (cellBytes) of the rows KEPT: the first row that takes the total over
// it stops the read and the call fails with ErrResultTooLarge, so a result of
// many large rows is never accumulated whole. It bounds accumulation across
// rows, not one row: rows.Scan builds a whole row before it is measured, so a
// single huge row (repeat('x', 100000000)) is built once and then refused. Nor
// does it bound DuckDB, which materialises its own result natively first
// (limited only by its memory limit and spill).
func ScanRows(rows *sql.Rows, maxRows int, maxBytes int64) (*ResultSet, int, error) {
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get column types: %w", err)
	}

	columns := make([]Column, len(colTypes))
	dbTypes := make([]string, len(colTypes))
	nodes := make([]*colType, len(colTypes)) // each column's nested type, parsed once
	for i, ct := range colTypes {
		dbTypes[i] = ct.DatabaseTypeName()
		nodes[i] = compositeType(dbTypes[i])
		columns[i] = Column{
			Name: ct.Name(),
			Type: dbTypes[i],
		}
	}

	var resultRows [][]any
	var kept int64
	total := 0
	for rows.Next() {
		total++
		if maxRows > 0 && total > maxRows {
			continue
		}

		scanDest := makeScanDest(len(columns))
		if err := rows.Scan(scanDest...); err != nil {
			return nil, 0, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make([]any, len(columns))
		for i, v := range scanDest {
			row[i] = deref(v, dbTypes[i], nodes[i])
			if maxBytes > 0 {
				kept += cellBytes(row[i])
			}
		}
		if maxBytes > 0 && kept > maxBytes {
			return nil, 0, ResultTooLargeError(maxBytes)
		}
		resultRows = append(resultRows, row)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating rows: %w", err)
	}

	return &ResultSet{
		Columns: columns,
		Rows:    resultRows,
	}, total, nil
}

// makeScanDest creates a slice of pointers to any for use with rows.Scan.
func makeScanDest(n int) []any {
	dest := make([]any, n)
	for i := range dest {
		var v any
		dest[i] = &v
	}
	return dest
}

// deref dereferences a pointer to any, returning the underlying value
// normalized for downstream consumers (see normalizeColumn).
func deref(v any, dbType string, t *colType) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		return normalizeColumn(rv.Elem().Interface(), dbType, t)
	}
	return normalizeColumn(v, dbType, t)
}
