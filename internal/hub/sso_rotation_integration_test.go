//go:build unix && integration

package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/miniotest"
)

// These are the acceptance of #131 step 7: the storage secret of an SSO-signed
// connection is renewed in place before its keys end, while a lake reads; when
// no new keys can be had the connection fails closed (the bucket is readable anonymously, but a secretless read cannot reach it: the
// deny secret must answer, which the test asserts); and an expired-token answer from
// the store ends in needs-credentials with the sign-in hint. MinIO's shortest
// STS session is 15 minutes, so the hub runs on a clock the test moves.

// offsetClock is time.Now plus an offset the test sets.
type offsetClock struct{ off atomic.Int64 }

func (c *offsetClock) now() time.Time { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *offsetClock) setTo(t time.Time) {
	c.off.Store(int64(time.Until(t)))
}

// s3Root sends a SigV4-signed request to MinIO as the root user.
func s3Root(t *testing.T, ep, method, path string, body string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+ep+path, strings.NewReader(body))
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(body))
	h := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", h)
	require.NoError(t, v4.NewSigner().SignHTTP(context.Background(),
		aws.Credentials{AccessKeyID: miniotest.KeyID, SecretAccessKey: miniotest.Secret}, req, h, "s3", "us-east-1", time.Now()))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	out, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 300, string(out))
}

// publicBucket lets anyone read objects: a read with no secret would work.
func publicBucket(t *testing.T, ep string) {
	t.Helper()
	pol := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, miniotest.Bucket)
	s3Root(t, ep, http.MethodPut, "/"+miniotest.Bucket+"?policy", pol)
}

func putObject(t *testing.T, ep, key, body string) string {
	t.Helper()
	s3Root(t, ep, http.MethodPut, "/"+miniotest.Bucket+"/"+key, body)
	return "s3://" + miniotest.Bucket + "/" + key
}

func anonymousGet(t *testing.T, ep, key string) int {
	t.Helper()
	resp, err := http.Get("http://" + ep + "/" + miniotest.Bucket + "/" + key)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

type rotWorld struct {
	w      *ssoWorld
	o      *Opened
	clk    *offsetClock
	e0     time.Time // when the first credentials end
	ep     string
	heard  *heardNames
	logs   *lockedBuf
	logger *slog.Logger
}

type heardNames struct {
	mu sync.Mutex
	n  []string
}

func (h *heardNames) add(s string) { h.mu.Lock(); h.n = append(h.n, s); h.mu.Unlock() }
func (h *heardNames) list() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.n...)
}

func openRotWorld(t *testing.T) *rotWorld {
	t.Helper()
	ep := miniotest.Start(t)
	miniotest.BuildLake(t, ep)
	miniotest.UseExtensionCache(t)
	w := newSSOWorld(t, ep, true)
	root, _ := flociWorkspace(t, ep, "aws-sso", w.res)
	r := &rotWorld{w: w, clk: &offsetClock{}, ep: ep, heard: &heardNames{}, logs: &lockedBuf{}}
	r.logger = slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o, err := Open(context.Background(), root, OpenOptions{App: "test", Logger: r.logger, Resolver: w.res, Redactor: w.red,
		SessionOptions: []session.Option{session.WithClock(r.clk.now), session.WithStatusListener(r.heard.add)}})
	require.NoError(t, err)
	t.Cleanup(o.Close)
	r.o = o
	c, _ := w.sts.Load().(stsCreds)
	r.e0 = c.Expires
	require.Equal(t, session.StatusOK, statusLine(o.Sess, "floci").Kind, statusLine(o.Sess, "floci").Message)
	return r
}

func (r *rotWorld) exec(q string) (string, error) {
	rs, err := r.o.Eng.Execute(context.Background(), q, nil)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(rs.Rows[0][0]), nil
}

// readObject reads a small CSV object's one cell. Unlike read_text (which
// answers no rows for an object it cannot read), read_csv raises the error.
func (r *rotWorld) readObject(url string) (string, error) {
	return r.exec("SELECT a FROM read_csv('" + url + "', header = false, columns = {'a': 'VARCHAR'})")
}

