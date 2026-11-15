package repl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDotComplete(t *testing.T) {
	cases := []struct {
		line, want string
		ok         bool
	}{
		{".ta", ".tables", true},
		{".TA", ".tables", true},
		{".sc", ".schema", true},
		{".m", ".m", false},   // .mode, .maxrows, .maxwidth: nothing common beyond "m"
		{".ma", ".max", true}, // ambiguous: the common prefix
		{".mode ma", ".mode markdown", true},
		{".mode b", ".mode box", true},
		{".receipts o", ".receipts o", false}, // on or off
		{".timer of", ".timer off", true},
		{".headers on x", ".headers on x", false},
		{".nope", ".nope", false},
	}
	for _, c := range cases {
		got, pos, ok := dotComplete(c.line, len(c.line))
		assert.Equal(t, c.ok, ok, c.line)
		if ok {
			assert.Equal(t, c.want, got, c.line)
			assert.Equal(t, len(c.want), pos, c.line)
		}
	}
}

func TestDotCompleteLine(t *testing.T) {
	got, pos, ok := dotCompleteLine("  .ta", 5)
	assert.True(t, ok)
	assert.Equal(t, "  .tables", got)
	assert.Equal(t, len(got), pos)

	for _, c := range []struct {
		line string
		pos  int
	}{
		{".ta", 0},   // before the dot
		{"  .ta", 1}, // in the leading spaces
		{"  .ta", 2}, // before the dot
		{".ta", 9},   // past the end
	} {
		_, _, ok := dotCompleteLine(c.line, c.pos)
		assert.False(t, ok, "%q at %d", c.line, c.pos)
	}
}

func TestTabOnDotLineIgnoresSQLCompleter(t *testing.T) {
	f := newSession(t, func(o *Options) {
		o.Complete = func(CompleteRequest) []string { return []string{"TABLE"} }
	})
	f.waitFor(prompt)
	f.typ(".ta\t")
	f.waitFor(".tables")
	assert.NotContains(t, f.out.String(), ".TABLE")
}

func TestSQLLineStillUsesSQLCompleter(t *testing.T) {
	f := newSession(t, func(o *Options) {
		o.Complete = func(CompleteRequest) []string { return []string{"TABLE"} }
	})
	f.waitFor(prompt)
	f.typ("select 1 ta\t")
	f.waitFor("TABLE")
}
