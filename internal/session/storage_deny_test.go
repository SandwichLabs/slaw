package session

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// --- no extension needed -------------------------------------------------

func TestDenyStatements(t *testing.T) {
	stmts := denyStatements()
	require.Len(t, stmts, 4)
	want := map[string][]string{
		"slaw-deny-s3":  {"TYPE s3", "SCOPE 's3'", "REGION 'us-east-1'"},
		"slaw-deny-r2":  {"TYPE r2", "SCOPE 'r2'", "REGION 'auto'"},
		"slaw-deny-gcs": {"TYPE gcs", "SCOPE 'gcs'", "REGION 'auto'"},
		"slaw-deny-gs":  {"TYPE gcs", "SCOPE 'gs'", "REGION 'auto'"},
	}
	for _, st := range stmts {
		matched := false
		for name, parts := range want {
			if !strings.Contains(st, `"`+name+`"`) {
				continue
			}
			matched = true
			for _, p := range parts {
				assert.Contains(t, st, p, name)
			}
		}
		assert.True(t, matched, st)
		assert.Contains(t, st, "CREATE TEMPORARY SECRET IF NOT EXISTS")
		assert.Contains(t, st, "KEY_ID '', SECRET '', SESSION_TOKEN ''", "nothing host-derived can fill in")
		assert.Contains(t, st, "ENDPOINT 'deny.invalid'")
		assert.NotContains(t, st, "://", "the scope is the bare scheme, shorter than any real scope")
	}
}

// fakeDriver records every statement and says httpfs is loaded.
type fakeDriver struct {
	mu     sync.Mutex
	stmts  []string
	failOn string            // an Exec starting with this fails ("network unreachable")
	http   map[string]string // http_timeout / http_retries as last set
	sealed bool              // SET GLOBAL s3_url_compatibility_mode = true was run
}

func (f *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{f}, nil }
func (f *fakeDriver) record(q string) {
	f.mu.Lock()
	f.stmts = append(f.stmts, q)
	f.mu.Unlock()
}
func (f *fakeDriver) sealCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.stmts {
		if strings.HasPrefix(s, "SET GLOBAL "+urlParamSetting+" = true") {
			n++
		}
	}
	return n
}
func (f *fakeDriver) denyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.stmts {
		if strings.Contains(s, "slaw-deny-") {
			n++
		}
	}
	return n
}

type fakeConn struct{ f *fakeDriver }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.f.record(q)
	if strings.HasPrefix(q, "SET GLOBAL "+urlParamSetting) {
		c.f.mu.Lock()
		c.f.sealed = true
		c.f.mu.Unlock()
	}
	for _, name := range []string{httpTimeoutSetting, httpRetriesSetting} {
		if strings.HasPrefix(q, "SET GLOBAL "+name+" = ") {
			c.f.mu.Lock()
			if c.f.http == nil {
				c.f.http = map[string]string{}
			}
			c.f.http[name] = strings.TrimPrefix(q, "SET GLOBAL "+name+" = ")
			c.f.mu.Unlock()
		}
	}
	if c.f.failOn != "" && strings.HasPrefix(q, c.f.failOn) {
		return nil, errors.New("network unreachable")
	}
	return driver.RowsAffected(0), nil
}
func (c *fakeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "duckdb_extensions") {
		return &fakeRows{cols: []string{"installed", "loaded"}, vals: []driver.Value{true, true}}, nil
	}
	if strings.Contains(q, "current_setting") {
		c.f.mu.Lock()
		defer c.f.mu.Unlock()
		for _, name := range []string{httpTimeoutSetting, httpRetriesSetting} {
			if strings.Contains(q, "'"+name+"'") {
				v := c.f.http[name]
				if v == "" {
					v = "0"
				}
				return &fakeRows{cols: []string{"v"}, vals: []driver.Value{v}}, nil
			}
		}
		return &fakeRows{cols: []string{"v"}, vals: []driver.Value{strconv.FormatBool(c.f.sealed)}}, nil
	}
	return &fakeRows{cols: []string{"v"}, vals: []driver.Value{""}}, nil
}

