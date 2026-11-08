//go:build integration

package connections_test

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

// minioLake builds a lake whose catalog ("duckdb" or "sqlite") is copied to
// s3://lake/<kind>/catalog.db and whose Parquet files are in s3://lake/<kind>/data/,
// on a MinIO at ep. It returns the local copy of the catalog (for tampering)
// and the plain connection that built it (detached).
func minioLake(t *testing.T, ep, kind string) (string, *sql.Conn) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	exec := func(s string) {
		t.Helper()
		_, err := conn.ExecContext(ctx, s)
		require.NoError(t, err, s)
	}
	exec("SET extension_directory = " + session.QuoteLiteral(extDir(t)))
	for _, e := range []string{"ducklake", "sqlite", "httpfs"} {
		exec("INSTALL " + e)
		exec("LOAD " + e)
	}
	exec("CREATE SECRET (TYPE s3, KEY_ID 'minioadmin', SECRET " + session.QuoteLiteral(minioSecret) + ", REGION 'us-east-1', ENDPOINT " +
		session.QuoteLiteral(ep) + ", URL_STYLE 'path', USE_SSL false)")
	cat := filepath.Join(t.TempDir(), "catalog.db")
	target := "ducklake:" + cat
	if kind == "sqlite" {
		target = "ducklake:sqlite:" + cat
	}
	for _, s := range []string{
		"ATTACH " + session.QuoteLiteral(target) + " AS l (DATA_PATH 's3://lake/" + kind + "/data/')",
		"CREATE TABLE l.people (id INTEGER, name VARCHAR)",
		"INSERT INTO l.people VALUES (1, 'ada'), (2, 'bob')",
		"CALL ducklake_flush_inlined_data('l')",
		"INSERT INTO l.people VALUES (3, 'cy')", // stays inlined in the catalog
		"DETACH l",
	} {
		exec(s)
	}
	return cat, conn
}

// uploadCatalog copies a local catalog file to s3://lake/<kind>/<name>.
func uploadCatalog(t *testing.T, conn *sql.Conn, cat, kind, name string) string {
	t.Helper()
	u := "s3://lake/" + kind + "/" + name
	_, err := conn.ExecContext(context.Background(), "COPY (SELECT content FROM read_blob("+session.QuoteLiteral(cat)+")) TO "+session.QuoteLiteral(u)+" (FORMAT blob)")
	require.NoError(t, err)
	return u
}

