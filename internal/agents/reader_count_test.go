package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// readerCount keeps the two budgets in separate fields: no number of catalog
// references can carry into the plain budget (the old single-int packing, one
// catalog unit being 1<<16, carried into it past 65535 references).
func TestReaderCountAddAndBudget(t *testing.T) {
	a := readerCount{plain: 2, catalog: 3}
	b := readerCount{plain: 5, catalog: 7}
	assert.Equal(t, readerCount{plain: 7, catalog: 10}, a.add(b))
	assert.Equal(t, a, a.add(readerCount{}), "zero is the identity")
	assert.Equal(t, a.add(b), b.add(a))

	// The rule, stated both ways: plain <= raw, plain+scans <= raw+catalog.
	vetted := readerCount{plain: 1, catalog: 2}
	assert.False(t, readerCount{plain: 1, catalog: 2}.exceeds(vetted))
	assert.False(t, readerCount{plain: 0, catalog: 3}.exceeds(vetted), "a scan may spend the unused literal")
	assert.True(t, readerCount{plain: 2}.exceeds(vetted), "plain over raw")
	assert.True(t, readerCount{catalog: 4}.exceeds(vetted), "scans over raw+catalog")
	assert.True(t, readerCount{plain: 1, catalog: 3}.exceeds(vetted), "total over raw+catalog")
}

// Many catalog references, none of them in the plan, leave the plain budget at
// zero: a hidden reader gets no credit from them, however many there are.
func TestReaderCountManyCatalogRefsDoNotCarry(t *testing.T) {
	var many readerCount
	for range 70000 { // past the old 1<<16 carry
		many = many.add(readerCount{catalog: 1})
	}
	assert.Equal(t, 0, many.plain)
	assert.Equal(t, 70000, many.catalog)

	csv := planNode{Name: "READ_CSV", ExtraInfo: map[string]any{"Function": "READ_CSV"}}
	var plan readerCount
	assert.Empty(t, checkPlanNode(&csv, &plan))
	assert.True(t, plan.exceeds(many), "a hidden reader gets no plain credit from catalog references")
}
