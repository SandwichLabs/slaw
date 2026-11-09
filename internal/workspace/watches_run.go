package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SandwichLabs/slaw/internal/connname"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/strictyaml"
	"github.com/SandwichLabs/slaw/internal/watches"
)

// Checking a watch: the person's action (`slaw watch check`, Studio's Check),
// never an agent tool or an MCP method. A check runs the watch's question once
// through the caller's ReadOnlyRunner, the agent policy's read-only path in the
// tightest tier: the watch file may come from a cloned repository, so a hostile
// saved query or metric body is refused there, not trusted here. A watch is
// checked only when asked; nothing runs by itself.

// Limits of one check.
const (
	// watchValueRows is how many rows a threshold check reads: it wants one,
	// and a few more are enough to say what it got instead.
	watchValueRows = 5
	// watchRowsRead is how many rows a rows check reads. Reaching it is
	// reported as "1,000+".
	watchRowsRead = 1001
	// WatchRowsShown is the largest count a rows check reports exactly.
	WatchRowsShown = watchRowsRead - 1

	// A sample is the first columns of the first rows of an alerting rows
	// check, kept in memory for the report and never written to the history.
	watchSampleRows  = 20
	watchSampleCols  = 4
	watchSampleCell  = 80
	watchSampleBytes = 4096
)

// watchVetTimeout bounds the follow-up vet after a refusal, so a refused watch
// cannot take a second statement timeout on top of the first.
const watchVetTimeout = 10 * time.Second

// WatchSample is the rows an alerting rows check read, capped. It exists for
// the report that ran the check; nothing stores it.
type WatchSample struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Capped says the sample is less than what the check read (rows, columns
	// or bytes were left out).
	Capped bool `json:"capped,omitempty"`
}

// WatchResult is the outcome of one check: the entry the history keeps, plus
// what only the report shows.
type WatchResult struct {
	watches.Entry
	// Question and Condition say what was asked ("metric revenue") and when it
	// fires ("above 1,000").
	Question  string `json:"question"`
	Condition string `json:"condition"`
	// Sample is set for an alerting rows check. It is never written to the
	// history.
	Sample *WatchSample `json:"sample,omitempty"`
	// HistoryError says the outcome could not be recorded; the check itself
	// stands.
	HistoryError string `json:"history_error,omitempty"`
	// Interrupted says the check was cut short by a cancel: its outcome says
	// nothing about the watch and it was never recorded in the history. Every
	// other result of a run was recorded (or carries HistoryError).
	Interrupted bool `json:"interrupted,omitempty"`
	// StoredReason, when set, is what the history keeps in place of Reason. A
	// database error can quote a value of a row, so the report shows the
	// message and the history keeps only its class.
	StoredReason string `json:"-"`
}

// CheckWatches checks the named watches (none named is every watch), one at a
// time, and appends each outcome to the workspace's history. A watch that
// cannot be read, planned or run is an error result and the others still run.
// A name that matches no watch is an error before anything runs. If ctx is
// canceled the results so far come back (the interrupted watch as an error
// result marked Interrupted, which is not recorded) with the context's error.
func CheckWatches(ctx context.Context, ws Workspace, names []string, runner ReadOnlyRunner, now time.Time) ([]WatchResult, error) {
	return CheckWatchesProgress(ctx, ws, names, runner, now, nil)
}

// WatchProgress is told when a watch starts (done is nil) and when it ends. It
// is how a surface shows a long run as it goes. The result it is given is the
// one CheckWatches returns, sample rows included, so a listener that must not
// hold rows keeps only the name and status.
type WatchProgress func(name string, done *WatchResult)

// CheckWatchesProgress is CheckWatches that reports each watch's start and end.
func CheckWatchesProgress(ctx context.Context, ws Workspace, names []string, runner ReadOnlyRunner, now time.Time, progress WatchProgress) ([]WatchResult, error) {
	list, err := selectWatches(ctx, ws.Watches(), names)
	if err != nil {
		return nil, err
	}
	out := make([]WatchResult, 0, len(list))
	for i := range list {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("the check was canceled: %w", err)
		}
		if progress != nil {
			progress(list[i].Name, nil)
		}
		res := checkWatch(ctx, ws, &list[i], runner, now)
		if ctx.Err() == nil {
			recordWatch(ctx, ws.WatchHistory(), &res)
		} else { // a canceled check says nothing about the watch
			res.Interrupted = true
		}
		if progress != nil {
			progress(list[i].Name, &res)
		}
		out = append(out, res)
	}
	if err := ctx.Err(); err != nil { // a cancel that landed in the last watch
		return out, fmt.Errorf("the check was canceled: %w", err)
	}
	return out, nil
}

