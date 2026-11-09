package session

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tableType is a catalog-less connection type for the tests: a storage type
// (it has a scope) or a table type (it has a location), by name.
type tableType struct {
	name  string
	scope bool
}

func (t tableType) Name() string                     { return t.name }
func (t tableType) Remote() bool                     { return true }
func (t tableType) Catalogless() bool                { return true }
func (t tableType) StorageScope() bool               { return t.scope }
func (t tableType) Validate(Spec) error              { return nil }
func (t tableType) AttachSQL(Spec) ([]string, error) { return nil, nil }

// snapHub is an unhardened hub over a bare in-memory DuckDB (so the tests can
// load the extensions installed on this computer and define stand-ins for
// functions an extension would provide) with the test types registered.
func snapHub(t *testing.T) (*hub, *sql.DB) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s := UnhardenedForTests(db, WithConnectionTypes(
		tableType{name: "s3", scope: true}, tableType{name: "iceberg"}, tableType{name: "delta"}))
	h, ok := s.(*hub)
	require.True(t, ok)
	return h, db
}

// addConn puts a connection of the given state in the hub without dialling.
func addConn(h *hub, spec Spec, kind StatusKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[spec.Name] = &entry{spec: spec, status: Status{Name: spec.Name, Kind: kind}, attach: kind == StatusOK}
}

func snapshotOf(t *testing.T, got []ConnectionSnapshot, name string) *Snapshot {
	t.Helper()
	for _, c := range got {
		if c.Info.Name == name {
			return c.Snapshot
		}
	}
	require.Failf(t, "connection not listed", "%s in %+v", name, got)
	return nil
}

func TestSnapshot_DuckDBFileIsSizeAndMtimeAndNeverThePath(t *testing.T) {
	h, _ := snapHub(t)
	path := mkDuckFile(t, "data", "CREATE TABLE t(x INT)")
	addConn(h, Spec{Name: "data", Type: "duckdb", Path: path}, StatusOK)

	got := SnapshotConnections(context.Background(), h, "data", "SELECT * FROM data.main.t")
	sn := snapshotOf(t, got, "data")
	require.NotNil(t, sn)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, "file size and mtime", sn.Fact)
	assert.Equal(t, SnapshotLiveOnly, sn.Label)
	assert.Equal(t, fmt.Sprintf("%d bytes, modified %s", fi.Size(), fi.ModTime().UTC().Format(time.RFC3339)), sn.Value)
	assert.NotContains(t, sn.Fact+sn.Value, filepath.Dir(path))
	assert.NotContains(t, sn.Fact+sn.Value, "data.duckdb")
}

func TestSnapshot_InMemoryAndTypesWithNoFactAreStatusOnly(t *testing.T) {
	h, _ := snapHub(t)
	addConn(h, Spec{Name: "mem", Type: "duckdb"}, StatusOK)
	addConn(h, Spec{Name: "lite", Type: "sqlite", Path: "/does/not/matter.db"}, StatusOK)
	addConn(h, Spec{Name: "md", Type: "motherduck"}, StatusOK)
	addConn(h, Spec{Name: "my", Type: "mysql"}, StatusOK)

	got := SnapshotConnections(context.Background(), h, "mem", "SELECT * FROM lite.t JOIN md.t USING (a) JOIN my.t USING (a)")
	require.Len(t, got, 4)
	for _, c := range got {
		require.NotNil(t, c.Snapshot, c.Info.Name)
		assert.Equal(t, "status", c.Snapshot.Fact, c.Info.Name)
		assert.Equal(t, SnapshotLiveOnly, c.Snapshot.Label, c.Info.Name)
		assert.NotContains(t, c.Snapshot.Value, "matter.db")
	}
}

