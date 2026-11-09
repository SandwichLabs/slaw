package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

func TestReceiptSettings_DefaultsWhenNothingIsSaved(t *testing.T) {
	ws, _ := newWorkspace(t)
	got := StoredReceiptSettings(ws.Local())
	assert.True(t, got.KeepParams, "parameter values are kept by default")
	assert.Zero(t, got.Days)
	assert.Equal(t, receipts.DefaultPolicy(), ReceiptPolicy(ws.Local()))
	assert.Equal(t, receipts.DefaultPolicy(), ws.Receipts().Policy())
	assert.Equal(t, ReceiptSettings{KeepParams: true, Days: 30, MaxCount: 20000, MaxMB: 64}, ReceiptDefaults())
	assert.Equal(t, ReceiptSettings{KeepParams: true, Days: 1, MaxCount: 100, MaxMB: 1}, ReceiptFloors())
	assert.Equal(t, ReceiptSettings{KeepParams: true, Days: 365, MaxCount: 200000, MaxMB: 1024}, ReceiptCeilings())
}

func TestSetLocalReceipts_RoundTripsAndAppliesLive(t *testing.T) {
	ws, root := newWorkspace(t)
	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: false, Days: 7, MaxCount: 500, MaxMB: 8}))

	want := ReceiptSettings{KeepParams: false, Days: 7, MaxCount: 500, MaxMB: 8}
	assert.Equal(t, want, StoredReceiptSettings(ws.Local()))
	p := ws.Receipts().Policy()
	assert.False(t, p.KeepParams)
	assert.Equal(t, 7*24*time.Hour, p.MaxAge)
	assert.Equal(t, 500, p.MaxCount)
	assert.Equal(t, int64(8<<20), p.MaxBytes)

	// A reopen (another process, a restart) reads the same settings, and the new
	// recorder starts under them.
	ws2, err := Open(root, WithConfigDir(configDirOf(t, ws)), WithUserConfig(UserConfig{Version: ConfigVersion}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws2.Close() })
	assert.Equal(t, want, StoredReceiptSettings(ws2.Local()))
	assert.Equal(t, p, ws2.Receipts().Policy())

	// Back to the defaults: zeros are unset, and parameter values come back on.
	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: true}))
	assert.Equal(t, receipts.DefaultPolicy(), ws.Receipts().Policy())
}

// configDirOf finds the per-user config dir a test workspace was opened with, by
// the settings file's location.
func configDirOf(t *testing.T, ws Workspace) string {
	t.Helper()
	w, ok := ws.(*workspace)
	require.True(t, ok)
	return w.configDir
}

func TestSetLocalReceipts_ClampsBothWays(t *testing.T) {
	ws, _ := newWorkspace(t)
	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: true, Days: 99999, MaxCount: 99999999, MaxMB: 999999}))
	assert.Equal(t, ReceiptCeilings(), StoredReceiptSettings(ws.Local()), "above the ceiling is moved down to it")
	assert.Equal(t, receipts.CeilingMaxAge, ws.Receipts().Policy().MaxAge)

	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: true, Days: 1, MaxCount: 1, MaxMB: 1}))
	assert.Equal(t, ReceiptFloors(), StoredReceiptSettings(ws.Local()), "below the floor is moved up to it")
	assert.Equal(t, receipts.FloorMaxCount, ws.Receipts().Policy().MaxCount)

	require.Error(t, SetLocalReceipts(ws, ReceiptSettings{Days: -1}), "a negative number is a mistake, not a default")
}

// A hand-edited local file is clamped on read too, and a giant number cannot overflow.
func TestReceiptPolicy_ClampsAHandEditedFile(t *testing.T) {
	p := ReceiptPolicy(LocalConfig{Receipts: LocalReceipts{Days: 1 << 40, MaxCount: -5, MaxMB: 1 << 40}})
	assert.Equal(t, receipts.CeilingMaxAge, p.MaxAge)
	assert.Equal(t, receipts.DefaultMaxCount, p.MaxCount)
	assert.Equal(t, receipts.CeilingMaxBytes, p.MaxBytes)
}

func TestReceiptSettings_AnUntrustedLocalFileMeansDefaults(t *testing.T) {
	ws, _ := newWorkspace(t)
	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: false, Days: 3, MaxCount: 300, MaxMB: 3}))
	require.NoError(t, os.Remove(filepath.Join(ws.Root(), ".slaw-local", "nonce")))
	require.NoError(t, ws.Reload())
	require.False(t, ws.LocalTrusted())

	assert.Equal(t, receipts.DefaultPolicy(), ReceiptPolicy(ws.Local()))
	assert.True(t, StoredReceiptSettings(ws.Local()).KeepParams)
	err := SetLocalReceipts(ws, ReceiptSettings{KeepParams: true, Days: 9})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different folder")
}

// The settings are this person's own: not in slaw.yaml, so not in git and not
// in a bundle (which carries the committed files and never the local ones).
func TestSetLocalReceipts_NeverReachesTheFolder(t *testing.T) {
	ws, root := newWorkspace(t)
	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: false, Days: 7, MaxCount: 500, MaxMB: 8}))

	cfg, err := os.ReadFile(filepath.Join(root, ConfigFile))
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(string(cfg)), "receipt")
	assert.NotContains(t, strings.ToLower(string(cfg)), "keep_params")

	// Nothing under the folder outside .slaw-local mentions the setting.
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() && rel == LocalDir {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			b, rerr := os.ReadFile(p)
			require.NoError(t, rerr)
			assert.NotContains(t, string(b), "keep_params", rel)
		}
		return nil
	}))
}

// Two handles on one workspace (Studio and the CLI): a change one saves reaches
// the other's recorder before its next receipt or prune.
func TestReceiptSettings_AnotherHandlesChangeReachesARunningRecorder(t *testing.T) {
	a, root := newWorkspace(t)
	b, err := Open(root, WithConfigDir(configDirOf(t, a)), WithUserConfig(UserConfig{Version: ConfigVersion}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.Equal(t, receipts.DefaultPolicy(), a.Receipts().Policy())

	require.NoError(t, SetLocalReceipts(b, ReceiptSettings{KeepParams: false, Days: 365}))
	got := a.Receipts().Policy()
	assert.Equal(t, 365*24*time.Hour, got.MaxAge, "A's next prune keeps 31-day-old receipts")
	assert.False(t, got.KeepParams)
	assert.Equal(t, ReceiptSettings{KeepParams: false, Days: 365}, ReceiptSettingsNow(a), "reads fresh from disk, not the loaded copy")
	assert.Equal(t, ReceiptSettings{KeepParams: true}, StoredReceiptSettings(a.Local()), "the loaded copy is stale until a reload")
}

// A reload re-applies the policy to an open recorder, and an untrusted file
// keeps the policy in force instead of dropping to the defaults.
func TestReceiptSettings_ReloadReappliesAndUntrustedKeepsThePolicy(t *testing.T) {
	ws, _ := newWorkspace(t)
	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: true, Days: 90}))
	require.Equal(t, 90*24*time.Hour, ws.Receipts().Policy().MaxAge)

	require.NoError(t, ws.Reload())
	assert.Equal(t, 90*24*time.Hour, ws.Receipts().Policy().MaxAge)

	require.NoError(t, os.Remove(filepath.Join(ws.Root(), ".slaw-local", "nonce")))
	require.NoError(t, ws.Reload())
	require.False(t, ws.LocalTrusted())
	assert.Equal(t, 90*24*time.Hour, ws.Receipts().Policy().MaxAge, "an untrusted file never falls back to stricter defaults mid-run")
}