// selectWatches resolves names against the store; an empty names is all.
func selectWatches(ctx context.Context, st watches.ReadStore, names []string) ([]watches.Watch, error) {
	all, err := st.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list watches: %w", err)
	}
	if len(names) == 0 {
		return all, nil
	}
	byName := make(map[string]watches.Watch, len(all))
	for _, w := range all {
		byName[w.Name] = w
	}
	var out []watches.Watch
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		w, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("there is no watch named %q (`slaw watch list` shows them)", n)
		}
		out = append(out, w)
	}
	return out, nil
}

// recordWatch appends the outcome to the history. A name that cannot be a
// history key (the listing's stub for too many files) is not recorded.
func recordWatch(ctx context.Context, h watches.History, res *WatchResult) {
	if h == nil || !connname.Valid(res.Watch) {
		return
	}
	e := res.Entry
	if res.StoredReason != "" {
		e.Reason = res.StoredReason
	}
	if err := h.Append(ctx, e); err != nil {
		res.HistoryError = reasonText(err.Error())
	}
}

// watchPlan is the statement a watch runs.
type watchPlan struct {
	sql        string
	connection string
	// label names the connection in a failure ("the default connection").
	label   string
	params  map[string]any
	maxRows int
	// origin is what the watch's read is recorded as (a receipt).
	origin receipts.Origin
}

func checkWatch(ctx context.Context, ws Workspace, w *watches.Watch, runner ReadOnlyRunner, now time.Time) (res WatchResult) {
	res = newWatchResult(w, now)
	start := time.Now()
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()

	if w.Error != "" {
		return res.fail("the file is not valid: " + w.Error)
	}
	plan, err := planWatch(ctx, ws, w, now)
	if err != nil {
		return res.fail(err.Error())
	}
	rs, truncated, err := runWatchPlan(ctx, runner, plan)
	if err != nil {
		return res.failRun(runFailure(ctx, runner, plan, err))
	}
	if w.When.IsThreshold() {
		res.evalThreshold(w, rs, truncated)
	} else {
		res.evalRows(w, rs, truncated)
	}
	return res
}

func newWatchResult(w *watches.Watch, now time.Time) WatchResult {
	res := WatchResult{Entry: watches.Entry{Watch: w.Name, Rev: w.Rev, At: now}}
	switch {
	case w.Question.Query != "":
		res.Question = "query " + w.Question.Query
	case w.Question.Metric != "":
		res.Question = "metric " + w.Question.Metric
	}
	res.Condition = w.When.Describe()
	return res
}

// fail makes the result an error outcome.
func (r WatchResult) fail(reason string) WatchResult {
	r.Status, r.Reason = watches.StatusError, reasonText(reason)
	return r
}

// failRun makes the result an error outcome whose stored reason differs from
// the shown one.
func (r WatchResult) failRun(f runFailed) WatchResult {
	r = r.fail(f.shown)
	r.StoredReason = reasonText(f.stored)
	return r
}

// reasonText is a message as one safe, bounded line: credential-shaped text
// redacted, control characters shown, capped. A database error may quote a
// value, so the cap is also what keeps a row from reaching the history.
func reasonText(s string) string {
	s = secrets.RedactPatterns(strings.Join(strings.Fields(s), " "))
	s = strictyaml.EscapeHidden(s)
	if r := []rune(s); len(r) > watches.MaxReasonRunes {
		s = string(r[:watches.MaxReasonRunes-3]) + "..."
	}
	return s
}

// --- planning ----------------------------------------------------------------