func (r *rotWorld) secretMentions(t *testing.T, keyID string) bool {
	t.Helper()
	var n int
	require.NoError(t, r.o.Sess.DB().QueryRow(`SELECT count(*) FROM duckdb_secrets() WHERE secret_string LIKE '%' || ? || '%'`, keyID).Scan(&n))
	return n > 0
}

func (r *rotWorld) secretNamed(url string) string {
	var name string
	if err := r.o.Sess.DB().QueryRow(`SELECT name FROM which_secret(?, 's3')`, url).Scan(&name); err != nil {
		return ""
	}
	return name
}

// Rotation while a lake reads: new STS keys every time, replaced in place, 0
// failed reads, and the lister (which builds the secret itself) works on the
// rotated keys with no portal call.
func TestSSORotationWhileALakeReads(t *testing.T) {
	r := openRotWorld(t)
	ctx := context.Background()
	obj := putObject(t, r.ep, "rot/keep.csv", "keep")

	var fails, reads atomic.Int64
	var firstErr atomic.Value
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				q := "SELECT count(*) FROM lake.warehouse.dim_students"
				if i%2 == 1 {
					q = "SELECT a FROM read_csv('" + obj + "', header = false, columns = {'a': 'VARCHAR'})"
				}
				if _, err := r.exec(q); err != nil {
					fails.Add(1)
					firstErr.CompareAndSwap(nil, err.Error())
				}
				reads.Add(1)
			}
		}(i)
	}

	for i := 1; i <= 5; i++ {
		fresh := assumeRole(t, r.ep)
		r.w.sts.Store(fresh)
		hits := r.w.hits.Load()
		r.clk.setTo(r.e0.Add(-4*time.Minute + time.Duration(i)*40*time.Second)) // inside the window, past the interval
		require.Eventually(t, func() bool { return r.w.hits.Load() > hits && r.secretMentions(t, fresh.KeyID) }, 30*time.Second, 20*time.Millisecond,
			"rotation %d: the portal was asked and the secret now carries the new key id", i)
	}
	close(stop)
	wg.Wait()
	assert.Zero(t, fails.Load(), "reads that failed during rotation: %v", firstErr.Load())
	assert.Greater(t, reads.Load(), int64(20))
	assert.Equal(t, session.StatusOK, statusLine(r.o.Sess, "floci").Kind)
	assert.Equal(t, session.StatusOK, statusLine(r.o.Sess, "lake").Kind)
	assert.Empty(t, r.heard.list(), "a rotation that works changes no status")

	// The lister reads the provider's cache, which the rotation refreshed.
	before := r.w.hits.Load()
	got, err := r.exec("SELECT count(*) > 0 FROM read_parquet('s3://" + miniotest.Bucket + "/warehouse/ducklake/**/*.parquet')")
	require.NoError(t, err)
	assert.Equal(t, "true", got)
	assert.Equal(t, before, r.w.hits.Load())

	needles := r.w.needles()
	noNeedles(t, "logs", r.logs.String(), needles)
	_ = ctx
}

