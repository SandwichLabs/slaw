package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
)

// DuckDB's VARIANT type (new in 1.5) cannot be scanned by duckdb-go
// (v2.10506, checked after the 0.9.2 driver bump): reading a row with one
// fails with "unsupported data type: VARIANT". DuckDB itself casts a VARIANT
// to JSON, and a JSON column scans fine. A statement is never run twice to
// work around this (a retry would advance a sequence twice and cost a remote
// query double): the datasheet knows its table's schema and casts a top-level
// VARIANT column in its one query (crud.Page); anything else fails once, with
// an error that names the column and the cast.

const variantScanMessage = "unsupported data type: VARIANT"

// IsVariantScanError reports whether err is the driver's refusal to scan a
// VARIANT value.
func IsVariantScanError(err error) bool {
	return err != nil && strings.Contains(err.Error(), variantScanMessage)
}

// VariantCastProjection is a select list that returns every column of its
// source with each named VARIANT column cast to JSON, in place:
// `* REPLACE (CAST("payload" AS JSON) AS "payload")`. It is "*" when names is
// empty.
func VariantCastProjection(names []string) string {
	if len(names) == 0 {
		return "*"
	}
	parts := make([]string, len(names))
	for i, n := range names {
		q := session.QuoteIdent(n)
		parts[i] = "CAST(" + q + " AS JSON) AS " + q
	}
	return "* REPLACE (" + strings.Join(parts, ", ") + ")"
}

// IsVariantType reports whether a DuckDB type name is VARIANT.
func IsVariantType(dbType string) bool {
	return strings.EqualFold(strings.TrimSpace(dbType), "VARIANT")
}

// variantScanFailure is the error for a result the driver could not scan
// because of a VARIANT column. It describes the statement (DESCRIBE plans it
// and does not run it) only to name the top-level VARIANT columns; when it
// cannot (not a single SELECT/WITH/FROM, or the VARIANT is nested inside a STRUCT
// or LIST) the message says to cast it without naming one.
func variantScanFailure(boundSQL string, args []any, run rowsFunc) error {
	if names := describedVariantColumns(boundSQL, args, run); len(names) > 0 {
		shown := make([]string, len(names))
		casts := make([]string, len(names))
		for i, n := range names {
			shown[i] = n
			casts[i] = castTarget(n, run) + "::JSON"
		}
		if len(names) == 1 {
			return fmt.Errorf("Column %s is VARIANT, which Slaw can't show yet; select it as %s.", shown[0], casts[0]) //nolint:staticcheck // a sentence shown to the person
		}
		return fmt.Errorf("Columns %s are VARIANT, which Slaw can't show yet; select them as %s.", strings.Join(shown, ", "), strings.Join(casts, ", ")) //nolint:staticcheck // a sentence shown to the person
	}
	return errors.New("A VARIANT value in the result can't be shown yet (a VARIANT nested inside a struct or list is not handled); select it as ::JSON or ::VARCHAR") //nolint:staticcheck // a sentence shown to the person
}

// castTarget is a column name as it must be written before a cast: bare when
// it is a plain identifier DuckDB does not reserve, quoted otherwise (a name
// like `select` would not run bare).
func castTarget(name string, run rowsFunc) string {
	plain := name != ""
	for i, r := range name {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			plain = false
		}
	}
	if plain && !isKeyword(name, run) {
		return name
	}
	return session.QuoteIdent(name)
}

// isKeyword reports whether DuckDB lists name as anything but an unreserved
// keyword; when it cannot ask, it says yes, which only costs quotes.
func isKeyword(name string, run rowsFunc) bool {
	rows, err := run("SELECT keyword_category FROM duckdb_keywords() WHERE keyword_name = ?", []any{strings.ToLower(name)})
	if err != nil {
		return true
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var cat string
		if rows.Scan(&cat) != nil || cat != "unreserved" {
			return true
		}
	}
	return false
}

// describedVariantColumns lists the top-level VARIANT columns of a single
// SELECT or WITH statement by DESCRIBE, or nil when it cannot tell.
func describedVariantColumns(boundSQL string, args []any, run rowsFunc) []string {
	stmt := strings.TrimSuffix(strings.TrimSpace(boundSQL), ";")
	upper := strings.ToUpper(stmt)
	if stmt == "" || strings.Contains(stmt, ";") || !(strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") || strings.HasPrefix(upper, "FROM")) {
		return nil
	}
	rows, err := run("DESCRIBE "+stmt, args)
	if err != nil {
		return nil
	}
	defer rows.Close() //nolint:errcheck
	cols, err := rows.Columns()
	if err != nil || len(cols) < 2 {
		return nil
	}
	var names []string
	for rows.Next() {
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = new(any)
		}
		if rows.Scan(dest...) != nil {
			return nil
		}
		name, _ := (*dest[0].(*any)).(string)
		typ, _ := (*dest[1].(*any)).(string)
		if IsVariantType(typ) {
			names = append(names, name)
		}
	}
	return names
}
