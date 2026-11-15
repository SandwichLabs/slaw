package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// This file holds the one function that opens a workspace for a process:
// workspace.Open, then the hub lock, then the DuckDB hub (workspace.OpenSession),
// then the query engine. The CLI, Studio and `slaw mcp serve` all call Open;
// they differ only in OpenOptions and in how they present what comes back.
//
// It lives in hub (level 7) rather than workspace (level 5) because the lock
// is hub's (AcquireHub, TakeOver) and workspace must not import hub.

// Mode says how a process relates to the workspace's hub lock.
type Mode int

const (
	// ModeReadWrite takes the hub lock. When another process holds it Open
	// fails at StepHub with a *HeldError, unless FallbackReadOnly is set.
	ModeReadWrite Mode = iota
	// ModeReadOnly takes no lock and attaches every connection read-only.
	ModeReadOnly
	// ModeTakeOver asks the current owner to let go (TakeOver), then takes the lock.
	ModeTakeOver
	// ModeRemoteOnly takes no lock and does not attach the workspace's local
	// files: nothing this process does writes one (`slaw conn connect`).
	ModeRemoteOnly
)

// Step names the stage of Open that failed, so an adapter can word the error
// for its own surface without parsing text.
type Step string

const (
	StepOpen    Step = "open"    // workspace.Open
	StepRepair  Step = "repair"  // EnsureID for a slaw.yaml without an id
	StepHub     Step = "hub"     // the hub lock
	StepSession Step = "session" // workspace.OpenSession
)

// OpenError is the error of Open: which step failed and why. It unwraps to the
// step's own error, so errors.Is(err, ErrHubHeld) and errors.As(*HeldError) work.
type OpenError struct {
	Step Step
	Err  error
}

func (e *OpenError) Error() string { return e.Err.Error() }
func (e *OpenError) Unwrap() error { return e.Err }

// OpenOptions configures Open. Every field exists because a caller needs it.
type OpenOptions struct {
	// App names the holder in hub.lock ("Slaw", "slaw query").
	App string
	// Mode is how the hub lock is used; the zero value is ModeReadWrite.
	Mode Mode
	// FallbackReadOnly (ModeReadWrite) turns "another process holds the hub"
	// into a read-only open with Opened.Holder naming the owner (Studio's
	// banner) instead of an error.
	FallbackReadOnly bool
	// PolicyGatedRuns (with a read-only open) is workspace.WithPolicyGatedRuns:
	// for a process whose every statement goes through the agent policy's
	// read-only runner (ReadOnlyRunner), so a connection that cannot be
	// attached read-only (MotherDuck) is still reachable without the hub lock.
	PolicyGatedRuns bool
	// RepairMissingID adds an id to a slaw.yaml that has none and retries.
	RepairMissingID bool
	// TakeOverTimeout is how long ModeTakeOver waits for the owner (default 10s).
	TakeOverTimeout time.Duration
	// HubOptions are passed to AcquireHub/TakeOver (WithReleaseHandler, WithPendingMCP).
	HubOptions []Option

	Logger   *slog.Logger
	Resolver *secrets.Resolver // nil: the workspace default
	Redactor *secrets.Redactor // nil: none

	// OnWorkspaceOpened runs once the workspace parsed, before the hub step.
	// Studio uses it to let go of the workspace it has open (its lock, DuckDB
	// files and in-memory credentials are not shared with the next one), which
	// happens after the new one validated and before the lock is taken. When a
	// later step fails Studio reopens the old workspace itself (#64).
	OnWorkspaceOpened func(ws workspace.Workspace)
	// SessionOptions are passed to the DuckDB session (session.New): Studio's
	// WithStatusListener.
	SessionOptions []session.Option
	// BeforeSession runs after the lock is held, before the session opens.
	// The CLI prints the workspace's warnings and asks about its .env here:
	// the answer decides which credentials the session can resolve.
	BeforeSession func(ws workspace.Workspace)
}

// Opened is a workspace opened by Open. Close releases it.
type Opened struct {
	WS   workspace.Workspace
	Sess session.Session
	Eng  *engine.Manager
	// Hold is the hub lock; nil when this process does not own the hub.
	Hold *Hold
	// Holder is who owns the hub when it is not this process (read-only
	// modes), when known.
	Holder   *Holder
	Issues   []workspace.SessionIssue
	ReadOnly bool

	log *slog.Logger
}

// Close releases everything in the reverse order it was acquired: the session
// (the DuckDB handles), then the hub lock (the next owner may open the files
// the moment it is free), then the workspace. Safe on nil and to call twice.
func (o *Opened) Close() {
	if o == nil {
		return
	}
	if o.Sess != nil {
		o.warn("close session", o.Sess.Close())
		o.Sess = nil
	}
	if o.Hold != nil {
		o.warn("release the hub lock", o.Hold.Release())
		o.Hold = nil
	}
	if o.WS != nil {
		o.warn("close workspace", o.WS.Close())
		o.WS = nil
	}
}

