package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

// colKind is the shape of a parsed DuckDB type name.
type colKind int

const (
	kindScalar colKind = iota
	kindList           // T[] and T[n] (LIST and ARRAY scan the same way)
	kindStruct
	kindMap
	kindUnion
)

// colType is a DuckDB type name (the string (*sql.ColumnType).DatabaseTypeName
// gives, e.g. `STRUCT("a" UUID, "b" DECIMAL(10,2)[])`) parsed far enough to
// give every nested value the same typed formatting a top-level column gets.
type colType struct {
	kind   colKind
	base   string     // kindScalar: the upper-case type name, normalized
	elem   *colType   // kindList: the element; kindMap: the value
	key    *colType   // kindMap: the key
	fields []colField // kindStruct and kindUnion, in declared order
	byName map[string]int
}

type colField struct {
	name string
	typ  *colType
}

// compositeTypeCache keeps parsed composite types for callers that format one
// value at a time (the console draws cell by cell); ScanRows parses each
// column's type once itself. Bounded, and emptied when full, so a session of
// many different ad-hoc types cannot grow it forever.
var (
	compositeTypeCache sync.Map
	compositeTypeCount int
	compositeTypeMu    sync.Mutex
)

const compositeTypeCacheMax = 512

// isCompositeTypeName is the cheap test for "needs parsing": a list/array
// suffix or a STRUCT/MAP/UNION prefix.
func isCompositeTypeName(s string) bool {
	if strings.HasSuffix(s, "]") {
		return true
	}
	u := strings.ToUpper(s)
	return strings.HasPrefix(u, "STRUCT(") || strings.HasPrefix(u, "MAP(") || strings.HasPrefix(u, "UNION(")
}

// compositeType parses dbType when it is a nested type, else returns nil.
func compositeType(dbType string) *colType {
	if !isCompositeTypeName(dbType) {
		return nil
	}
	if c, ok := compositeTypeCache.Load(dbType); ok {
		if t, ok := c.(*colType); ok {
			return t
		}
	}
	t := parseColType(dbType)
	compositeTypeMu.Lock()
	if compositeTypeCount >= compositeTypeCacheMax {
		// Full: start over rather than stop caching, so the types in use now
		// are the ones kept.
		compositeTypeCache.Clear()
		compositeTypeCount = 0
	}
	compositeTypeCount++
	compositeTypeCache.Store(dbType, t)
	compositeTypeMu.Unlock()
	return t
}

// parseColType parses a DuckDB type name. It never fails: anything it does
// not understand is a scalar with that text as its name, which formats
// through the type-agnostic rules.
func parseColType(s string) *colType {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "]") {
		if i := strings.LastIndexByte(s, '['); i > 0 {
			return &colType{kind: kindList, elem: parseColType(s[:i])}
		}
	}
	u := strings.ToUpper(s)
	if inner, ok := cutCall(s, u, "STRUCT"); ok {
		return parseFields(kindStruct, inner)
	}
	if inner, ok := cutCall(s, u, "UNION"); ok {
		return parseFields(kindUnion, inner)
	}
	if inner, ok := cutCall(s, u, "MAP"); ok {
		parts := splitTop(inner)
		if len(parts) == 2 {
			return &colType{kind: kindMap, key: parseColType(parts[0]), elem: parseColType(parts[1])}
		}
	}
	return &colType{kind: kindScalar, base: scalarName(u)}
}

// scalarName maps the spelled-out names a type nested in a STRUCT/LIST/MAP
// carries onto the short names DatabaseTypeName gives a top-level column.
func scalarName(u string) string {
	switch u {
	case "TIMESTAMP WITH TIME ZONE":
		return "TIMESTAMPTZ"
	case "TIME WITH TIME ZONE":
		return "TIMETZ"
	}
	return u
}

// cutCall returns the text between the parentheses of `NAME(...)`.
func cutCall(s, u, name string) (string, bool) {
	if strings.HasPrefix(u, name+"(") && strings.HasSuffix(s, ")") {
		return s[len(name)+1 : len(s)-1], true
	}
	return "", false
}

