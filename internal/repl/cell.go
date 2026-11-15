package repl

import (
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
)

// cellText is the DuckDB-like text of a normalised result value (see
// the engine's column normalising); a SQL NULL reads as null and dbType (which may
// be empty) tells a MAP from a STRUCT. It is not yet safe to print: pass it
// through safe.
func cellText(v any, null, dbType string) string {
	switch x := v.(type) {
	case nil:
		return null
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return engine.CellTextTyped(v, null, dbType)
}

// oneLine shows a value's line breaks as the two characters \n and \r, as
// DuckDB does, so a value cannot break a table row.
var oneLine = strings.NewReplacer("\r\n", `\r\n`, "\n", `\n`, "\r", `\r`)

// typeNames maps the database type names to the lower-case names DuckDB's own
// CLI shows.
var typeNames = map[string]string{
	"INTEGER": "int32", "BIGINT": "int64", "SMALLINT": "int16", "TINYINT": "int8",
	"UINTEGER": "uint32", "UBIGINT": "uint64", "USMALLINT": "uint16", "UTINYINT": "uint8",
	"HUGEINT": "int128", "UHUGEINT": "uint128", "FLOAT": "float", "DOUBLE": "double",
	"NULL": `"null"`,
}

// typeLabel is a column's type as the type row shows it.
func typeLabel(dbType string) string {
	base, suffix := splitType(dbType)
	if name, ok := typeNames[strings.ToUpper(base)]; ok {
		return name + suffix
	}
	return strings.ToLower(dbType)
}

// splitType separates a type's base name from a trailing array marker list
// (INTEGER[] -> INTEGER, []); a parameterised or nested type stays whole.
func splitType(t string) (base, suffix string) {
	i := len(t)
	for i > 0 && t[i-1] == ']' {
		j := strings.LastIndexByte(t[:i], '[')
		if j < 0 {
			break
		}
		i = j
	}
	return t[:i], t[i:]
}

// numericBases are the type families whose cells are right-aligned.
var numericBases = map[string]bool{
	"TINYINT": true, "SMALLINT": true, "INTEGER": true, "BIGINT": true, "HUGEINT": true,
	"UTINYINT": true, "USMALLINT": true, "UINTEGER": true, "UBIGINT": true, "UHUGEINT": true,
	"FLOAT": true, "DOUBLE": true, "DECIMAL": true, "REAL": true, "INT": true, "NUMERIC": true,
}

// isNumericType reports whether dbType is a number (a list of numbers is not).
func isNumericType(dbType string) bool {
	if strings.HasSuffix(dbType, "]") {
		return false
	}
	base := strings.ToUpper(dbType)
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = base[:i]
	}
	return numericBases[base]
}
