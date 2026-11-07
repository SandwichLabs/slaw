package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

// catalogSess answers only what ListCatalog's schema level asks.
type catalogSess struct {
	session.Session
	typ     string
	schemas []string
}

func (c catalogSess) ConnectionInfos() []session.ConnectionInfo {
	return []session.ConnectionInfo{{Name: "cat", Type: c.typ}}
}

func (c catalogSess) ListSchemas(context.Context, string) ([]string, error) { return c.schemas, nil }

func manySchemas(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("ns%04d", i)
	}
	return out
}

// An iceberg_catalog lists at most MaxCatalogNamespaces namespaces and says how
// many it left out; another type is never cut.
func TestListCatalogCapsAnIcebergCatalogsNamespaces(t *testing.T) {
	ctx := context.Background()
	l, err := ListCatalog(ctx, catalogSess{typ: IcebergCatalogType, schemas: manySchemas(MaxCatalogNamespaces + 37)}, CatalogRequest{Connection: "cat"})
	require.NoError(t, err)
	assert.Len(t, l.Nodes, MaxCatalogNamespaces)
	assert.Equal(t, 37, l.More)
	assert.Equal(t, "ns0000", l.Nodes[0].Name)

	l, err = ListCatalog(ctx, catalogSess{typ: IcebergCatalogType, schemas: manySchemas(MaxCatalogNamespaces)}, CatalogRequest{Connection: "cat"})
	require.NoError(t, err)
	assert.Len(t, l.Nodes, MaxCatalogNamespaces)
	assert.Zero(t, l.More)

	l, err = ListCatalog(ctx, catalogSess{typ: "postgres", schemas: manySchemas(MaxCatalogNamespaces + 37)}, CatalogRequest{Connection: "cat"})
	require.NoError(t, err)
	assert.Len(t, l.Nodes, MaxCatalogNamespaces+37)
	assert.Zero(t, l.More)
}
