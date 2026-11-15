package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/repl"
	"github.com/SandwichLabs/slaw/internal/session"
)

type replOut struct {
	mu sync.Mutex
	b  strings.Builder
}

func (r *replOut) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.Write(p)
}

func (r *replOut) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.String()
}

// TestConsoleTakesRunQuerysGates: a console wired the way Studio's tab will be
// (its AfterRun hook is the App's afterRun) refuses what RunQuery refuses, with
// the same text, and announces a catalog change for DDL exactly once, as
// RunQuery does.
func TestConsoleTakesRunQuerysGates(t *testing.T) {
	app := newOpenApp(t)
	var ev eventLog
	app.emit = ev.sink

	out := &replOut{}
	s, err := repl.New(context.Background(), out, repl.Options{
		Engine: app.engine, Sess: app.sess, Workspace: "App Test", Cols: 80, Rows: 24,
		Hooks: repl.Hooks{AfterRun: func(before map[string]session.StatusKind, sqlText string) {
			app.afterRun(app.sess, before, sqlText, unknownConn)
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	prompt := "App Test ▸ D "
	waitPrompts := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for strings.Count(out.String(), prompt) < n {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for prompt %d:\n%s", n, out.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitPrompts(1)
	prompts := 1
	runInConsole := func(stmt string) {
		t.Helper()
		require.NoError(t, s.Input([]byte(stmt+";\r")))
		prompts++
		waitPrompts(prompts)
	}

	for _, stmt := range []string{"INSTALL httpfs", "CREATE SECRET s (TYPE s3)"} {
		_, err := app.RunQuery(RunQueryRequest{SQL: stmt})
		require.Error(t, err, stmt)
		runInConsole(stmt)
		// RunQuery's error is the guard's text; the console prints the same text.
		guard := strings.TrimPrefix(err.Error(), "query failed: ")
		assert.Contains(t, out.String(), "Error: "+guard, stmt)
	}
	assert.Equal(t, 0, ev.count("catalog:changed"), "a refusal changes nothing")

	_, err = app.RunQuery(RunQueryRequest{SQL: "CREATE TABLE par_editor(x INT)"})
	require.NoError(t, err)
	assert.Equal(t, 1, ev.count("catalog:changed"))
	runInConsole("CREATE TABLE par_console(x INT)")
	assert.Equal(t, 2, ev.count("catalog:changed"), "one more, from the console's DDL")

	runInConsole("SELECT 1")
	assert.Equal(t, 2, ev.count("catalog:changed"), "a plain SELECT changes nothing")
}
