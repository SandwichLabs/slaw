package cmd

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

// "default" returns one limit to unset without touching the others.
func TestReceiptsSettings_DefaultWordUnsetsALimit(t *testing.T) {
	root := receiptsWS(t, 0)
	_, stderr, err := runSlaw("receipts", "settings", "--workspace", root, "--days", "7", "--max-mb", "8")
	require.NoError(t, err, stderr)
	out, stderr, err := runSlaw("receipts", "settings", "--workspace", root, "--days", "default", "--json")
	require.NoError(t, err, stderr)
	var got struct {
		Settings  workspace.ReceiptSettings `json:"settings"`
		Effective workspace.ReceiptSettings `json:"effective"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, 0, got.Settings.Days, "stored as unset")
	assert.Equal(t, 30, got.Effective.Days)
	assert.Equal(t, 8, got.Settings.MaxMB, "the others keep their value")
}

// A lowered limit deletes the oldest receipts before the command returns.
func TestReceiptsSettings_LoweringALimitPrunesBeforeExit(t *testing.T) {
	root := receiptsWS(t, 0)
	for i := 0; i < 105; i++ {
		_, stderr, err := runSlaw("query", "--workspace", root, "SELECT "+strconv.Itoa(i)+" AS n")
		require.NoError(t, err, stderr)
	}
	require.Equal(t, 105, receiptFiles(t, root))
	out, stderr, err := runSlaw("receipts", "settings", "--workspace", root, "--max-count", "100")
	require.NoError(t, err, stderr)
	assert.Equal(t, 100, receiptFiles(t, root))
	assert.Contains(t, out, "Kept now: 100 receipts")
}