type fakeRows struct {
	cols []string
	vals []driver.Value
	done bool
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.vals)
	return nil
}

var fakeSeq int

func newFake(t *testing.T) (*fakeDriver, *sql.DB) {
	t.Helper()
	f := &fakeDriver{}
	fakeSeq++
	name := "fake-storage-" + strings.Repeat("x", fakeSeq)
	sql.Register(name, f)
	db, err := sql.Open(name, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return f, db
}

type fakeStorageType struct{}

func (fakeStorageType) Name() string                     { return "fakestore" }
func (fakeStorageType) Validate(Spec) error              { return nil }
func (fakeStorageType) AttachSQL(Spec) ([]string, error) { return nil, nil }
func (fakeStorageType) Remote() bool                     { return false }
func (fakeStorageType) Extensions() []string             { return []string{"httpfs"} }
func (fakeStorageType) NeedsCredential(Spec) bool        { return false }
func (fakeStorageType) SecretSteps(Spec, secrets.Credential) ([]SecretStep, error) {
	return nil, nil
}

// CI-enforced without any extension: every place that can load httpfs runs the
// deny step. Removing a call fails one of these.
func TestEveryLoadPathRunsTheDenyStep(t *testing.T) {
	ctx := context.Background()
	t.Run("EnsureExtension", func(t *testing.T) {
		f, db := newFake(t)
		require.NoError(t, EnsureExtension(ctx, db, "httpfs", "test"))
		assert.Equal(t, len(denySecrets), f.denyCount())
		assert.Equal(t, 1, f.sealCount(), "URL parameters are turned off")
	})
	t.Run("applyHardening", func(t *testing.T) {
		f, db := newFake(t)
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer conn.Close()                            //nolint:errcheck
		_ = applyHardening(ctx, conn, newConfig(nil)) // read-back fails on the fake; the deny step ran first
		assert.Equal(t, len(denySecrets), f.denyCount())
		assert.Equal(t, 1, f.sealCount())
	})
	t.Run("readyForConnect", func(t *testing.T) {
		f, db := newFake(t)
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer conn.Close() //nolint:errcheck
		h := &hub{redactor: secrets.NewRedactor()}
		_, kind, _, err := h.readyForConnect(ctx, conn, &entry{}, fakeStorageType{})
		require.NoError(t, err)
		assert.Equal(t, StatusOK, kind)
		assert.Equal(t, len(denySecrets), f.denyCount())
		assert.Equal(t, 1, f.sealCount())
	})
}

// --- needs httpfs for the running DuckDB version -------------------------

// hostExtensionDir returns an extension directory that already holds httpfs for
// the exact DuckDB version and platform running, so nothing is downloaded. It
// skips when there is none, and fails when CI=true (CI installs it).
func hostExtensionDir(t *testing.T) string {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck
	var ver, platform string
	require.NoError(t, db.QueryRow("SELECT library_version, platform FROM pragma_version(), pragma_platform()").Scan(&ver, &platform))
	appDir, _ := DefaultDirs()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{appDir, filepath.Join(home, ".duckdb", "extensions")} {
		if _, err := os.Stat(filepath.Join(dir, ver, platform, "httpfs.duckdb_extension")); err == nil {
			return dir
		}
	}
	if os.Getenv("CI") == "true" {
		t.Fatalf("httpfs %s/%s is not installed; CI installs it before the tests", ver, platform)
	}
	t.Skipf("httpfs %s/%s is not installed; skipping", ver, platform)
	return ""
}

type authLog struct {
	mu   sync.Mutex
	auth []string
	tok  []string
}

func (a *authLog) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.auth...)
}
func (a *authLog) tokens() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.tok...)
}

