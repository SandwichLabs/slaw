package main

// What the shell shows about the open workspace: info, connections, counts and
// the catalog groups on Home.

import (
	"sort"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// ConnectionInfo is one connection as the workspace screens show it.
type ConnectionInfo struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Scope       string   `json:"scope"` // shared | local | user (the UI says Team / This computer / All my workspaces)
	Shadows     []string `json:"shadows,omitempty"`
	ReadOnly    bool     `json:"readOnly"`
	Default     bool     `json:"default"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail,omitempty"`
	LockedByPID int      `json:"lockedByPid,omitempty"`
	// Warning is set on a shared lake or table definition (workspace.MetadataWarning):
	// the rail shows it so a lake that arrived with a pull is seen before it is opened.
	Warning string `json:"warning,omitempty"`
	// Remote connections dial on first use (Postgres, MotherDuck, storage);
	// Catalogless ones (s3, gcs, r2) have no tables, only a scoped secret.
	Remote      bool `json:"remote"`
	Catalogless bool `json:"catalogless,omitempty"`
	// SignIn is this person's sign-in method (keychain, prompt or env:PREFIX).
	SignIn string `json:"signIn,omitempty"`
	// Extension names the DuckDB extension an extension-missing connection
	// needs, for the install prompt.
	Extension string `json:"extension,omitempty"`
	// ServerChanged is set when a needs-credentials status is because the
	// definition now points at a different server than the saved sign-in.
	ServerChanged *ServerChange `json:"serverChanged,omitempty"`
	// SignInNeeded is set when a needs-credentials status is because the
	// person must sign in with an AWS profile (kind, profile, missing|expired):
	// the banner and the rail's Sign in button read it. No account or role.
	SignInNeeded *secrets.SignInHint `json:"signInNeeded,omitempty"`
	// BlockedBy names the connection this one waits on when that one is not
	// ready (an s3_tables catalog and its storage connection): sign in there.
	BlockedBy string `json:"blockedBy,omitempty"`
}

// WorkspaceCounts are the live counts shown on Home.
type WorkspaceCounts struct {
	Notebooks int `json:"notebooks"`
	Queries   int `json:"queries"`
	Skills    int `json:"skills"`
	Tables    int `json:"tables"`
}

// AgentPolicyInfo is a sandbox tier plus the ask-before-changes gate.
type AgentPolicyInfo struct {
	Sandbox         string `json:"sandbox"`
	AskBeforeWrites bool   `json:"askBeforeWrites"`
}

