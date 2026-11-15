package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

// maxSafeInt is Number.MAX_SAFE_INTEGER: the largest magnitude an integer
// can have and still round-trip exactly through a JavaScript double. Any
// BIGINT/UBIGINT/HUGEINT value outside [-maxSafeInt, maxSafeInt] crosses the
// wire as a decimal string instead of a JSON number (see NormalizeValue and
// the wire contract below).
const maxSafeInt = 1<<53 - 1

// maxSafeDecimalUnscaled is the largest unscaled DECIMAL magnitude
// (duckdb.Decimal.Value) for which the exact scale-padded text still
// round-trips through json.Number/a JS double (15 significant digits).
// Above this, the value crosses as an exact string instead.
var maxSafeDecimalUnscaled = big.NewInt(1_000_000_000_000_000) // 1e15

// NormalizeValue maps DuckDB driver-specific scan types onto the wire
// contract every consumer (Studio bindings, agent tools, MCP, notebooks)
// shares: **a value crosses as a JSON number exactly when a JS double holds
// it exactly, otherwise it crosses as a string holding DuckDB's own text
// for that value.** Without this, a DECIMAL column reaches the frontend as
// the raw duckdb.Decimal struct, a BIGINT above 2^53 silently loses
// precision the moment JavaScript parses the JSON, and HUGEINT arrives as a
// *big.Int the JSON encoder cannot marshal at all.
//
// It also sanitizes non-finite floats (see sanitizeFloat), RECURSIVELY: a
// NaN/Inf buried inside a LIST/ARRAY ([]any), STRUCT (map[string]any), MAP
// (duckdb.Map), or UNION (duckdb.Union) crashed the app exactly like a bare
// top-level one did — `SELECT ['nan'::DOUBLE, 1.0] AS l` scans as
// []any{float64(NaN), float64(1)}, and the old non-recursive version only
// ever looked at the top-level value, missing the NaN nested one level (or
// more — LIST-of-STRUCT-of-MAP nests arbitrarily) inside. Every composite
// case below normalizes its own elements/values through NormalizeValue
// again, so nesting of any depth or combination is covered.
//
// This is the one place ScanRows (shared by DuckDBEngine and the other result readers)
// funnels every scanned cell through, so ExecuteQuery, GetTableData,
// RunA2UIQuery, run_query/preview_query, MCP, and the TUI are all covered
// by a single fix rather than needing their own guard against a value
// encoding/json cannot marshal, or one it marshals losslessly but
// imprecisely once JavaScript reads it back.
func NormalizeValue(v any) any {
	switch t := v.(type) {
	case duckdb.Decimal:
		return decimalWire(t.Value, int(t.Scale))
	case *duckdb.Decimal:
		if t == nil {
			return nil
		}
		return decimalWire(t.Value, int(t.Scale))
	case *big.Int:
		if t == nil {
			return nil
		}
		return bigIntWire(t)
	case big.Int:
		return bigIntWire(&t)
	case int64:
		return safeInt(t)
	case uint64:
		return safeUint(t)
	case int:
		return safeInt(int64(t))
	case uint:
		return safeUint(uint64(t))
	case float64:
		return sanitizeFloat(t)
	case float32:
		// Round-trip through float32's shortest decimal representation
		// rather than converting the float32 bits straight to float64: the
		// latter reproduces float32's full binary imprecision as decimal
		// noise (0.1 becomes 0.10000000149011612 instead of 0.1), which is
		// not what DuckDB's own ::VARCHAR or any other consumer shows.
		f, err := strconv.ParseFloat(strconv.FormatFloat(float64(t), 'g', -1, 32), 64)
		if err != nil {
			return nil
		}
		return sanitizeFloat(f)
	case duckdb.Interval:
		return FormatInterval(t)
	case []byte:
		return bytesText(t)
	case duckdb.Map:
		// duckdb.Map is map[any]any: a DuckDB MAP key need not be a string,
		// but encoding/json can only marshal a map keyed by string, an
		// integer type, or an encoding.TextMarshaler — normalize the key
		// through fmt.Sprint so this is always JSON-safe regardless of the
		// MAP's declared key type.
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(NormalizeValue(k))] = NormalizeValue(val)
		}
		return out
	case duckdb.OrderedMap:
		// duckdb-go (DuckDB 1.5) scans MAP into an OrderedMap; same wire
		// shape as duckdb.Map above.
		keys, vals := t.Keys(), t.Values()
		out := make(map[string]any, len(keys))
		for i, k := range keys {
			out[fmt.Sprint(NormalizeValue(k))] = NormalizeValue(vals[i])
		}
		return out
	case duckdb.Union:
		return NormalizeValue(t.Value)
	case []any:
		// LIST/ARRAY.
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = NormalizeValue(val)
		}
		return out
	case map[string]any:
		// STRUCT.
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = NormalizeValue(val)
		}
		return out
	}
	return v
}

