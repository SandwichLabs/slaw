package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/SandwichLabs/slaw/internal/receipts"
)

// printReceipt words the drawer's states: replayable, live only, not recorded,
// parameter values not kept and a withheld statement.
func TestPrintReceipt_WordsTheDrawerStates(t *testing.T) {
	base := receipts.Receipt{
		ID: "r_0123456789abcdef0123", Surface: receipts.SurfaceNotebookCell, Actor: receipts.ActorPerson,
		Source: receipts.Source{Kind: receipts.SourceCell, Ref: "notebooks/n.slnb#c1"},
		SQL:    "SELECT 1", RunSQL: "SELECT 1", Verdict: receipts.VerdictRead,
		Rows:       receipts.Rows{Returned: 500, Total: 1200, Truncated: true},
		Columns:    []receipts.Column{{Name: "n", Type: "INTEGER"}, {Type: "VARCHAR", Withheld: true}},
		ResultHMAC: strings.Repeat("9c", 32),
	}
	show := func(r receipts.Receipt) string {
		var b bytes.Buffer
		printReceipt(&b, r)
		return b.String()
	}
	conn := func(label receipts.SnapshotLabel, value string) []receipts.Connection {
		return []receipts.Connection{{Name: "lake", Type: "ducklake", ReadOnly: true, Status: receipts.StatusConnected, Snapshot: &receipts.Snapshot{Fact: "snapshot", Value: value, Label: label}}}
	}

	r := base
	r.Connections = conn(receipts.LabelReplayable, "4812")
	out := show(r)
	assert.Contains(t, out, "One query on lake, read at snapshot 4812. Run it at that snapshot and you read the same data.")
	assert.Contains(t, out, "Replayable; Read-only check passed")
	assert.Contains(t, out, "Rows: 500 returned of 1200 (truncated)")
	assert.Contains(t, out, "(name withheld)  VARCHAR")
	assert.Contains(t, out, "Fingerprint: 9c9c9c…9c9c")
	assert.Contains(t, out, "You, opening the notebook")

	r.Connections = conn(receipts.LabelLiveOnly, "0/16B3748")
	out = show(r)
	assert.Contains(t, out, "Live only; ")
	assert.NotContains(t, out, "Run it at that snapshot")
	assert.Contains(t, out, "you can't read that moment again")

	r.Connections = conn(receipts.LabelNotRecorded, "not available for catalog tables")
	out = show(r)
	assert.Contains(t, out, "Not recorded; ")
	assert.Contains(t, out, "Not recorded: not available for catalog tables.")
	assert.NotContains(t, strings.ToLower(out), "replayable")

	r.ParamsWithheld, r.Params, r.ParamsHash = true, map[string]receipts.ParamValue{"start": {}}, strings.Repeat("ab", 32)
	out = show(r)
	assert.Contains(t, out, "Parameters: $start (values not kept; hash abab")

	r.SQL, r.SQLWithheld = "", true
	assert.Contains(t, show(r), "(not kept: it looked like it held a credential)")
}
