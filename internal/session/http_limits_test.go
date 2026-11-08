package session

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readSetting(t *testing.T, s Session, name string) string {
	t.Helper()
	var v string
	require.NoError(t, s.DB().QueryRow("SELECT CAST(current_setting("+QuoteLiteral(name)+") AS VARCHAR)").Scan(&v))
	return v
}

// http_timeout and http_retries bound a hung catalog (a context cancel does
// not); they are set where httpfs loads, read back, and stay locked.
func TestHTTPLimitsAreSetWhenHTTPFSLoadsAtOpen(t *testing.T) {
	ext := hostExtensionDir(t)
	s, err := New(WithExtensionDirectory(ext), WithHTTPLimits(7, 2))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.Equal(t, "7", readSetting(t, s, "http_timeout"))
	assert.Equal(t, "2", readSetting(t, s, "http_retries"))
	_, err = s.DB().Exec("SET http_timeout = 600")
	assert.Error(t, err, "locked")
}

func TestHTTPLimitsAreSetWhenHTTPFSLoadsAfterTheLock(t *testing.T) {
	src := hostExtensionDir(t)
	dst := t.TempDir()
	s, err := New(WithExtensionDirectory(dst))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	copyHTTPFS(t, src, dst)
	require.NoError(t, EnsureExtension(context.Background(), s.DB(), "httpfs", "test"))
	assert.Equal(t, strconv.Itoa(DefaultHTTPTimeoutSeconds), readSetting(t, s, "http_timeout"))
	assert.Equal(t, strconv.Itoa(DefaultHTTPRetries), readSetting(t, s, "http_retries"))
	// The lock let both through before httpfs existed, as it does the URL setting
	// (LazyStorage): the editor's parser check refuses a statement naming them.
	assert.True(t, LazyStorage(s))
}

// In a lazy session the lock lets the settings through, so every check
// (DenyUncoveredStorage, run after each extension load) sets both scopes back
// to the configured limits, whatever they were changed to and wherever: a raised
// value, 0 (no timeout at all), a connection's own override, tighter values too.
func TestHTTPLimitsAreResetByTheNextCheck(t *testing.T) {
	src := hostExtensionDir(t)
	dst := t.TempDir()
	s, err := New(WithExtensionDirectory(dst))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	copyHTTPFS(t, src, dst)
	ctx := context.Background()
	require.NoError(t, EnsureExtension(ctx, s.DB(), "httpfs", "test"))
	timeout, retries := strconv.Itoa(DefaultHTTPTimeoutSeconds), strconv.Itoa(DefaultHTTPRetries)

	a, err := s.DB().Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	b, err := s.DB().Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	read := func(c *sql.Conn, name string) string {
		var v string
		require.NoError(t, c.QueryRowContext(ctx, "SELECT CAST(current_setting("+QuoteLiteral(name)+") AS VARCHAR)").Scan(&v))
		return v
	}
	exec := func(c *sql.Conn, q string) {
		_, err := c.ExecContext(ctx, q)
		require.NoError(t, err, q+": the lazy session lets it through the lock")
	}

	// The first check pins connection a's own values.
	require.NoError(t, protectStorage(ctx, a, true, defaultHTTPLimits()))
	assert.Equal(t, timeout, read(a, "http_timeout"))

	for _, st := range []string{
		"SET GLOBAL http_timeout = 600", "SET GLOBAL http_timeout = 0", "SET GLOBAL http_retries = 9",
		"SET SESSION http_timeout = 0", "SET SESSION http_retries = 9",
		"SET GLOBAL http_timeout = 5", "SET GLOBAL http_retries = 0", // tighter is not kept either
	} {
		exec(a, st)
		require.NoError(t, protectStorage(ctx, a, true, defaultHTTPLimits()))
		assert.Equal(t, timeout, read(a, "http_timeout"), st)
		assert.Equal(t, retries, read(a, "http_retries"), st)
		assert.Equal(t, timeout, read(b, "http_timeout"), st+": a second pooled connection reads the global value")
		assert.Equal(t, retries, read(b, "http_retries"), st+": a second pooled connection reads the global value")
	}
	// The path with only a *sql.DB (Studio's settleRun) does the same.
	exec(b, "SET GLOBAL http_timeout = 0")
	require.NoError(t, DenyUncoveredStorage(ctx, s.DB()))
	assert.Equal(t, timeout, read(b, "http_timeout"))
}

// A server that accepts and never answers: the read fails within the bound
// even though the context carries no deadline (S0 item 11).
func TestAHungEndpointFailsWithinTheHTTPTimeout(t *testing.T) {
	ext := hostExtensionDir(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	s, err := New(WithExtensionDirectory(ext), WithHTTPLimits(1, 1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	start := time.Now()
	_, err = s.DB().ExecContext(context.Background(), "SELECT * FROM read_parquet("+QuoteLiteral(srv.URL+"/x.parquet")+")")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 20*time.Second, "bounded by http_timeout and http_retries, not by the context")
}
