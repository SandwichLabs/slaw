package hub

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/artifacts"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// ResolveSandbox is the workspace's effective sandbox tightened, never
// loosened, by an extra request (the --sandbox flag). An empty request keeps
// base; an unrecognised one is an error.
func ResolveSandbox(base agents.SandboxMode, request string) (agents.SandboxMode, error) {
	if request == "" {
		return base, nil
	}
	mode, err := agents.ParseSandboxMode(request)
	if err != nil {
		return "", fmt.Errorf("invalid --sandbox %q: want read-only, read-write, or full", request)
	}
	return agents.Tighter(base, mode), nil
}

// NewAgentHandler builds the agent tool handler for an open workspace: its
// file-backed stores and hub (workspace.AgentAccess) under the workspace's
// effective policy (Workspace.AgentPolicy: the committed ceiling and this
// person's setting), further tightened by tighten ("" = no change).
//
// No approver is attached. A write that needs approval is refused until the
// owner (Studio) calls Handler.SetApprover; a headless process has nobody to
// ask. The returned policy is what the handler was built with.
func NewAgentHandler(ws workspace.Workspace, sess session.Session, tighten string, logger *slog.Logger) (*agents.Handler, agents.FilePolicy, error) {
	if logger == nil {
		logger = slog.Default()
	}
	base := agents.PolicyFrom(ws)
	mode, err := ResolveSandbox(base.Mode, tighten)
	if err != nil {
		return nil, agents.FilePolicy{}, err
	}
	policy := agents.FilePolicy{Mode: mode, AskBeforeWrites: base.AskBeforeWrites}
	cfg := &agents.SandboxConfig{Mode: policy.Mode, MaxQueryRows: 10000, RequireApproval: policy.AskBeforeWrites}
	eng := engine.NewManager(sess, engine.WithLogger(logger))
	h := agents.NewHandler(workspace.NewAgentAccess(ws, sess), eng, cfg, logger)
	// Every read this handler runs keeps a receipt (nil when the store cannot be
	// opened: the run goes on without one).
	h.SetReceipts(ws.Receipts())
	return h, policy, nil
}

// MetricVetter is the read-only policy check a person's surface runs on the
// SQL a metric() call expands to (workspace.BodyVetter): the agent policy's
// own verdict, in the tightest tier, so a metric from a cloned repository is
// held to what a cloned artifact query is. It executes nothing.
func MetricVetter(ws workspace.Workspace, sess session.Session, logger *slog.Logger) (workspace.BodyVetter, error) {
	h, _, err := NewAgentHandler(ws, sess, string(agents.ModeReadOnly), logger)
	if err != nil {
		return nil, err
	}
	return h.VetReadOnlySQLIn, nil
}

// ReadOnlyRunner is the agent policy's read-only runner in the tightest tier,
// for the person's runs of what a definition declares (assertions, join
// profiling, a metric's "Try it"): every statement goes through the same
// gates as a cloned artifact query, whatever the sandbox setting.
func ReadOnlyRunner(ws workspace.Workspace, sess session.Session, logger *slog.Logger) (workspace.ReadOnlyRunner, error) {
	h, _, err := NewAgentHandler(ws, sess, string(agents.ModeReadOnly), logger)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// ArtifactPageRunner answers a static page's queries for a person's surface
// with no window (`slaw artifact render`), the way Studio's page host does: the
// queries come from m (the manifest the page was hashed with, not artifact.yaml
// as it is by the time a lazy query runs) and every one goes through the agent
// policy's read-only path in the tightest tier (vetted SQL at every run, Bind so
// a value is never SQL text, the read-only gate, the row cap). pins are the
// metric and saved-query revisions the person's allow covers: the only ones a
// run may read.
func ArtifactPageRunner(ws workspace.Workspace, sess session.Session, logger *slog.Logger, name string, m *artifacts.Manifest, pins map[string]string) (artifacts.PageRunner, error) {
	h, _, err := NewAgentHandler(ws, sess, string(agents.ModeReadOnly), logger)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, query string, values map[string]any) (*artifacts.PageRows, error) {
		rs, truncated, err := h.RunManifestQuery(semantics.WithPins(ctx, pins), name, m, query, values)
		if err != nil {
			return nil, err
		}
		cols, _ := rs.NamesAndTypes()
		return &artifacts.PageRows{Columns: cols, Rows: rs.Rows, Truncated: truncated}, nil
	}, nil
}