// normalizeColumn is NormalizeValue plus formatting that needs the
// column's DuckDB type (dbType = (*sql.ColumnType).DatabaseTypeName()):
// DATE/TIME/TIMETZ/TIMESTAMP*/TIMESTAMPTZ (all scan as time.Time, and only
// the column type says which one), UUID (scans as []byte, needs the
// canonical 8-4-4-4-12 text instead of NormalizeValue's generic
// UTF-8-or-escape rule), and BLOB (scans as []byte, and unlike other
// []byte columns must always use DuckDB's \xHH escaping even when the
// bytes happen to be valid UTF-8 — a BLOB is bytes, not text, and the grid
// must show it exactly as `COPY ... FORMAT csv` would).
//
// A nested type (LIST, ARRAY, STRUCT, MAP, UNION) is parsed from the same
// type name and every element, field and map entry gets the formatting its
// own declared type asks for (normalizeTyped), so a UUID, DATE or TIMESTAMP
// inside a list or struct reads like one in its own column. A JSON column
// stays the decoded value the driver scans it into (the display layers print
// it as JSON text).
//
// Everything else falls through to NormalizeValue, which has no column type
// information and must stay type-agnostic — do not duplicate its logic here.
//
// ScanRows parses each column's nested type once and calls this per cell.
func normalizeColumn(v any, dbType string, t *colType) any {
	if v == nil {
		return nil
	}
	if t != nil {
		return normalizeTyped(v, t)
	}
	if out, ok := scalarColumnValue(v, dbType); ok {
		return out
	}
	return NormalizeValue(v)
}

// scalarColumnValue is the type-aware formatting of one non-nested value, or
// false when the type has none (the caller falls back to NormalizeValue).
func scalarColumnValue(v any, dbType string) (any, bool) {
	switch dbType {
	case "DATE":
		if t, ok := v.(time.Time); ok {
			return t.Format("2006-01-02"), true
		}
	case "TIME":
		if t, ok := v.(time.Time); ok {
			return t.Format("15:04:05.999999"), true
		}
	case "TIMETZ":
		// go-duckdb scans TIMETZ into a UTC time.Time and drops the
		// original zone offset entirely, so the best this can do is show
		// the same instant with a UTC "+00" suffix. Known gap.
		if t, ok := v.(time.Time); ok {
			return t.UTC().Format("15:04:05.999999") + "+00", true
		}
	case "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS":
		if t, ok := v.(time.Time); ok {
			return t.Format("2006-01-02 15:04:05.999999999"), true
		}
	case "TIMESTAMPTZ":
		if t, ok := v.(time.Time); ok {
			return timestampTZText(t), true
		}
	case "UUID":
		if b, ok := v.([]byte); ok {
			return uuidText(b), true
		}
	case "BLOB":
		if b, ok := v.([]byte); ok {
			return blobEscape(b), true
		}
	}
	return nil, false
}

// safeInt returns i as a JSON number when a JS double holds it exactly, or
// its decimal text otherwise.
func safeInt(i int64) any {
	if i >= -maxSafeInt && i <= maxSafeInt {
		return i
	}
	return strconv.FormatInt(i, 10)
}

