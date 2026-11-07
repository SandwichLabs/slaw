package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An ICEBERG_SCAN is one reader in the plan, whether it is a raw
// iceberg_scan('<path>') or an attached catalog's table (the plans look alike),
// so the statement must account for it: a raw call by its literal, a catalog
// table by the reference the analyzer counts. Both ways: one scan with no vetted
// reader is refused, one with a vetted reader passes; the catalog's own DML
// operators are remote writes and are not readers.
func TestIcebergScanIsOneCountedReader(t *testing.T) {
	scan := planNode{Name: "ICEBERG_SCAN ", ExtraInfo: map[string]any{"Function": "ICEBERG_SCAN"}}
	var readers int
	assert.Empty(t, checkPlanNode(&scan, &readers))
	assert.Equal(t, 1, readers)

	two := planNode{Name: "HASH_JOIN", Children: []planNode{scan, scan}}
	readers = 0
	assert.Empty(t, checkPlanNode(&two, &readers))
	assert.Equal(t, 2, readers, "a self join scans twice")

	for _, n := range []string{"ICEBERG_INSERT", "ICEBERG_UPDATE", "ICEBERG_DELETE"} {
		assert.True(t, isRemoteWriteOp(n), n)
		w := planNode{Name: n}
		readers = 0
		assert.Empty(t, checkPlanNode(&w, &readers), n)
		assert.Zero(t, readers, n)
	}
}
