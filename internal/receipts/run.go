package receipts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Intent is what a caller says about a run it wants a receipt for, put on the
// context of the final run only (WithIntent): who asked, from where, what they
// wrote and what the SQL policy said. The engine adds what only it knows (the
// Facts); the caller finishes it (Finish) after its own trimming of the rows.
type Intent struct {
	Surface Surface
	Actor   Actor
	// AgentSession is the MCP transport session, the chat conversation or the
	// task run; only for ActorAgent.
	AgentSession string
	Source       Source
	// SQL is the text as written, with metric() and $name intact. Params are
	// the bound values, as text (ParamStrings).
	SQL     string
	Params  map[string]string
	Verdict Verdict
	// Kind, when the caller already knows the statement is a single read (a
	// policy-gated path, or the console after its own parse), spares the engine
	// working it out. Empty: the engine parses the text (never preparing it).
	Kind StmtKind
}

// StmtKind is the statement type DuckDB reported for what ran. Only a select
// and an explain are minted.
type StmtKind string

// The statement kinds.
const (
	StmtSelect  StmtKind = "select"
	StmtExplain StmtKind = "explain"
	StmtOther   StmtKind = "other"
)

// Facts are what only the engine knows about a run.
type Facts struct {
	At       time.Time
	Duration time.Duration
	// Executed is the text DuckDB ran: after metric() expansion, the caller's
	// cap wrapper and wildcard expansion. It is held here only to be hashed
	// (run_sql_hash) and kept as Summary, never stored as it is.
	Executed string
	// Summary is Executed with each expanded key list put back as its pattern
	// (storageglob.Rewrite.Summarize).
	Summary string
	// NamesFromData says the result's column names come from data (a PIVOT, a
	// file's header row): they are never kept.
	NamesFromData bool
	Stmt          StmtKind
	Columns       []Column
	Connections   []Connection
}

// Mintable reports whether the run is one a receipt is made for: the engine
// ran something, and it was a single select or explain.
func (f Facts) Mintable() bool {
	return f.Executed != "" && (f.Stmt == StmtSelect || f.Stmt == StmtExplain)
}

// Run is the accumulator on a context: the intent, the facts the engine fills
// in, and whether the receipt was already minted (a run makes at most one).
type Run struct {
	mu       sync.Mutex
	intent   Intent
	facts    Facts
	finished bool
}

type runKey struct{}

// WithIntent marks ctx as the context of a run that wants a receipt.
func WithIntent(ctx context.Context, in Intent) context.Context {
	return context.WithValue(ctx, runKey{}, &Run{intent: in})
}

// RunFrom returns the run on ctx, or nil when nobody asked for a receipt. The
// engine records nothing when it is nil.
func RunFrom(ctx context.Context) *Run {
	r, _ := ctx.Value(runKey{}).(*Run)
	return r
}

// Record lets the engine change the facts under the run's lock. It is a no-op
// on a nil Run.
func (r *Run) Record(f func(*Facts)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.facts)
}

// claim returns the intent and facts and marks the run minted: only the first
// caller gets ok, so a run is never minted twice.
func (r *Run) claim() (Intent, Facts, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return Intent{}, Facts{}, false
	}
	r.finished = true
	return r.intent, r.facts, true
}

// Origin says where a render-time or policy-gated read came from, for a caller
// that reaches RunReadOnlySQLIn on its way (an artifact, a metric's Try it, an
// assertion, a watch). Without one such a read is a render with no source.
type Origin struct {
	Surface Surface
	Source  Source
	// Skip says the read is not one a receipt is made for (a join profile, which
	// measures a definition and answers nobody's question).
	Skip bool
}

type originKey struct{}

// WithOrigin puts an Origin on ctx.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom is the Origin on ctx, or a render with no source.
func OriginFrom(ctx context.Context) Origin {
	if o, ok := ctx.Value(originKey{}).(Origin); ok && (o.Surface != "" || o.Skip) {
		return o
	}
	return Origin{Surface: SurfaceRender}
}

type sessionKey struct{}

// WithAgentSession puts the agent session on ctx: the MCP transport session,
// the chat conversation or the task run. It is not the handler (one handler
// serves every SSE client, and chat builds a new one each turn).
func WithAgentSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

// AgentSessionFrom is the agent session on ctx, or "".
func AgentSessionFrom(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}

// NewSessionID is a random id for an agent session that has no transport id
// (one per process for stdio).
func NewSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UnixNano())
	}
	return "s-" + hex.EncodeToString(b)
}

// ParamStrings is a run's bound values as text, for an Intent and for
// HashParams: a string as it is, a number in plain decimal, anything else as Go
// prints it.
func ParamStrings(params map[string]any) map[string]string {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]string, len(params))
	for name, v := range params {
		switch x := v.(type) {
		case string:
			out[name] = x
		case float64:
			out[name] = strconv.FormatFloat(x, 'f', -1, 64)
		case nil:
			out[name] = "NULL"
		default:
			out[name] = fmt.Sprint(x)
		}
	}
	return out
}

// Kind is the statement kind the caller declared, or "".
func (r *Run) Kind() StmtKind {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.intent.Kind
}

// Facts is a copy of the facts recorded so far. It is the zero Facts on a nil Run.
func (r *Run) Facts() Facts {
	if r == nil {
		return Facts{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.facts
}
