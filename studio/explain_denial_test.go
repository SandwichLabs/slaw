package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
)

// refusalErr stands in for the agents package's definition refusal: an
// ErrReadOnlyBindingDenied that carries its own reason and whether it names a
// missing table or catalog.
type refusalErr struct {
	reason  string
	missing bool
}

func (e refusalErr) Error() string        { return e.reason }
func (e refusalErr) Is(target error) bool { return target == agents.ErrReadOnlyBindingDenied }
func (e refusalErr) MissingObject() bool  { return e.missing }

// In a window opened read-only (another program owns the database) the generic
// refusal and a missing table are explained as "in use"; a statement problem
// (empty, a syntax or binding mistake) is shown as it is.
func TestExplainReadOnlyDenialKeepsStatementProblems(t *testing.T) {
	app := newOpenApp(t)
	app.mu.Lock()
	app.readOnly = true
	app.mu.Unlock()

	syntax := refusalErr{reason: "the statement is empty"}
	assert.Equal(t, error(syntax), app.explainReadOnlyDenial(syntax))

	for _, err := range []error{agents.ErrReadOnlyBindingDenied, refusalErr{reason: `table "t" not found`, missing: true}} {
		got := app.explainReadOnlyDenial(err)
		require.Error(t, got)
		assert.Contains(t, got.Error(), "is in use by")
	}
}
