package receipts

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Checks are the two text rules a Recorder applies, handed in by the package
// that owns them (engine.Recordable and engine.RecordableStatement: this leaf
// cannot import engine). A nil check refuses everything, so a Recorder built
// without them withholds every text and every value.
type Checks struct {
	// Statement says whether SQL text may be kept as written.
	Statement func(text string) bool
	// Value says whether one bound parameter value may be kept.
	Value func(text string) bool
}

// Recorder makes and keeps receipts: the store, the workspace's key, the
// per-person Policy and the text rules. The zero value and nil record nothing.
// Callers get one from their handler or from hub.Open, never from the engine.
type Recorder struct {
	store  Store
	key    []byte
	checks Checks
	log    *slog.Logger

	mu     sync.RWMutex
	policy Policy
}

// NewRecorder builds a Recorder over a store and the key LoadKey returned.
func NewRecorder(store Store, key []byte, p Policy, c Checks, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{store: store, key: key, checks: c, log: log, policy: p.Clamp()}
}

// Store is the receipts the Recorder keeps, for a reader (nil on a nil Recorder).
func (r *Recorder) Store() Store {
	if r == nil {
		return nil
	}
	return r.store
}

// SetPolicy changes the policy new receipts are made under and the store prunes by.
func (r *Recorder) SetPolicy(p Policy) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.policy = p.Clamp()
	r.mu.Unlock()
	// The store prunes by the same policy: one owner, forwarded, so a lowered
	// limit prunes.
	if ps, ok := r.store.(interface{ SetPolicy(Policy) }); ok {
		ps.SetPolicy(p)
	}
}

// Close waits for the store's background prune, if it has one. Safe on nil.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	if c, ok := r.store.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

func (r *Recorder) currentPolicy() Policy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

// Finish makes the receipt of the run on ctx and keeps it, and returns its id.
// shown are the rows as the caller shows them: after its own trimming (the
// probe row, DuckLake internal rows, a row cap, a result budget), so the
// receipt describes what was shown and not what was fetched. truncated says the
// caller cut the result; total is the true row count when the caller knows it
// (at least len(shown)), otherwise len(shown).
//
// It never fails a run. rec nil, no intent on ctx, a statement that is not a
// single select or explain, or a second call for the same run all give "" and
// a nil error; a store that cannot be written is logged at warn
// level and returned, and the caller sends its result without a receipt_id.
func Finish(ctx context.Context, rec *Recorder, shown [][]any, truncated bool, total int) (string, error) {
	run := RunFrom(ctx)
	if rec == nil || rec.store == nil || run == nil {
		return "", nil
	}
	in, facts, ok := run.claim()
	if !ok || !facts.Mintable() {
		return "", nil
	}
	r, err := rec.build(in, facts, shown, truncated, total)
	if err != nil {
		rec.log.Warn("a run receipt was not made", "error", err)
		return "", fmt.Errorf("failed to make the receipt: %w", err)
	}
	if err := rec.store.Put(r); err != nil {
		rec.log.Warn("a run receipt was not kept", "error", err)
		return "", fmt.Errorf("failed to keep the receipt: %w", err)
	}
	return r.ID, nil
}

// build makes the sealed receipt: every text and value through its check, the
// keyed hashes over what was written and what ran, the policy on the params and
// the result fingerprint over the rows as shown.
func (r *Recorder) build(in Intent, f Facts, shown [][]any, truncated bool, total int) (Receipt, error) {
	rec := Receipt{
		At: f.At, DurationMS: f.Duration.Milliseconds(),
		Surface: in.Surface, Actor: in.Actor, AgentSession: in.AgentSession,
		Source:  in.Source,
		Verdict: in.Verdict, Connections: f.Connections,
		Rows: Rows{Returned: int64(len(shown)), Total: int64(max(total, len(shown))), Truncated: truncated},
	}
	if !ValidRef(rec.Source.Ref) {
		rec.Source.Ref = ""
	}
	r.fillText(&rec, in, f)
	r.fillParams(&rec, in)
	rec.Columns = r.keptColumns(f, rec.SQLWithheld || rec.RunSQLWithheld)
	h := NewResultHasher(r.key, f.Columns)
	for _, row := range shown {
		if err := h.AddRow(row); err != nil {
			return Receipt{}, err
		}
	}
	rec.ResultHMAC = h.Sum()
	r.currentPolicy().ApplyParams(&rec)
	return Seal(rec)
}

// fillText sets the statement as written and as run: kept when the check passes,
// otherwise only its hash.
func (r *Recorder) fillText(rec *Receipt, in Intent, f Facts) {
	if in.SQL != "" {
		rec.SQLHash = HashText(r.key, in.SQL)
		if r.checks.Statement != nil && r.checks.Statement(in.SQL) {
			rec.SQL = in.SQL
		} else {
			rec.SQLWithheld = true
		}
	}
	rec.RunSQLHash = HashRunText(r.key, f.Executed)
	if r.checks.Statement != nil && r.checks.Statement(f.Summary) {
		rec.RunSQL = f.Summary
	} else {
		rec.RunSQLWithheld = true
	}
}

// keptColumns is the columns a receipt may keep. A name is kept only when it
// passes the value check; every name is withheld when the statement's text was
// (a name can quote the text) or the names come from data (a PIVOT). The types
// and the count always stay. The result fingerprint still covers the real names.
func (r *Recorder) keptColumns(f Facts, textWithheld bool) []Column {
	out := make([]Column, len(f.Columns))
	for i, c := range f.Columns {
		out[i] = Column{Type: c.Type}
		keep := c.Name == "" || (!textWithheld && !f.NamesFromData && r.checks.Value != nil && r.checks.Value(c.Name))
		if keep {
			out[i].Name = c.Name
		} else {
			out[i].Withheld = true
		}
	}
	return out
}

// fillParams sets the bound values, each through the value check, and the keyed
// hash over the full set (before the policy cuts or drops any).
func (r *Recorder) fillParams(rec *Receipt, in Intent) {
	if len(in.Params) == 0 {
		return
	}
	rec.ParamsHash = HashParams(r.key, in.Params)
	rec.Params = make(map[string]ParamValue, len(in.Params))
	for name, v := range in.Params {
		switch {
		case v == "":
			rec.Params[name] = ParamValue{}
		case r.checks.Value != nil && r.checks.Value(v):
			rec.Params[name] = ParamValue{Value: v}
		default:
			rec.Params[name] = ParamValue{Withheld: true}
		}
	}
}

// Policy is the policy new receipts are made under (clamped); the default on a
// nil Recorder.
func (r *Recorder) Policy() Policy {
	if r == nil {
		return DefaultPolicy()
	}
	return r.currentPolicy()
}
