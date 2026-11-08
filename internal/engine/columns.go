package engine

// Table/column introspection and identifier validation shared by the CRUD
// write core, the Query Wizard agent tool, and the container's auto-number
// backfill. Ported from the (now-removed) internal/schema package's
// relationships_fk.go — this codebase's Access-application layer is gone
// (#68), but these helpers are generic DuckDB introspection, not
// forms/relationships-UI specific, so they moved here instead of being
// deleted.

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
)

// RowQuerier is the subset of *sql.DB / *sql.Conn-less callers need: a plain
// Query. (*sql.DB and *sql.Tx satisfy it.)
type RowQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// ColumnInfo describes a table column (name, type, nullability, primary-key
// and foreign-key membership, default expression) in ordinal order.
type ColumnInfo struct {
	Name    string
	Type    string
	NotNull bool
	PK      bool
	// FK is true when the column participates in a FOREIGN KEY constraint
	// (a plain-SQL inline REFERENCES — #68 dropped the _vibe_relationships
	// metadata table this used to be read from). Read straight from
	// DuckDB's own duckdb_constraints() catalog, so it reflects reality
	// regardless of how the FK was declared.
	FK      bool
	Default string
}

// TableColumns introspects a DuckDB table's columns (name, type, nullability,
// primary-key membership, default expression) in ordinal order. ref names the
// table by catalog.schema.table; parts it leaves empty resolve against the
// connection's current catalog and schema (the session default), so no
// catalog name is assumed. Returns an empty slice if the table does not exist.
func TableColumns(db RowQuerier, ref session.TableRef) ([]ColumnInfo, error) {
	cond, args := session.RefFilter(ref, "table_catalog", "table_schema", "table_name", 1)
	rows, err := db.Query(
		`SELECT column_name, data_type, CASE WHEN is_nullable = 'NO' THEN true ELSE false END,
		        COALESCE(column_default, '')
		   FROM information_schema.columns
		  WHERE `+cond+`
		  ORDER BY ordinal_position`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query columns: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var cols []ColumnInfo
	for rows.Next() {
		var c ColumnInfo
		if err := rows.Scan(&c.Name, &c.Type, &c.NotNull, &c.Default); err != nil {
			return nil, fmt.Errorf("failed to scan column: %w", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate columns: %w", err)
	}

	if len(cols) == 1 && session.UnresolvedColumn(cols[0].Name, cols[0].Type) && ref.Catalog != "" && ref.Schema != "" {
		// A lazily resolved table (an Iceberg catalog's): bind it for its real columns.
		dr, err := db.Query(session.DescribeSQL(ref))
		if err != nil {
			return nil, fmt.Errorf("failed to describe the table: %w", err)
		}
		described, err := session.ScanDescribe(dr)
		if err != nil {
			return nil, err
		}
		out := make([]ColumnInfo, 0, len(described))
		for _, d := range described {
			out = append(out, ColumnInfo{Name: d.Name, Type: d.DataType, NotNull: d.NotNull})
		}
		return out, nil
	}

	pk, err := constraintColumns(db, ref, "PRIMARY KEY")
	if err != nil {
		return nil, err
	}
	fk, err := constraintColumns(db, ref, "FOREIGN KEY")
	if err != nil {
		return nil, err
	}
	for i := range cols {
		if pk[cols[i].Name] {
			cols[i].PK = true
		}
		if fk[cols[i].Name] {
			cols[i].FK = true
		}
	}
	return cols, nil
}

// PrimaryKeyColumns returns the names of the columns in the table's PRIMARY KEY,
// read from duckdb_constraints(). A catalog DuckDB cannot read constraints from
// (an attached Postgres or MySQL database, say) reports none, so an empty set
// means "no key DuckDB can see", not necessarily "no key".
func PrimaryKeyColumns(db RowQuerier, ref session.TableRef) (map[string]bool, error) {
	return constraintColumns(db, ref, "PRIMARY KEY")
}

// constraintColumns returns the set of column names participating in any
// constraint of the given type on the table, using DuckDB's duckdb_constraints()
// table function.
func constraintColumns(db RowQuerier, ref session.TableRef, constraintType string) (map[string]bool, error) {
	cond, args := session.RefFilter(ref, "database_name", "schema_name", "table_name", 2)
	rows, err := db.Query(
		`SELECT constraint_column_names FROM duckdb_constraints()
		  WHERE constraint_type = $1 AND `+cond, append([]any{constraintType}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s constraints: %w", constraintType, err)
	}
	defer rows.Close() //nolint:errcheck

	set := map[string]bool{}
	for rows.Next() {
		var names []any
		if err := rows.Scan(&names); err != nil {
			return nil, fmt.Errorf("failed to scan constraint columns: %w", err)
		}
		for _, n := range names {
			if s, ok := n.(string); ok {
				set[s] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate constraints: %w", err)
	}
	return set, nil
}

// ValidIdentifier reports whether name is a safe SQL identifier (letters,
// digits, underscores; not starting with a digit) naming a table/column a
// caller may create, read, or write — this is the ONE identifier validator
// for the whole codebase (studio's own isValidIdentifier is a thin wrapper
// around it, not a second implementation) used to gate identifiers that are
// interpolated into DDL/DML.
//
// The "_slaw_" prefix (and the pre-rename "_vibe_") is always rejected: reserved for this
// app's own internal tables (_vibe_meta, _vibe_skills, _slaw_import_stage,
// ...), and a caller validating a target for its own read/write must never
// be allowed to touch one, whether by a crafted table name in a shared file
// or an import/create-table request that happens to collide with the
// prefix.
func ValidIdentifier(name string) bool {
	if name == "" || strings.HasPrefix(name, "_slaw_") || strings.HasPrefix(name, "_vibe_") {
		return false
	}
	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		isDigit := r >= '0' && r <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}
