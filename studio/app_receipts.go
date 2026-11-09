package main

import (
	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// --- Evidence (the drawer, and `slaw receipts show|list`) ---

// GetReceipt returns one receipt by id: what was asked and what it touched,
// never a result value. An id with no receipt is an error.
func (a *App) GetReceipt(id string) (Receipt, error) {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return Receipt{}, err
	}
	return getReceipt(ws, id)
}

// ListReceipts returns the newest receipts, one line each, newest first.
func (a *App) ListReceipts(filter ReceiptFilter) ([]receipts.Summary, error) {
	ws, _, _, err := a.snapshot()
	if err != nil {
		return nil, err
	}
	return listReceipts(ws, filter)
}

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