// warn logs an error from tearing down; there is nothing more a caller can do.
func (o *Opened) warn(what string, err error) {
	if err != nil && o.log != nil {
		o.log.Warn("failed to "+what, "error", err)
	}
}

// steps are the four stages of Open as replaceable functions, so a test can
// fail each one.
type steps struct {
	openWorkspace func(root string, opts ...workspace.Option) (workspace.Workspace, error)
	acquire       func(ctx context.Context, ws workspace.Workspace, o OpenOptions) (*Hold, error)
	openSession   func(ctx context.Context, ws workspace.Workspace, logger *slog.Logger, opts ...workspace.SessionOption) (session.Session, []workspace.SessionIssue, error)
	newEngine     func(sess session.Session, logger *slog.Logger) (*engine.Manager, error)
}

func defaultSteps() steps {
	return steps{
		openWorkspace: workspace.Open,
		acquire: func(ctx context.Context, ws workspace.Workspace, o OpenOptions) (*Hold, error) {
			if o.Mode == ModeTakeOver {
				return TakeOver(ctx, ws, o.App, o.TakeOverTimeout, o.HubOptions...)
			}
			return AcquireHub(ws, o.App, o.HubOptions...)
		},
		openSession: workspace.OpenSession,
		newEngine: func(sess session.Session, logger *slog.Logger) (*engine.Manager, error) {
			return engine.NewManager(sess, engine.WithLogger(logger)), nil
		},
	}
}

// Open opens the workspace at root (a folder with a slaw.yaml; use ResolveRoot
// or workspace.Discover to find it). On any failure everything already acquired
// is released and the error is an *OpenError.
func Open(ctx context.Context, root string, o OpenOptions) (*Opened, error) {
	return openWith(ctx, root, o, defaultSteps())
}

func openWith(ctx context.Context, root string, o OpenOptions, st steps) (opened *Opened, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := &Opened{log: o.Logger}
	defer func() {
		if err != nil {
			out.Close()
			opened = nil
		}
	}()

	var wopts []workspace.Option
	if o.Logger != nil {
		wopts = append(wopts, workspace.WithLogger(o.Logger))
	}
	if o.Resolver != nil {
		wopts = append(wopts, workspace.WithResolver(o.Resolver))
	}
	ws, err := st.openWorkspace(root, wopts...)
	if errors.Is(err, workspace.ErrMissingID) && o.RepairMissingID {
		// A hand-written slaw.yaml, or a template copied without an id.
		if _, ierr := workspace.EnsureID(root); ierr != nil {
			return nil, &OpenError{StepRepair, fmt.Errorf("failed to give this workspace an id: %w", ierr)}
		}
		ws, err = st.openWorkspace(root, wopts...)
	}
	if err != nil {
		return nil, &OpenError{StepOpen, err}
	}
	out.WS = ws
	if o.OnWorkspaceOpened != nil {
		o.OnWorkspaceOpened(ws)
	}

	sopts := []workspace.SessionOption{}
	if o.Redactor != nil {
		sopts = append(sopts, workspace.WithSessionOptions(session.WithRedactor(o.Redactor)))
	}
	if len(o.SessionOptions) > 0 {
		sopts = append(sopts, workspace.WithSessionOptions(o.SessionOptions...))
	}
	switch o.Mode {
	case ModeReadOnly:
		out.ReadOnly = true
		out.Holder, _ = ReadHolder(ws.Root())
	case ModeRemoteOnly:
		sopts = append(sopts, workspace.WithSkipLocalFiles())
	default: // ModeReadWrite, ModeTakeOver
		hold, herr := st.acquire(ctx, ws, o)
		var held *HeldError
		switch {
		case herr == nil:
			out.Hold = hold
		case o.Mode == ModeReadWrite && o.FallbackReadOnly && errors.As(herr, &held):
			out.ReadOnly, out.Holder = true, held.Holder
		default:
			return nil, &OpenError{StepHub, herr}
		}
	}
	if out.ReadOnly {
		sopts = append(sopts, workspace.WithForceReadOnly())
		if o.PolicyGatedRuns {
			sopts = append(sopts, workspace.WithPolicyGatedRuns())
		}
	}
	if o.BeforeSession != nil {
		o.BeforeSession(ws)
	}

	sess, issues, err := st.openSession(ctx, ws, o.Logger, sopts...)
	if err != nil {
		return nil, &OpenError{StepSession, err}
	}
	out.Sess, out.Issues = sess, issues
	eng, err := st.newEngine(sess, o.Logger)
	if err != nil {
		return nil, &OpenError{StepSession, fmt.Errorf("failed to start the query engine: %w", err)}
	}
	out.Eng = eng
	return out, nil
}