func planWatch(ctx context.Context, ws Workspace, w *watches.Watch, now time.Time) (watchPlan, error) {
	maxRows := watchValueRows
	if !w.When.IsThreshold() {
		maxRows = watchRowsRead
	}
	var (
		p   watchPlan
		err error
	)
	if w.Question.Metric != "" {
		p, err = planMetricWatch(ctx, ws, w)
	} else {
		p, err = planSavedQueryWatch(ctx, ws, w, now)
	}
	if err != nil {
		return watchPlan{}, err
	}
	p.maxRows = maxRows
	p.label = p.connection
	if p.label == "" {
		p.label = ws.DefaultConnection()
	}
	if p.label == "" {
		p.label = "the default connection"
	}
	return p, nil
}

// planSavedQueryWatch binds the watch's parameter text to the saved query the
// way `slaw query --saved -p` does: queries.ParseValues types it by the
// declared parameters and queries.Bind turns it into bound values. The text of
// the statement is the saved query's own, unchanged.
func planSavedQueryWatch(ctx context.Context, ws Workspace, w *watches.Watch, now time.Time) (watchPlan, error) {
	id := w.Question.Query
	q, err := ws.Queries().Get(ctx, id)
	if err != nil {
		if errors.Is(err, queries.ErrNotFound) {
			return watchPlan{}, fmt.Errorf("there is no saved query %q (see the queries/ folder)", id)
		}
		return watchPlan{}, fmt.Errorf("failed to read saved query %q: %w", id, err)
	}
	if q.Error != "" {
		return watchPlan{}, fmt.Errorf("saved query %q cannot be run: %s", id, q.Error)
	}
	if err := checkConnectionDefined(ws, q.Connection, fmt.Sprintf("saved query %q", id)); err != nil {
		return watchPlan{}, err
	}
	for _, p := range q.Parameters {
		if _, given := w.Question.Params[p.Name]; !given && p.Required && p.Default == "" {
			return watchPlan{}, fmt.Errorf("saved query %q needs a value for parameter %q: add question.params.%s to the watch", id, p.Name, p.Name)
		}
	}
	values, err := queries.ParseValues(q, w.Question.Params)
	if err != nil {
		return watchPlan{}, fmt.Errorf("saved query %q: %w", id, err)
	}
	params, err := queries.Bind(q, values, queries.BindOptions{Now: now})
	if err != nil {
		return watchPlan{}, fmt.Errorf("saved query %q: %w", id, err)
	}
	origin := receipts.Origin{Surface: receipts.SurfaceWatch, Source: receipts.Source{Kind: receipts.SourceSavedQuery, Ref: "queries/" + id + ".sql"}}
	return watchPlan{sql: q.SQL, connection: q.Connection, params: params, origin: origin}, nil
}

// planMetricWatch is the metric at its grain total: MetricQuery with no
// dimensions, the same statement as the Semantics view's "Try it". The
// read-only runner expands the metric() call like any other.
func planMetricWatch(ctx context.Context, ws Workspace, w *watches.Watch) (watchPlan, error) {
	sql, conn, err := MetricQuery(ctx, ws, w.Question.Metric, nil)
	if err != nil {
		return watchPlan{}, err
	}
	if err := checkConnectionDefined(ws, conn, fmt.Sprintf("the saved query behind metric %q", w.Question.Metric)); err != nil {
		return watchPlan{}, err
	}
	origin := receipts.Origin{Surface: receipts.SurfaceWatch, Source: receipts.Source{Kind: receipts.SourceMetric, Ref: "semantics/metrics/" + w.Question.Metric + ".yaml"}}
	return watchPlan{sql: sql, connection: conn, origin: origin}, nil
}

// checkConnectionDefined says plainly that a query names a connection this
// workspace does not define, rather than leaving the engine's "not attached".
func checkConnectionDefined(ws Workspace, name, what string) error {
	if name == "" {
		return nil
	}
	if _, ok := ws.Connection(name); !ok {
		return fmt.Errorf("%s runs on connection %q, which this workspace does not define (slaw conn list)", what, name)
	}
	return nil
}

// --- running -----------------------------------------------------------------

// runWatchPlan runs the statement through the read-only runner, bounded by
// CheckStatementTimeout and the caller's context.
func runWatchPlan(ctx context.Context, runner ReadOnlyRunner, p watchPlan) (*engine.ResultSet, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckStatementTimeout)
	defer cancel()
	return runner.RunReadOnlySQLIn(receipts.WithOrigin(ctx, p.origin), p.connection, p.sql, p.params, p.maxRows)
}