func TestSnapshot_ANotAttachedConnectionIsSkippedAndNeverConnected(t *testing.T) {
	h, db := snapHub(t)
	// A pg connection that would fail loudly if anything asked it a question.
	_, err := db.Exec(`CREATE MACRO postgres_query(n, q) AS TABLE SELECT error('asked a connection that is not attached') AS v`)
	require.NoError(t, err)
	addConn(h, Spec{Name: "pg", Type: "postgres"}, StatusNotConnected)

	got := SnapshotConnections(context.Background(), h, "pg", "SELECT 1")
	require.Len(t, got, 1)
	assert.Equal(t, StatusNotConnected, got[0].Info.Kind)
	assert.Nil(t, got[0].Snapshot, "a connection that is not attached is not asked")
	st, _ := h.Status("pg")
	assert.Equal(t, StatusNotConnected, st.Kind, "collecting never connects")
}

func TestSnapshot_PostgresIsTheWalPositionAndLiveOnly(t *testing.T) {
	h, db := snapHub(t)
	// DuckDB's postgres extension is not loaded here: a stand-in with its shape.
	_, err := db.Exec(`CREATE MACRO postgres_query(n, q) AS TABLE SELECT '0/16B3748' AS v`)
	require.NoError(t, err)
	addConn(h, Spec{Name: "pg", Type: "postgres"}, StatusOK)

	sn := snapshotOf(t, SnapshotConnections(context.Background(), h, "pg", "SELECT 1"), "pg")
	require.NotNil(t, sn)
	assert.Equal(t, SnapshotLiveOnly, sn.Label)
	assert.Equal(t, "pg_current_wal_lsn()", sn.Fact)
	assert.Equal(t, "0/16B3748", sn.Value)

	// Anything that is not a position is not recorded (a hostile server).
	_, err = db.Exec(`CREATE OR REPLACE MACRO postgres_query(n, q) AS TABLE SELECT 'password=hunter2' AS v`)
	require.NoError(t, err)
	sn = snapshotOf(t, SnapshotConnections(context.Background(), h, "pg", "SELECT 1"), "pg")
	assert.Equal(t, SnapshotNotRecorded, sn.Label)
	assert.NotContains(t, sn.Value, "hunter2")
}

func TestSnapshot_DuckLakeIsItsCurrentSnapshotAndFollowsAWrite(t *testing.T) {
	h, db := snapHub(t)
	dir := t.TempDir()
	for _, q := range []string{
		"LOAD ducklake", "LOAD sqlite",
		"ATTACH 'ducklake:sqlite:" + filepath.Join(dir, "meta.sqlite") + "' AS lake (DATA_PATH '" + filepath.Join(dir, "files") + "')",
		"CREATE TABLE lake.t AS SELECT 1 AS a",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Skipf("the ducklake extension is not installed here: %v", err)
		}
	}
	addConn(h, Spec{Name: "lake", Type: "ducklake"}, StatusOK)

	first := snapshotOf(t, SnapshotConnections(context.Background(), h, "lake", "SELECT * FROM lake.t"), "lake")
	require.NotNil(t, first)
	assert.Equal(t, "ducklake_current_snapshot()", first.Fact)
	assert.Equal(t, SnapshotReplayable, first.Label)
	assert.Regexp(t, `^[0-9]+$`, first.Value)

	_, err := db.Exec("INSERT INTO lake.t VALUES (2)")
	require.NoError(t, err)
	second := snapshotOf(t, SnapshotConnections(context.Background(), h, "lake", "SELECT * FROM lake.t"), "lake")
	assert.NotEqual(t, first.Value, second.Value, "a commit moves the snapshot")
	assert.NotContains(t, second.Value, dir)
}

