package agents

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A relative COPY ... TO target is resolved against the workspace root like a
// reader's path, and vetWrite still vets the result (#289).
func TestAgentResolvesARelativeCopyTarget(t *testing.T) {
	fx := newPolicyFixture(t, ModeFull)
	ctx := context.Background()

	text, _, err := fx.h.expand(ctx, `COPY (SELECT 1 AS a) TO 'data/out.csv'`)
	require.NoError(t, err)
	assert.Equal(t, `COPY (SELECT 1 AS a) TO '`+fx.p("out.csv")+`'`, text)
	v := fx.classify(text)
	assert.NotEqual(t, sqlClassOther, v.Class, v.Reason)

	for _, q := range []string{
		`COPY (SELECT 1 AS a) TO '../out.csv'`,
		`COPY (SELECT 1 AS a) TO 'out.csv'`,
		`COPY (SELECT 1 AS a) TO 'data/db.duckdb'`,
	} {
		text, _, err := fx.h.expand(ctx, q)
		require.NoError(t, err)
		assert.Equal(t, sqlClassOther, fx.classify(text).Class, q)
	}
}
