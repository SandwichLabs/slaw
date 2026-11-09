package llm

import (
	"math"
	"math/big"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Number marks for a chat answer (docs/plans/run-receipts.md, R5). The answer
// of a turn is checked against the rows the turn's own reads returned to the
// model: a number that is one of those values, or that value rounded the way
// people write it, is marked as matching that read's receipt; any other number
// is marked "not from a query". It runs on the in-memory rows of the turn and
// never reads the receipt store, which holds no value. The text is never
// rewritten: a mark names a number as it is written and the panel draws over
// the original text.
//
// The rules, in full (docs/reference/receipts.md repeats them):
//
// A number is written as an optional sign, an optional currency symbol ($ EUR
// GBP JPY), digits (plain, or in groups of three with commas), an optional
// decimal part, an optional suffix (K, M, B, T, bn, or the words thousand,
// million, billion, trillion) and an optional %. It is not a number when it is
// part of a longer word or identifier (a letter, digit, underscore, # / : or @
// before it, a letter or digit after it) or of a date, time, range, fraction
// or version (a digit joined to it by - / : , or . on either side). A single
// digit, a year (1900 to 2100) and a number with a leading zero, written with
// no other formatting, are not marked at all: they are indistinguishable from
// prose ("two", "step 3", "in 2024", "007").
//
// A written number T matches a value v of a shown row when v, rounded to the
// digits T shows, is T: |v - T| <= half a unit of T's last digit (1,234 is
// 1233.5 up to 1234.5; 1.2M is 1,150,000 up to 1,250,000; 12.5% is 12.45 up to
// 12.55). The unit of a suffixed number is its last digit times the suffix, and
// its tolerance is also capped at 5% of T, so "1M" is not every value from half
// a million up. A % number matches v as written (12% is 12) or, for a cell
// that is a fraction (|v| < 1 and not 0), as that fraction times 100 (12% is
// 0.12). The sign must agree. Only numeric cells count: a number, or text that
// is wholly a plain decimal number. Three guards keep a coincidence from
// reading as evidence:
//   - Identifier columns are never matched: a column named id or ending in
//     _id, _key or _pk (any case), or an integer column whose shown values
//     (three or more) are all distinct and consecutive, which is a row number
//     or a key, whatever it is called.
//   - The value must be unique: a read with two or more cells a number
//     matches does not vouch for it.
//   - In a read that showed more than 10 rows, only a specific number can
//     match: four or more significant digits (1,843,210) or a decimal with two
//     or more places as written (12.47, 3.25%). Shorter numbers, `214` or
//     `45%` or `$87`, are in any long result by chance. In a read of 10 rows
//     or fewer a number of any length can match, if it is unique.
//
// The numbers are tokenised by the same rules in the panel (lib/numberMarks.ts);
// testdata/number_tokens.json pins both.

// RunValues is what one read tool call of the turn returned to the model: the
// receipt it made and the rows as shown, in memory only.
type RunValues struct {
	ReceiptID string
	Columns   []string
	Rows      [][]any
}

// NumberMark is the verdict on one distinct number as written in an answer.
// Every occurrence of the same text has the same verdict.
type NumberMark struct {
	Text    string `json:"text"`
	Matched bool   `json:"matched"`
	// Withheld: a shown value is this number, but it could be a coincidence (a
	// long read, a repeated value, an identifier column), so it is left unmarked.
	Withheld  bool   `json:"withheld,omitempty"`
	ReceiptID string `json:"receiptId,omitempty"`
	Column    string `json:"column,omitempty"`
}

const (
	maxMarks        = 200
	maxCellsPerRun  = 20000
	shortResultRows = 10
)

// number is one written number the rules above recognise.
type number struct {
	text     string
	value    *big.Rat // T, with its sign and its suffix applied
	half     *big.Rat // the tolerance
	percent  bool
	specific bool // enough digits to match in a long result
	valueF   float64
	halfF    float64
}

// specificNumber reports whether a number is specific enough to match in a read
// of more than shortResultRows rows: four or more significant digits, or two or
// more decimal places as written.
func specificNumber(intPart, frac string) bool {
	if len(frac) >= 3 { // ".dd"
		return true
	}
	digits := strings.TrimLeft(intPart+strings.TrimPrefix(frac, "."), "0")
	return len(digits) >= 4
}

var numberRE = regexp.MustCompile(`[-+\x{2212}]?[$\x{20AC}\x{00A3}\x{00A5}]?(\d{1,3}(?:,\d{3})+|\d+)(\.\d+)?( ?(?i:thousand|million|billion|trillion)|[KkMBT]|bn)?%?`)

// suffixScale is each suffix's factor, keyed by its lower-case text.
var suffixScale = map[string]int64{
	"k": 1e3, "thousand": 1e3,
	"m": 1e6, "million": 1e6,
	"b": 1e9, "bn": 1e9, "billion": 1e9,
	"t": 1e12, "trillion": 1e12,
}

func tokenize(text string) []number {
	var out []number
	for _, m := range numberRE.FindAllStringSubmatchIndex(text, -1) {
		if n, ok := parseNumber(text, m); ok {
			out = append(out, n)
		}
	}
	return out
}

// parseNumber turns one match into a number, or reports that the match is not
// one by the context rules.
func parseNumber(text string, m []int) (number, bool) {
	start, end := m[0], m[1]
	whole := text[start:end]
	if !leftBoundaryOK(text, start) || !rightBoundaryOK(text, end) {
		return number{}, false
	}
	intPart := strings.ReplaceAll(text[m[2]:m[3]], ",", "")
	frac := ""
	if m[4] >= 0 {
		frac = text[m[4]:m[5]]
	}
	suffix := ""
	if m[6] >= 0 {
		suffix = strings.ToLower(strings.TrimSpace(text[m[6]:m[7]]))
	}
	percent := strings.HasSuffix(whole, "%")
	grouped := strings.Contains(text[m[2]:m[3]], ",")
	currency := strings.ContainsAny(whole, "$€£¥")
	bare := !grouped && frac == "" && suffix == "" && !percent && !currency
	if bare && ignoredBare(intPart) {
		return number{}, false
	}
	v, ok := new(big.Rat).SetString(intPart + frac)
	if !ok {
		return number{}, false
	}
	if strings.HasPrefix(whole, "-") || strings.HasPrefix(whole, "−") {
		v.Neg(v)
	}
	decimals := 0
	if frac != "" {
		decimals = len(frac) - 1 // frac is ".ddd"
	}
	half := new(big.Rat).SetFrac(big.NewInt(5), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)+1), nil)) // 0.5 * 10^-d
	if suffix != "" {
		scale := new(big.Rat).SetInt64(suffixScale[suffix])
		v.Mul(v, scale)
		half.Mul(half, scale)
		if fifth := new(big.Rat).Mul(new(big.Rat).Abs(v), big.NewRat(1, 20)); fifth.Cmp(half) < 0 {
			half = fifth
		}
	}
	vf, _ := v.Float64()
	hf, _ := half.Float64()
	return number{text: whole, value: v, half: half, percent: percent, specific: specificNumber(intPart, frac), valueF: vf, halfF: hf}, true
}