func TestSnapshot_IcebergTableIsTheNewestSnapshotOfTheConnectionsOwnLocation(t *testing.T) {
	h, db := snapHub(t)
	loc := t.TempDir()
	// The iceberg extension is not loaded here: a stand-in with its shape that
	// answers only for the connection's own location and fails for any other.
	_, err := db.Exec(`CREATE MACRO iceberg_snapshots(p) AS TABLE
		SELECT * FROM (VALUES (1, 111::BIGINT), (2, 222::BIGINT)) AS s(sequence_number, snapshot_id)
		WHERE CASE WHEN p = '` + loc + `' THEN true ELSE error('read a location that is not the connection''s') END`)
	require.NoError(t, err)
	addConn(h, Spec{Name: "ice", Type: "iceberg", Path: loc}, StatusOK)

	got := SnapshotConnections(context.Background(), h, "", "SELECT * FROM iceberg_scan('"+loc+"') WHERE a = '/warehouse/other'")
	sn := snapshotOf(t, got, "ice")
	require.NotNil(t, sn)
	assert.Equal(t, "iceberg snapshot id", sn.Fact)
	assert.Equal(t, "222", sn.Value)
	assert.Equal(t, SnapshotReplayable, sn.Label)

	// A statement that does not name the table does not list it.
	for _, c := range SnapshotConnections(context.Background(), h, "", "SELECT 1") {
		assert.NotEqual(t, "ice", c.Info.Name)
	}

	// A literal cannot steer the collector: one with a .. segment does not even
	// list the connection, and a target built with such a literal still reads
	// only the connection's own location.
	for _, lit := range []string{loc + "/../../../etc/passwd", "/warehouse/orders/../../etc/passwd"} {
		for _, c := range SnapshotConnections(context.Background(), h, "", "SELECT * FROM iceberg_scan('"+lit+"')") {
			assert.NotEqual(t, "ice", c.Info.Name, lit)
		}
	}
	steered := collectAll(context.Background(), h, []snapTarget{{
		info: ConnectionInfo{Name: "ice", Type: "iceberg", Kind: StatusOK}, spec: Spec{Path: loc}, urls: []string{"/etc/passwd"},
	}}, time.Second)
	assert.Equal(t, "222", steered[0].Snapshot.Value)

	// A location that is not a folder is not read at all.
	file := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	odd := collectAll(context.Background(), h, []snapTarget{{
		info: ConnectionInfo{Name: "ice", Type: "iceberg", Kind: StatusOK}, spec: Spec{Path: file},
	}}, time.Second)
	assert.Equal(t, SnapshotNotRecorded, odd[0].Snapshot.Label)
}

func TestSnapshot_AStatementThatPinsAVersionIsNotRecordedAsNewest(t *testing.T) {
	h, db := snapHub(t)
	loc := t.TempDir()
	_, err := db.Exec(`CREATE MACRO iceberg_snapshots(p) AS TABLE SELECT 1 AS sequence_number, 9::BIGINT AS snapshot_id`)
	require.NoError(t, err)
	addConn(h, Spec{Name: "ice", Type: "iceberg", Path: loc}, StatusOK)
	addConn(h, Spec{Name: "pg", Type: "postgres"}, StatusOK)
	_, err = db.Exec(`CREATE MACRO postgres_query(n, q) AS TABLE SELECT '0/1' AS v`)
	require.NoError(t, err)

	for _, q := range []string{
		"SELECT * FROM iceberg_scan('" + loc + "', version = 'v3')",
		"SELECT * FROM iceberg_scan('" + loc + "', snapshot_from_id := 12)",
		"SELECT * FROM iceberg_scan('" + loc + "', snapshot_from_timestamp => now())",
	} {
		got := SnapshotConnections(context.Background(), h, "pg", q+" JOIN pg.t USING (a)")
		sn := snapshotOf(t, got, "ice")
		require.NotNil(t, sn, q)
		assert.Equal(t, SnapshotNotRecorded, sn.Label, q)
		assert.Equal(t, "version pinned in the statement", sn.Value, q)
		// A live-only fact is not about a version: it stays.
		assert.Equal(t, SnapshotLiveOnly, snapshotOf(t, got, "pg").Label, q)
	}
	assert.True(t, pinsVersion("SELECT * FROM lake.t AT (VERSION => 3)"))
	assert.True(t, pinsVersion("SELECT * FROM lake.t AT (TIMESTAMP => now())"))
	assert.False(t, pinsVersion("SELECT version FROM t WHERE at = 1"))
	assert.False(t, pinsVersion("SELECT * FROM iceberg_scan('x')"))
}

