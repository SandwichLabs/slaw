package engine

import (
	"strings"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// RecordableMax is the longest text Recordable accepts, in bytes.
const RecordableMax = 64 << 10

// Recordable reports whether a piece of text may be kept, in the console's
// history, in memory, in a saved query or in a run receipt: it is non-empty and
// not oversized, it is unchanged by terminal sanitising (text holding ESC, a C1
// control or a bidi character would be replayed to a terminal on recall), and no
// credential-shaped text in it would be redacted (a password=..., a token, an
// s3 secret key in a URL, a DSN). Conservative on purpose: a WHERE password =
// 'x' is not kept either. It is RecordableStatement on one line: no line
// break and no tab.
func Recordable(text string) bool {
	return !strings.ContainsAny(text, "\r\n\t") && RecordableStatement(text)
}

// RecordableStatement is Recordable for SQL kept as written: the same rule,
// except that line breaks (\n and \r\n) and tabs are allowed, since a statement
// spans lines. A lone \r, every other control character, C1 and bidi
// characters, text over RecordableMax and any credential-shaped text are still
// refused. Parameter values use Recordable.
func RecordableStatement(text string) bool {
	if text == "" || len(text) > RecordableMax {
		return false
	}
	norm := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\t", " ")
	return TerminalText(norm, TerminalOpts{KeepNewlines: true, StripC1: true, StripBidi: true}) == norm &&
		secrets.RedactPatterns(text) == text
}
