package main

import "github.com/SandwichLabs/slaw/internal/workspace"

// --- Receipts settings (Settings > Workspace > Receipts) ---

// GetReceiptSettings returns this person's receipt settings, the values in
// force, what is kept now and the bounds of each limit.
func (a *App) GetReceiptSettings() (ReceiptSettingsInfo, error) {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return ReceiptSettingsInfo{}, err
	}
	return receiptSettingsInfo(ws), nil
}

// SetReceiptSettings saves this person's receipt settings (kept outside the
// folder, never in slaw.yaml or a bundle). Limits are clamped; a lowered one
// removes the oldest receipts in the background.
func (a *App) SetReceiptSettings(s ReceiptSettings) error {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return err
	}
	return workspace.SetLocalReceipts(ws, s)
}

// ClearReceipts deletes every receipt of the open workspace. The key stays.
func (a *App) ClearReceipts() error {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return err
	}
	return clearReceipts(ws)
}