func TestSnapshot_AMentionIsNotARead(t *testing.T) {
	h, _ := snapHub(t)
	loc := t.TempDir()
	addConn(h, Spec{Name: "ice", Type: "iceberg", Path: loc}, StatusOK)
	addConn(h, Spec{Name: "lake_s3", Type: "s3", Params: map[string]string{"scope": "s3://b/data/"}}, StatusOK)

	assert.Empty(t, literalTargets(h, h, "SELECT * FROM t WHERE a = 's3://b/data/x.csv' OR b = '"+loc+"'"))
	assert.Empty(t, literalTargets(h, h, "SELECT 's3://b/data/x.csv'"))
	assert.Len(t, literalTargets(h, h, "SELECT * FROM 's3://b/data/x.csv'"), 1)
	assert.Len(t, literalTargets(h, h, "SELECT * FROM t JOIN read_blob('s3://b/data/x.csv') ON true"), 1)
	assert.Len(t, literalTargets(h, h, "SELECT * FROM delta_scan('"+loc+"')"), 1)
}

func TestSnapshot_CollectorsAreCappedAndAFullQueueIsTimedOut(t *testing.T) {
	h, _ := snapHub(t)
	path := mkDuckFile(t, "data", "CREATE TABLE t(x INT)")
	for i := 0; i < cap(snapSlots); i++ {
		snapSlots <- struct{}{}
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		for i := 0; i < cap(snapSlots); i++ {
			<-snapSlots
		}
	}
	defer release()

	start := time.Now()
	got := collectAll(context.Background(), h, []snapTarget{{
		info: ConnectionInfo{Name: "data", Type: "duckdb", Kind: StatusOK}, spec: Spec{Path: path},
	}}, 50*time.Millisecond)
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, SnapshotNotRecorded, got[0].Snapshot.Label)
	assert.Equal(t, snapshotValueTimeout, got[0].Snapshot.Value)

	release()
	got = collectAll(context.Background(), h, []snapTarget{{
		info: ConnectionInfo{Name: "data", Type: "duckdb", Kind: StatusOK}, spec: Spec{Path: path},
	}}, time.Second)
	assert.Equal(t, SnapshotLiveOnly, got[0].Snapshot.Label, "a free slot collects")
}

func TestSnapshot_IcebergCatalogSaysItIsNotAvailable(t *testing.T) {
	h, _ := snapHub(t)
	addConn(h, Spec{Name: "cat", Type: "iceberg_catalog"}, StatusOK)
	sn := snapshotOf(t, SnapshotConnections(context.Background(), h, "cat", "SELECT * FROM cat.ns.t"), "cat")
	require.NotNil(t, sn)
	assert.Equal(t, SnapshotNotRecorded, sn.Label)
	assert.Equal(t, "iceberg snapshot id of each table read", sn.Fact)
}

func TestSnapshot_DeltaIsTheNewestCommitOfALocalTable(t *testing.T) {
	h, _ := snapHub(t)
	table := t.TempDir()
	logDir := filepath.Join(table, "_delta_log")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	for _, n := range []string{"00000000000000000000.json", "00000000000000000007.json", "00000000000000000010.checkpoint.parquet", "_last_checkpoint"} {
		require.NoError(t, os.WriteFile(filepath.Join(logDir, n), nil, 0o600))
	}
	addConn(h, Spec{Name: "dl", Type: "delta", Path: table}, StatusOK)

	sn := snapshotOf(t, SnapshotConnections(context.Background(), h, "", "SELECT * FROM delta_scan('"+table+"')"), "dl")
	require.NotNil(t, sn)
	assert.Equal(t, "delta table version", sn.Fact)
	assert.Equal(t, "7", sn.Value)
	assert.Equal(t, SnapshotReplayable, sn.Label)
	assert.NotContains(t, sn.Value, table)

	// A literal cannot send the collector elsewhere.
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "_delta_log"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "_delta_log", "00000000000000000099.json"), nil, 0o600))
	for _, q := range []string{
		"SELECT '" + table + "/../../../" + outside + "'",
		"SELECT * FROM delta_scan('" + table + "/../" + filepath.Base(outside) + "')",
	} {
		for _, c := range SnapshotConnections(context.Background(), h, "", q) {
			assert.NotEqual(t, "dl", c.Info.Name, q)
		}
	}
	steered := collectAll(context.Background(), h, []snapTarget{{
		info: ConnectionInfo{Name: "dl", Type: "delta", Kind: StatusOK}, spec: Spec{Path: table}, urls: []string{outside},
	}}, time.Second)
	assert.Equal(t, "7", steered[0].Snapshot.Value, "the connection's own folder, whatever the statement says")

	// A _delta_log that is a link to somewhere else is not followed.
	linked := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "_delta_log"), filepath.Join(linked, "_delta_log")))
	sl := collectAll(context.Background(), h, []snapTarget{{
		info: ConnectionInfo{Name: "dl", Type: "delta", Kind: StatusOK}, spec: Spec{Path: linked},
	}}, time.Second)
	assert.Equal(t, SnapshotNotRecorded, sl[0].Snapshot.Label)

	// A table in object storage cannot be listed: not recorded.
	addConn(h, Spec{Name: "dlr", Type: "delta", Path: "s3://bucket/tbl"}, StatusOK)
	sn = snapshotOf(t, SnapshotConnections(context.Background(), h, "", "SELECT * FROM delta_scan('s3://bucket/tbl')"), "dlr")
	assert.Equal(t, SnapshotNotRecorded, sn.Label)
}