func parseFields(kind colKind, inner string) *colType {
	t := &colType{kind: kind, byName: map[string]int{}}
	for _, part := range splitTop(inner) {
		name, typ := splitFieldName(part)
		t.byName[name] = len(t.fields)
		t.fields = append(t.fields, colField{name: name, typ: parseColType(typ)})
	}
	return t
}

// splitTop splits on commas outside parentheses, brackets and double quotes.
func splitTop(s string) []string {
	var parts []string
	depth, start := 0, 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inQuote = !inQuote // a doubled "" toggles twice
		case inQuote:
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		parts = append(parts, rest)
	}
	return parts
}

// splitFieldName splits `"a b" INTEGER` or `a INTEGER` into the (unquoted)
// field name and its type text.
func splitFieldName(part string) (name, typ string) {
	if strings.HasPrefix(part, `"`) {
		var sb strings.Builder
		for i := 1; i < len(part); i++ {
			if part[i] == '"' {
				if i+1 < len(part) && part[i+1] == '"' {
					sb.WriteByte('"')
					i++
					continue
				}
				return sb.String(), strings.TrimSpace(part[i+1:])
			}
			sb.WriteByte(part[i])
		}
		return sb.String(), ""
	}
	if i := strings.IndexByte(part, ' '); i > 0 {
		return part[:i], strings.TrimSpace(part[i+1:])
	}
	return part, ""
}

// normalizeTyped is normalizeColumn for a value whose type is known all
// the way down: every element, field and map entry goes through the typed
// formatting its own declared type asks for.
func normalizeTyped(v any, t *colType) any {
	if v == nil {
		return nil
	}
	switch t.kind {
	case kindScalar:
		if out, ok := scalarColumnValue(v, t.base); ok {
			return out
		}
	case kindList:
		if list, ok := v.([]any); ok {
			out := make([]any, len(list))
			for i, e := range list {
				out[i] = normalizeTyped(e, t.elem)
			}
			return out
		}
	case kindStruct:
		if m, ok := v.(map[string]any); ok {
			out := make(map[string]any, len(m))
			for k, e := range m {
				out[k] = normalizeTyped(e, t.fieldType(k))
			}
			return out
		}
	case kindMap:
		if keys, vals, ok := mapEntries(v); ok {
			out := make(map[string]any, len(keys))
			for i, k := range keys {
				out[fmt.Sprint(normalizeTyped(k, t.key))] = normalizeTyped(vals[i], t.elem)
			}
			return out
		}
	case kindUnion:
		return normalizeUnion(v, t)
	}
	return NormalizeValue(v)
}

// fieldType is the declared type of a struct field; an unknown name (never
// expected) is an untyped scalar.
func (t *colType) fieldType(name string) *colType {
	if i, ok := t.byName[name]; ok {
		return t.fields[i].typ
	}
	return &colType{kind: kindScalar}
}

// mapEntries reads a scanned MAP, whichever shape the driver gave.
func mapEntries(v any) (keys, vals []any, ok bool) {
	switch m := v.(type) {
	case duckdb.OrderedMap:
		return m.Keys(), m.Values(), true
	case duckdb.Map:
		for k, e := range m {
			keys = append(keys, k)
			vals = append(vals, e)
		}
		return keys, vals, true
	case map[string]any:
		for k, e := range m {
			keys = append(keys, k)
			vals = append(vals, e)
		}
		return keys, vals, true
	}
	return nil, nil, false
}

// normalizeUnion types a UNION value. The driver hands back the bare member
// value (its tag is dropped), so the member is the one its Go type fits: a
// duckdb.Union names it; otherwise the first member whose scalar type takes
// this kind of value (a time.Time fits only temporal members, a []byte only
// UUID or BLOB ones). Other values need no type.
func normalizeUnion(v any, t *colType) any {
	if u, ok := v.(duckdb.Union); ok {
		if i, found := t.byName[u.Tag]; found {
			return normalizeTyped(u.Value, t.fields[i].typ)
		}
		return NormalizeValue(u.Value)
	}
	for _, f := range t.fields {
		if f.typ.kind == kindScalar && scalarFits(v, f.typ.base) {
			return normalizeTyped(v, f.typ)
		}
	}
	return NormalizeValue(v)
}

