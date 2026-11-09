package workspace

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/connname"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/semantics/assert"
)

// Running a definition: assertions, join profiling and a metric's "Try it".
// These are the person's actions (`slaw semantics check|profile`, the
// Semantics view); no agent tool or MCP method reaches them. Every statement
// they plan runs through the caller's ReadOnlyRunner, which is the agent
// policy's read-only path in the tightest tier (hub.NewAgentHandler with
// read-only, or Studio's read-only handler): the definitions may come from a
// cloned repository, so a hostile table name or metric body is refused there,
// not trusted here.

// ReadOnlyRunner is agents.Handler.RunReadOnlySQLIn (or Studio's wrapper of
// it). The workspace package cannot import agents, so each surface hands it in.
type ReadOnlyRunner interface {
	RunReadOnlySQLIn(ctx context.Context, connection, sql string, params map[string]any, maxRows int) (*engine.ResultSet, bool, error)
	// VetReadOnlySQLIn gives the same verdict without running the statement:
	// it binds against the real connection (EXPLAIN, no row is read). An error
	// reporting ConnectFailure() true is the connection, not the statement.
	VetReadOnlySQLIn(ctx context.Context, connection, sql string, params map[string]any) error
}

// Caps and limits of a run.
const (
	// CheckStatementTimeout bounds one assertion or profiling statement.
	CheckStatementTimeout = 2 * time.Minute
	// countRowCap is the rows a counting statement may return: one.
	countRowCap = 10
)

// ReadOnlyRunFunc adapts a ReadOnlyRunner to the QueryFunc assertions and
// profiling take, with CheckStatementTimeout per statement.
func ReadOnlyRunFunc(r ReadOnlyRunner) semantics.QueryFunc {
	return func(ctx context.Context, connection, sql string, params map[string]any) (*engine.ResultSet, error) {
		ctx, cancel := context.WithTimeout(ctx, CheckStatementTimeout)
		defer cancel()
		rs, _, err := r.RunReadOnlySQLIn(ctx, connection, sql, params, countRowCap)
		return rs, err
	}
}

// ReadOnlyVetFunc adapts a ReadOnlyRunner to the VetFunc compile checks take,
// with CheckStatementTimeout per statement.
func ReadOnlyVetFunc(r ReadOnlyRunner) assert.VetFunc {
	return func(ctx context.Context, connection, sql string, params map[string]any) error {
		ctx, cancel := context.WithTimeout(ctx, CheckStatementTimeout)
		defer cancel()
		err := r.VetReadOnlySQLIn(ctx, connection, sql, params)
		if err != nil && ctx.Err() != nil {
			// The policy flattens the database's error to text, so the deadline
			// is only visible here: a statement that ran out of time says
			// nothing about the definition.
			return fmt.Errorf("the compile check did not finish: %w", ctx.Err())
		}
		return err
	}
}

// AssertionRunner is what RunAssertions runs checks with: the runner's read
// path for counting checks and its vet for compile checks.
func AssertionRunner(r ReadOnlyRunner) assert.Runner {
	return assert.Runner{Run: assert.RunFunc(ReadOnlyRunFunc(r)), Vet: ReadOnlyVetFunc(r)}
}

// AssertionOptions narrow a run of RunAssertions.
type AssertionOptions struct {
	// CompileOnly runs only the compile checks: each definition is bound
	// against its connection and no rows are scanned.
	CompileOnly bool
}

// RunAssertions plans and runs the declared assertions of one entity or metric
// (kind "entity" or "metric" with a name), of every entity or metric (a kind
// with no name), or of everything (neither). A subject whose checks cannot be
// planned (an unreadable file, a missing saved query) is one error result and
// the others still run. Every subject starts with its compile check (it binds
// the definition against the connection and reads no data); when it fails the
// subject's assertions are not run.
func RunAssertions(ctx context.Context, ws Workspace, kind, name string, runner assert.Runner, now time.Time, opts AssertionOptions) ([]assert.Result, error) {
	plans, err := planAssertions(ctx, ws, kind, name, now)
	if err != nil {
		return nil, err
	}
	out := []assert.Result{}
	for _, p := range plans {
		if p.err != nil {
			out = append(out, assert.Failure(p.kind, p.name, p.err))
			continue
		}
		checks := p.checks
		if opts.CompileOnly {
			checks = checks[:1] // the compile check is always first
		}
		out = append(out, assert.Run(receipts.WithOrigin(ctx, p.origin()), checks, runner)...)
	}
	return out, nil
}

type subjectPlan struct {
	kind, name string
	checks     []assert.Check
	err        error
}