// runFailed is why a run failed: shown in the report, and the part of it the
// history may keep.
type runFailed struct{ shown, stored string }

// policyRefusedText is the watch's wording for a statement the policy refuses.
const policyRefusedText = "refused: a watch may only read (no writes, COPY, ATTACH or SET; files only inside data/)"

// bindFailure asks the runner's vet why the gate refused. A statement that does
// not bind (an unknown column) is the statement's fault, not a policy denial:
// the database's message is shown and only its class is stored.
func bindFailure(ctx context.Context, runner ReadOnlyRunner, p watchPlan) (runFailed, bool) {
	ctx, cancel := context.WithTimeout(ctx, watchVetTimeout)
	defer cancel()
	verr := runner.VetReadOnlySQLIn(ctx, p.connection, p.sql, p.params)
	var bf interface{ BindFailure() bool }
	if verr == nil || !errors.As(verr, &bf) || !bf.BindFailure() {
		return runFailed{}, false
	}
	return runFailed{"could not run: " + watches.CleanDBError(verr.Error()), "could not run: " + watches.ErrorClass(verr.Error())}, true
}

// runFailure says why a run failed. A database's own message can quote a row's
// value, so it is shown but never stored: the history gets its error class.
func runFailure(parent context.Context, runner ReadOnlyRunner, p watchPlan, err error) runFailed {
	var cf interface{ ConnectFailure() bool }
	var pr interface{ PolicyRefused() bool }
	switch {
	case parent.Err() != nil:
		return runFailed{"the check was canceled", "the check was canceled"}
	case errors.Is(err, context.DeadlineExceeded):
		t := fmt.Sprintf("the statement did not finish within %s", CheckStatementTimeout)
		return runFailed{t, t}
	case errors.As(err, &cf) && cf.ConnectFailure():
		return runFailed{
			fmt.Sprintf("could not connect to %s: %s", p.label, watches.CleanDBError(err.Error())),
			fmt.Sprintf("could not connect to %s", p.label),
		}
	case errors.As(err, &pr) && pr.PolicyRefused():
		if f, ok := bindFailure(parent, runner, p); ok {
			return f
		}
		return runFailed{policyRefusedText, policyRefusedText}
	}
	return runFailed{"could not run: " + watches.CleanDBError(err.Error()), "could not run: " + watches.ErrorClass(err.Error())}
}

// --- judging -----------------------------------------------------------------

// evalThreshold reads the one value the result must hold and judges it.
func (r *WatchResult) evalThreshold(w *watches.Watch, rs *engine.ResultSet, truncated bool) {
	v, err := thresholdValue(rs, truncated)
	if err != nil {
		*r = r.fail(err.Error())
		return
	}
	r.Value = v.number()
	target := w.Question.Target()
	if why, fired := crossed(w.When, v); fired {
		r.Status = watches.StatusAlert
		r.Reason = reasonText(fmt.Sprintf("%s is %s, %s", target, v.text(), why))
		return
	}
	r.Status = watches.StatusOK
	r.Reason = reasonText(fmt.Sprintf("%s is %s; alerts %s", target, v.text(), w.When.Describe()))
}

// numVal is a number read from a result. An integer or decimal is kept exact
// (a BIGINT or DECIMAL beyond a double's safe range would otherwise compare
// wrongly); a DOUBLE is compared as a double.
type numVal struct {
	rat   *big.Rat // nil for a float
	f     float64
	exact string // the decimal text a rat was read from, "" for an integer
}

// number is the value as the JSON number the history and --json keep: an
// integer in full, a decimal as its exact text, a double in its shortest form.
func (v numVal) number() json.Number {
	shortest := func(f float64) json.Number { return json.Number(strconv.FormatFloat(f, 'g', -1, 64)) }
	switch {
	case v.rat == nil:
		return shortest(v.f)
	case v.rat.IsInt():
		return json.Number(v.rat.Num().String())
	case v.exact != "" && json.Valid([]byte(v.exact)):
		return json.Number(v.exact)
	}
	f, _ := v.rat.Float64()
	return shortest(f)
}

// cmp compares the value with a threshold: -1, 0 or 1.
func (v numVal) cmp(t watches.Threshold) int {
	if v.rat != nil {
		return v.rat.Cmp(t.Rat())
	}
	switch tf := t.Float(); {
	case v.f < tf:
		return -1
	case v.f > tf:
		return 1
	}
	return 0
}