func TestSnapshot_ObjectStoragePicksOnlyPlainObjectsInsideTheScope(t *testing.T) {
	h, _ := snapHub(t)
	addConn(h, Spec{Name: "lake_s3", Type: "s3", Params: map[string]string{"scope": "s3://acme-secret-bucket/data/"}}, StatusOK)
	// Only literals inside the scope that are plain objects count: not another
	// bucket, not a wildcard, not a table another connection claims.
	sql := "SELECT * FROM read_parquet(['s3://acme-secret-bucket/data/a.parquet', 's3://acme-secret-bucket/data/b.parquet'])" +
		" UNION ALL SELECT * FROM read_csv('s3://other/x.csv') UNION ALL SELECT * FROM read_csv('s3://acme-secret-bucket/data/*.csv')"
	targets := literalTargets(h, h, sql)
	require.Len(t, targets, 1)
	assert.Equal(t, []string{"s3://acme-secret-bucket/data/a.parquet", "s3://acme-secret-bucket/data/b.parquet"}, targets[0].urls)
}

func TestSnapshot_ObjectsAreAskedOneRequestEachAtMostEightAndCounted(t *testing.T) {
	h, db := snapHub(t)
	if _, err := db.Exec("LOAD httpfs"); err != nil {
		t.Skipf("the httpfs extension is not installed here: %v", err)
	}
	mod := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var contentReads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Last-Modified", mod.Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "1234")
			return
		}
		contentReads++
		http.Error(w, "no content is read for a fact", http.StatusForbidden)
	}))
	defer srv.Close()
	urls := make([]string, 10)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/bucket/key-%02d.csv", srv.URL, i)
	}

	value, label, err := collectObjects(context.Background(), h, snapTarget{urls: urls})
	if err != nil {
		t.Skipf("this DuckDB cannot read from a loopback server here: %v", err)
	}
	assert.Equal(t, SnapshotLiveOnly, label)
	want := strings.TrimSuffix(strings.Repeat(fmt.Sprintf("1234@%d,", mod.Unix()), 8), ",")
	assert.Equal(t, "10 objects: "+want+" (+2 more)", value)
	assert.Zero(t, contentReads, "only metadata is asked for")
	assert.NotContains(t, value, srv.URL)
	assert.NotContains(t, value, "key-0")

	// One object that cannot be asked leaves the fact not recorded, not a short list.
	_, _, err = collectObjects(context.Background(), h, snapTarget{urls: []string{urls[0], srv.URL + "/bucket/missing"}})
	assert.Error(t, err)
}