// lakeSession is a hub with the extensions installed and a storage connection
// "store" over the MinIO, connected (its secret is what a URL catalog is read
// with).
func lakeSession(t *testing.T, ep string) session.Session {
	t.Helper()
	ctx := context.Background()
	sess, err := session.New(session.WithConnectionTypes(connections.Types()...), session.WithExtensionDirectory(extDir(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	for _, e := range []string{"httpfs", "ducklake", "sqlite"} {
		require.NoError(t, sess.InstallExtension(ctx, e))
	}
	store := connections.Def{Name: "store", Type: "s3", Scope: "s3://lake/", Region: "us-east-1",
		Options: map[string]string{"endpoint": ep, "url_style": "path", "use_ssl": "false"}}
	spec := store.Spec()
	spec.Credential = func(context.Context) (secrets.Credential, error) {
		return secrets.Credential{KeyID: "minioadmin", Secret: minioSecret}, nil
	}
	require.NoError(t, sess.Attach(ctx, spec))
	return sess
}

func urlLakeDef(url, kind string) connections.Def {
	opts := map[string]string{"data_path": "s3://lake/" + kind + "/data/"}
	if kind == "sqlite" {
		opts["catalog"] = "sqlite"
	}
	return connections.Def{Name: "lake", Type: "ducklake", Path: url, ReadOnly: true, Options: opts}
}

func countPeople(t *testing.T, sess session.Session) int {
	t.Helper()
	var n int
	require.NoError(t, sess.DB().QueryRow("SELECT count(*) FROM lake.people").Scan(&n))
	return n
}

// leftovers counts what a refused attach must not leave behind.
func leftovers(t *testing.T, sess session.Session) int {
	t.Helper()
	var n int
	require.NoError(t, sess.DB().QueryRow("SELECT count(*) FROM duckdb_databases() WHERE database_name IN ('lake', '__ducklake_metadata_lake') OR database_name LIKE '__slaw_vet_%'").Scan(&n))
	return n
}

// A catalog in object storage, for both catalog kinds: vetted over httpfs with
// the storage connection's secret, attached read-only, read (parquet in the
// bucket plus rows inlined in the catalog). A write is refused. Crafted
// catalogs and a missing one are refused with nothing attached or left behind.
func TestDuckLakeURLCatalogOnMinIO(t *testing.T) {
	ep := startMinIO(t)
	ctx := context.Background()
	secretFile := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("TOP-SECRET"), 0o600))
	for _, kind := range []string{"duckdb", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			cat, conn := minioLake(t, ep, kind)
			url := uploadCatalog(t, conn, cat, kind, "catalog.db")
			sess := lakeSession(t, ep)
			def := urlLakeDef(url, kind)

			// Honest catalog.
			require.NoError(t, sess.Attach(ctx, def.Spec()), "vetted and attached")
			assert.Equal(t, 3, countPeople(t, sess))
			_, err := sess.DB().Exec("INSERT INTO lake.people VALUES (9, 'zed')")
			require.Error(t, err, "a URL catalog is read-only")
			require.NoError(t, sess.Detach(ctx, "lake"))
			assert.Zero(t, leftovers(t, sess))

			// Crafted catalogs, uploaded under other names.
			inlined := catalogInlined(t, conn, cat, kind)
			for name, stmts := range map[string][]string{
				"view replaces inlined data": attackCases(inlined, kind)["view replaces the inlined data table"],
				"view over data file":        attackCases(inlined, kind)["view over ducklake_data_file"],
				"registry splice":            attackCases(inlined, kind)["inlined registry splices a file read"],
				"extra table":                attackCases(inlined, kind)["extra unknown table"],
			} {
				bad, bconn := minioLake(t, ep, kind)
				sub := make([]string, len(stmts))
				for i, st := range stmts {
					sub[i] = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(st, "%SECRET%", secretFile), "%CAT%", bad), inlined, catalogInlined(t, bconn, bad, kind))
				}
				tamperCatalog(t, bconn, bad, kind, sub...)
				badURL := uploadCatalog(t, bconn, bad, kind, strings.ReplaceAll(name, " ", "_")+".db")
				err := sess.Attach(ctx, urlLakeDef(badURL, kind).Spec())
				require.Error(t, err, name)
				assert.Contains(t, err.Error(), "refusing", name)
				assert.NotContains(t, err.Error(), "TOP-SECRET", name)
				assert.Zero(t, leftovers(t, sess), name)
			}

			// A catalog that is not there is an error, not a new lake.
			err = sess.Attach(ctx, urlLakeDef("s3://lake/"+kind+"/missing.db", kind).Spec())
			require.Error(t, err)
			assert.Zero(t, leftovers(t, sess))

			// Test does not read it: the throwaway session has no storage secret.
			res := connections.Test(ctx, def, secrets.Credential{}, connections.WithInstall(true), connections.WithTestExtensionDir(extDir(t)))
			require.True(t, res.OK, res.Error)
			assert.Contains(t, res.Detail, "connect the lake from the rail")
		})
	}
}

// swapServer is an S3-ish endpoint (path style, no auth check) for one catalog
// object, s3://lake/x/catalog.db. It serves the honest bytes until bad() is
// true and the crafted bytes (with a new ETag, so a cache revalidates to them)
// after, counting the requests for the object.
type swapServer struct {
	honest, crafted []byte
	requests        atomic.Int64
	flipAfter       atomic.Int64 // serve crafted bytes once this many requests were made (0: never)
	flipped         atomic.Bool
}

func (s *swapServer) bad() bool {
	if s.flipped.Load() {
		return true
	}
	if n := s.flipAfter.Load(); n > 0 && s.requests.Load() > n {
		return true
	}
	return false
}