// safeUint is safeInt for UBIGINT, whose range exceeds int64.
func safeUint(u uint64) any {
	if u <= maxSafeInt {
		return int64(u)
	}
	return strconv.FormatUint(u, 10)
}

// bigIntWire is safeInt for HUGEINT (*big.Int): int64 when it both fits an
// int64 and is within the safe-double range, else the exact decimal text.
func bigIntWire(t *big.Int) any {
	if t.IsInt64() {
		if i := t.Int64(); i >= -maxSafeInt && i <= maxSafeInt {
			return i
		}
	}
	return t.String()
}

// decimalWire builds DuckDB's own scale-padded decimal text from a
// duckdb.Decimal's unscaled integer value ("15675.30" for Value=1567530,
// Scale=2; "0.050" for Value=50, Scale=3), then returns it as a
// json.Number (an exact JS number) when the unscaled magnitude is small
// enough for the double round-trip to be exact, or as a plain string
// otherwise. json.Number marshals verbatim, so the trailing/leading zeros
// the text carries reach JSON exactly as written — unlike float64(d), which
// would both lose precision and drop them.
func decimalWire(v *big.Int, scale int) any {
	if v == nil {
		return nil
	}
	text := decimalText(v, scale)
	abs := new(big.Int).Abs(v)
	if abs.Cmp(maxSafeDecimalUnscaled) < 0 {
		return json.Number(text)
	}
	return text
}

// decimalText renders an unscaled DECIMAL integer with its scale as exact
// text, keeping trailing zeros the scale implies (12.5 at scale 4 is
// "12.5000", not "12.5" — the scale is the column's contract, not a
// rounding suggestion).
func decimalText(v *big.Int, scale int) string {
	neg := v.Sign() < 0
	digits := new(big.Int).Abs(v).String()
	if scale <= 0 {
		if neg {
			return "-" + digits
		}
		return digits
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	intPart := digits[:len(digits)-scale]
	fracPart := digits[len(digits)-scale:]
	text := intPart + "." + fracPart
	if neg {
		text = "-" + text
	}
	return text
}

// timestampTZText formats a TIMESTAMPTZ's UTC instant in the process's
// local zone with DuckDB's own offset suffix ("-05", or "+05:30" when the
// offset has a non-zero minute part) — matching what `::VARCHAR` and CSV
// export show when DuckDB's session TimeZone is the same as the OS zone
// (the default). A `SET TimeZone` that diverges from the OS zone is a
// documented edge case: the grid still shows a correct
// instant, just in a different zone than DuckDB's session.
func timestampTZText(t time.Time) string {
	local := t.In(time.Local)
	base := local.Format("2006-01-02 15:04:05.999999")
	_, offsetSec := local.Zone()
	sign := "+"
	if offsetSec < 0 {
		sign = "-"
		offsetSec = -offsetSec
	}
	hh := offsetSec / 3600
	mm := (offsetSec % 3600) / 60
	if mm != 0 {
		return fmt.Sprintf("%s%s%02d:%02d", base, sign, hh, mm)
	}
	return fmt.Sprintf("%s%s%02d", base, sign, hh)
}

// uuidText renders a scanned UUID's 16 raw bytes as canonical lowercase
// 8-4-4-4-12 text. Studio's old `string(b)` produced mojibake instead (the
// bytes are not text), which also broke a UUID primary-key round-trip
// (`CAST(mojibake AS UUID)` fails).
func uuidText(b []byte) string {
	if len(b) != 16 {
		return bytesText(b)
	}
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// bytesText is the generic (column-type-agnostic) rule for a raw []byte
// value: valid UTF-8 bytes cross as a plain string, anything else as
// DuckDB's own `\xHH` escape text. Column-aware callers (BLOB, UUID) apply
// a stricter rule instead — see normalizeColumn.
func bytesText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return blobEscape(b)
}

// blobEscape renders bytes the way DuckDB's own CSV/`::VARCHAR` does:
// printable ASCII (0x20-0x7E) passes through unchanged, except backslash,
// single quote and double quote (which DuckDB escapes so the text stays
// unambiguous to re-parse), and everything else becomes `\xHH`.
func blobEscape(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x20 && c <= 0x7E && c != '\\' && c != '\'' && c != '"' {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "\\x%02X", c)
		}
	}
	return sb.String()
}