// text is the value as a reason shows it.
func (v numVal) text() string {
	if v.rat != nil {
		return watches.FormatRat(v.rat)
	}
	return watches.FormatNumber(v.f)
}

// crossed says whether v is on the alerting side, and how, as a phrase that
// follows "is 1,240, ".
func crossed(c watches.Condition, v numVal) (string, bool) {
	switch {
	case c.Above != nil:
		return "above " + c.Above.Display(), v.cmp(*c.Above) > 0
	case c.Below != nil:
		return "below " + c.Below.Display(), v.cmp(*c.Below) < 0
	case len(c.Outside) == 2:
		lo, hi := c.Outside[0], c.Outside[1]
		if v.cmp(lo) < 0 {
			return "below " + lo.Display(), true
		}
		if v.cmp(hi) > 0 {
			return "above " + hi.Display(), true
		}
	}
	return "", false
}

const thresholdShape = "a threshold check needs exactly one row and one numeric column (or a column named value)"

// thresholdValue is the one number a threshold result holds. Any other shape
// is an error that names the shape it got.
func thresholdValue(rs *engine.ResultSet, truncated bool) (numVal, error) {
	switch {
	case rs == nil || len(rs.Rows) == 0:
		return numVal{}, fmt.Errorf("the question returned no rows; %s", thresholdShape)
	case len(rs.Rows) > 1 || truncated:
		more := fmt.Sprint(len(rs.Rows))
		if truncated {
			more = "more than " + more
		}
		return numVal{}, fmt.Errorf("the question returned %s rows; %s", more, thresholdShape)
	}
	idx, err := valueColumn(rs.Columns)
	if err != nil {
		return numVal{}, err
	}
	col := rs.Columns[idx]
	cell := rs.Value(0, idx)
	if cell == nil {
		return numVal{}, fmt.Errorf("column %q is NULL", columnText(col.Name))
	}
	v, ok := asNum(cell)
	if !ok {
		return numVal{}, fmt.Errorf("column %q is not a finite number; %s", columnText(col.Name), thresholdShape)
	}
	return v, nil
}

// valueColumn picks the column a threshold reads: the only one, or the one
// named value, and it must be numeric.
func valueColumn(cols []engine.Column) (int, error) {
	idx, named := -1, 0
	if len(cols) == 1 {
		idx = 0
	} else {
		for i, c := range cols {
			if strings.EqualFold(c.Name, "value") {
				idx = i
				named++
			}
		}
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = columnText(c.Name)
	}
	switch {
	case named > 1:
		return 0, fmt.Errorf("the question returned %d columns named value (%s); %s", named, strings.Join(names, ", "), thresholdShape)
	case idx < 0:
		return 0, fmt.Errorf("the question returned %d columns (%s); %s", len(cols), strings.Join(names, ", "), thresholdShape)
	}
	if t := cols[idx].Type; t != "" && !numericType(t) {
		return 0, fmt.Errorf("column %q is %s, not a number; %s", columnText(cols[idx].Name), t, thresholdShape)
	}
	return idx, nil
}

// columnText is a column name made safe to show.
func columnText(s string) string {
	s = strictyaml.EscapeHidden(s)
	if r := []rune(s); len(r) > 40 {
		s = string(r[:37]) + "..."
	}
	return s
}

var numericTypes = map[string]bool{
	"TINYINT": true, "SMALLINT": true, "INTEGER": true, "INT": true, "BIGINT": true, "HUGEINT": true,
	"UTINYINT": true, "USMALLINT": true, "UINTEGER": true, "UBIGINT": true, "UHUGEINT": true,
	"FLOAT": true, "REAL": true, "DOUBLE": true, "DECIMAL": true,
}

// numericType reports whether a DuckDB type name ("DECIMAL(12,2)") is a number.
func numericType(t string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(t)), "(")
	return numericTypes[base]
}

// plainDecimalRE is the exact text the engine gives for a decimal or an integer
// too big for a double: digits, a point, no exponent (so no unbounded Rat).
var plainDecimalRE = regexp.MustCompile(`^-?[0-9]{1,100}(\.[0-9]{1,100})?$`)

