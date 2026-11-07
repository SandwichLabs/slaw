package session

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #138. httpfs reads ?s3_endpoint= (and s3_use_ssl, s3_url_style, s3_access_key_id,
// ...) from a URL and lets it override the secret that the URL's scope matched,
// so the connection's key id, session token and a valid signature went to a
// host the URL chose. These tests run offline against loopback servers.

// reqLog is a fake S3 endpoint that records every request it receives.
type reqLog struct {
	mu   sync.Mutex
	reqs []string // "METHOD path?query auth=... tok=..."
}

func (l *reqLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, fmt.Sprintf("%s %s auth=%s tok=%s", r.Method, r.URL.RequestURI(),
		r.Header.Get("Authorization"), r.Header.Get("X-Amz-Security-Token")))
}

func (l *reqLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.reqs...)
}

// fakeEndpoint answers every request 403 and, for a ListObjectsV2, a one-key listing.
func fakeEndpoint(t *testing.T, listing bool) (host string, log *reqLog) {
	t.Helper()
	log = &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if listing && r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`+
				`<Contents><Key>raw/a.parquet</Key><Size>1</Size></Contents></ListBucketResult>`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), log
}

// connectionSecret is a storage connection's secret as the connection types write it.
func connectionSecret(t *testing.T, s Session, name, kind, scope, endpoint string) {
	t.Helper()
	params := []SecretParam{
		{Key: "SCOPE", Value: scope}, {Key: "KEY_ID", Value: "OWNKEYID"},
		{Key: "SECRET", Value: "OWNSECRET", Secret: true},
		{Key: "SESSION_TOKEN", Value: "OWNSESSIONTOKEN", Secret: true},
		{Key: "REGION", Value: "us-east-1"}, {Key: "ENDPOINT", Value: endpoint},
		{Key: "URL_STYLE", Value: "path"}, {Key: "USE_SSL", Value: false},
	}
	require.NoError(t, s.CreateTemporarySecret(context.Background(), name, kind, params))
}

func requireURLParamsOff(t *testing.T, db *sql.DB) {
	t.Helper()
	var v string
	require.NoError(t, db.QueryRow("SELECT CAST(current_setting('s3_url_compatibility_mode') AS VARCHAR)").Scan(&v))
	require.Equal(t, "true", v, "URL parameters and globs are off, so the whole URL is the key")
}

// assertRedirectClosed is the #138 regression test. For every scheme it
// requires, before any read, that the URL is matched by the connection's own
// secret (so a regression would sign it) and that the setting is on, then reads
// the URL and asserts the alternate host saw nothing.
func assertRedirectClosed(t *testing.T, s Session) {
	t.Helper()
	ctx := context.Background()
	requireURLParamsOff(t, s.DB())
	good, goodLog := fakeEndpoint(t, false)
	evil, evilLog := fakeEndpoint(t, false)
	redirect := "?s3_endpoint=" + evil + "&s3_use_ssl=false&s3_url_style=path&s3_access_key_id=Q&s3_secret_access_key=Q&s3_session_token=Q"

	cases := []struct{ kind, scheme string }{
		{"s3", "s3"}, {"r2", "r2"}, {"gcs", "gcs"}, {"gcs", "gs"},
	}
	for i, c := range cases {
		scope := fmt.Sprintf("%s://bucket%d/", c.scheme, i)
		connectionSecret(t, s, fmt.Sprintf("slaw_c%d", i), c.kind, scope, good)
		for _, u := range []string{
			scope + "x.parquet" + redirect,
			scope + "*.parquet" + redirect,
		} {
			require.Equal(t, fmt.Sprintf("slaw_c%d", i), whichSecret(t, s, u, c.kind), "%s is matched by the connection's secret", u)
			_, err := s.DB().ExecContext(ctx, "SELECT count(*) FROM read_parquet("+QuoteLiteral(u)+")")
			require.Error(t, err, u)
			_, err = s.DB().ExecContext(ctx, "SELECT count(*) FROM glob("+QuoteLiteral(u)+")")
			assert.NoError(t, err, "glob() of a literal key echoes it back without a request")
		}
	}
	assert.Empty(t, evilLog.all(), "nothing was sent to the host the URL named")
	for _, r := range goodLog.all() {
		assert.NotContains(t, r, "auth=AWS4-HMAC-SHA256 Credential=Q/", "the URL's own key id is not used")
		assert.NotContains(t, r, "tok=Q")
	}
	require.NotEmpty(t, goodLog.all(), "the reads went to the connection's own endpoint, with the URL as the key")
	for _, r := range goodLog.all() {
		assert.Contains(t, r, "%3F", "the \"?\" is part of the key: "+r)
		assert.NotContains(t, r, "list-type", "a wildcard is read as a key, never listed")
	}
}

func TestURLParametersNeverRedirectASignedRequest(t *testing.T) {
	ext := hostExtensionDir(t)
	s, err := New(WithExtensionDirectory(ext))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, EnsureExtension(context.Background(), s.DB(), "httpfs", "test"))
	assertRedirectClosed(t, s)
}

// copyHTTPFS installs httpfs into a fresh extension directory the session was
// opened without, so its first load happens after lock_configuration.
func copyHTTPFS(t *testing.T, from, to string) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck
	var ver, platform string
	require.NoError(t, db.QueryRow("SELECT library_version, platform FROM pragma_version(), pragma_platform()").Scan(&ver, &platform))
	dir := filepath.Join(to, ver, platform)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, f := range []string{"httpfs.duckdb_extension", "httpfs.duckdb_extension.info"} {
		b, err := os.ReadFile(filepath.Join(from, ver, platform, f))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), b, 0o644))
	}
}

// The first httpfs load usually happens after the lock (a first-time storage
// connection); the one setting it must change is let through, nothing else is.
func TestURLParametersAreOffWhenHTTPFSLoadsAfterTheLock(t *testing.T) {
	src := hostExtensionDir(t)
	dst := t.TempDir()
	s, err := New(WithExtensionDirectory(dst))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	h, ok := s.(*hub)
	require.True(t, ok)
	assert.True(t, h.cfg.lazyStorage, "httpfs was not loaded at open")
	assert.True(t, LazyStorage(s))
	var allowed string
	require.NoError(t, s.DB().QueryRow("SELECT CAST(value AS VARCHAR) FROM duckdb_settings() WHERE name = 'allowed_configs'").Scan(&allowed))
	assert.True(t, sameSet("[http_retries, http_timeout, "+urlParamSetting+"]", allowed), allowed)

	copyHTTPFS(t, src, dst)
	require.NoError(t, EnsureExtension(context.Background(), s.DB(), "httpfs", "test"))
	assertRedirectClosed(t, s)

	for _, stmt := range []string{"SET s3_region = 'x'", "SET allowed_configs = []", "SET lock_configuration = false", "SET enable_external_access = true"} {
		_, err := s.DB().Exec(stmt)
		assert.Error(t, err, stmt+": the rest of the configuration stays locked")
	}
}

// The setting is on from the moment the session opens when httpfs is installed.
func TestURLParametersAreOffWhenHTTPFSLoadsAtOpen(t *testing.T) {
	ext := hostExtensionDir(t)
	s, err := New(WithExtensionDirectory(ext))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	h, ok := s.(*hub)
	require.True(t, ok)
	assert.False(t, h.cfg.lazyStorage)
	assert.False(t, LazyStorage(s), "the editor's parser check is skipped: the lock already refuses the setting")
	requireURLParamsOff(t, s.DB())
	var allowed string
	require.NoError(t, s.DB().QueryRow("SELECT CAST(value AS VARCHAR) FROM duckdb_settings() WHERE name = 'allowed_configs'").Scan(&allowed))
	assert.Equal(t, "[]", allowed, "nothing is let through the lock")
	_, err = s.DB().Exec("SET s3_url_compatibility_mode = false")
	assert.Error(t, err, "locked")
}

// The private listing session still globs, and only it.
func TestListObjectsGlobsInAPrivateSession(t *testing.T) {
	ext := hostExtensionDir(t)
	good, goodLog := fakeEndpoint(t, true)
	s, err := New(WithExtensionDirectory(ext))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	lister, ok := s.(ObjectLister)
	require.True(t, ok)
	step := SecretStep{Name: "slaw_lake", Type: "s3", Params: []SecretParam{
		{Key: "SCOPE", Value: "s3://lake/raw/"}, {Key: "KEY_ID", Value: "OWNKEYID"}, {Key: "SECRET", Value: "OWNSECRET", Secret: true},
		{Key: "SESSION_TOKEN", Value: "", Secret: true}, {Key: "REGION", Value: "us-east-1"},
		{Key: "ENDPOINT", Value: good}, {Key: "URL_STYLE", Value: "path"}, {Key: "USE_SSL", Value: false},
	}}
	n, err := lister.ListObjects(context.Background(), step, "s3://lake/raw/**", 1000)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	reqs := goodLog.all()
	require.NotEmpty(t, reqs)
	assert.Contains(t, reqs[0], "list-type=2")
	assert.Contains(t, reqs[0], "Credential=OWNKEYID/")

	// The hub that asked is unaffected: it still reads URLs literally.
	require.NoError(t, EnsureExtension(context.Background(), s.DB(), "httpfs", "test"))
	requireURLParamsOff(t, s.DB())
}
