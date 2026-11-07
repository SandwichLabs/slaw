//go:build integration

package icebergtest_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
)

func scalar[T any](t *testing.T, db *sql.DB, q string) T {
	t.Helper()
	var v T
	require.NoError(t, db.QueryRowContext(context.Background(), q).Scan(&v), q)
	return v
}

func TestFixtureSeedAndRead(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	f.SetTableComment(t, "orders", "all the orders")

	db := f.Open(t)
	icebergtest.Exec(t, db, icebergtest.AttachSQL("cat", f.RESTURI, ", READ_ONLY, ACCESS_DELEGATION_MODE 'none'"))
	require.EqualValues(t, 300, scalar[int64](t, db, "SELECT count(*) FROM cat.ns.orders"))
	require.EqualValues(t, 10, scalar[int64](t, db, "SELECT count(*) FROM cat.ns.events"))
	require.EqualValues(t, 2, scalar[int64](t, db, "SELECT count(*) FROM iceberg_snapshots('cat.ns.events')"))
}

func TestCraftedLocalDataReadsSentinel(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	db := f.Open(t)

	// The threat: a table whose manifest names a local file reads that file.
	dir := t.TempDir() + "/d/"
	tbl := f.Craft(t, "hostl1", icebergtest.Hostile{DataDir: dir, NameMapping: true})
	require.Len(t, tbl.DataPaths, 1)
	icebergtest.WriteSentinelLocal(t, tbl.DataPaths[0], tbl.DataSizes[0])

	icebergtest.Exec(t, db, icebergtest.AttachSQL("cat", f.RESTURI, ", READ_ONLY, ACCESS_DELEGATION_MODE 'none'"))
	require.EqualValues(t, icebergtest.SentinelID, scalar[int64](t, db, "SELECT max(id) FROM cat.ns.hostl1"))
	require.Equal(t, icebergtest.SentinelRegion, scalar[string](t, db, "SELECT max(region) FROM cat.ns.hostl1"))
	// The seeded table is untouched.
	require.EqualValues(t, 300, scalar[int64](t, db, "SELECT count(*) FROM cat.ns.orders"))
}

func TestCraftedOtherBucket(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	db := f.Open(t)
	tbl := f.Craft(t, "hostb1", icebergtest.Hostile{DataDir: "s3://other/", NameMapping: true})
	f.WriteSentinelS3(t, tbl.DataPaths[0], tbl.DataSizes[0])
	require.True(t, strings.HasPrefix(tbl.DataPaths[0], "s3://other/"))
	icebergtest.Exec(t, db, icebergtest.AttachSQL("cat", f.RESTURI, ", READ_ONLY"))
	require.EqualValues(t, icebergtest.SentinelID, scalar[int64](t, db, "SELECT max(id) FROM cat.ns.hostb1"))
}

func TestCraftedManifestListLocalAndHTTP(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	db := f.Open(t)

	ml := f.Craft(t, "hostm1", icebergtest.Hostile{ManifestListDir: t.TempDir()})
	require.True(t, filepath.IsAbs(ml.ManifestList))

	srv := icebergtest.NewLogServer(t, t.TempDir())
	hp := f.Craft(t, "hosth1", icebergtest.Hostile{DataDir: srv.URL + "/", NameMapping: true})
	require.True(t, strings.HasPrefix(hp.DataPaths[0], "http://127.0.0.1:"))
	require.Empty(t, srv.Requests())
	icebergtest.Exec(t, db, icebergtest.AttachSQL("cat", f.RESTURI, ", READ_ONLY"))
	_, err := db.ExecContext(context.Background(), "SELECT max(id) FROM cat.ns.hosth1")
	require.Error(t, err) // the file is not served, but the request is logged
	require.NotEmpty(t, srv.Requests())
}

func TestBearerProxy(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	p := f.Proxy(t, icebergtest.ProxyOptions{RequireBearer: true})

	db := f.Open(t)
	_, err := db.ExecContext(context.Background(), icebergtest.AttachSQL("noauth", p.URL, ", READ_ONLY"))
	require.Error(t, err)

	icebergtest.Exec(t, db, "ATTACH 'warehouse' AS authed (TYPE iceberg, ENDPOINT "+session.QuoteLiteral(p.URL)+
		", TOKEN "+session.QuoteLiteral(icebergtest.BearerToken)+", READ_ONLY, ACCESS_DELEGATION_MODE 'none')")
	require.EqualValues(t, 300, scalar[int64](t, db, "SELECT count(*) FROM authed.ns.orders"))
}

func TestVendedCredentialsProxy(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	p := f.Proxy(t, icebergtest.ProxyOptions{Vended: f.VendedConfig()})

	// A DuckDB with no s3 secret of its own can only read through the vended one.
	db := f.Open(t)
	icebergtest.Exec(t, db, "DROP SECRET minio_it")
	icebergtest.Exec(t, db, icebergtest.AttachSQL("v", p.URL, ", READ_ONLY"))
	require.EqualValues(t, 300, scalar[int64](t, db, "SELECT count(*) FROM v.ns.orders"))
	require.Positive(t, scalar[int64](t, db, "SELECT count(*) FROM duckdb_secrets() WHERE type = 's3' AND provider = 'iceberg'"))
}

func TestCraftKinds(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	for i, h := range []icebergtest.Hostile{
		{DataDir: "file:///var/tmp/x/"},
		{DataDir: "rel/x/"},
		{DataDir: "az://other/"},
		{Location: "s3://"},
		{Location: "s3://other/", NameMapping: true},
	} {
		name := "kind" + string(rune(97+i)) + "1"
		tbl := f.Craft(t, name, h)
		require.Contains(t, tbl.MetadataLocation, "/ns/"+name+"/metadata/")
		if h.DataDir != "" {
			require.True(t, strings.HasPrefix(tbl.DataPaths[0], h.DataDir), tbl.DataPaths[0])
		}
	}
}
