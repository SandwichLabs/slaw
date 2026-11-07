//go:build integration

package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
)

// The plan operator names of iceberg_scan, iceberg_metadata and
// iceberg_snapshots on the DuckDB this build embeds (checked against a real
// Iceberg table in MinIO): each is one reader to the plan check, so a statement
// that wrote one reader out and vetted it passes, and one reader more fails.
func TestPlanCheckAcceptsIcebergReaders(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	db := f.Open(t)
	icebergtest.Exec(t, db, icebergtest.AttachSQL("meta", f.RESTURI, ", READ_ONLY, ACCESS_DELEGATION_MODE 'none'"))
	_, manifests := f.IcebergFilePaths(t, "orders")
	require.NotEmpty(t, manifests)
	metas := f.List(t, db, manifests[0][:strings.LastIndex(manifests[0], "/")]+"/*.metadata.json")
	require.NotEmpty(t, metas)
	loc := metas[len(metas)-1]

	for _, fn := range []string{"iceberg_scan", "iceberg_metadata", "iceberg_snapshots"} {
		stmt := fmt.Sprintf("SELECT * FROM %s('%s')", fn, loc)
		assert.Empty(t, planCheck(context.Background(), db, stmt, nil, 1), stmt)
		assert.Contains(t, planCheck(context.Background(), db, stmt, nil, 0), "not written out as a literal", "uncounted reader: "+stmt)
	}
}
