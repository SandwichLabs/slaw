package main

// TestNotifyAgentToolChange is a regression guard for a reviewer-caught bug
// in the #69 page-retirement pass: the agent tool-category "page" was
// renamed "content" everywhere EXCEPT notifyAgentToolChange's switch, which
// still matched the literal string "page" — so a successful content-category
// write (create_or_update_skill, add_query_cell, create_dashboard) silently
// stopped emitting "data:changed", leaving the Dashboard and Skills view
// stale until a manual reload. Covers every category notifyAgentToolChange
// is documented to care about: data/schema/content emit, introspect does
// not, and an unknown method name is a no-op (never panics).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifyAgentToolChange(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		wantChange bool
	}{
		{"data category emits", "create_record", true},
		{"schema category emits", "create_table", true},
		{"content category emits (the bug: was gated on the old name \"page\")", "create_or_update_skill", true},
		{"content category emits: add_query_cell", "add_query_cell", true},
		{"introspect category does not emit", "introspect_schema", false},
		{"unknown method is a no-op, never panics", "totally_unknown_method", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{logger: testLogger()}
			var events []string
			app.emit = func(name string, _ ...any) {
				events = append(events, name)
			}

			require.NotPanics(t, func() {
				app.notifyAgentToolChange(tc.method, map[string]any{})
			})

			if tc.wantChange {
				assert.Contains(t, events, "data:changed", "method %q should emit data:changed", tc.method)
				// The rail lists tables from the catalog (#71).
				assert.Contains(t, events, "catalog:changed", "method %q may change tables, so the rail reloads", tc.method)
			} else {
				assert.NotContains(t, events, "data:changed", "method %q should NOT emit data:changed", tc.method)
				assert.NotContains(t, events, "catalog:changed", tc.method)
			}
		})
	}
}

// Agent notebook tools tell an open notebook what happened: an appended cell is
// merged in by id, an edit, delete, move or mid-list insert makes it reload.
func TestNotifyAgentToolChange_NotebookEvents(t *testing.T) {
	cases := []struct {
		name   string
		method string
		result map[string]any
		want   NotebooksChangedEvent
	}{
		{"add_query_cell appends", "add_query_cell", map[string]any{"notebook_id": "nb", "cell_id": "c1"}, NotebooksChangedEvent{NotebookID: "nb", AppendedCellIDs: []string{"c1"}}},
		{"add_markdown_cell appends", "add_markdown_cell", map[string]any{"notebook_id": "nb", "cell_id": "c1"}, NotebooksChangedEvent{NotebookID: "nb", AppendedCellIDs: []string{"c1"}}},
		{"add_markdown_cell mid-list reloads", "add_markdown_cell", map[string]any{"notebook_id": "nb", "cell_id": "c1", "inserted": true}, NotebooksChangedEvent{NotebookID: "nb", Changed: true}},
		{"update_cell reloads", "update_cell", map[string]any{"notebook_id": "nb", "cell_id": "c1"}, NotebooksChangedEvent{NotebookID: "nb", Changed: true}},
		{"delete_cell reloads", "delete_cell", map[string]any{"notebook_id": "nb", "cell_id": "c1"}, NotebooksChangedEvent{NotebookID: "nb", Changed: true}},
		{"move_cell reloads", "move_cell", map[string]any{"notebook_id": "nb", "cell_id": "c1"}, NotebooksChangedEvent{NotebookID: "nb", Changed: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{logger: testLogger()}
			var got []NotebooksChangedEvent
			app.emit = func(name string, args ...any) {
				if name != "notebooks:changed" {
					return
				}
				require.Len(t, args, 1)
				ev, ok := args[0].(NotebooksChangedEvent)
				require.True(t, ok)
				got = append(got, ev)
			}
			app.notifyAgentToolChange(tc.method, tc.result)
			assert.Equal(t, []NotebooksChangedEvent{tc.want}, got)
		})
	}
}
