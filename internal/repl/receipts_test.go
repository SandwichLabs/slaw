package repl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

const promptOn = "ws ▸ D [receipts] "

func withReceipts(m *receipts.Mock) func(*Options) {
	return func(o *Options) {
		o.Receipts = receipts.NewRecorder(m, bytes.Repeat([]byte{3}, receipts.KeySize), receipts.DefaultPolicy(),
			receipts.Checks{Statement: engine.RecordableStatement, Value: engine.Recordable}, nil)
	}
}

func TestReceipts_OffByDefaultAndOnForTheSessionOnly(t *testing.T) {
	m := receipts.NewMock()
	f := newSession(t, withReceipts(m))
	f.waitFor(prompt)

	f.run("select 1 as a;\r")
	assert.Empty(t, m.All(), "receipts are off until the person turns them on")
	assert.NotContains(t, f.out.String(), "receipt r_")

	f.typ(".receipts on\r")
	f.waitFor(promptOn)
	f.typ("select 41 + 1 as answer;\r")
	f.waitCount(promptOn, 2)
	require.Len(t, m.All(), 1)
	r := m.All()[0]
	assert.Equal(t, receipts.SurfaceConsole, r.Surface)
	assert.Equal(t, receipts.ActorPerson, r.Actor)
	assert.Equal(t, receipts.VerdictUnchecked, r.Verdict)
	assert.Equal(t, receipts.Source{Kind: receipts.SourceSQL}, r.Source)
	assert.Equal(t, "select 41 + 1 as answer", r.SQL)
	assert.EqualValues(t, 1, r.Rows.Returned)
	assert.Contains(t, f.out.String(), "receipt "+r.ID, "the console says which receipt it kept")

	// A write and a script are not reads: none.
	f.typ("create table made(x int);\r")
	f.waitCount(promptOn, 3)
	f.typ("insert into made values (1);\r")
	f.waitCount(promptOn, 4)
	assert.Len(t, m.All(), 1)

	f.typ(".receipts off\r")
	f.waitCount(prompt, 2)
	f.run("select 2 as b;\r")
	assert.Len(t, m.All(), 1)

	// Nothing is remembered: a new console on the same hub starts off.
	require.NoError(t, f.s.Close())
	g := &fixture{t: t, out: &syncBuffer{}, sess: f.sess, eng: f.eng, db: f.db}
	require.NoError(t, g.open(withReceipts(m)))
	g.waitFor(prompt)
	g.run("select 3 as c;\r")
	assert.Len(t, m.All(), 1)
}

func TestReceipts_TheDotCommandReportsAndRefuses(t *testing.T) {
	m := receipts.NewMock()
	f := newSession(t, withReceipts(m))
	f.waitFor(prompt)
	f.run(".receipts\r")
	assert.Contains(t, f.out.String(), "receipts are off")
	f.typ(".receipts on\r")
	f.waitFor(promptOn)
	f.typ(".receipts\r")
	f.waitCount(promptOn, 2)
	assert.Contains(t, f.out.String(), "receipts are on")
	f.typ(".receipts maybe\r")
	f.waitCount(promptOn, 3)
	assert.Contains(t, f.out.String(), "Usage: '.receipts on|off'")

	// Without a recorder (its folder could not be opened) the command says so.
	h := newSession(t, nil)
	h.waitFor(prompt)
	h.run(".receipts on\r")
	assert.Contains(t, h.out.String(), "receipts are not available here")
	assert.NotContains(t, h.out.String(), promptOn)
}

func TestReceipts_AFailingStoreStillShowsTheResult(t *testing.T) {
	m := receipts.NewMock()
	m.PutErr = assert.AnError
	f := newSession(t, withReceipts(m))
	f.waitFor(prompt)
	f.typ(".receipts on\r")
	f.waitFor(promptOn)
	f.typ("select 7 as seven;\r")
	f.waitCount(promptOn, 2)
	assert.Contains(t, f.out.String(), "seven")
	assert.NotContains(t, f.out.String(), "receipt r_")
	assert.Empty(t, m.All())
}

func TestPromptsTagReceiptsAndKeepWidths(t *testing.T) {
	for _, ws := range []string{"", "ws", "a-longer-name"} {
		main, cont := prompts(ws, palette{}, false)
		mainOn, contOn := prompts(ws, palette{}, true)
		assert.False(t, strings.Contains(main, "[receipts]"))
		assert.Contains(t, mainOn, "[receipts]")
		assert.Equal(t, cellWidth(main), cellWidth(cont), "continuation lines up with the main prompt: %q", ws)
		assert.Equal(t, cellWidth(mainOn), cellWidth(contOn), "and still does while receipts are on: %q", ws)
	}
}
