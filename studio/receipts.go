package main

// The logic behind the receipt settings bindings (app_receipts.go): the form's
// shape, the usage line and the clear. The rules (clamping, the per-person
// file, the live policy) are workspace.SetLocalReceipts and the receipts store.

import (
	"errors"
	"time"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

// ReceiptSettings is the Settings > Workspace > Receipts form.
type ReceiptSettings = workspace.ReceiptSettings

// ReceiptUsage is what the receipts folder holds now. Oldest is RFC 3339, or ""
// when there is no receipt.
type ReceiptUsage struct {
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
	Oldest string `json:"oldest"`
}

// ReceiptSettingsInfo is everything the section shows: what the person saved,
// the values in force (clamped, defaults filled in), usage, and the bounds. When
// Available is false the receipts folder cannot be used (a symbolic link, say)
// and nothing is being recorded.
type ReceiptSettingsInfo struct {
	Settings  ReceiptSettings `json:"settings"`
	Effective ReceiptSettings `json:"effective"`
	Defaults  ReceiptSettings `json:"defaults"`
	Floors    ReceiptSettings `json:"floors"`
	Ceilings  ReceiptSettings `json:"ceilings"`
	Usage     ReceiptUsage    `json:"usage"`
	Available bool            `json:"available"`
}

// receiptSettingsInfo reads the saved settings, the recorder's policy and usage.
func receiptSettingsInfo(ws workspace.Workspace) ReceiptSettingsInfo {
	info := ReceiptSettingsInfo{
		Settings:  workspace.StoredReceiptSettings(ws.Local()),
		Effective: workspace.SettingsOf(workspace.ReceiptPolicy(ws.Local())),
		Defaults:  workspace.ReceiptDefaults(),
		Floors:    workspace.ReceiptFloors(),
		Ceilings:  workspace.ReceiptCeilings(),
	}
	rec := ws.Receipts()
	if rec == nil {
		return info
	}
	info.Available = true
	n, size, oldest := rec.Store().Usage()
	info.Usage = ReceiptUsage{Count: n, Bytes: size}
	if !oldest.IsZero() {
		info.Usage.Oldest = oldest.UTC().Format(time.RFC3339)
	}
	return info
}

// clearReceipts deletes every receipt of the workspace.
func clearReceipts(ws workspace.Workspace) error {
	rec := ws.Receipts()
	if rec == nil {
		return errors.New("receipts are not being kept for this workspace")
	}
	return rec.Store().Clear()
}
