package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// maxSnapshotObjects is how many objects of a storage connection a fact looks
// at; the rest are only counted.
const maxSnapshotObjects = 8

// maxSnapshotValue keeps a fact's value well inside the receipt's own cap.
const maxSnapshotValue = 200

// snapCollector is how one connection type states its fact: the fact's name
// (named even when it is not recorded) and the query or check behind it.
type snapCollector struct {
	fact string
	// replayable: the fact names a version, so a statement that pins one makes
	// "the newest" wrong.
	replayable bool
	run        func(ctx context.Context, s Session, t snapTarget) (value string, label SnapshotLabel, err error)
}

// collectorFor is the whole table of R3: which fact each connection type gives
// and whether it can be replayed. A type with no fact of its own, and a type
// this build does not know, is status only.
func collectorFor(t snapTarget) snapCollector {
	switch t.info.Type {
	case "ducklake":
		return snapCollector{"ducklake_current_snapshot()", true, collectDuckLake}
	case "iceberg":
		return snapCollector{"iceberg snapshot id", true, collectIcebergTable}
	case "iceberg_catalog":
		return snapCollector{"iceberg snapshot id of each table read", false, collectIcebergCatalog}
	case "delta":
		return snapCollector{"delta table version", true, collectDeltaTable}
	case "postgres":
		return snapCollector{"pg_current_wal_lsn()", false, collectPostgres}
	case "duckdb":
		if t.spec.Path == "" || t.spec.Path == ":memory:" { // the in-memory default has no file
			break
		}
		return snapCollector{"file size and mtime", false, collectDuckDBFile}
	case "s3", "gcs", "r2", "azure":
		return snapCollector{"object size and mtime", false, collectObjects}
	}
	return snapCollector{"status", false, collectStatusOnly}
}

func collectStatusOnly(context.Context, Session, snapTarget) (string, SnapshotLabel, error) {
	return "no snapshot fact for this connection type", SnapshotLiveOnly, nil
}

// collectDuckLake is the lake's current snapshot id: the one fact that reads the
// same data again (SELECT ... AT (VERSION => id)).
func collectDuckLake(ctx context.Context, s Session, t snapTarget) (string, SnapshotLabel, error) {
	var id int64
	err := s.DB().QueryRowContext(ctx, `SELECT id FROM ducklake_current_snapshot(`+QuoteLiteral(t.info.Name)+`)`).Scan(&id)
	if err != nil {
		return "", "", err
	}
	return strconv.FormatInt(id, 10), SnapshotReplayable, nil
}

