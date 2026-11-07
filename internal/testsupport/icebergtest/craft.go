//go:build integration

package icebergtest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

const (
	// SentinelID is the id and amount of the sentinel row; SentinelRegion its region.
	SentinelID     = 424242
	SentinelRegion = "SENTINEL_LOCAL"

	// NameMapping is the schema.name-mapping.default of ns.orders; without it a
	// plain Parquet file that has no field ids reads back as NULLs.
	NameMapping = `[{"field-id":1,"names":["id"]},{"field-id":2,"names":["amount"]},{"field-id":3,"names":["region"]}]`

	sentinelSQL = "SELECT 424242::BIGINT AS id, 424242::BIGINT AS amount, 'SENTINEL_LOCAL' AS region"

	// craftSource is the seeded table every crafted table is copied from. Its
	// directory name is 6 characters long, and so must a crafted table's name
	// be: the Avro manifests are rewritten without an Avro library, by
	// same-length byte replacement of length-prefixed strings.
	craftSource = "orders"
)

// Hostile says what is wrong with a crafted table. Zero values leave the
// corresponding part as the seeded table has it.
type Hostile struct {
	// DataDir is the prefix of every data file's path in the manifest, ending
	// in "/": "/abs/dir/", "file:///abs/dir/", "rel/dir/", "http://127.0.0.1:1234/",
	// "s3://other/", "az://other/". The file name is padded so the new path has
	// exactly the old path's length (the manifest is edited in place); Craft
	// fails if DataDir is too long for that.
	DataDir string
	// ManifestListDir, a local directory, is where the manifest list is placed
	// and what metadata.json's snapshot names (an absolute local path).
	ManifestListDir string
	// Location overrides the table's "location" in metadata.json.
	Location string
	// NameMapping adds schema.name-mapping.default (NameMapping).
	NameMapping bool
}

// Table is a registered crafted table, ns.<Name>.
type Table struct {
	Name string
	// MetadataLocation is the s3:// URL of its metadata.json.
	MetadataLocation string
	// DataPaths are the data file paths its manifest now holds, in the order of
	// the seeded table's files. Put a file at each (WriteSentinelLocal,
	// WriteSentinelS3) for the table to read as anything but an error.
	DataPaths []string
	// DataSizes are the byte sizes the manifest records for DataPaths. The
	// sentinel writers pad their Parquet file to the size, because DuckDB
	// range-reads by the recorded size and refuses a file of another length.
	DataSizes []int64
	// ManifestList is the manifest list location metadata.json names.
	ManifestList string
}

func s3url(bucket, key string) string { return "s3://" + bucket + "/" + key }

// ReadObject returns the object at an s3:// URL.
func (f *Fixture) ReadObject(t testing.TB, db *sql.DB, url string) []byte {
	t.Helper()
	var b []byte
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT content FROM read_blob("+session.QuoteLiteral(url)+")").Scan(&b), url)
	return b
}

// PutObject writes data to an s3:// URL through DuckDB (no extra image needed).
func (f *Fixture) PutObject(t testing.TB, url string, data []byte) {
	t.Helper()
	db := f.shared(t)
	local := filepath.Join(t.TempDir(), "obj")
	require.NoError(t, os.WriteFile(local, data, 0o600))
	Exec(t, db, "COPY (SELECT content FROM read_blob("+session.QuoteLiteral(local)+")) TO "+
		session.QuoteLiteral(url)+" (FORMAT blob)")
}

// List returns the s3:// URLs matching a glob.
func (f *Fixture) List(t testing.TB, db *sql.DB, glob string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT file FROM glob("+session.QuoteLiteral(glob)+") ORDER BY 1")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// sentinelBytes builds the sentinel Parquet file (id=424242, amount=424242,
// region='SENTINEL_LOCAL', no field ids). When size > 0 the file is padded, with
// a key-value metadata entry, to exactly that many bytes.
func sentinelBytes(t testing.TB, size int64) []byte {
	t.Helper()
	db := openPlain(t)
	p := filepath.Join(t.TempDir(), "s.parquet")
	write := func(pad int) int64 {
		opt := ""
		if pad > 0 {
			opt = ", KV_METADATA {pad: " + session.QuoteLiteral(strings.Repeat("x", pad)) + "}"
		}
		Exec(t, db, "COPY ("+sentinelSQL+") TO "+session.QuoteLiteral(p)+" (FORMAT parquet"+opt+")")
		st, err := os.Stat(p)
		require.NoError(t, err)
		return st.Size()
	}
	got := write(0)
	if size > 0 {
		require.GreaterOrEqual(t, size, got, "the manifest's file size %d is smaller than the sentinel Parquet (%d)", size, got)
		pad := int(size - got)
		for i := 0; i < 5 && got != size; i++ {
			got = write(pad)
			pad += int(size - got)
		}
		require.Equal(t, size, got, "could not pad the sentinel Parquet to %d bytes", size)
	}
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return b
}

// WriteSentinelLocal writes the sentinel Parquet file at a local path (a file://
// prefix is accepted), creating directories; size, when > 0, is the length to
// pad it to (Table.DataSizes).
func WriteSentinelLocal(t testing.TB, p string, size int64) {
	t.Helper()
	p = strings.TrimPrefix(p, "file://")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, sentinelBytes(t, size), 0o600))
}

// WriteSentinelS3 writes the sentinel Parquet file at an s3:// URL (a hostile
// table reaches the Other bucket this way), padded to size when size > 0.
func (f *Fixture) WriteSentinelS3(t testing.TB, url string, size int64) {
	t.Helper()
	f.PutObject(t, url, sentinelBytes(t, size))
}

// ObjectSize is the size of the object at an s3:// URL.
func (f *Fixture) ObjectSize(t testing.TB, db *sql.DB, url string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT size FROM read_blob("+session.QuoteLiteral(url)+")").Scan(&n), url)
	return n
}

func openPlain(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
