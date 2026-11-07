//go:build integration

package icebergtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/miniotest"
)

const (
	// KeyID and Secret are the MinIO root credentials; Secret is the needle the
	// leak audits look for (shared with miniotest).
	KeyID  = miniotest.KeyID
	Secret = miniotest.Secret
	// Warehouse and Other are the two buckets Start creates. The catalog's
	// warehouse is Warehouse; Other is where hostile tables point.
	Warehouse = "warehouse"
	Other     = "other"
	// Namespace is the namespace Seed creates.
	Namespace = "ns"
	// BearerToken is the needle a Proxy with RequireBearer demands.
	BearerToken = "iceberg_token_needle1"

	minioImage   = "docker.io/minio/minio"
	catalogImage = "docker.io/apache/iceberg-rest-fixture"
)

// Fixture is a running MinIO plus REST catalog.
type Fixture struct {
	// RESTURI is the catalog's base URL, e.g. http://127.0.0.1:41231.
	RESTURI string
	// S3Endpoint is MinIO's host:port as the test process reaches it.
	S3Endpoint string
	// KeyID and Secret are MinIO's credentials.
	KeyID, Secret string

	db *sql.DB
}

func containerBin() string {
	for _, b := range []string{"docker", "podman"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return "podman"
}

func lastLine(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func hostPort(t testing.TB, bin, id, port string) string {
	t.Helper()
	out, err := exec.Command(bin, "port", id, port+"/tcp").CombinedOutput()
	require.NoError(t, err, string(out))
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	return "127.0.0.1:" + line[strings.LastIndex(line, ":")+1:]
}

// envArgs turns KEY=VALUE pairs into -e flags (values travel as arguments, as
// miniotest's do, and never through a file).
func envArgs(env ...string) []string {
	var a []string
	for _, e := range env {
		a = append(a, "-e", e)
	}
	return a
}

func catalogEnv() []string {
	return envArgs(
		"CATALOG_WAREHOUSE=s3://"+Warehouse+"/",
		"CATALOG_IO__IMPL=org.apache.iceberg.aws.s3.S3FileIO",
		"CATALOG_S3_ENDPOINT=http://localhost:9000",
		"CATALOG_S3_PATH__STYLE__ACCESS=true",
		"AWS_ACCESS_KEY_ID="+KeyID,
		"AWS_SECRET_ACCESS_KEY="+Secret,
		"AWS_REGION=us-east-1",
	)
}

func minioArgs() []string {
	// tmpfs on /data: the image declares VOLUME /data, which would leave an
	// anonymous volume behind on every run.
	args := []string{"--mount", "type=tmpfs,destination=/data", "--entrypoint", "sh"}
	args = append(args, envArgs("MINIO_ROOT_USER="+KeyID, "MINIO_ROOT_PASSWORD="+Secret)...)
	return append(args, minioImage, "-c",
		"mkdir -p /data/"+Warehouse+" /data/"+Other+" && exec minio server /data")
}

// Start runs MinIO and the REST catalog in one network namespace (a pod under
// podman, MinIO's own namespace under docker, which has no pods) and waits
// until both answer. It skips the test only when the container runtime itself
// is unusable (`<bin> info` fails); any later failure fails the test, after
// dumping the containers' logs.
func Start(t testing.TB) *Fixture {
	t.Helper()
	bin := containerBin()
	if out, err := exec.Command(bin, "info").CombinedOutput(); err != nil {
		t.Skipf("no usable container runtime (%s info): %v: %s", bin, err, out)
	}
	var ids []string
	dumpLogs := func() {
		for _, id := range ids {
			out, _ := exec.Command(bin, "logs", id).CombinedOutput()
			t.Logf("%s logs %s:\n%s", bin, id, out)
		}
	}
	fail := func(format string, args ...any) {
		t.Helper()
		dumpLogs()
		t.Fatalf(format, args...)
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(bin, append([]string{"run", "-d", "--rm"}, args...)...).CombinedOutput()
		if err != nil {
			fail("%s run failed: %v: %s", bin, err, out)
		}
		id := lastLine(out)
		ids = append(ids, id)
		return id
	}
	var restHP, s3HP string
	if out, err := exec.Command(bin, "pod", "create", "-p", "127.0.0.1::8181", "-p", "127.0.0.1::9000").CombinedOutput(); err == nil {
		pod := lastLine(out)
		t.Cleanup(func() { _ = exec.Command(bin, "pod", "rm", "-f", pod).Run() })
		run(append([]string{"--pod", pod}, minioArgs()...)...)
		run(append(append([]string{"--pod", pod}, catalogEnv()...), catalogImage)...)
		pc, err := exec.Command(bin, "pod", "inspect", pod, "--format", "{{.InfraContainerID}}").CombinedOutput()
		require.NoError(t, err, string(pc))
		infra := lastLine(pc)
		restHP, s3HP = hostPort(t, bin, infra, "8181"), hostPort(t, bin, infra, "9000")
	} else {
		mid := run(append([]string{"-p", "127.0.0.1::8181", "-p", "127.0.0.1::9000"}, minioArgs()...)...)
		t.Cleanup(func() { _ = exec.Command(bin, "rm", "-f", "-v", mid).Run() })
		cid := run(append(append([]string{"--network", "container:" + mid}, catalogEnv()...), catalogImage)...)
		t.Cleanup(func() { _ = exec.Command(bin, "rm", "-f", "-v", cid).Run() })
		restHP, s3HP = hostPort(t, bin, mid, "8181"), hostPort(t, bin, mid, "9000")
	}
	f := &Fixture{RESTURI: "http://" + restHP, S3Endpoint: s3HP, KeyID: KeyID, Secret: Secret}
	if err := waitHTTP("http://"+s3HP+"/minio/health/ready", "minio"); err != nil {
		fail("%v", err)
	}
	if err := waitHTTP(f.RESTURI+"/v1/config", "iceberg rest catalog"); err != nil {
		fail("%v", err)
	}
	f.db = f.Open(t)
	return f
}

func waitHTTP(url, what string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url) //nolint:noctx // loopback readiness probe
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s never became ready", what)
}

// shared is the one unhardened DuckDB per fixture for the object helpers
// (ReadObject, PutObject, List, ObjectSize), opened in Start with Start's t so a
// subtest that crafts first cannot close it under later callers.
func (f *Fixture) shared(t testing.TB) *sql.DB {
	t.Helper()
	require.NotNil(t, f.db, "Fixture not made by Start")
	return f.db
}

// Open returns a throwaway, deliberately unhardened DuckDB (one connection, so
// session state sticks) with httpfs, avro and iceberg loaded and an s3 secret
// for the fixture's MinIO. It is for seeding and for the threat demonstrations;
// the code under test opens its own hardened hub.
func (f *Fixture) Open(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	Exec(t, db, "SET extension_directory = "+session.QuoteLiteral(miniotest.ExtensionDir(t)))
	for _, e := range []string{"httpfs", "avro", "iceberg"} {
		Exec(t, db, "INSTALL "+e)
		Exec(t, db, "LOAD "+e)
	}
	Exec(t, db, f.S3SecretSQL())
	return db
}

// S3SecretSQL is the CREATE SECRET statement that reaches the fixture's MinIO.
func (f *Fixture) S3SecretSQL() string {
	return fmt.Sprintf("CREATE OR REPLACE SECRET minio_it (TYPE s3, KEY_ID %s, SECRET %s, REGION 'us-east-1', ENDPOINT %s, URL_STYLE 'path', USE_SSL false)",
		session.QuoteLiteral(f.KeyID), session.QuoteLiteral(f.Secret), session.QuoteLiteral(f.S3Endpoint))
}

// AttachSQL is an ATTACH of the catalog (or a proxy URI in its place) with no
// authorization; extra is appended to the option list, e.g. ", READ_ONLY".
func AttachSQL(alias, restURI, extra string) string {
	return fmt.Sprintf("ATTACH %s AS %s (TYPE iceberg, ENDPOINT %s, AUTHORIZATION_TYPE 'none'%s)",
		session.QuoteLiteral(Warehouse), alias, session.QuoteLiteral(restURI), extra)
}

// Exec runs a statement and fails the test on error.
func Exec(t testing.TB, db *sql.DB, q string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), q)
	require.NoError(t, err, q)
}

