package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

// A receipt file can be edited by hand and its id recomputed, so text mode must
// never print an escape sequence from one: OSC 52 (clipboard) and OSC 0 (title)
// would reach the terminal. --json is the record as it is.
func TestReceiptsShowAndList_PrintNoEscapeBytes(t *testing.T) {
	root := newWS(t)
	st, err := receipts.NewFileStore(root, receipts.DefaultPolicy())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	osc := "\x1b]52;c;aGFja2Vk\x07"
	title := "\x1b]0;pwned\x07"
	c1 := "\u009b31m" // a C1 control
	key := []byte("0123456789abcdef0123456789abcdef")
	rec := receipts.Receipt{
		At: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), DurationMS: 5,
		Surface: receipts.SurfaceQueryEditor, Actor: receipts.ActorPerson,
		Source: receipts.Source{Kind: receipts.SourceSavedQuery, Ref: "queries/x" + c1 + ".sql"},
		SQL:    "SELECT 1 " + title + "\n  FROM t " + c1, RunSQL: "SELECT 2" + osc,
		SQLHash: receipts.HashText(key, "a"), RunSQLHash: receipts.HashRunText(key, "b"),
		Params:     map[string]receipts.ParamValue{"p" + osc: {Value: "v" + title}},
		ParamsHash: receipts.HashParams(key, map[string]string{"p": "v"}),
		Connections: []receipts.Connection{{
			Name: "lake" + osc, Type: "duck" + title, Status: receipts.StatusConnected,
			Snapshot: &receipts.Snapshot{Fact: "f" + osc, Value: "v" + title, Label: receipts.LabelLiveOnly},
		}},
		Verdict:    receipts.VerdictUnchecked,
		Rows:       receipts.Rows{Returned: 1, Total: 1},
		Columns:    []receipts.Column{{Name: "n" + osc, Type: "INT" + title}},
		ResultHMAC: strings.Repeat("ab", 32),
	}
	sealed, err := receipts.Seal(rec)
	require.NoError(t, err)
	require.NoError(t, st.Put(sealed))

	out, stderr, err := runSlaw("receipts", "show", sealed.ID, "--workspace", root)
	require.NoError(t, err, stderr)
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\x07")
	assert.NotContains(t, out, "\u009b")
	assert.Contains(t, out, "SELECT 1 ", "the text itself still shows")
	assert.Contains(t, out, "FROM t")

	out, stderr, err = runSlaw("receipts", "list", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\x07")
	assert.Contains(t, out, sealed.ID)

	// --json is the record, unchanged.
	out, _, err = runSlaw("receipts", "show", sealed.ID, "--workspace", root, "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `\u001b`)
}