func (s *swapServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/x/catalog.db") {
		if r.URL.Query().Get("list-type") != "" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>lake</Name><KeyCount>0</KeyCount><IsTruncated>false</IsTruncated></ListBucketResult>`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
		return
	}
	s.requests.Add(1)
	b, tag, mod := s.honest, `"honest"`, time.Unix(1700000000, 0)
	if s.bad() {
		b, tag, mod = s.crafted, `"crafted"`, time.Unix(1700003600, 0)
	}
	w.Header().Set("ETag", tag)
	http.ServeContent(w, r, "catalog.db", mod, bytes.NewReader(b))
}

// A server that answers the catalog URL with different bytes after the first
// read must not get its bytes vetted-then-swapped into the lake: the catalog is
// copied once, and the copy is what is checked and attached. Whatever request
// the swap lands on, the lake either shows the honest rows or is refused, and
// the crafted catalog's file read never happens; and after the attach, a swap
// changes nothing until the next connect, which reads (and vets) the new bytes.
func TestDuckLakeURLCatalogIsSnapshotOnce(t *testing.T) {
	ctx := context.Background()
	secretFile := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("TOP-SECRET"), 0o600))
	for _, kind := range []string{"duckdb", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			honest := filepath.Join(dir, "honest.db")
			db, err := sql.Open("duckdb", "")
			require.NoError(t, err)
			db.SetMaxOpenConns(1)
			defer db.Close() //nolint:errcheck
			conn, err := db.Conn(ctx)
			require.NoError(t, err)
			target := "ducklake:" + honest
			if kind == "sqlite" {
				target = "ducklake:sqlite:" + honest
			}
			for _, s := range []string{"SET extension_directory = " + session.QuoteLiteral(extDir(t)), "LOAD ducklake", "LOAD sqlite",
				"ATTACH " + session.QuoteLiteral(target) + " AS l (DATA_PATH 's3://lake/x/data/')",
				"CREATE TABLE l.people (id INTEGER, name VARCHAR)", "INSERT INTO l.people VALUES (1, 'ada')", "DETACH l"} {
				_, err := conn.ExecContext(ctx, s)
				require.NoError(t, err, s)
			}
			crafted := filepath.Join(dir, "crafted.db")
			b, err := os.ReadFile(honest)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(crafted, b, 0o600))
			stmts := attackCases(catalogInlined(t, conn, crafted, kind), kind)["view replaces the inlined data table"]
			for i := range stmts {
				stmts[i] = strings.ReplaceAll(stmts[i], "%SECRET%", secretFile)
			}
			tamperCatalog(t, conn, crafted, kind, stmts...)
			craftedBytes, err := os.ReadFile(crafted)
			require.NoError(t, err)

			read := func(sess session.Session) []string {
				rows, err := sess.DB().Query("SELECT name FROM lake.people ORDER BY id")
				require.NoError(t, err)
				defer rows.Close() //nolint:errcheck
				var out []string
				for rows.Next() {
					var n sql.NullString
					require.NoError(t, rows.Scan(&n))
					out = append(out, n.String)
				}
				return out
			}
			spec := func(ep string) (session.Session, session.Spec) {
				sess := lakeSession(t, ep)
				d := urlLakeDef("s3://lake/x/catalog.db", kind)
				d.Options["data_path"] = "s3://lake/x/data/"
				return sess, d.Spec()
			}
			serve := func() (*swapServer, string) {
				srv := &swapServer{honest: b, crafted: craftedBytes}
				ts := httptest.NewServer(srv)
				t.Cleanup(ts.Close)
				return srv, strings.TrimPrefix(ts.URL, "http://")
			}

			// The swap lands on request 1, 2, 3...: never the crafted lake.
			for after := int64(1); after <= 6; after++ {
				srv, ep := serve()
				srv.flipAfter.Store(after)
				sess, sp := spec(ep)
				if err := sess.Attach(ctx, sp); err == nil {
					assert.NotContains(t, strings.Join(read(sess), ","), "TOP-SECRET", "swap after %d", after)
				} else {
					assert.NotContains(t, err.Error(), "TOP-SECRET")
				}
			}

			// The swap after the attach changes nothing; a reconnect picks it up and refuses it.
			srv, ep := serve()
			sess, sp := spec(ep)
			require.NoError(t, sess.Attach(ctx, sp))
			srv.flipped.Store(true)
			assert.Equal(t, []string{"ada"}, read(sess), "the lake reflects the catalog as of the connect")
			require.NoError(t, sess.Detach(ctx, "lake"))
			err = sess.Attach(ctx, sp)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing")
			assert.Zero(t, leftovers(t, sess))
		})
	}
}

// Sidecars a copy would miss, and an over-sized catalog, are refused; the
// snapshot is removed on detach and when the session closes.
func TestDuckLakeURLCatalogSnapshotRules(t *testing.T) {
	ep := startMinIO(t)
	ctx := context.Background()
	cat, conn := minioLake(t, ep, "sqlite")
	url := uploadCatalog(t, conn, cat, "sqlite", "catalog.db")
	for _, suffix := range []string{".wal", "-wal", "-journal"} {
		name := "sidecar" + strings.TrimLeft(suffix, ".-") + ".db"
		u := uploadCatalog(t, conn, cat, "sqlite", name)
		uploadCatalog(t, conn, cat, "sqlite", name+suffix)
		sess := lakeSession(t, ep)
		err := sess.Attach(ctx, urlLakeDef(u, "sqlite").Spec())
		require.Error(t, err, suffix)
		assert.Contains(t, err.Error(), "refusing", suffix)
		require.NoError(t, sess.Close())
	}

	restore := connections.SetMaxCatalogBytes(1024)
	sess := lakeSession(t, ep)
	err := sess.Attach(ctx, urlLakeDef(url, "sqlite").Spec())
	restore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")

	// Honest again: the snapshot exists while attached and is gone after detach / close.
	sess = lakeSession(t, ep)
	require.NoError(t, sess.Attach(ctx, urlLakeDef(url, "sqlite").Spec()))
	var path string
	require.NoError(t, sess.DB().QueryRow("SELECT path FROM duckdb_databases() WHERE database_name = 'lake'").Scan(&path))
	path = strings.TrimPrefix(path, "sqlite:")
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "the lake is attached from a local snapshot")
	assert.NotContains(t, path, "s3://")
	info, _ := os.Stat(path)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoError(t, sess.Detach(ctx, "lake"))
	_, statErr = os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "removed on detach")
	require.NoError(t, sess.Attach(ctx, urlLakeDef(url, "sqlite").Spec()))
	require.NoError(t, sess.DB().QueryRow("SELECT path FROM duckdb_databases() WHERE database_name = 'lake'").Scan(&path))
	path = strings.TrimPrefix(path, "sqlite:")
	require.NoError(t, sess.Close())
	_, statErr = os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "removed on close")
}

// A server that reports a small size to the first request and a huge one to the
// next must not get the huge object downloaded: the size and the content come
// from one request, and an over-cap object is not fetched.
func TestDuckLakeURLCatalogSizeIsReadOnce(t *testing.T) {
	ctx := context.Background()
	var requests, served, claimed atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/x/catalog.db") {
			if r.URL.Query().Get("list-type") != "" {
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>lake</Name><KeyCount>0</KeyCount><IsTruncated>false</IsTruncated></ListBucketResult>`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// The size a HEAD reports grows after the first one; a GET serves what the
		// latest HEAD claimed.
		if r.Method == http.MethodHead {
			if requests.Add(1) == 1 {
				claimed.Store(1000)
			} else {
				claimed.Store(100 << 20)
			}
		}
		size := claimed.Load()
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"x"`)
		if r.Method == http.MethodHead {
			return
		}
		zeros := make([]byte, 64<<10)
		for sent := int64(0); sent < size; {
			n := int64(len(zeros))
			if size-sent < n {
				n = size - sent
			}
			if _, err := w.Write(zeros[:n]); err != nil {
				return
			}
			served.Add(n)
			sent += n
		}
	}))
	defer ts.Close()
	restore := connections.SetMaxCatalogBytes(4096)
	defer restore()
	sess := lakeSession(t, strings.TrimPrefix(ts.URL, "http://"))
	d := urlLakeDef("s3://lake/x/catalog.db", "sqlite")
	d.Options["data_path"] = "s3://lake/x/data/"
	err := sess.Attach(ctx, d.Spec())
	require.Error(t, err)
	t.Logf("attach: %v; requests %d, served %d", err, requests.Load(), served.Load())
	assert.Less(t, served.Load(), int64(1<<20), "the over-cap object was not downloaded")
}

// The lake is connected FIRST, with its storage connection only registered: the
// hub brings "store" up (Spec.Requires), then vets and attaches the catalog.
// With the store's credentials missing the lake says so, not an S3 error.
func TestDuckLakeConnectsItsStorageFirstOnMinIO(t *testing.T) {
	ep := startMinIO(t)
	ctx := context.Background()
	cat, conn := minioLake(t, ep, "sqlite")
	url := uploadCatalog(t, conn, cat, "sqlite", "catalog.db")

	sess, err := session.New(session.WithConnectionTypes(connections.Types()...), session.WithExtensionDirectory(extDir(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	for _, e := range []string{"httpfs", "ducklake", "sqlite"} {
		require.NoError(t, sess.InstallExtension(ctx, e))
	}
	var have atomic.Bool
	store := connections.Def{Name: "store", Type: "s3", Scope: "s3://lake/", Region: "us-east-1",
		Options: map[string]string{"endpoint": ep, "url_style": "path", "use_ssl": "false"}}.Spec()
	store.Credential = func(context.Context) (secrets.Credential, error) {
		if !have.Load() {
			return secrets.Credential{}, &secrets.ErrNeedsCredentials{Method: "keychain", Reason: "not set"}
		}
		return secrets.Credential{KeyID: "minioadmin", Secret: minioSecret}, nil
	}
	require.NoError(t, sess.Register(store))
	lake := urlLakeDef(url, "sqlite").Spec()
	lake.Requires = []string{"store"}
	require.NoError(t, sess.Register(lake))

	err = sess.Connect(ctx, "lake")
	require.Error(t, err)
	st, _ := sess.Status("lake")
	assert.Equal(t, session.StatusNeedsCredentials, st.Kind)
	assert.Equal(t, `needs the "store" connection: it needs credentials`, st.Message)
	assert.NotContains(t, err.Error(), minioSecret)

	have.Store(true)
	sess.Preflight(ctx, "store")
	require.NoError(t, sess.ConnectReferenced(ctx, "", "SELECT count(*) FROM lake.people"))
	assert.Equal(t, 3, countPeople(t, sess))
	st, _ = sess.Status("store")
	assert.Equal(t, session.StatusOK, st.Kind)
}