// ignoredBare reports whether a number written with no formatting at all is
// left unmarked: a single digit, a year or a number with a leading zero.
func ignoredBare(digits string) bool {
	if len(digits) == 1 {
		return true
	}
	if digits[0] == '0' {
		return true
	}
	return len(digits) == 4 && digits >= "1900" && digits <= "2100"
}

func isJoiner(r rune) bool { return r == '-' || r == '/' || r == ':' || r == ',' || r == '.' }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// leftBoundaryOK checks what comes before a number.
func leftBoundaryOK(text string, start int) bool {
	if start == 0 {
		return true
	}
	p, _ := utf8.DecodeLastRuneInString(text[:start])
	if isWordRune(p) || p == '#' || p == '/' || p == ':' || p == '@' || p == '\\' {
		return false
	}
	if p == '.' || p == ',' {
		// "v.5", "1,5" and the like: a joiner after a word.
		if q, _ := utf8.DecodeLastRuneInString(text[:start-1]); isWordRune(q) {
			return false
		}
	}
	return true
}

// rightBoundaryOK checks what follows a number.
func rightBoundaryOK(text string, end int) bool {
	if end >= len(text) {
		return true
	}
	r, size := utf8.DecodeRuneInString(text[end:])
	if isWordRune(r) {
		return false
	}
	if isJoiner(r) && end+size < len(text) {
		if n, _ := utf8.DecodeRuneInString(text[end+size:]); unicode.IsDigit(n) {
			return false
		}
	}
	return true
}

