//go:build unix && integration

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/sandbox"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/miniotest"
	"github.com/SandwichLabs/slaw/internal/testsupport/ssofake"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// This is the whole of #131/#130 as one person lives it, against MinIO behind
// the fake Identity Center (the OIDC device flow and the portal are the fake's;
// the role keys are real MinIO STS keys):
//
//	signed out -> device sign-in -> reads (plain s3, wildcard, DuckLake) ->
//	rotation -> the session ends -> fail closed (never anonymous) ->
//	`aws sso login` outside Slaw -> the next statement revives it.
//
// The bucket is anonymous-readable throughout, but a secretless read could not
// reach it (it would go to the AWS default endpoint), so "never anonymous" is
// asserted where it is decided: before each refused read, which_secret must name
// the catch-all deny secret (#135) and the error must show the unresolvable deny
// endpoint. A needle audit runs at the end over every surface the test can see.
func TestSSOJourneyFromSignedOutToRevivedAfterAnExternalLogin(t *testing.T) {
	ep := miniotest.Start(t)
	miniotest.BuildLake(t, ep)
	miniotest.UseExtensionCache(t)
	publicBucket(t, ep)
	ctx := context.Background()

	w := newSSOWorld(t, ep, false)
	root, cfg := flociWorkspace(t, ep, "aws-sso", w.res)
	obj := putObject(t, ep, "journey/a.csv", "plain")
	obj2 := putObject(t, ep, "journey/b.csv", "after")
	obj3 := putObject(t, ep, "journey/c.csv", "revived")

	clk := &offsetClock{}
	heard := &heardNames{}
	logs := &lockedBuf{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o, err := Open(ctx, root, OpenOptions{App: "test", Logger: logger, Resolver: w.res, Redactor: w.red,
		SessionOptions: []session.Option{session.WithClock(clk.now), session.WithStatusListener(heard.add)}})
	require.NoError(t, err)
	defer o.Close()
	require.Empty(t, o.Issues)
	r := &rotWorld{w: w, o: o, clk: clk, ep: ep, heard: heard, logs: logs, logger: logger}

	var seen []string // every error, message and result the person or an agent could see
	note := func(s string) { seen = append(seen, s) }
	h, _, err := NewAgentHandler(o.WS, o.Sess, "", logger)
	require.NoError(t, err)

	// 1. Signed out: needs sign-in with the hint, the lake waits on it, nothing
	// was read anonymously, and the portal and the OIDC service were never called.
	st := statusLine(o.Sess, "floci")
	assert.Equal(t, session.StatusNeedsCredentials, st.Kind, st.Message)
	require.NotNil(t, st.SignIn)
	assert.Equal(t, secrets.SignInHint{Kind: "aws-sso", Subject: "dev", Reason: "missing"}, *st.SignIn)
	assert.Contains(t, st.Message, "aws sso login --profile dev")
	note(st.Message)
	assert.Equal(t, "slaw-deny-s3", r.secretNamed(obj), "no storage secret: the catch-all deny secret answers")
	_, err = r.readObject(obj)
	require.Error(t, err, "a signed-out connection reads nothing")
	requireDenied(t, err, miniotest.Bucket) // refused by the deny endpoint, not answered by any store
	note(err.Error())
	assert.NotEqual(t, session.StatusOK, statusLine(o.Sess, "lake").Kind)
	resp := agentCall(h, "SELECT count(*) FROM read_parquet('s3://"+miniotest.Bucket+"/journey/a.csv')")
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, `the storage connection "floci" covers it but is not ready`, "the agent's refusal names the connection")
	assert.Contains(t, resp.Error.Message, "aws sso login --profile dev")
	note(resp.Error.Message)
	assert.Zero(t, w.hits.Load())
	assert.Zero(t, w.fake.Registrations(), "no device flow starts by itself")
	assert.Zero(t, w.fake.Waiting())

	// 2. Device sign-in, as Studio and `slaw conn credentials sign-in` run it.
	shown := make(chan secrets.DevicePrompt, 1)
	done := make(chan error, 1)
	go func() {
		_, derr := workspace.DeviceSignIn(ctx, o.WS, "floci", func(p secrets.DevicePrompt) { shown <- p })
		done <- derr
	}()
	var prompt secrets.DevicePrompt
	select {
	case prompt = <-shown:
	case <-time.After(20 * time.Second):
		t.Fatal("the sign-in showed no prompt")
	}
	assert.Equal(t, ssofake.UserCode, prompt.Code)
	assert.True(t, strings.HasPrefix(prompt.URL, "http://127.0.0.1:"), prompt.URL)
	require.Eventually(t, func() bool { return w.fake.Waiting() == 1 }, 10*time.Second, 20*time.Millisecond)
	w.fake.Approve() // the person approves in the browser
	select {
	case derr := <-done:
		require.NoError(t, derr)
	case <-time.After(30 * time.Second):
		t.Fatal("the sign-in did not finish after approval")
	}
	o.Sess.Preflight(ctx, "floci") // what Studio does on signin:done
	assert.Equal(t, session.StatusOK, statusLine(o.Sess, "floci").Kind, statusLine(o.Sess, "floci").Message)
	assert.Nil(t, statusLine(o.Sess, "floci").SignIn)
	c, _ := w.sts.Load().(stsCreds)
	r.e0 = c.Expires
	minted := []string{c.Secret, c.Token} // every generation of role keys is a needle

	// 3. Reads: plain s3, a wildcard, DuckLake, and the agent inside its scope.
	got, err := r.readObject(obj)
	require.NoError(t, err)
	assert.Equal(t, "plain", got)
	assert.Equal(t, "slaw_floci", r.secretNamed(obj))
	got, err = r.exec("SELECT count(*) > 0 FROM read_parquet('s3://" + miniotest.Bucket + "/warehouse/ducklake/**/*.parquet')")
	require.NoError(t, err)
	assert.Equal(t, "true", got)
	got, err = r.exec("SELECT count(*) FROM lake.warehouse.dim_students")
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprint(miniotest.Students), got)
	assert.Equal(t, fmt.Sprint(miniotest.Students), fmt.Sprint(rowCount(t, agentCall(h, "SELECT count(*) FROM lake.warehouse.dim_students"))))
	// The secret surfaces, the SSO cache and the process environment stay out
	// of reach at every sandbox tier, `full` included.
	for _, mode := range []sandbox.Mode{sandbox.ReadOnly, sandbox.ReadWrite, sandbox.Full} {
		mh := agents.NewHandler(workspace.NewAgentAccess(o.WS, o.Sess), engine.NewManager(o.Sess, engine.WithLogger(logger)),
			&agents.SandboxConfig{Mode: mode, MaxQueryRows: 10000, RequireApproval: true}, logger)
		for _, q := range []string{
			"SELECT * FROM duckdb_secrets()",
			"SELECT * FROM which_secret('s3://" + miniotest.Bucket + "/x', 's3')",
			"SELECT getenv('AWS_SESSION_TOKEN')",
			"SELECT * FROM read_text('" + w.home + "/.aws/sso/cache/*.json')",
			"SELECT * FROM read_text('~/.aws/sso/cache/*.json')",
			"SELECT * FROM read_blob('" + w.cachePath() + "')",
			"SELECT * FROM read_csv('" + w.cachePath() + "')",
			"SELECT * FROM glob('" + w.home + "/.aws/**')",
			"SELECT * FROM read_text('" + w.home + "/.aws/config')",
			"SELECT * FROM read_text('" + os.TempDir() + "/*')",
		} {
			resp = mh.Handle(ctx, &agents.Request{JSONRPC: "2.0", ID: "1", Method: "run_query", Params: map[string]any{"sql": q}})
			require.NotNil(t, resp.Error, "%s: %s", mode, q)
			assert.Regexp(t, `managed by Slaw|not allowed|outside the workspace|relative path`, resp.Error.Message, "%s: %s", mode, q)
			note(resp.Error.Message)
		}
	}

	// 4. Rotation: new STS keys before the old ones end; the secret is replaced
	// in place, no status changes, the lake keeps reading.
	fresh := assumeRole(t, ep)
	w.sts.Store(fresh)
	hits := w.hits.Load()
	clk.setTo(r.e0.Add(-4 * time.Minute))
	got, err = r.exec("SELECT count(*) FROM lake.warehouse.dim_students")
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprint(miniotest.Students), got)
	require.Eventually(t, func() bool { return w.hits.Load() > hits && r.secretMentions(t, fresh.KeyID) }, 30*time.Second, 20*time.Millisecond)
	assert.Equal(t, session.StatusOK, statusLine(o.Sess, "floci").Kind)
	assert.Empty(t, heard.list(), "a rotation that works changes no status")

	// 5. The Identity Center session ends (the portal refuses the token and the
	// refresh grant): fail closed, with the hint, the lake detached, and an
	// object never read before refused although the bucket is public.
	minted = append(minted, fresh.Secret, fresh.Token)
	w.fake.Expire()
	clk.setTo(fresh.Expires.Add(-3 * time.Minute)) // past the 30 s rotation interval
	_, err = r.exec("SELECT count(*) FROM lake.warehouse.dim_students")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aws sso login --profile dev")
	note(err.Error())
	st = statusLine(o.Sess, "floci")
	assert.Equal(t, session.StatusNeedsCredentials, st.Kind, st.Message)
	require.NotNil(t, st.SignIn)
	assert.Contains(t, heard.list(), "floci")
	var lakes int
	require.NoError(t, o.Sess.DB().QueryRow(`SELECT count(*) FROM duckdb_databases() WHERE database_name = 'lake'`).Scan(&lakes))
	assert.Zero(t, lakes, "the lake is detached")
	assert.Equal(t, "slaw-deny-s3", r.secretNamed(obj2), "the SSO secret is gone and the deny secret answers")
	got, err = r.readObject(obj2)
	require.Error(t, err, "an expired session never falls back to anonymous (got %q)", got)
	requireDenied(t, err, miniotest.Bucket)
	note(err.Error())
	resp = agentCall(h, "SELECT count(*) FROM read_parquet('s3://"+miniotest.Bucket+"/journey/b.csv')")
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Message, `"floci"`)
	note(resp.Error.Message)

	// 6. `aws sso login --profile dev` in a terminal writes the shared token
	// cache. No Slaw call is made: the next statement, once the 30 s revive
	// interval has passed, finds the session and brings back the connection and
	// its lake.
	w.signIn(t)
	w.sts.Store(assumeRole(t, ep))
	clk.setTo(fresh.Expires.Add(-2 * time.Minute))
	// The agent is the reviver (the path `slaw mcp serve` takes): its next read
	// finds the session; the lake is attached again for the engine after it.
	assert.Equal(t, "plain", fmt.Sprint(rowsOfResp(t, agentCall(h, "SELECT a FROM read_csv('"+obj+"', header = false, columns = {'a': 'VARCHAR'})"))[0][0]))
	got, err = r.exec("SELECT count(*) FROM lake.warehouse.dim_students")
	require.NoError(t, err, "the lake it covers is back")
	assert.Equal(t, fmt.Sprint(miniotest.Students), got)
	assert.Equal(t, session.StatusOK, statusLine(o.Sess, "floci").Kind)
	got, err = r.readObject(obj3)
	require.NoError(t, err)
	assert.Equal(t, "revived", got)
	require.Zero(t, w.fake.Waiting(), "no second device flow was started on the person's behalf")

	// 7. The needle audit across every surface this test can see.
	for _, s := range o.Sess.Connections() {
		j, _ := json.Marshal(s)
		note(string(j))
	}
	j, _ := json.Marshal(heard.list())
	note(string(j))
	note(prompt.URL + " " + prompt.Code)
	needles := append(w.needles(), minted...)
	all, _ := json.Marshal(seen)
	noNeedles(t, "statuses, errors and agent results", string(all), needles)
	noNeedles(t, "debug logs", logs.String(), needles)
	assert.Contains(t, logs.String(), "level=DEBUG")
	auditDirs(t, needles, root, cfg)
	auditHome(t, w, minted)
}