// Seed creates ns.orders (300 rows: id, amount, region) and ns.events (two
// snapshots: a create of 5 rows, then an insert of 5 more) through DuckDB's
// iceberg catalog attach, under the alias "seed".
func (f *Fixture) Seed(t testing.TB) {
	t.Helper()
	db := f.Open(t)
	for _, s := range []string{
		AttachSQL("seed", f.RESTURI, ""),
		"CREATE SCHEMA seed." + Namespace,
		"CREATE TABLE seed.ns.orders AS SELECT i AS id, (i*7)%100 AS amount, 'r'||(i%3) AS region FROM range(300) t(i)",
		"CREATE TABLE seed.ns.events AS SELECT i AS id, 'e'||i AS kind FROM range(5) t(i)",
		"INSERT INTO seed.ns.events SELECT i AS id, 'e'||i AS kind FROM range(5, 10) t(i)",
		"DETACH seed",
	} {
		Exec(t, db, s)
	}
}

// restDo sends a JSON request to the catalog and returns the response body.
func (f *Fixture) restDo(t testing.TB, method, path string, body any) []byte {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.RESTURI+path, rd)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Less(t, resp.StatusCode, 300, "%s %s: %s", method, path, out)
	return out
}

// SetTableComment sets the table property "comment" through the REST API
// (DuckDB 1.5 has no COMMENT ON for Iceberg).
func (f *Fixture) SetTableComment(t testing.TB, table, comment string) {
	t.Helper()
	f.restDo(t, http.MethodPost, "/v1/namespaces/"+Namespace+"/tables/"+table, map[string]any{
		"requirements": []any{},
		"updates":      []any{map[string]any{"action": "set-properties", "updates": map[string]string{"comment": comment}}},
	})
}