// WorkspaceInfo is everything the shell needs to describe the open workspace.
type WorkspaceInfo struct {
	Root      string `json:"root"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	SessionID string `json:"sessionId"`
	// GitBranch is read from .git/HEAD (no git process); empty when the
	// folder is not a repository.
	GitBranch         string           `json:"gitBranch"`
	IsGit             bool             `json:"isGit"`
	DefaultConnection string           `json:"defaultConnection"`
	Connections       []ConnectionInfo `json:"connections"`
	Counts            WorkspaceCounts  `json:"counts"`
	// Policy is in effect for this person; Team is the committed limit alone;
	// Local is this person's own choice.
	Policy AgentPolicyInfo `json:"policy"`
	Team   AgentPolicyInfo `json:"team"`
	Local  AgentPolicyInfo `json:"local"`
	// TeamSandboxSet is false when slaw.yaml sets no tier limit.
	TeamSandboxSet bool `json:"teamSandboxSet"`
	// ReadOnly is true when another process owns the hub.
	ReadOnly bool                     `json:"readOnly"`
	Holder   *HolderInfo              `json:"holder,omitempty"`
	Issues   []workspace.SessionIssue `json:"issues"`
	Warnings []string                 `json:"warnings"`
	// DuplicateIDs lists other known folders that carry this workspace's id (a
	// copied folder): offer "Regenerate id".
	DuplicateIDs []string `json:"duplicateIds"`
	// LocalUntrusted is true when the saved per-person settings were made for a
	// different folder at this path: they are ignored until confirmed.
	LocalUntrusted bool `json:"localUntrusted"`
	MCPRunning     bool `json:"mcpRunning"`
}

// CatalogTable is one table of one connection.
type CatalogTable struct {
	Name      string `json:"name"`
	Qualified string `json:"qualified"`
	Kind      string `json:"kind"` // table | view
}

// CatalogGroup is one connection and its tables, for the Tables view.
type CatalogGroup struct {
	Connection string         `json:"connection"`
	Type       string         `json:"type"`
	Status     string         `json:"status"`
	Detail     string         `json:"detail,omitempty"`
	ReadOnly   bool           `json:"readOnly"`
	Default    bool           `json:"default"`
	Tables     []CatalogTable `json:"tables"`
}

// CheckGitAvailable reports whether git is on PATH.
func (a *App) CheckGitAvailable() bool { return workspace.GitAvailable() }

// GetWorkspaceInfo describes the open workspace.
func (a *App) GetWorkspaceInfo() (*WorkspaceInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	return a.infoLocked(), nil
}

// GetSessionID returns the open workspace's session token (see App.sessionID).
func (a *App) GetSessionID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sessionID
}

// IsWorkspaceOpen reports whether a workspace is open.
func (a *App) IsWorkspaceOpen() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ws != nil
}

// infoLocked builds WorkspaceInfo; the caller holds a.mu and has checked a.ws.
func (a *App) infoLocked() *WorkspaceInfo {
	ws := a.ws
	ctx := a.ctxOrBackground()
	cfg := ws.Config()
	local := ws.Local()
	pol := agents.PolicyFrom(ws)
	branch, isGit := workspace.GitInfo(ws.Root())

	info := &WorkspaceInfo{
		Root: ws.Root(), ID: ws.ID(), Name: ws.Name(), SessionID: a.sessionID,
		GitBranch: branch, IsGit: isGit,
		DefaultConnection: ws.DefaultConnection(),
		Connections:       a.connectionInfosLocked(),
		Policy:            AgentPolicyInfo{Sandbox: string(pol.Mode), AskBeforeWrites: pol.AskBeforeWrites},
		Team: AgentPolicyInfo{
			Sandbox:         orDefault(cfg.Sandbox, workspace.SandboxFull),
			AskBeforeWrites: cfg.AskBeforeWrites != nil && *cfg.AskBeforeWrites,
		},
		TeamSandboxSet: cfg.Sandbox != "",
		Local: AgentPolicyInfo{
			Sandbox:         orDefault(local.Agent.Sandbox, workspace.DefaultSandbox),
			AskBeforeWrites: local.Agent.AskBeforeWrites == nil || *local.Agent.AskBeforeWrites,
		},
		ReadOnly:       a.readOnly,
		Holder:         holderInfo(a.holder),
		Issues:         append([]workspace.SessionIssue{}, a.issues...),
		Warnings:       append([]string{}, ws.Warnings()...),
		DuplicateIDs:   append([]string{}, a.duplicateOf...),
		LocalUntrusted: !ws.LocalTrusted(),
		MCPRunning:     a.wmcp != nil,
	}
	if a.readOnly {
		info.Policy.Sandbox = string(agents.ModeReadOnly)
	}
	if nbs, err := ws.Notebooks().List(ctx); err == nil {
		info.Counts.Notebooks = len(nbs)
	}
	if qs, err := ws.Queries().List(ctx); err == nil {
		info.Counts.Queries = len(qs)
	}
	if sk, err := ws.Skills().List(ctx); err == nil {
		info.Counts.Skills = len(sk)
	}
	if tabs, err := a.sess.ListTables(ctx, "", ""); err == nil {
		info.Counts.Tables = len(tabs)
	}
	return info
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (a *App) connectionInfosLocked() []ConnectionInfo {
	states := workspace.ConnectionStates(a.ws, a.sess, a.issues)
	out := make([]ConnectionInfo, 0, len(states))
	for _, st := range states {
		ci := ConnectionInfo{
			Name: st.Name, Type: st.Type, Scope: string(st.Scope), ReadOnly: st.EffectiveReadOnly,
			Default: st.Default, Status: string(st.Status), Detail: st.Detail, LockedByPID: st.LockedByPID, Warning: st.Warning,
			SignIn: st.SignIn, SignInNeeded: st.SignInHint, BlockedBy: st.BlockedBy, Remote: st.Remote, Catalogless: st.Catalogless, Extension: st.Extension,
		}
		for _, sc := range st.Shadows {
			ci.Shadows = append(ci.Shadows, string(sc))
		}
		if st.ServerChanged != nil {
			ci.ServerChanged = &ServerChange{Old: st.ServerChanged.Old, New: st.ServerChanged.New}
		}
		out = append(out, ci)
	}
	return out
}

// ListCatalogGroups lists every connection that is attached, with its tables,
// for the Tables view and Home. A connection that is not connected yet is
// listed with no tables and is never dialled by this call.
func (a *App) ListCatalogGroups() ([]CatalogGroup, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	ctx := a.ctxOrBackground()
	groups := []CatalogGroup{}
	for _, c := range a.connectionInfosLocked() {
		g := CatalogGroup{
			Connection: c.Name, Type: c.Type, Status: c.Status, Detail: c.Detail,
			ReadOnly: c.ReadOnly, Default: c.Default, Tables: []CatalogTable{},
		}
		// A storage connection (s3, gcs, r2, azure) has no catalog to list.
		if c.Status == string(session.StatusOK) && !c.Catalogless {
			tabs, err := a.sess.ListTables(ctx, c.Name, "")
			if err != nil {
				g.Status, g.Detail = string(session.StatusError), a.sess.Redact(err.Error())
			}
			for _, t := range tabs {
				g.Tables = append(g.Tables, CatalogTable{Name: t.Name, Qualified: t.Ref().String(), Kind: engine.RelationKind(t.Type)})
			}
		}
		groups = append(groups, g)
	}
	// The default connection first, as the sidebar and Home read it.
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Default && !groups[j].Default })
	return groups, nil
}