// asNum reads a number as the engine delivers it: a JSON-safe int or float, or
// exact text for a decimal or an integer too big for a double.
func asNum(v any) (numVal, bool) {
	switch x := v.(type) {
	case int64:
		return numVal{rat: new(big.Rat).SetInt64(x)}, true
	case int:
		return numVal{rat: new(big.Rat).SetInt64(int64(x))}, true
	case int32:
		return numVal{rat: new(big.Rat).SetInt64(int64(x))}, true
	case int16:
		return numVal{rat: new(big.Rat).SetInt64(int64(x))}, true
	case int8:
		return numVal{rat: new(big.Rat).SetInt64(int64(x))}, true
	case uint16:
		return numVal{rat: new(big.Rat).SetInt64(int64(x))}, true
	case uint8:
		return numVal{rat: new(big.Rat).SetInt64(int64(x))}, true
	case uint:
		return numVal{rat: new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(x)))}, true
	case uint64:
		return numVal{rat: new(big.Rat).SetInt(new(big.Int).SetUint64(x))}, true
	case uint32:
		return numVal{rat: new(big.Rat).SetInt64(int64(x))}, true
	case float64:
		return finiteFloat(x)
	case float32:
		// The shortest decimal of the float32, as the engine's own wire form
		// does: 0.95 as a FLOAT is 0.95, not 0.949999988.
		f, err := strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		if err != nil {
			return numVal{}, false
		}
		return finiteFloat(f)
	case json.Number:
		return exactNum(x.String())
	case string:
		return exactNum(strings.TrimSpace(x))
	}
	return numVal{}, false
}

func finiteFloat(f float64) (numVal, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return numVal{}, false
	}
	return numVal{f: f}, true
}

func exactNum(text string) (numVal, bool) {
	if !plainDecimalRE.MatchString(text) {
		return numVal{}, false
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return numVal{}, false
	}
	return numVal{rat: r, exact: text}, true
}

// evalRows counts the rows a rows check read and judges the count.
func (r *WatchResult) evalRows(w *watches.Watch, rs *engine.ResultSet, truncated bool) {
	n := 0
	if rs != nil {
		n = len(rs.Rows)
	}
	capped := truncated || n > WatchRowsShown
	r.Rows, r.RowsCapped = &n, capped
	target := w.Question.Target()
	fired := (w.When.Rows == watches.RowsAny) == (n > 0)
	r.Status = watches.StatusOK
	if fired {
		r.Status = watches.StatusAlert
	}
	r.Reason = reasonText(fmt.Sprintf("%s returned %s", target, rowsText(n, capped)))
	if fired && n > 0 {
		r.Sample = buildSample(rs)
	}
}

// rowsText is "no rows", "1 row", "3 rows" or "1,000+ rows".
func rowsText(n int, capped bool) string {
	switch {
	case capped:
		return watches.FormatNumber(WatchRowsShown) + "+ rows"
	case n == 0:
		return "no rows"
	case n == 1:
		return "1 row"
	}
	return watches.FormatNumber(float64(n)) + " rows"
}

// buildSample takes the first columns of the first rows, each cell capped and
// shown with control characters escaped, within a byte budget.
func buildSample(rs *engine.ResultSet) *WatchSample {
	cols := min(len(rs.Columns), watchSampleCols)
	s := &WatchSample{Columns: make([]string, cols), Rows: [][]string{}}
	for i := 0; i < cols; i++ {
		s.Columns[i] = columnText(rs.Columns[i].Name)
	}
	s.Capped = len(rs.Columns) > cols || len(rs.Rows) > watchSampleRows
	budget := watchSampleBytes
	for i, row := range rs.Rows {
		if i >= watchSampleRows {
			break
		}
		cells := make([]string, cols)
		size := 0
		for c := 0; c < cols && c < len(row); c++ {
			cells[c] = cellText(row[c])
			size += len(cells[c])
		}
		if size > budget {
			s.Capped = true
			break
		}
		budget -= size
		s.Rows = append(s.Rows, cells)
	}
	return s
}

// cellText is one value as short text.
func cellText(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		s = x
	default:
		s = fmt.Sprint(x)
	}
	s = strictyaml.EscapeHidden(s)
	if utf8.RuneCountInString(s) > watchSampleCell {
		s = string([]rune(s)[:watchSampleCell-3]) + "..."
	}
	return s
}
