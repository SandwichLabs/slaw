package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An ICEBERG_SCAN is what an attached catalog's table and a raw
// iceberg_scan('<path>') both plan as. The catalog references of a statement are
// a budget only ICEBERG_SCAN nodes may spend: a pruned catalog scan (an unused
// CTE, LIMIT 0, a false filter, count(*) from metadata) leaves a credit that can
// never cover another reader.
func TestIcebergScanBudgetIsSeparate(t *testing.T) {
	scan := planNode{Name: "ICEBERG_SCAN ", ExtraInfo: map[string]any{"Function": "ICEBERG_SCAN"}}
	csv := planNode{Name: "READ_CSV", ExtraInfo: map[string]any{"Function": "READ_CSV"}}
	glob := planNode{Name: "GLOB", ExtraInfo: map[string]any{"Function": "GLOB"}}
	plan := func(ns ...planNode) int {
		readers := 0
		for i := range ns {
			assert.Empty(t, checkPlanNode(&ns[i], &readers))
		}
		return readers
	}
	oneRef := catalogScanUnit // one reference into an attached catalog, no literal reader
	twoRefs := 2 * catalogScanUnit

	// The honest cases: one reference and one scan, a self join, a raw literal call.
	assert.False(t, readersExceedBudget(plan(scan), oneRef))
	assert.False(t, readersExceedBudget(plan(scan, scan), twoRefs))
	assert.False(t, readersExceedBudget(plan(scan), 1), "a raw iceberg_scan('<literal>')")
	assert.False(t, readersExceedBudget(plan(csv), 1))
	assert.False(t, readersExceedBudget(plan(csv, scan), 1+oneRef), "a read_csv literal beside a catalog table")

	// Pruned: fewer scans than references is fine.
	assert.False(t, readersExceedBudget(plan(), oneRef), "LIMIT 0, an unused CTE, count(*) from metadata")
	assert.False(t, readersExceedBudget(plan(scan), twoRefs))

	// A hidden plain reader beside a catalog reference is refused, whether or not
	// the catalog scan itself was pruned.
	assert.True(t, readersExceedBudget(plan(csv), oneRef), "hidden READ_CSV, catalog scan pruned")
	assert.True(t, readersExceedBudget(plan(csv, scan), oneRef), "hidden READ_CSV beside a live catalog scan")
	assert.True(t, readersExceedBudget(plan(glob), twoRefs))
	assert.True(t, readersExceedBudget(plan(csv, csv), 1+oneRef))

	// More scans than references and literals is refused (a view that wraps a raw call).
	assert.True(t, readersExceedBudget(plan(scan, scan), oneRef))
	assert.True(t, readersExceedBudget(plan(scan), 0))

	// The catalog's own DML operators are remote writes, not readers.
	for _, n := range []string{"ICEBERG_INSERT", "ICEBERG_UPDATE", "ICEBERG_DELETE"} {
		assert.True(t, isRemoteWriteOp(n), n)
		w := planNode{Name: n}
		readers := 0
		assert.Empty(t, checkPlanNode(&w, &readers), n)
		assert.Zero(t, readers, n)
	}
}
