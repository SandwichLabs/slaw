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
	plan := func(ns ...planNode) readerCount {
		var readers readerCount
		for i := range ns {
			assert.Empty(t, checkPlanNode(&ns[i], &readers))
		}
		return readers
	}
	oneRef := readerCount{catalog: 1} // one reference into an attached catalog, no literal reader
	twoRefs := readerCount{catalog: 2}
	lit := readerCount{plain: 1} // one reader written out as a literal

	// The honest cases: one reference and one scan, a self join, a raw literal call.
	assert.False(t, plan(scan).exceeds(oneRef))
	assert.False(t, plan(scan, scan).exceeds(twoRefs))
	assert.False(t, plan(scan).exceeds(lit), "a raw iceberg_scan('<literal>')")
	assert.False(t, plan(csv).exceeds(lit))
	assert.False(t, plan(csv, scan).exceeds(lit.add(oneRef)), "a read_csv literal beside a catalog table")

	// Pruned: fewer scans than references is fine.
	assert.False(t, plan().exceeds(oneRef), "LIMIT 0, an unused CTE, count(*) from metadata")
	assert.False(t, plan(scan).exceeds(twoRefs))

	// A hidden plain reader beside a catalog reference is refused, whether or not
	// the catalog scan itself was pruned.
	assert.True(t, plan(csv).exceeds(oneRef), "hidden READ_CSV, catalog scan pruned")
	assert.True(t, plan(csv, scan).exceeds(oneRef), "hidden READ_CSV beside a live catalog scan")
	assert.True(t, plan(glob).exceeds(twoRefs))
	assert.True(t, plan(csv, csv).exceeds(lit.add(oneRef)))

	// More scans than references and literals is refused (a view that wraps a raw call).
	assert.True(t, plan(scan, scan).exceeds(oneRef))
	assert.True(t, plan(scan).exceeds(readerCount{}))

	// The catalog's own DML operators are remote writes, not readers.
	for _, n := range []string{"ICEBERG_INSERT", "ICEBERG_UPDATE", "ICEBERG_DELETE"} {
		assert.True(t, isRemoteWriteOp(n), n)
		w := planNode{Name: n}
		var readers readerCount
		assert.Empty(t, checkPlanNode(&w, &readers), n)
		assert.Zero(t, readers, n)
	}
}