// matches reports whether number n is the value v (in one reading) within n's
// tolerance. frac says v is a fraction (|v| < 1, not 0), the only kind of cell a
// % number reads times 100.
func (n number) matches(v *big.Rat, frac bool) bool {
	readings := []*big.Rat{v}
	if n.percent && frac {
		readings = append(readings, new(big.Rat).Mul(v, big.NewRat(100, 1)))
	}
	for _, c := range readings {
		d := new(big.Rat).Sub(c, n.value)
		if d.Abs(d).Cmp(n.half) <= 0 {
			return true
		}
	}
	return false
}

// cellRE is a plain decimal: what a text cell must be, whole, to count.
var cellRE = regexp.MustCompile(`^[-+]?\d+(\.\d+)?$`)

// cellValue is a result cell as a number, or nil.
func cellValue(c any) *big.Rat {
	switch v := c.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(v))
	case int8:
		return new(big.Rat).SetInt64(int64(v))
	case int16:
		return new(big.Rat).SetInt64(int64(v))
	case int32:
		return new(big.Rat).SetInt64(int64(v))
	case int64:
		return new(big.Rat).SetInt64(v)
	case uint:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(v)))
	case uint8:
		return new(big.Rat).SetInt64(int64(v))
	case uint16:
		return new(big.Rat).SetInt64(int64(v))
	case uint32:
		return new(big.Rat).SetInt64(int64(v))
	case uint64:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(v))
	case float32:
		return floatRat(float64(v))
	case float64:
		return floatRat(v)
	case *big.Int:
		if v == nil {
			return nil
		}
		return new(big.Rat).SetInt(v)
	case string:
		s := strings.TrimSpace(v)
		if !cellRE.MatchString(s) {
			return nil
		}
		r, _ := new(big.Rat).SetString(s)
		return r
	}
	return nil
}

func floatRat(f float64) *big.Rat {
	r := new(big.Rat)
	if r.SetFloat64(f) == nil { // NaN or infinity
		return nil
	}
	return r
}

// cell is one numeric cell of a read, converted once for the whole answer.
type cell struct {
	v     *big.Rat
	f     float64
	frac  bool // |v| < 1 and not 0
	col   int
	ident bool // in an identifier column: it never vouches, but it is a shown value
}

// preparedRun is a read with its numeric cells converted.
type preparedRun struct {
	id      string
	columns []string
	rows    int
	cells   []cell
}

// identifierSuffixes end the name of a column that holds a key, not a measure.
var identifierSuffixes = []string{"_id", "_key", "_pk"}

func identifierName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "id" {
		return true
	}
	for _, s := range identifierSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// consecutiveInts reports whether every row of column ci is an integer, there are
// at least three, and together they are distinct and consecutive: a row number
// or a key whatever it is called.
func consecutiveInts(rows [][]any, ci int) bool {
	if len(rows) < 3 {
		return false
	}
	seen := make(map[int64]struct{}, len(rows))
	lo, hi := int64(0), int64(0)
	for i, row := range rows {
		if ci >= len(row) {
			return false
		}
		v := cellValue(row[ci])
		if v == nil || !v.IsInt() || !v.Num().IsInt64() {
			return false
		}
		n := v.Num().Int64()
		if i == 0 || n < lo {
			lo = n
		}
		if i == 0 || n > hi {
			hi = n
		}
		seen[n] = struct{}{}
	}
	return len(seen) == len(rows) && hi-lo == int64(len(rows)-1)
}

// prepareRuns converts each read's cells once.
func prepareRuns(runs []RunValues) []preparedRun {
	out := make([]preparedRun, 0, len(runs))
	for _, run := range runs {
		if run.ReceiptID == "" {
			continue
		}
		pr := preparedRun{id: run.ReceiptID, columns: run.Columns, rows: len(run.Rows)}
		width := len(run.Columns)
		for _, row := range run.Rows {
			if len(row) > width {
				width = len(row)
			}
		}
		skip := make([]bool, width)
		for ci := 0; ci < width; ci++ {
			skip[ci] = (ci < len(run.Columns) && identifierName(run.Columns[ci])) || consecutiveInts(run.Rows, ci)
		}
		for _, row := range run.Rows {
			for ci, c := range row {
				if len(pr.cells) >= maxCellsPerRun {
					break
				}
				v := cellValue(c)
				if v == nil {
					continue
				}
				f, _ := v.Float64()
				pr.cells = append(pr.cells, cell{v: v, f: f, frac: v.Sign() != 0 && new(big.Rat).Abs(v).Cmp(big.NewRat(1, 1)) < 0, col: ci, ident: skip[ci]})
			}
		}
		out = append(out, pr)
	}
	return out
}