func TestObjectsValue(t *testing.T) {
	st := func(n int64) objectStat { return objectStat{size: n, mtime: time.Unix(1790000000, 0)} }
	assert.Equal(t, "1 object: 5@1790000000", objectsValue([]objectStat{st(5)}, 0))
	assert.Equal(t, "3 objects: 5@1790000000,6@1790000000 (+1 more)", objectsValue([]objectStat{st(5), st(6)}, 1))
	// A long list (huge sizes) is a count and a digest, within the receipt's cap.
	big := make([]objectStat, 8)
	for i := range big {
		big[i] = st(9_000_000_000_000_000_000 + int64(i))
	}
	v := objectsValue(big, 4)
	assert.LessOrEqual(t, len(v), 256)
	assert.Regexp(t, `^12 objects, digest [0-9a-f]{16} \(\+4 more\)$`, v)
}

func TestSnapshot_ABudgetThatIsSpentLeavesEveryFactNotRecordedAndNeverRetried(t *testing.T) {
	h, _ := snapHub(t)
	path := mkDuckFile(t, "data", "CREATE TABLE t(x INT)")
	addConn(h, Spec{Name: "data", Type: "duckdb", Path: path}, StatusOK)
	addConn(h, Spec{Name: "pg", Type: "postgres"}, StatusOK)
	var targets []snapTarget
	for _, info := range h.ConnectionInfos() {
		spec, _ := h.snapshotSpec(info.Name)
		targets = append(targets, snapTarget{info: info, spec: spec})
	}
	start := time.Now()
	got := collectAll(context.Background(), h, targets, -time.Second)
	assert.Less(t, time.Since(start), 100*time.Millisecond)
	require.Len(t, got, len(targets))
	for _, c := range got {
		if c.Info.Kind != StatusOK {
			continue
		}
		require.NotNil(t, c.Snapshot, c.Info.Name)
		assert.Equal(t, SnapshotNotRecorded, c.Snapshot.Label, c.Info.Name)
		assert.Equal(t, snapshotValueTimeout, c.Snapshot.Value, c.Info.Name)
		assert.NotEmpty(t, c.Snapshot.Fact, "the fact is still named")
	}
}

func TestSnapshot_ASlowCollectorCannotHoldTheRunPastTheBudget(t *testing.T) {
	h, db := snapHub(t)
	// A postgres that takes ten seconds to answer.
	_, err := db.Exec(`CREATE MACRO postgres_query(n, q) AS TABLE
		SELECT count(*)::VARCHAR AS v FROM range(2000000000) r WHERE r.range % 7 = 100`)
	require.NoError(t, err)
	addConn(h, Spec{Name: "pg", Type: "postgres"}, StatusOK)

	start := time.Now()
	got := collectAll(context.Background(), h, []snapTarget{{info: ConnectionInfo{Name: "pg", Type: "postgres", Kind: StatusOK}}}, 50*time.Millisecond)
	assert.Less(t, time.Since(start), 2*time.Second, "the wait ends with the budget")
	assert.Equal(t, SnapshotNotRecorded, got[0].Snapshot.Label)
}

func TestSnapshot_CollectionRunsOnThePoolAndLeavesTheRunsPinnedConnectionAlone(t *testing.T) {
	h, db := snapHub(t)
	_, err := db.Exec(`CREATE MACRO postgres_query(n, q) AS TABLE SELECT '0/1' AS v`)
	require.NoError(t, err)
	path := mkDuckFile(t, "side", "CREATE TABLE t(x INT)")
	addConn(h, Spec{Name: "pg", Type: "postgres"}, StatusOK)
	addConn(h, Spec{Name: "side", Type: "duckdb", Path: path}, StatusOK)
	_, err = db.Exec("ATTACH '" + path + "' AS side")
	require.NoError(t, err)

	conn, err := h.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	require.NoError(t, conn.Use(context.Background(), "side"))

	got := SnapshotConnections(context.Background(), h, "side", "SELECT * FROM pg.t")
	require.Len(t, got, 2)
	var cur string
	rows, err := conn.Query(context.Background(), "SELECT current_database()")
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&cur))
	assert.Equal(t, "side", cur, "the pinned connection is where it was")
}
