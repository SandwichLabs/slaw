package main

// The logic behind the receipt settings bindings (app_receipts.go): the form's
// shape, the usage line and the clear. The rules (clamping, the per-person
// file, the live policy) are workspace.SetLocalReceipts and the receipts store.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/receipts"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// Receipt is one receipt as GetReceipt returns it: the record itself, which
// holds no result value.
type Receipt = receipts.Receipt

// ReceiptFilter narrows ListReceipts. An empty Surface is every surface; a zero
// Limit is the default page.
type ReceiptFilter struct {
	Surface string `json:"surface"`
	Limit   int    `json:"limit"`
}

// ReceiptListing is ListReceipts's answer: the newest receipts, and whether the
// scan stopped early (Capped), so an empty or short list means "none among the
// newest", not "none".
type ReceiptListing struct {
	Receipts []receipts.Summary `json:"receipts"`
	Capped   bool               `json:"capped"`
}

// receiptStore is the open workspace's receipts, or an error saying there are
// none to read.
func receiptStore(ws workspace.Workspace) (receipts.Store, error) {
	rec := ws.Receipts()
	if rec == nil {
		return nil, errors.New("receipts are not being kept for this workspace")
	}
	return rec.Store(), nil
}

// getReceipt reads one receipt by id.
func getReceipt(ws workspace.Workspace, id string) (Receipt, error) {
	st, err := receiptStore(ws)
	if err != nil {
		return Receipt{}, err
	}
	r, err := st.Get(strings.TrimSpace(id))
	if err != nil {
		return Receipt{}, fmt.Errorf("failed to open the receipt: %w", err)
	}
	return r, nil
}

// listReceipts is the newest receipts, a line each.
func listReceipts(ws workspace.Workspace, f ReceiptFilter) (ReceiptListing, error) {
	st, err := receiptStore(ws)
	if err != nil {
		return ReceiptListing{}, err
	}
	surface := receipts.Surface(f.Surface)
	if surface != "" && !surface.Valid() {
		return ReceiptListing{}, fmt.Errorf("unknown surface %q", f.Surface)
	}
	out, capped, err := st.List(receipts.ListFilter{Surface: surface, Limit: f.Limit})
	if err != nil {
		return ReceiptListing{}, fmt.Errorf("failed to list receipts: %w", err)
	}
	return ReceiptListing{Receipts: out, Capped: capped}, nil
}

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
		Settings:  workspace.ReceiptSettingsNow(ws),
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
	info.Effective = workspace.SettingsOf(rec.Policy()) // asks the settings file first
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