// auditHome checks the person's home: the token cache is the one place the SSO
// tokens and the client registration are written (0600 in a 0700 directory),
// and no role key, session token or MinIO secret is written anywhere in it.
func auditHome(t *testing.T, w *ssoWorld, minted []string) {
	t.Helper()
	cacheDir := filepath.Dir(w.cachePath())
	info, err := os.Stat(cacheDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "the token cache directory is private")
	tokenOnly := []string{ssofake.NeedleAccess, ssofake.NeedleRefresh, ssofake.NeedleClientSecret}
	c, _ := w.sts.Load().(stsCreds)
	never := append(minted, c.Secret, c.Token, miniotest.Secret, ssofake.NeedleSecretKey, ssofake.NeedleSessionToken)
	sawToken := false
	require.NoError(t, filepath.WalkDir(w.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		noNeedles(t, p, string(b), never)
		inCache := filepath.Dir(p) == cacheDir
		for _, n := range tokenOnly {
			if strings.Contains(string(b), n) {
				assert.True(t, inCache, "%s holds %s outside the token cache", p, n)
				sawToken = true
			}
		}
		if inCache {
			fi, _ := d.Info()
			assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "%s is private", p)
		} else if filepath.Base(p) != "config" {
			noNeedles(t, p, string(b), []string{"NeedleRoleName", "123456789012", "corp.awsapps.com"})
		}
		return nil
	}))
	assert.True(t, sawToken, "the token cache holds the session")
}
