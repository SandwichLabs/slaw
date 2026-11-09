package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

// firstReceiptID is the id of the newest receipt of the workspace.
func firstReceiptID(t *testing.T, root string) string {
	t.Helper()
	out, stderr, err := runSlaw("receipts", "list", "--workspace", root, "--json", "--limit", "1")
	require.NoError(t, err, stderr)
	var rows []receipts.Summary
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 1)
	return rows[0].ID
}

func TestReceiptsShow_PrintsTheDrawersSections(t *testing.T) {
	root := newWS(t)
	_, stderr, err := runSlaw("query", "--workspace", root, "SELECT 42 AS answer")
	require.NoError(t, err, stderr)
	id := firstReceiptID(t, root)

	out, stderr, err := runSlaw("receipts", "show", id, "--workspace", root)
	require.NoError(t, err, stderr)
	for _, want := range []string{
		"RECEIPT " + id,
		"\nSUMMARY\n", "\nQUERY\n", "\nCONNECTIONS\n", "\nRESULT\n", "\nRUN\n",
		"As written:", "SELECT 42 AS answer", "As run:",
		"Your own query",
		"Rows: 1 returned",
		"Columns: 1", "answer  INTEGER",
		"Fingerprint: ", "never the rows",
		"You, with slaw query",
		"Kept on this computer, never in the folder.",
	} {
		assert.Contains(t, out, want)
	}
	// The value of the result is not in a receipt, so not in its printout.
	assert.NotContains(t, out, "42\n  answer")
}

func TestReceiptsShow_JSONIsTheRecord(t *testing.T) {
	root := newWS(t)
	_, stderr, err := runSlaw("query", "--workspace", root, "SELECT 1 AS n")
	require.NoError(t, err, stderr)
	id := firstReceiptID(t, root)

	out, stderr, err := runSlaw("receipts", "show", id, "--workspace", root, "--json")
	require.NoError(t, err, stderr)
	r, err := receipts.Decode([]byte(strings.TrimSpace(out)))
	require.NoError(t, err, "the JSON is a receipt, id and all")
	assert.Equal(t, id, r.ID)
	assert.Equal(t, receipts.SurfaceCLIQuery, r.Surface)
}

func TestReceiptsShow_UnknownIDIsAnError(t *testing.T) {
	root := newWS(t)
	for _, id := range []string{"r_00000000000000000000", "../../etc/passwd", "nope"} {
		_, _, err := runSlaw("receipts", "show", id, "--workspace", root)
		require.Error(t, err, id)
	}
}

func TestReceiptsList_NewestFirstFilteredAndLimited(t *testing.T) {
	root := receiptsWS(t, 3)

	out, stderr, err := runSlaw("receipts", "list", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Equal(t, 3, strings.Count(out, "cli_query"))
	assert.Contains(t, out, "r_")

	out, stderr, err = runSlaw("receipts", "list", "--workspace", root, "--limit", "2", "--json")
	require.NoError(t, err, stderr)
	var rows []receipts.Summary
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	assert.Len(t, rows, 2)

	out, stderr, err = runSlaw("receipts", "list", "--workspace", root, "--surface", "a2ui")
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "No receipts.")
	out, _, err = runSlaw("receipts", "list", "--workspace", root, "--surface", "a2ui", "--json")
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(out), "an empty list is [], never null")

	_, _, err = runSlaw("receipts", "list", "--workspace", root, "--surface", "nowhere")
	require.Error(t, err)
	_, _, err = runSlaw("receipts", "list", "--workspace", root, "--limit", "0")
	require.Error(t, err)
}