func fakeS3(t *testing.T) (*httptest.Server, *authLog) {
	t.Helper()
	l := &authLog{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.auth = append(l.auth, r.Header.Get("Authorization"))
		if v := r.Header.Get("X-Amz-Security-Token"); v != "" {
			l.tok = append(l.tok, v)
		}
		l.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(s.Close)
	return s, l
}

func whichSecret(t *testing.T, s Session, url, kind string) string {
	t.Helper()
	var name sql.NullString
	require.NoError(t, s.DB().QueryRow("SELECT name FROM which_secret("+QuoteLiteral(url)+", "+QuoteLiteral(kind)+")").Scan(&name))
	return name.String
}

func setHostAWSEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "HOSTKEYID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "HOSTSECRETVALUE")
	t.Setenv("AWS_SESSION_TOKEN", "HOSTSESSIONTOKEN")
	t.Setenv("AWS_REGION", "eu-west-1")
}

// #135 regression. Before any uncovered read, which_secret must already name
// the deny secret, so a regression fails here before a request is built.
func TestHostAWSEnvNeverSignsAnUncoveredRead(t *testing.T) {
	ext := hostExtensionDir(t)
	srv, log := fakeS3(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	setHostAWSEnv(t)

	s, err := New(WithExtensionDirectory(ext))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	require.NoError(t, EnsureExtension(ctx, s.DB(), "httpfs", "test"))

	read := func(url string) error {
		_, err := s.DB().ExecContext(ctx, "SELECT count(*) FROM read_parquet('"+url+"')")
		return err
	}
	for _, c := range []struct{ url, kind, secret string }{
		{"s3://other/x.parquet", "s3", "slaw-deny-s3"}, {"s3a://other/x.parquet", "s3", "slaw-deny-s3"},
		{"s3n://other/x.parquet", "s3", "slaw-deny-s3"}, {"r2://other/x.parquet", "r2", "slaw-deny-r2"},
		{"gcs://other/x.parquet", "gcs", "slaw-deny-gcs"}, {"gs://other/x.parquet", "gcs", "slaw-deny-gs"},
	} {
		require.Equal(t, c.secret, whichSecret(t, s, c.url, c.kind), "%s must be matched by the deny secret before it is read", c.url)
		err := read(c.url)
		require.Error(t, err, c.url)
		assert.Contains(t, err.Error(), denyEndpoint, c.url)
		assert.NotContains(t, err.Error(), "HOSTKEYID", c.url)
	}
	assert.Empty(t, log.all(), "an uncovered read sent nothing anywhere")

	// A URL's own ?s3_* parameters can redirect the request. The deny secret
	// carries no host token or region, so even then nothing host-derived is sent.
	q := "s3://other/x.parquet?s3_endpoint=" + host + "&s3_use_ssl=false&s3_url_style=path&s3_access_key_id=Q&s3_secret_access_key=Q"
	require.Equal(t, "slaw-deny-s3", whichSecret(t, s, q, "s3"))
	_ = read(q)
	assert.Empty(t, log.tokens(), "the host session token never reaches an endpoint a URL chose")
	for _, a := range log.all() {
		assert.NotContains(t, a, "eu-west-1", "nor the host region")
	}

	// A scoped secret still wins for its own URLs, and signs with its own key.
	require.NoError(t, s.CreateTemporarySecret(ctx, "slaw_lake", "s3", []SecretParam{
		{Key: "SCOPE", Value: "s3://mine/"}, {Key: "KEY_ID", Value: "OWNKEYID"},
		{Key: "SECRET", Value: "OWNSECRET", Secret: true}, {Key: "SESSION_TOKEN", Value: "", Secret: true},
		{Key: "REGION", Value: "us-east-1"},
		{Key: "ENDPOINT", Value: host}, {Key: "URL_STYLE", Value: "path"}, {Key: "USE_SSL", Value: false},
	}))
	require.Equal(t, "slaw_lake", whichSecret(t, s, "s3://mine/x.parquet", "s3"))
	require.Error(t, read("s3://mine/x.parquet"), "the fake server answers 403")
	got := log.all()
	require.NotEmpty(t, got, "the covered read reached its own endpoint")
	for _, a := range got {
		if strings.Contains(a, "Credential=Q/") {
			continue // the query-parameter request above
		}
		assert.Contains(t, a, "Credential=OWNKEYID/")
	}
	assert.Empty(t, log.tokens())
	require.Equal(t, "slaw-deny-s3", whichSecret(t, s, "s3://mine2/x.parquet", "s3"), "a sibling bucket is uncovered")
}

func TestDenyScopeIsShorterThanAnyRealScope(t *testing.T) {
	ext := hostExtensionDir(t)
	srv, log := fakeS3(t)
	s, err := New(WithExtensionDirectory(ext))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	require.NoError(t, EnsureExtension(ctx, s.DB(), "httpfs", "test"))
	require.NoError(t, s.CreateTemporarySecret(ctx, "slaw_all", "s3", []SecretParam{
		{Key: "SCOPE", Value: "s3://"}, {Key: "KEY_ID", Value: "OWNKEYID"}, {Key: "SECRET", Value: "OWNSECRET", Secret: true},
		{Key: "REGION", Value: "us-east-1"}, {Key: "ENDPOINT", Value: strings.TrimPrefix(srv.URL, "http://")},
		{Key: "URL_STYLE", Value: "path"}, {Key: "USE_SSL", Value: false},
	}))
	require.Equal(t, "slaw_all", whichSecret(t, s, "s3://anything/x.parquet", "s3"))
	_, err = s.DB().ExecContext(ctx, "SELECT count(*) FROM read_parquet('s3://anything/x.parquet')")
	require.Error(t, err)
	assert.NotEmpty(t, log.all(), "the whole-scheme connection won")
}

func TestDenySecretsExistAsSoonAsTheSessionOpens(t *testing.T) {
	ext := hostExtensionDir(t)
	s, err := New(WithExtensionDirectory(ext))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	var n int
	require.NoError(t, s.DB().QueryRow("SELECT count(*) FROM duckdb_secrets() WHERE name LIKE 'slaw-deny-%'").Scan(&n))
	assert.Equal(t, len(denySecrets), n, "httpfs is installed, so it is loaded and protected before the session is handed out")
	require.NoError(t, DenyUncoveredStorage(context.Background(), s.DB()))
}

// Concurrent first creation used to fail with a catalog write-write conflict.
func TestDenyUncoveredStorageConcurrentFirstCreation(t *testing.T) {
	ext := hostExtensionDir(t)
	ctx := context.Background()
	for round := 0; round < 10; round++ {
		db, err := sql.Open("duckdb", "")
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, "SET extension_directory="+QuoteLiteral(ext))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, "LOAD httpfs")
		require.NoError(t, err)
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				conn, err := db.Conn(ctx)
				if err != nil {
					errs <- err
					return
				}
				defer conn.Close() //nolint:errcheck
				errs <- DenyUncoveredStorage(ctx, conn)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		_ = db.Close()
	}
}

// A garbage httpfs file must not stop the session opening.
func TestNewSurvivesAnUnloadableHTTPFS(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	var ver, platform string
	require.NoError(t, db.QueryRow("SELECT library_version, platform FROM pragma_version(), pragma_platform()").Scan(&ver, &platform))
	_ = db.Close()
	dir := t.TempDir()
	d := filepath.Join(dir, ver, platform)
	require.NoError(t, os.MkdirAll(d, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(d, "httpfs.duckdb_extension"), []byte("not an extension"), 0o644))
	s, err := New(WithExtensionDirectory(dir))
	require.NoError(t, err, "a broken httpfs is reported when a connection needs it, not when the session opens")
	require.NoError(t, s.Close())
}
