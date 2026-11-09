package repl

import (
	"bytes"
	"context"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
)

// assertTerminalSafe fails when out could do anything to a terminal beyond
// drawing text: ESC (so no CSI or OSC), a C1 control (8-bit CSI/OSC), a C0
// control other than a line feed, or a bidirectional format character.
func assertTerminalSafe(t *testing.T, out []byte) {
	t.Helper()
	require.True(t, utf8.Valid(out), "output is valid UTF-8: %q", out)
	for _, r := range string(out) {
		switch {
		case r == '\n':
		case r < 0x20 || r == 0x7f:
			t.Fatalf("control character %U in output %q", r, out)
		case r >= 0x80 && r < 0xa0:
			t.Fatalf("C1 control %U in output %q", r, out)
		case isBidiRune(r):
			t.Fatalf("bidi format character %U in output %q", r, out)
		}
	}
}

// FuzzRenderNoEscape: whatever a cell value, a column name, the NULL text, an
// error message or the workspace name holds, nothing the console draws
// carries an escape, OSC or CSI sequence.
func FuzzRenderNoEscape(f *testing.F) {
	for _, seed := range []string{
		"\x1b[31mred", "\x1b]52;c;aGVsbG8=\x07", "\x9b31m", "‮abc", "\x1b]8;;http://x\x1b\\link",
		"a\tb\r\nc", "", "\x9d0;title\x07", "⁦x⁩", "日本語\x00", "\xff\xfe",
	} {
		f.Add(seed, seed, seed, uint8(0))
	}
	f.Add("n", "\x1b[2J", "\x1b[H", uint8(3))
	all := []mode{modeDuckbox, modeBox, modeLine, modeCSV, modeJSON, modeMarkdown}
	f.Fuzz(func(t *testing.T, colName, cell, null string, sel uint8) {
		rs := &engine.ResultSet{
			Columns: []engine.Column{{Name: colName, Type: "VARCHAR"}, {Name: "n", Type: "INTEGER"}, {Name: cell, Type: cell}},
			Rows: [][]any{
				{cell, int64(1), []any{cell, nil}},
				{nil, nil, map[string]any{cell: cell}},
			},
		}
		st := defaultSettings()
		st.mode = all[int(sel)%len(all)]
		st.nullvalue = null
		st.columns = sel&0x80 != 0
		var b bytes.Buffer
		require.NoError(t, render(context.Background(), &b, rs, len(rs.Rows), renderOpts{st: st, width: 80}))
		assertTerminalSafe(t, b.Bytes())

		b.Reset()
		renderError(&b, "Error:", cell, palette{})
		assertTerminalSafe(t, b.Bytes())

		// What the person types or pastes: no C1 or bidi character reaches the
		// editor, and a paste carries no control character.
		var flt inputFilter
		cleaned := flt.apply([]byte(cell))
		assertNoRiskyRunes(t, cleaned)
		var pflt inputFilter
		pasted := pflt.apply([]byte("\x1b[200~" + cell))
		assertNoRiskyRunes(t, pasted[len("\x1b[200~"):])
		for _, c := range pasted[len("\x1b[200~"):] {
			if c < 0x20 && c != '\r' && c != '\n' || c == 0x7f {
				t.Fatalf("control byte %#x inside a paste: %q", c, pasted)
			}
		}
		// A recordable history entry is its own sanitised form.
		if e := foldEntry(cell); engine.Recordable(e) && safe(e) != e {
			t.Fatalf("recordable entry changes under safe: %q", e)
		}

		main, cont := prompts(colName, palette{})
		assertTerminalSafe(t, []byte(main+cont+bannerText(colName)))
	})
}

func assertNoRiskyRunes(t *testing.T, b []byte) {
	t.Helper()
	for _, r := range string(b) {
		if r >= 0x80 && r < 0xa0 || isBidiRune(r) || r == interruptRune {
			t.Fatalf("risky rune %U passed the input filter: %q", r, b)
		}
	}
}