// scalarFits reports whether v is the Go shape the scalar type scans into
// and needs typed formatting for.
func scalarFits(v any, base string) bool {
	switch v.(type) {
	case time.Time:
		switch base {
		case "DATE", "TIME", "TIMETZ", "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS", "TIMESTAMPTZ":
			return true
		}
	case []byte:
		return base == "UUID" || base == "BLOB"
	}
	return false
}

// NestedText is a normalised value as the text DuckDB's own CLI prints:
// `[1, 2]` for a LIST or ARRAY, `{'a': 1, 'b': x}` for a STRUCT (fields in
// their declared order when dbType names them, else sorted), `{k=1}` for a
// MAP, strings unquoted, NULL inside a composite as NULL. dbType may be empty;
// then a map is taken to be a STRUCT. A scalar is shown as CellText shows it.
func NestedText(v any, dbType string) string {
	return nestedText(v, compositeType(dbType))
}

func nestedText(v any, t *colType) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []any:
		var et *colType
		if t != nil && t.kind == kindList {
			et = t.elem
		}
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = nestedText(e, et)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		if t != nil && t.kind == kindMap {
			return mapText(x, t)
		}
		return structText(x, t)
	case string:
		return x
	case float64:
		return leafFloatText(x, 64, t)
	case float32:
		return leafFloatText(float64(x), 32, t)
	}
	return fmt.Sprintf("%v", v)
}

// leafFloatText prints a number the way DuckDB prints a DOUBLE: shortest
// digits, an exponent (1e-07, 1.5e+16) below 1e-4 and from 1e16, and a ".0" on
// a whole DOUBLE/FLOAT/REAL; a DECIMAL(w,s) keeps its scale (12.50).
func leafFloatText(f float64, bits int, t *colType) string {
	base := ""
	if t != nil && t.kind == kindScalar {
		base = t.base
	}
	if scale := decimalScaleOf(base); scale > 0 {
		return strconv.FormatFloat(f, 'f', scale, 64)
	}
	e := strconv.FormatFloat(f, 'e', -1, bits)
	if i := strings.IndexByte(e, 'e'); i >= 0 {
		if exp, err := strconv.Atoi(e[i+1:]); err == nil && f != 0 && (exp < -4 || exp >= 16) {
			return e
		}
	}
	s := strconv.FormatFloat(f, 'f', -1, bits)
	if !strings.Contains(s, ".") && (base == "DOUBLE" || base == "FLOAT" || base == "REAL") {
		s += ".0"
	}
	return s
}

// decimalScaleOf is s of a DECIMAL(w,s) or NUMERIC(w,s) type name, else 0.
func decimalScaleOf(base string) int {
	if !strings.HasPrefix(base, "DECIMAL(") && !strings.HasPrefix(base, "NUMERIC(") {
		return 0
	}
	inner := strings.TrimSuffix(base[strings.IndexByte(base, '(')+1:], ")")
	parts := strings.Split(inner, ",")
	if len(parts) != 2 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func structText(m map[string]any, t *colType) string {
	keys := make([]string, 0, len(m))
	if t != nil && t.kind == kindStruct {
		for _, f := range t.fields {
			if _, ok := m[f.name]; ok {
				keys = append(keys, f.name)
			}
		}
	}
	if len(keys) != len(m) {
		keys = keys[:0]
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		var ft *colType
		if t != nil && t.kind == kindStruct {
			ft = t.fieldType(k)
		}
		parts[i] = "'" + k + "': " + nestedText(m[k], ft)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func mapText(m map[string]any, t *colType) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + nestedText(m[k], t.elem)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
