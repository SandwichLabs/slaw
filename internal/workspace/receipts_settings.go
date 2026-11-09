package workspace

import (
	"errors"
	"fmt"
	"time"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

// LocalReceipts is one person's receipt settings, kept with the rest of their
// per-workspace settings outside the folder (never in slaw.yaml, never in a
// bundle). A zero limit is unset and means the default; a nil KeepParams means
// on.
type LocalReceipts struct {
	KeepParams *bool `yaml:"keep_params,omitempty"`
	Days       int   `yaml:"days,omitempty"`
	MaxCount   int   `yaml:"max_count,omitempty"`
	MaxMB      int   `yaml:"max_mb,omitempty"`
}

// ReceiptSettings is the Settings > Workspace > Receipts form (and the shape
// `slaw receipts settings` prints). A zero limit means the default.
type ReceiptSettings struct {
	KeepParams bool `json:"keepParams"`
	Days       int  `json:"days"`
	MaxCount   int  `json:"maxCount"`
	MaxMB      int  `json:"maxMB"`
}

const (
	oneDay = 24 * time.Hour
	oneMB  = int64(1 << 20)
	// Anything above these is far over every ceiling; the caps keep a
	// hand-edited number from overflowing the conversion to a duration or bytes.
	sanityDays = 100000
	sanityMB   = 1 << 20
)

// Policy is the settings as a receipts.Policy (not yet clamped).
func (s ReceiptSettings) Policy() receipts.Policy {
	return receipts.Policy{
		KeepParams: s.KeepParams,
		MaxAge:     time.Duration(min(max(s.Days, 0), sanityDays)) * oneDay,
		MaxCount:   s.MaxCount,
		MaxBytes:   int64(min(max(s.MaxMB, 0), sanityMB)) * oneMB,
	}
}

// SettingsOf is a policy as the form's numbers (whole days, megabytes rounded up).
func SettingsOf(p receipts.Policy) ReceiptSettings {
	return ReceiptSettings{
		KeepParams: p.KeepParams,
		Days:       int(p.MaxAge / oneDay),
		MaxCount:   p.MaxCount,
		MaxMB:      int((p.MaxBytes + oneMB - 1) / oneMB),
	}
}

// ReceiptDefaults is the policy of a person who never changed a setting, in the
// form's units.
func ReceiptDefaults() ReceiptSettings { return SettingsOf(receipts.DefaultPolicy()) }

// ReceiptFloors is the lowest each limit may go, in the form's units.
func ReceiptFloors() ReceiptSettings {
	return ReceiptSettings{KeepParams: true, Days: int(receipts.FloorMaxAge / oneDay), MaxCount: receipts.FloorMaxCount, MaxMB: int(receipts.FloorMaxBytes / oneMB)}
}

// ReceiptCeilings is the highest each limit may go, in the form's units.
func ReceiptCeilings() ReceiptSettings {
	return ReceiptSettings{KeepParams: true, Days: int(receipts.CeilingMaxAge / oneDay), MaxCount: receipts.CeilingMaxCount, MaxMB: int(receipts.CeilingMaxBytes / oneMB)}
}

// StoredReceiptSettings is what this person saved, as the form's numbers (a
// limit never set is 0). A missing, untrusted or unreadable local file is no
// settings: Local() is empty then, which reads as the defaults.
func StoredReceiptSettings(l LocalConfig) ReceiptSettings {
	r := l.Receipts
	return ReceiptSettings{KeepParams: r.KeepParams == nil || *r.KeepParams, Days: r.Days, MaxCount: r.MaxCount, MaxMB: r.MaxMB}
}

// ReceiptPolicy is the clamped policy of this person's saved settings.
func ReceiptPolicy(l LocalConfig) receipts.Policy {
	return StoredReceiptSettings(l).Policy().Clamp()
}

// SetLocalReceipts saves this person's receipt settings outside the folder and
// applies them to the open recorder at once: new receipts follow them and a
// lowered limit prunes the store in the background. It does not go through
// ApplySettings (nothing here is slaw.yaml). Every limit is clamped to its floor
// and ceiling on the way in (a zero stays unset), so what is stored is what
// takes effect.
func SetLocalReceipts(ws Workspace, s ReceiptSettings) error {
	if s.Days < 0 || s.MaxCount < 0 || s.MaxMB < 0 {
		return errors.New("a receipt limit can't be negative")
	}
	eff := SettingsOf(s.Policy().Clamp())
	r := LocalReceipts{}
	if !s.KeepParams {
		r.KeepParams = Bool(false)
	}
	if s.Days != 0 {
		r.Days = eff.Days
	}
	if s.MaxCount != 0 {
		r.MaxCount = eff.MaxCount
	}
	if s.MaxMB != 0 {
		r.MaxMB = eff.MaxMB
	}
	local := ws.Local()
	local.Receipts = r
	if err := ws.SaveLocal(local); err != nil {
		if errors.Is(err, ErrLocalUntrusted) {
			return errors.New("Your saved settings for this workspace were made for a different folder. Confirm them first.")
		}
		return fmt.Errorf("failed to save your receipt settings: %w", err)
	}
	ws.Receipts().SetPolicy(ReceiptPolicy(ws.Local()))
	return nil
}