func planAssertions(ctx context.Context, ws Workspace, kind, name string, now time.Time) ([]subjectPlan, error) {
	if name != "" && kind == "" {
		return nil, fmt.Errorf("give the kind of %q: entity or metric", name)
	}
	if kind != "" && kind != assert.SubjectEntity && kind != assert.SubjectMetric {
		return nil, fmt.Errorf("unknown kind %q (want entity or metric)", kind)
	}
	st := ws.Semantics()
	var out []subjectPlan
	if kind == "" || kind == assert.SubjectEntity {
		list, err := entitiesNamed(ctx, st, name)
		if err != nil {
			return nil, err
		}
		for i := range list {
			p := subjectPlan{kind: assert.SubjectEntity, name: list[i].Name}
			p.checks, p.err = planEntityChecks(&list[i], now)
			out = append(out, p)
		}
	}
	if kind == "" || kind == assert.SubjectMetric {
		list, err := metricsNamed(ctx, st, name)
		if err != nil {
			return nil, err
		}
		src := semantics.NewReader(st, ws.Queries())
		for i := range list {
			p := subjectPlan{kind: assert.SubjectMetric, name: list[i].Name}
			p.checks, p.err = planMetricChecks(ctx, &list[i], src, now)
			out = append(out, p)
		}
	}
	return out, nil
}

// origin is where an assertion's read came from, for its receipt: the entity
// or metric file that declares the check.
func (p subjectPlan) origin() receipts.Origin {
	if p.kind == assert.SubjectMetric {
		return receipts.Origin{Surface: receipts.SurfaceAssertion, Source: receipts.Source{Kind: receipts.SourceMetric, Ref: "semantics/metrics/" + p.name + ".yaml"}}
	}
	return receipts.Origin{Surface: receipts.SurfaceAssertion, Source: receipts.Source{Ref: "semantics/entities/" + p.name + ".yaml"}}
}

// planEntityChecks is the compile check followed by the declared assertions.
func planEntityChecks(e *semantics.Entity, now time.Time) ([]assert.Check, error) {
	compile, err := assert.PlanEntityCompile(e)
	if err != nil {
		return nil, err
	}
	rest, err := assert.PlanEntity(e, now)
	if err != nil {
		return nil, err
	}
	return append([]assert.Check{compile}, rest...), nil
}

// planMetricChecks is the compile check followed by the declared assertions.
func planMetricChecks(ctx context.Context, m *semantics.Metric, src semantics.Reader, now time.Time) ([]assert.Check, error) {
	compile, err := assert.PlanMetricCompile(ctx, m, src, now)
	if err != nil {
		return nil, err
	}
	rest, err := assert.PlanMetric(ctx, m, src, now)
	if err != nil {
		return nil, err
	}
	return append([]assert.Check{compile}, rest...), nil
}

func entitiesNamed(ctx context.Context, st semantics.ReadStore, name string) ([]semantics.Entity, error) {
	list, err := st.Entities(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list entities: %w", err)
	}
	if name == "" {
		return list, nil
	}
	for _, e := range list {
		if e.Name == name {
			return []semantics.Entity{e}, nil
		}
	}
	return nil, fmt.Errorf("there is no entity named %q (`slaw semantics list` shows them)", name)
}

func metricsNamed(ctx context.Context, st semantics.ReadStore, name string) ([]semantics.Metric, error) {
	list, err := st.Metrics(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list metrics: %w", err)
	}
	if name == "" {
		return list, nil
	}
	for _, m := range list {
		if m.Name == name {
			return []semantics.Metric{m}, nil
		}
	}
	return nil, fmt.Errorf("there is no metric named %q (`slaw semantics list` shows them)", name)
}

// ProfileJoin profiles one join and records the measure in its file (see
// semantics.ProfileJoin): a file that changed while the profile ran is a
// semantics.ErrConflict and nothing is written.
func ProfileJoin(ctx context.Context, ws Workspace, name string, run semantics.QueryFunc, now time.Time) (*semantics.Profile, error) {
	// A profile measures a definition and answers nobody's question: no receipt.
	ctx = receipts.WithOrigin(ctx, receipts.Origin{Skip: true})
	return semantics.ProfileJoin(ctx, ws.Semantics(), name, run, now)
}

// MetricQuery is the statement behind a metric's "Try it": the metric broken
// down by dims (a subset of its dimensions; none is the scalar), ordered by
// them, and the connection it runs on (its saved query's). The caller runs it
// through the read-only policy, which expands the call like any other.
func MetricQuery(ctx context.Context, ws Workspace, name string, dims []string) (sql, connection string, err error) {
	if !connname.Valid(name) {
		return "", "", fmt.Errorf("there is no metric named %q", name)
	}
	for _, d := range dims {
		if !connname.Valid(d) {
			return "", "", fmt.Errorf("%q is not a dimension name", d)
		}
	}
	m, err := ws.Semantics().Metric(ctx, name)
	if err != nil {
		return "", "", fmt.Errorf("failed to read metric %q: %w", name, err)
	}
	q, err := ws.Queries().Get(ctx, m.Query)
	if err != nil {
		return "", "", fmt.Errorf("metric %q: its saved query %q cannot be read: %w", name, m.Query, err)
	}
	sql = "SELECT * FROM " + semantics.MetricCall(name, dims)
	if len(dims) > 0 {
		order := make([]string, len(dims))
		for i := range dims {
			order[i] = strconv.Itoa(i + 1)
		}
		sql += " ORDER BY " + strings.Join(order, ", ")
	}
	return sql, q.Connection, nil
}
