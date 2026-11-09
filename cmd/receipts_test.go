package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

// receiptsWS is a workspace with n receipts, minted by real `slaw query` runs.
func receiptsWS(t *testing.T, n int) string {
	t.Helper()
	root := newWS(t)
	for i := 0; i < n; i++ {
		_, stderr, err := runSlaw("query", "--workspace", root, "SELECT "+string(rune('1'+i))+" AS n")
		require.NoError(t, err, stderr)
	}
	return root
}

func receiptFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	require.NoError(t, filepath.WalkDir(filepath.Join(root, ".slaw-local", "receipts"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "r_") {
			n++
		}
		return nil
	}))
	return n
}

func TestReceiptsSettings_PrintsTheDefaultsAndUsage(t *testing.T) {
	root := receiptsWS(t, 2)
	out, stderr, err := runSlaw("receipts", "settings", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Keep parameter values  on")
	assert.Contains(t, out, "Keep for               30 days")
	assert.Contains(t, out, "At most                20000 receipts")
	assert.Contains(t, out, "Using at most          64 MB")
	assert.Contains(t, out, "Allowed: 1-365 days, 100-200000 receipts, 1-1024 MB")
	assert.Contains(t, out, "Kept now: 2 receipts")
	assert.Contains(t, out, "oldest from ")
}

func TestReceiptsSettings_FlagsSaveAndOthersKeepTheirValue(t *testing.T) {
	root := receiptsWS(t, 1)
	out, stderr, err := runSlaw("receipts", "settings", "--workspace", root, "--keep-params", "off", "--days", "7")
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Keep parameter values  off")
	assert.Contains(t, out, "Keep for               7 days")

	// A second call changes one limit; the first call's values stay.
	out, stderr, err = runSlaw("receipts", "settings", "--workspace", root, "--max-count", "500", "--max-mb", "8", "--json")
	require.NoError(t, err, stderr)
	var got struct {
		Settings  workspace.ReceiptSettings `json:"settings"`
		Effective workspace.ReceiptSettings `json:"effective"`
		Count     int                       `json:"count"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, workspace.ReceiptSettings{KeepParams: false, Days: 7, MaxCount: 500, MaxMB: 8}, got.Effective)
	assert.Equal(t, 1, got.Count)

	// Read back by a later, flagless call (the settings live outside the folder).
	out, _, err = runSlaw("receipts", "settings", "--workspace", root)
	require.NoError(t, err)
	assert.Contains(t, out, "At most                500 receipts")
	cfg, err := os.ReadFile(filepath.Join(root, "slaw.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(cfg), "receipts")
}

func TestReceiptsSettings_OutOfRangeIsClampedAndSaysSo(t *testing.T) {
	root := receiptsWS(t, 0)
	out, stderr, err := runSlaw("receipts", "settings", "--workspace", root, "--days", "9999", "--max-count", "5")
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Keep for               365 days")
	assert.Contains(t, out, "At most                100 receipts")
	assert.Contains(t, out, "note: --days 9999 is outside the allowed range; using 365")
	assert.Contains(t, out, "note: --max-count 5 is outside the allowed range; using 100")
}

func TestReceiptsSettings_RefusesBadFlags(t *testing.T) {
	root := receiptsWS(t, 0)
	_, _, err := runSlaw("receipts", "settings", "--workspace", root, "--keep-params", "maybe")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "on or off")
	_, _, err = runSlaw("receipts", "settings", "--workspace", root, "--days", "0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive")
	_, _, err = runSlaw("receipts", "settings", "--workspace", root, "--days", "-3")
	require.Error(t, err)
}

func TestReceiptsClear_AsksFirst(t *testing.T) {
	root := receiptsWS(t, 2)
	require.Equal(t, 2, receiptFiles(t, root))

	// Without --yes it asks, and anything but y/yes deletes nothing. (stdin here
	// is a reader, not a terminal file, so the no-terminal refusal is not reached.)
	out, stderr, err := runSlawIn(strings.NewReader("n\n"), "receipts", "clear", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, stderr, "Delete all 2 receipts")
	assert.Contains(t, out, "Nothing deleted.")
	assert.Equal(t, 2, receiptFiles(t, root))

	out, stderr, err = runSlawIn(strings.NewReader("y\n"), "receipts", "clear", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Deleted 2 receipts.")
	assert.Zero(t, receiptFiles(t, root))
	assert.FileExists(t, filepath.Join(root, ".slaw-local", "receipts", "key"), "the key stays")
}

func TestReceiptsClear_YesSkipsTheQuestion(t *testing.T) {
	root := receiptsWS(t, 3)
	out, stderr, err := runSlaw("receipts", "clear", "--workspace", root, "--yes")
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Deleted 3 receipts.")
	assert.NotContains(t, stderr, "Delete all")
	assert.Zero(t, receiptFiles(t, root))

	out, _, err = runSlaw("receipts", "settings", "--workspace", root)
	require.NoError(t, err)
	assert.Contains(t, out, "Kept now: 0 receipts")
}