// collectIcebergTable is the newest snapshot id of an Iceberg table connection,
// read from the connection's own location (never from a literal in the
// statement, which could name any file). A local location must be a folder. The
// metadata is read once; a table that needs a version DuckDB will not guess is
// not recorded.
func collectIcebergTable(ctx context.Context, s Session, t snapTarget) (string, SnapshotLabel, error) {
	loc := t.spec.Path
	if loc == "" {
		return "", "", errors.New("no table location")
	}
	if !strings.Contains(loc, "://") {
		if fi, err := os.Lstat(loc); err != nil || !fi.IsDir() {
			return "", "", errors.New("not a folder")
		}
	}
	var id int64
	err := s.DB().QueryRowContext(ctx,
		`SELECT snapshot_id FROM iceberg_snapshots(`+QuoteLiteral(loc)+`) ORDER BY sequence_number DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return "", "", err
	}
	return strconv.FormatInt(id, 10), SnapshotReplayable, nil
}

// collectIcebergCatalog records that no fact is available. DuckDB 1.5's
// iceberg_snapshots takes a table location, not a catalog table, so the snapshot
// a REST or S3 Tables catalog resolved a table to cannot be read without a second
// round trip that re-binds the remote table, which a receipt does not make.
func collectIcebergCatalog(context.Context, Session, snapTarget) (string, SnapshotLabel, error) {
	return "not available for catalog tables", SnapshotNotRecorded, nil
}

var deltaCommitRE = regexp.MustCompile(`^[0-9]{1,20}$`)

// collectDeltaTable is the newest commit of a Delta table in a folder on this
// computer: the highest <version>.json in the connection's own _delta_log. Only
// a real folder is opened (Lstat first: a symbolic link, a FIFO or a file in its
// place is refused without being opened, so nothing can block or escape), and
// only names are read. A table in object storage cannot be listed by the hub,
// so it is not recorded.
func collectDeltaTable(_ context.Context, _ Session, t snapTarget) (string, SnapshotLabel, error) {
	if t.spec.Path == "" || strings.Contains(t.spec.Path, "://") {
		return "", "", errors.New("not a local table")
	}
	logDir := filepath.Join(t.spec.Path, "_delta_log")
	if fi, err := os.Lstat(logDir); err != nil || !fi.Mode().IsDir() {
		return "", "", errors.New("no log folder")
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return "", "", err
	}
	best := int64(-1)
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !deltaCommitRE.MatchString(name) {
			continue
		}
		if v, err := strconv.ParseInt(name, 10, 64); err == nil && v > best {
			best = v
		}
	}
	if best < 0 {
		return "", "", errors.New("no commits")
	}
	return strconv.FormatInt(best, 10), SnapshotReplayable, nil
}

var lsnRE = regexp.MustCompile(`^[0-9A-F]{1,8}/[0-9A-F]{1,8}$`)

// collectPostgres is the server's WAL position, asked on the session's own
// attachment. It is live only: a position names no data a later read can return.
// A standby has no such position and is not recorded.
func collectPostgres(ctx context.Context, s Session, t snapTarget) (string, SnapshotLabel, error) {
	var lsn string
	err := s.DB().QueryRowContext(ctx,
		`SELECT v FROM postgres_query(`+QuoteLiteral(t.info.Name)+`, 'SELECT pg_current_wal_lsn()::text') AS q(v)`).Scan(&lsn)
	if err != nil {
		return "", "", err
	}
	if !lsnRE.MatchString(lsn) {
		return "", "", errors.New("unexpected position")
	}
	return lsn, SnapshotLiveOnly, nil
}

// collectDuckDBFile is the file's size and modification time, from the file
// system. The path is read from the definition and never leaves this function.
func collectDuckDBFile(_ context.Context, _ Session, t snapTarget) (string, SnapshotLabel, error) {
	fi, err := os.Stat(t.spec.Path)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%d bytes, modified %s", fi.Size(), fi.ModTime().UTC().Format(time.RFC3339)), SnapshotLiveOnly, nil
}

// objectURL is a plain storage URL a HEAD can be made for: no wildcard (a
// pattern the hub expanded is already a list of plain URLs).
func objectURL(u string) bool {
	return storageurl.Scheme(u) != "" && !strings.ContainsAny(u, "*?[")
}

func withinScope(u, scope string) bool { return storageurl.Within(u, scope) }

// objectStat is what one HEAD of an object says.
type objectStat struct {
	size  int64
	mtime time.Time
}

// collectObjects is the size and modification time of up to maxSnapshotObjects
// of the objects the statement read, each from one metadata request on the
// session that read them (read_blob, which only asks for the content when it is
// selected), at once under the shared deadline. DuckDB does not give an ETag,
// so the pair stands in for it. The value is a count and the numbers, never a
// URL, bucket or key; when the list would not fit it is a count and a digest.
// If any object cannot be asked, the fact is not recorded: a half list would
// read as the whole.
func collectObjects(ctx context.Context, s Session, t snapTarget) (string, SnapshotLabel, error) {
	urls := t.urls
	if len(urls) == 0 {
		return "", "", errors.New("no objects")
	}
	more := 0
	if len(urls) > maxSnapshotObjects {
		more = len(urls) - maxSnapshotObjects
		urls = urls[:maxSnapshotObjects]
	}
	stats := make([]objectStat, len(urls))
	errs := make([]error, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var size int64
			var mt time.Time
			errs[i] = s.DB().QueryRowContext(ctx, `SELECT size, last_modified FROM read_blob(`+QuoteLiteral(u)+`)`).Scan(&size, &mt)
			stats[i] = objectStat{size, mt}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return "", "", err
		}
	}
	return objectsValue(stats, more), SnapshotLiveOnly, nil
}

// objectsValue renders the stats: "3 objects: 120@1790000000, ..." (size at Unix
// seconds), "+N more" after the first eight, and when the list is long a digest
// of it in its place.
func objectsValue(stats []objectStat, more int) string {
	parts := make([]string, len(stats))
	for i, st := range stats {
		parts[i] = fmt.Sprintf("%d@%d", st.size, st.mtime.Unix())
	}
	total := len(stats) + more
	list := strings.Join(parts, ",")
	head := fmt.Sprintf("%d object", total)
	if total != 1 {
		head += "s"
	}
	suffix := ""
	if more > 0 {
		suffix = fmt.Sprintf(" (+%d more)", more)
	}
	if v := head + ": " + list + suffix; len(v) <= maxSnapshotValue {
		return v
	}
	sum := sha256.Sum256([]byte(list))
	return head + ", digest " + hex.EncodeToString(sum[:8]) + suffix
}
