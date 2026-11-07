//go:build integration

package miniotest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

const (
	// KeyID and Secret are the MinIO root credentials. Secret is the needle the
	// leak audits look for.
	KeyID  = "minioadmin"
	Secret = "minioSecret_needle1"
	// Bucket is the bucket Start creates.
	Bucket = "oib-data-local"
	// Students is how many rows warehouse.dim_students holds after BuildLake:
	// 6000 in Parquet files and 7 inlined in the catalog.
	Students = 6007
)

func containerBin() string {
	for _, b := range []string{"docker", "podman"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return "podman"
}

// ExtensionDir is the extension cache shared by integration runs.
func ExtensionDir(t testing.TB) string {
	t.Helper()
	d := filepath.Join(os.TempDir(), "slaw-conn-it-ext")
	require.NoError(t, os.MkdirAll(d, 0o755))
	return d
}

// UseExtensionCache points the app-private extension directory (os.UserCacheDir
// under XDG_CACHE_HOME) at the shared cache, so a real hub.Open finds the
// extensions without downloading them into the person's own cache. Linux only.
func UseExtensionCache(t *testing.T) {
	t.Helper()
	cache := t.TempDir()
	dir := filepath.Join(cache, "slaw", "duckdb")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Symlink(ExtensionDir(t), filepath.Join(dir, "extensions")))
	t.Setenv("XDG_CACHE_HOME", cache)
}

// Start runs a throwaway S3-compatible server with the bucket Bucket and returns
// its host:port (path style, no TLS). It skips the test when no container runtime
// can run it.
func Start(t testing.TB) string {
	t.Helper()
	bin := containerBin()
	out, err := exec.Command(bin, "run", "--rm", "-d", "--mount", "type=tmpfs,destination=/data", "--entrypoint", "sh",
		"-e", "MINIO_ROOT_USER="+KeyID, "-e", "MINIO_ROOT_PASSWORD="+Secret,
		"-p", "127.0.0.1::9000", "docker.io/minio/minio",
		"-c", "mkdir -p /data/"+Bucket+" && exec minio server /data").CombinedOutput()
	if err != nil {
		t.Skipf("minio is not available here: %v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	if i := strings.LastIndex(id, "\n"); i >= 0 {
		id = id[i+1:]
	}
	t.Cleanup(func() { _ = exec.Command(bin, "rm", "-f", "-v", id).Run() })
	pout, err := exec.Command(bin, "port", id, "9000/tcp").CombinedOutput()
	require.NoError(t, err, string(pout))
	line := strings.TrimSpace(strings.Split(string(pout), "\n")[0])
	ep := "127.0.0.1:" + line[strings.LastIndex(line, ":")+1:]
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("curl", "-s", "-o", "/dev/null", "-m", "2", "http://"+ep+"/minio/health/ready").Run() == nil {
			return ep
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("minio never became ready")
	return ""
}

// BuildLake builds, with DuckDB itself, the lake the issue is about and uploads
// it to the MinIO at ep: a SQLite catalog at
// s3://<Bucket>/warehouse/ducklake/catalog.sqlite, Parquet files under
// s3://<Bucket>/warehouse/ducklake/, and a table warehouse.dim_students with
// Students rows (a few of them inlined in the catalog, not in Parquet).
func BuildLake(t testing.TB, ep string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	run := func(s string) {
		t.Helper()
		_, err := conn.ExecContext(ctx, s)
		require.NoError(t, err, s)
	}
	run("SET extension_directory = " + session.QuoteLiteral(ExtensionDir(t)))
	for _, e := range []string{"ducklake", "sqlite", "httpfs"} {
		run("INSTALL " + e)
		run("LOAD " + e)
	}
	run(fmt.Sprintf("CREATE SECRET (TYPE s3, KEY_ID %s, SECRET %s, REGION 'us-east-1', ENDPOINT %s, URL_STYLE 'path', USE_SSL false)",
		session.QuoteLiteral(KeyID), session.QuoteLiteral(Secret), session.QuoteLiteral(ep)))
	base := "s3://" + Bucket + "/warehouse/ducklake/"
	local := filepath.Join(t.TempDir(), "catalog.sqlite")
	for _, s := range []string{
		"ATTACH " + session.QuoteLiteral("ducklake:sqlite:"+local) + " AS l (DATA_PATH " + session.QuoteLiteral(base) + ")",
		"CREATE SCHEMA l.warehouse",
		"CREATE TABLE l.warehouse.dim_students (student_id INTEGER, name VARCHAR, cohort INTEGER)",
		"INSERT INTO l.warehouse.dim_students SELECT i, 'student_' || i, i % 4 FROM range(0, 3000) t(i)",
		"INSERT INTO l.warehouse.dim_students SELECT i, 'student_' || i, i % 4 FROM range(3000, 6000) t(i)",
		"CALL ducklake_flush_inlined_data('l')",
		"INSERT INTO l.warehouse.dim_students SELECT i, 'student_' || i, i % 4 FROM range(6000, 6007) t(i)", // stays inlined
		"DETACH l",
		"COPY (SELECT content FROM read_blob(" + session.QuoteLiteral(local) + ")) TO " + session.QuoteLiteral(base+"catalog.sqlite") + " (FORMAT blob)",
	} {
		run(s)
	}
}