// MarkNumbers marks each distinct number of answer: matched, with the first
// read of the turn (in the order they ran) that showed a value it matches and
// that value's column; withheld (a shown value is that number, but a guard keeps
// the check back, so it is left unmarked); or unmatched (no shown cell is it:
// "not from a query"). runs are this turn's reads only.
func MarkNumbers(answer string, runs []RunValues) []NumberMark {
	var marks []NumberMark
	seen := map[string]bool{}
	var prepared []preparedRun
	for _, n := range tokenize(maskMarkdown(answer)) {
		if seen[n.text] {
			continue
		}
		if len(marks) >= maxMarks {
			break
		}
		seen[n.text] = true
		if prepared == nil {
			prepared = prepareRuns(runs)
		}
		mk := NumberMark{Text: n.text}
		id, col, v := firstMatch(n, prepared)
		switch v {
		case verdictMatched:
			mk.Matched, mk.ReceiptID, mk.Column = true, id, col
		case verdictWithheld:
			mk.Withheld = true
		}
		marks = append(marks, mk)
	}
	return marks
}

// verdict is what a number's reads say of it.
type verdict int

const (
	verdictUnmatched verdict = iota // no shown cell is this number
	verdictWithheld                 // a shown cell is, but a guard keeps the check back
	verdictMatched
)

// count is how many cells outside identifier columns of run n matches, the
// column of the first, and whether any cell at all (an identifier's too) does.
func (n number) count(run preparedRun) (count, firstCol int, any bool) {
	tol := n.halfF*(1+1e-9) + math.Abs(n.valueF)*1e-12
	for _, c := range run.cells {
		near := math.Abs(c.f-n.valueF) <= tol || (n.percent && c.frac && math.Abs(c.f*100-n.valueF) <= tol)
		if !near || !n.matches(c.v, c.frac) {
			continue
		}
		any = true
		if c.ident {
			continue
		}
		if count == 0 {
			firstCol = c.col
		}
		count++
	}
	return count, firstCol, any
}

// firstMatch is the first run, in order, whose shown cells vouch for n: exactly
// one cell outside an identifier column matches it, and in a long read n is
// specific. When no run vouches but some shown cell is n, the verdict is
// withheld, not unmatched.
func firstMatch(n number, runs []preparedRun) (receiptID, column string, v verdict) {
	v = verdictUnmatched
	for _, run := range runs {
		count, ci, any := n.count(run)
		if !any {
			continue
		}
		if (run.rows > shortResultRows && !n.specific) || count != 1 {
			v = verdictWithheld
			continue
		}
		col := ""
		if ci < len(run.columns) {
			col = run.columns[ci]
		}
		return run.id, col, verdictMatched
	}
	return "", "", v
}

// RunValuesFrom reads a read tool call's result (run_query, run_saved_query
// or preview_query) into the receipt it made and the rows as shown. It reports
// false for a result with no receipt (anything that is not a read, a refusal,
// a store that could not be written).
func RunValuesFrom(result any) (RunValues, bool) {
	m, ok := result.(map[string]any)
	if !ok {
		return RunValues{}, false
	}
	id, _ := m["receipt_id"].(string)
	if id == "" {
		return RunValues{}, false
	}
	rv := RunValues{ReceiptID: id}
	switch cols := m["columns"].(type) {
	case []string:
		rv.Columns = cols
	case []map[string]any:
		for _, c := range cols {
			name, _ := c["name"].(string)
			rv.Columns = append(rv.Columns, name)
		}
	}
	switch rows := m["rows"].(type) {
	case [][]any:
		rv.Rows = rows
	case []map[string]any: // preview_query's sample: objects keyed by column name
		for _, obj := range rows {
			row := make([]any, len(rv.Columns))
			for i, name := range rv.Columns {
				row[i] = obj[name]
			}
			rv.Rows = append(rv.Rows, row)
		}
	}
	return rv, true
}