// Rotation fails (the session is gone): the connection fails closed. The bucket
// is readable anonymously here, and the object read afterwards was never read
// before (DuckDB's external file cache cannot serve it). A secretless read could
// not reach the bucket anyway (it would go to the AWS default endpoint), so the
// test asserts where fail closed is decided: the catch-all deny secret answers.
func TestSSORotationFailureFailsClosedNeverAnonymous(t *testing.T) {
	r := openRotWorld(t)
	publicBucket(t, r.ep)
	seen := putObject(t, r.ep, "rot/seen.csv", "seen")
	fresh := putObject(t, r.ep, "rot/fresh.csv", "fresh")
	require.Equal(t, 200, anonymousGet(t, r.ep, "rot/fresh.csv"), "the bucket really is anonymous-readable")

	got, err := r.readObject(seen)
	require.NoError(t, err)
	assert.Equal(t, "seen", got)
	assert.Equal(t, "slaw_floci", r.secretNamed(seen))

	r.w.signOut(t)
	r.clk.setTo(r.e0.Add(-4 * time.Minute))
	_, err = r.exec("SELECT count(*) FROM lake.warehouse.dim_students")
	require.Error(t, err, "the statement that finds the keys ending is refused with the hint")
	assert.Contains(t, err.Error(), "aws sso login --profile dev")

	st := statusLine(r.o.Sess, "floci")
	assert.Equal(t, session.StatusNeedsCredentials, st.Kind)
	require.NotNil(t, st.SignIn)
	assert.Equal(t, "dev", st.SignIn.Subject)
	assert.NotEqual(t, session.StatusOK, statusLine(r.o.Sess, "lake").Kind, "the lake is detached")
	var lakes int
	require.NoError(t, r.o.Sess.DB().QueryRow(`SELECT count(*) FROM duckdb_databases() WHERE database_name = 'lake'`).Scan(&lakes))
	assert.Zero(t, lakes)
	assert.Equal(t, "slaw-deny-s3", r.secretNamed(fresh), "the SSO secret is gone and the deny secret answers")
	assert.Contains(t, r.heard.list(), "floci", "the status listener told the UI")

	got, err = r.readObject(fresh)
	require.Error(t, err, "an object never read before is not read anonymously (got %q)", got)
	requireDenied(t, err, miniotest.Bucket)
	noNeedles(t, "logs", r.logs.String(), r.w.needles())

	// Signing in again brings it back.
	r.w.signIn(t)
	r.clk.setTo(time.Now())
	r.o.Sess.Preflight(context.Background(), "floci")
	assert.Equal(t, session.StatusOK, statusLine(r.o.Sess, "floci").Kind)
	got, err = r.readObject(fresh)
	require.NoError(t, err)
	assert.Equal(t, "fresh", got)
}

// The store says the session token is no good (revoked or expired): the read
// fails, one rotation is tried, and when the sign-in cannot give new keys the
// connection needs sign-in, with the hint.
func TestSSOExpiredTokenOnS3NeedsSignIn(t *testing.T) {
	r := openRotWorld(t)
	good, _ := r.w.sts.Load().(stsCreds)
	o1 := putObject(t, r.ep, "rot/e1.csv", "v1")
	o2 := putObject(t, r.ep, "rot/e2.csv", "v2")
	o3 := putObject(t, r.ep, "rot/e3.csv", "v3")

	// The portal starts handing out keys whose session token the store refuses.
	bad := good
	bad.Token = "invalid" + good.Token
	r.w.sts.Store(bad)
	r.clk.setTo(r.e0.Add(-4 * time.Minute)) // due: the secret is replaced with the refused keys
	_, err := r.exec("SELECT 1")
	require.NoError(t, err)
	assert.Equal(t, session.StatusOK, statusLine(r.o.Sess, "floci").Kind)

	_, err = r.readObject(o1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Equal(t, session.StatusOK, statusLine(r.o.Sess, "floci").Kind, "one forced rotation per 30 s: this one is rate-limited")

	// 31 s later the session is gone: the next refused read forces a rotation
	// that cannot be had.
	r.w.signOut(t)
	r.clk.setTo(r.e0.Add(-4*time.Minute + 31*time.Second))
	// Through the pool, not the engine: no rotateDue runs, so only the refused
	// read can have started the rotation.
	rows, err := r.o.Sess.Query(context.Background(), "SELECT a FROM read_csv('"+o2+"', header = false, columns = {'a': 'VARCHAR'})")
	if rows != nil {
		_ = rows.Close()
	}
	require.Error(t, err)
	st := statusLine(r.o.Sess, "floci")
	assert.Equal(t, session.StatusNeedsCredentials, st.Kind, st.Message)
	require.NotNil(t, st.SignIn)
	assert.Contains(t, st.Message, "aws sso login --profile dev")
	assert.Contains(t, r.heard.list(), "floci")
	_, err = r.readObject(o3)
	require.Error(t, err)
	noNeedles(t, "logs", r.logs.String(), append(r.w.needles(), bad.Token))
}
