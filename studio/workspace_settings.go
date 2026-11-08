package main

// The Settings > Workspace form.

import (
	"errors"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

// WorkspaceSettings is the Settings > Workspace form; the rules (validation, "can
// only tighten", the stale-form check) live in workspace.ApplySettings.
type WorkspaceSettings = workspace.Settings

// settingsErr puts a settings conflict in the bridge's wire format; any other
// error passes through untouched.
func settingsErr(err error) error {
	var sc *workspace.SettingsConflictError
	if errors.As(err, &sc) {
		return newConflict(sc.Msg)
	}
	return err
}

// SetWorkspaceSettings saves the Settings > Workspace form: the name and default
// connection and the team limit go into slaw.yaml (shared through git), and this
// person's own agent choice goes into the per-person file outside the folder.
// slaw.yaml can only tighten: a person's choice above the team limit is refused.
func (a *App) SetWorkspaceSettings(s WorkspaceSettings) (*WorkspaceInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireWorkspace(); err != nil {
		return nil, err
	}
	if err := workspace.ApplySettings(a.ws, s); err != nil {
		return nil, settingsErr(err)
	}
	a.recents.Record(a.ws.Root(), a.ws.ID(), a.ws.Name())
	policy := a.agentPolicy()
	a.emitEvent("agent:config", map[string]any{"sandbox": string(policy.Mode), "askBeforeWrites": policy.AskBeforeWrites})
	a.wmcp.NotifyToolsChanged() // an MCP client lists again: the tier shapes tools/list
	a.emitEvent("workspace:changed", map[string]any{"kind": "settings"})
	return a.infoLocked(), nil
}
