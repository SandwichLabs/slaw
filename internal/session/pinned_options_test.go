package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A caller's own per-call options survive a move to a non-default catalog,
// by Conn(WithCatalog), by Use and by Follow (#350).
func TestPinnedConnKeepsCallerOptions(t *testing.T) {
	ctx := context.Background()
	s := newTestSession(t)
	p := mkDuckFile(t, "other", "CREATE TABLE t(a INT)")
	require.NoError(t, s.Attach(ctx, Spec{Name: "other", Path: p, RowCap: 50, Timeout: 40 * time.Second}))
	count := func(c *Conn) int {
		rows, err := c.Query(ctx, "SELECT * FROM range(100)")
		require.NoError(t, err)
		n := 0
		for rows.Next() {
			n++
		}
		require.NoError(t, rows.Close())
		return n
	}

	c, err := s.Conn(ctx, WithCatalog("other"), WithRowCap(7), WithTimeout(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, 7, count(c), "the caller's cap beats the catalog's")
	assert.Equal(t, 3*time.Second, c.cfg.timeout)
	require.NoError(t, c.Close())

	// The default catalog keeps the cap.
	c, err = s.Conn(ctx, WithRowCap(7))
	require.NoError(t, err)
	assert.Equal(t, 7, count(c))

	// Use keeps it, and so does Follow in either direction.
	require.NoError(t, c.Use(ctx, "other"))
	assert.Equal(t, "other", c.Catalog())
	assert.Equal(t, 7, count(c))
	c.Follow(s.DefaultCatalog())
	assert.Equal(t, 7, count(c))
	c.Follow("other")
	assert.Equal(t, 7, count(c))
	require.NoError(t, c.Close())

	// Without a caller cap the catalog's Spec defaults apply after Use.
	c, err = s.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, c.Use(ctx, "other"))
	assert.Equal(t, 50, count(c))
	assert.Equal(t, 40*time.Second, c.cfg.timeout)
	require.NoError(t, c.Close())

	// A caller's WithCatalog never beats the catalog the conn moved to.
	c, err = s.Conn(ctx, WithCatalog("other"), WithRowCap(7))
	require.NoError(t, err)
	c.Follow(s.DefaultCatalog())
	assert.Empty(t, c.Catalog())
	assert.Empty(t, c.cfg.catalog)
	assert.Equal(t, 7, count(c))
	require.NoError(t, c.Close())
}