// FormatInterval renders a duckdb.Interval the way DuckDB's own
// `::VARCHAR` does: a "N year(s)" part (Months/12), a "N month(s)" part
// (Months%12), a "N day(s)" part (Days), then an "HH:MM:SS[.ffffff]" time
// part built from Micros — included whenever Micros is non-zero, or when
// every other part is zero/absent (so a zero interval prints "00:00:00",
// not ""). Each part uses its own sign (DuckDB does not normalize
// years/months/days/time against each other), and the unit is pluralized
// unless the part's magnitude is exactly 1.
func FormatInterval(iv duckdb.Interval) string {
	var parts []string

	years := iv.Months / 12
	months := iv.Months % 12
	if years != 0 {
		parts = append(parts, intervalPart(years, "year"))
	}
	if months != 0 {
		parts = append(parts, intervalPart(months, "month"))
	}
	if iv.Days != 0 {
		parts = append(parts, intervalPart(iv.Days, "day"))
	}
	if iv.Micros != 0 || len(parts) == 0 {
		parts = append(parts, formatIntervalTime(iv.Micros))
	}
	return strings.Join(parts, " ")
}

// intervalPart renders one signed year/month/day component with its unit,
// singular only when the magnitude is exactly 1.
func intervalPart(n int32, unit string) string {
	abs := n
	if abs < 0 {
		abs = -abs
	}
	if abs == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// formatIntervalTime renders an interval's microsecond component as
// "HH:MM:SS[.ffffff]". Hours are not wrapped to 24 (an interval of 25
// hours prints "25:00:00", not a day-plus-hour split — DuckDB keeps
// Days and Micros independent), and a negative value gets a single
// leading "-" rather than negating each field.
func formatIntervalTime(micros int64) string {
	neg := micros < 0
	if neg {
		micros = -micros
	}
	hours := micros / 3_600_000_000
	micros %= 3_600_000_000
	minutes := micros / 60_000_000
	micros %= 60_000_000
	seconds := micros / 1_000_000
	frac := micros % 1_000_000

	s := fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	if frac != 0 {
		fracText := strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
		s += "." + fracText
	}
	if neg {
		s = "-" + s
	}
	return s
}

// sanitizeFloat maps a non-finite float64 (NaN, +Inf, -Inf) to DuckDB's own
// ::VARCHAR text for it ("nan", "inf", "-inf"), following this package's
// general wire rule: a value crosses as a JSON number exactly when a JS
// double holds it exactly, otherwise as a string holding DuckDB's own text.
// NaN/Inf are real, DuckDB-producible values (a bare 'nan'/'inf' cast,
// 1.0/0, log(-1), an empty AVG(), ...) distinct from SQL NULL, so — like an
// out-of-range BIGINT/DECIMAL — the fix is to carry the exact text across,
// not to collapse a real value into null.
//
// encoding/json.Marshal does not silently drop a NaN/Inf float64 — it
// returns a hard *json.UnsupportedValueError, and left unhandled up through
// Wails' own result-marshaling bridge that took the whole Studio process
// down (observed live: `SELECT 'nan'::DOUBLE` reaching
// RunA2UIQuery/ExecuteQuery crashed the app rather than erroring the one
// call). Returning a string here avoids that crash the same way returning
// nil did, while keeping the value faithful. Every string-aware consumer
// already treats an unparsed string as non-numeric (TS `isNum`/Go
// `isNumericValue` — this string simply joins the same "not a chartable
// number" bucket a too-big BIGINT/DECIMAL string is already in), and
// formatCell's float branch falls back to displaying the raw string.
func sanitizeFloat(f float64) any {
	if math.IsNaN(f) {
		return "nan"
	}
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	return f
}
