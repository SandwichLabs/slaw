package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

var (
	_ session.CredentialedType   = postgresType{}
	_ session.CredentialedType   = motherduckType{}
	_ session.CredentialedType   = sqliteType{}
	_ session.CredentialedType   = storageType{}
	_ session.CataloglessType    = storageType{}
	_ session.HubTokenType       = motherduckType{}
	_ session.Refresher          = postgresType{}
	_ session.CredentialedType   = mysqlType{}
	_ session.Refresher          = mysqlType{}
	_ session.CredentialedType   = azureType{}
	_ session.CataloglessType    = azureType{}
	_ session.CredentialedType   = ducklakeType{}
	_ session.PostAttacher       = ducklakeType{}
	_ session.PreAttacher        = ducklakeType{}
	_ session.SpecExtensionsType = ducklakeType{}
	_ session.Stager             = ducklakeType{}
	_ session.CredentialedType   = tableSourceType{}
	_ session.CataloglessType    = tableSourceType{}
	_ session.CredentialedType   = icebergCatalogType{}
	_ session.SpecExtensionsType = icebergCatalogType{}
)

// TestTimeout bounds one Test (secret, attach and probe).
const TestTimeout = 10 * time.Second

// maxRequiredTotal caps a Test that tries storage connections first: each part
// has its own TestTimeout, the whole at most this.
const maxRequiredTotal = 30 * time.Second

// TestResult is what Test returns. Every string in it is redacted.
type TestResult struct {
	OK bool `json:"ok"`
	// Status is the connection status the attempt ended in: ok,
	// needs-credentials, extension-missing or error.
	Status session.StatusKind `json:"status"`
	// Latency covers secret, attach and probe.
	Latency time.Duration `json:"latency"`
	// Count is the table count (catalogs) or object count (storage scopes).
	Count  int    `json:"count"`
	Detail string `json:"detail,omitempty"`
	// Requirement names the storage connection that failed, when the lake or
	// table under test read with another connection's secret and that part
	// failed first ("" when the definition itself failed or all was well).
	Requirement string `json:"requirement,omitempty"`
	// Error is the redacted failure message; it never holds a credential.
	Error string `json:"error,omitempty"`
	// InstallHint names the extension the person can install and retry.
	InstallExtension string `json:"installExtension,omitempty"`
	// Code and Message classify a failure (Code* constants, plain-language
	// Message), set where the error is produced. Empty on success.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// timedOut records that the deadline, not a driver message, ended the attempt.
	timedOut bool
}

// classified returns r with Code and Message filled from its own error.
func (r TestResult) classified(def Def) TestResult {
	if r.OK {
		return r
	}
	r.Code, r.Message = Classify(Failure{Raw: r.Error, Status: r.Status, InstallExtension: r.InstallExtension, TimedOut: r.timedOut, Def: def})
	return r
}

// TestOption tunes Test.
type TestOption func(*testOpts)

type testOpts struct {
	extDir   string
	install  bool
	exe      string
	timeout  time.Duration
	redactor *secrets.Redactor
	// required are the storage connections tried first, in the same session.
	required []requiredStorage
}

// WithTestExtensionDir pins the extension directory (default: the app's).
func WithTestExtensionDir(dir string) TestOption { return func(o *testOpts) { o.extDir = dir } }

// WithInstall is the person's consent to download a missing extension.
func WithInstall(yes bool) TestOption { return func(o *testOpts) { o.install = yes } }

// WithExecutable sets the binary that runs the MotherDuck subprocess test
// (default os.Executable); it must answer to `__conn-test`.
func WithExecutable(path string) TestOption { return func(o *testOpts) { o.exe = path } }

// WithTestTimeout replaces the 10 s limit (tests).
func WithTestTimeout(d time.Duration) TestOption { return func(o *testOpts) { o.timeout = d } }

// WithTestRedactor adds a redactor that also scrubs the result.
func WithTestRedactor(r *secrets.Redactor) TestOption { return func(o *testOpts) { o.redactor = r } }

// Test attaches def with cred in a throwaway, hardened, in-memory DuckDB and
// probes it. It never touches the workspace's session. A MotherDuck definition
// is tested in a subprocess because a process holds one MotherDuck token.
func Test(ctx context.Context, def Def, cred secrets.Credential, opts ...TestOption) TestResult {
	return runClassified(def, func() TestResult { return test(ctx, def, cred, opts...) })
}

// runClassified classifies a failed result against the definition tried. A
// result that came from the MotherDuck child already carries its Code.
func runClassified(def Def, run func() TestResult) TestResult {
	res := run()
	if res.OK || res.Code != "" {
		return res
	}
	return res.classified(def)
}

func test(ctx context.Context, def Def, cred secrets.Credential, opts ...TestOption) TestResult {
	o := testOpts{timeout: TestTimeout}
	for _, f := range opts {
		f(&o)
	}
	red := secrets.NewRedactor()
	o.addRedacted(red, cred)
	for _, r := range o.required {
		o.addRedacted(red, r.cred)
	}
	fail := func(kind session.StatusKind, err error) TestResult {
		msg := secrets.RedactPatterns(red.Redact(err.Error()))
		return TestResult{Status: kind, Error: msg}
	}
	if def.Type == "motherduck" {
		def.ReadOnly = false // MotherDuck has no read-only attach; a test never writes anyway
	}
	if err := Validate(def); err != nil {
		return fail(session.StatusError, err)
	}
	if def.Type == "ducklake" && def.Path != "" && !IsDataURL(def.Path) {
		// A test never writes, and a lake whose catalog file does not exist yet
		// can only be attached by creating it.
		if _, err := os.Stat(def.Path); errors.Is(err, os.ErrNotExist) {
			if o.install {
				// The consent was given: download what the lake needs now, so the
				// first real connect finds it.
				if err := installBeforeOpen(ctx, def, o); err != nil {
					return fail(session.StatusError, err)
				}
			}
			return TestResult{OK: true, Status: session.StatusOK, Detail: "a new lake: its catalog file is created the first time something writes to it"}
		}
	}
	if def.Type == "motherduck" {
		ctx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		return testInSubprocess(ctx, def, cred, o, red)
	}
	if len(o.required) > 0 {
		// Each part (the storage connections, then the definition) has its own
		// limit, runTest applies them; the whole is capped too.
		ctx, cancel := context.WithTimeout(ctx, min(maxRequiredTotal, o.timeout*time.Duration(len(o.required)+1)))
		defer cancel()
		return runTest(ctx, def, cred, o, red)
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	return runTest(ctx, def, cred, o, red)
}

func extensionDir(o testOpts) string {
	if o.extDir != "" {
		return o.extDir
	}
	d, _ := session.DefaultDirs()
	return d
}

// installBeforeOpen downloads the type's extensions into the extension
// directory in a plain instance, before the hardened session opens.
func installBeforeOpen(ctx context.Context, def Def, o testOpts) error {
	t, ok := registry[def.Type]
	if !ok {
		return nil
	}
	ct, ok := t.(session.CredentialedType)
	if !ok {
		return nil
	}
	db, err := session.OpenDB("", session.WithExtensionDirectory(extensionDir(o)))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	for _, e := range session.ExtensionsFor(ct, def.Spec()) {
		// e comes from the registry, never from the definition.
		if _, err := db.ExecContext(ctx, "INSTALL "+e); err != nil {
			return fmt.Errorf("the DuckDB %q extension could not be downloaded; check your network connection and try again: %w", e, err)
		}
	}
	return nil
}

// runTest is the in-process part: used directly for every type but MotherDuck,
// and by the subprocess for MotherDuck.
func runTest(ctx context.Context, def Def, cred secrets.Credential, o testOpts, red *secrets.Redactor) TestResult {
	fail := func(kind session.StatusKind, err error, ext string) TestResult {
		return TestResult{Status: kind, Error: secrets.RedactPatterns(red.Redact(err.Error())), InstallExtension: ext}
	}
	if len(o.required) > 0 {
		if err := checkCovered(def, o.required); err != nil {
			return fail(session.StatusError, err, "")
		}
	}
	if o.install && len(o.required) == 0 {
		if err := installBeforeOpen(ctx, def, o); err != nil {
			return fail(session.StatusError, err, "")
		}
	}
	sopts := []session.Option{
		session.WithConnectionTypes(Types()...),
		session.WithExtensionDirectory(extensionDir(o)),
		session.WithRedactor(red),
	}
	if def.Type == "motherduck" {
		sopts = append(sopts, session.WithMotherDuckToken(cred.Token))
	}
	sess, err := session.New(sopts...)
	if err != nil {
		return fail(session.StatusError, err, "")
	}
	defer sess.Close() //nolint:errcheck

	spec := def.Spec()
	spec.ReadOnly = def.Type != "motherduck" // a test never writes (MotherDuck cannot attach read-only)
	c := cred
	spec.Credential = func(context.Context) (secrets.Credential, error) { return c, nil }
	if len(o.required) > 0 {
		var failed bool
		var res TestResult
		if spec.Requires, res, failed = attachRequired(ctx, sess, def, o, red); failed {
			return res
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
		if o.install {
			if err := installBeforeOpen(ctx, def, o); err != nil {
				return fail(session.StatusError, err, "")
			}
		}
	}
	return attachAndProbe(ctx, sess, def, spec, o, red)
}

// attachAndProbe attaches spec in sess and probes it. ctx carries the limit.
func attachAndProbe(ctx context.Context, sess session.Session, def Def, spec session.Spec, o testOpts, red *secrets.Redactor) TestResult {
	fail := func(kind session.StatusKind, err error, ext string) TestResult {
		return TestResult{Status: kind, Error: secrets.RedactPatterns(red.Redact(err.Error())), InstallExtension: ext}
	}
	start := time.Now()
	if attachesOnlyInWorkspace(def) && len(spec.Requires) == 0 {
		// Read with the storage connection whose scope contains it: this
		// throwaway session has no storage secret. Probe says so.
		_, detail, _ := registry[def.Type].Probe(ctx, sess, spec)
		return TestResult{OK: true, Status: session.StatusOK, Latency: time.Since(start), Detail: detail}
	}
	if err := sess.Attach(ctx, spec); err != nil {
		kind := session.StatusError
		ext := ""
		if st, ok := sess.Status(def.Name); ok {
			kind = st.Kind
			if kind == session.StatusExtensionMissing {
				ext = st.Extension // the first missing one, not the type's first
			}
		}
		if ctx.Err() != nil {
			res := fail(kind, fmt.Errorf("timed out after %s", o.timeout), ext)
			res.timedOut = true
			return res
		}
		return fail(kind, err, ext)
	}
	var (
		n      int
		detail string
		err    error
	)
	if t, ok := registry[def.Type]; ok {
		n, detail, err = t.Probe(ctx, sess, spec)
	} else {
		n, detail, err = duckdbProbe(ctx, sess, spec)
	}
	if err != nil {
		kind := session.StatusError
		if authFailure(err) {
			kind = session.StatusNeedsCredentials
		}
		if ctx.Err() != nil {
			res := fail(kind, fmt.Errorf("timed out after %s", o.timeout), "")
			res.timedOut = true
			return res
		}
		return fail(kind, err, "")
	}
	return TestResult{OK: true, Status: session.StatusOK, Latency: time.Since(start), Count: n, Detail: detail}
}

// attachesOnlyInWorkspace is true for a lake whose catalog is in object
// storage: it needs the storage connection's secret, which only the workspace's
// own session has.
func attachesOnlyInWorkspace(def Def) bool {
	return (def.Type == "ducklake" && IsDataURL(def.Path)) || signsWithStorage(def)
}

// signsWithStorage is true for an S3 Tables catalog: it signs with the secret of
// the s3 connection its option storage names.
func signsWithStorage(def Def) bool {
	return def.Type == "iceberg_catalog" && def.Options["endpoint_type"] == endpointS3Tables
}

// --- MotherDuck subprocess ---

// ChildCommand is the hidden command the subprocess test answers to.
const ChildCommand = "__conn-test"

type childRequest struct {
	// Action is "" (test Def) or childActionListDatabases.
	Action      string             `json:"action,omitempty"`
	Def         Def                `json:"def"`
	Cred        secrets.Credential `json:"cred"`
	Install     bool               `json:"install"`
	ExtensionDr string             `json:"extension_dir,omitempty"`
	TimeoutMS   int64              `json:"timeout_ms"`
}

// childEnv is the environment for the subprocess: everything but MotherDuck's
// ambient token variables, so the token it uses is the one it was handed.
func childEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k := strings.ToLower(strings.SplitN(kv, "=", 2)[0])
		if k == "motherduck_token" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func testInSubprocess(ctx context.Context, def Def, cred secrets.Credential, o testOpts, red *secrets.Redactor) TestResult {
	fail := func(err error) TestResult {
		return TestResult{Status: session.StatusError, Error: secrets.RedactPatterns(red.Redact(err.Error()))}
	}
	if cred.Token == "" {
		return TestResult{Status: session.StatusNeedsCredentials, Error: "No MotherDuck token is set for this connection."}
	}
	line, err := runChildProcess(ctx, childRequest{Def: def, Cred: cred, Install: o.install, ExtensionDr: o.extDir, TimeoutMS: o.timeout.Milliseconds()}, o)
	if err != nil {
		res := fail(err)
		res.timedOut = ctx.Err() != nil
		return res
	}
	var res TestResult
	if err := json.Unmarshal(line, &res); err != nil {
		return fail(errors.New("the MotherDuck test process gave no result"))
	}
	res.Error = secrets.RedactPatterns(red.Redact(res.Error))
	res.Detail = red.Redact(res.Detail)
	res.Message = red.Redact(res.Message)
	return res
}

// runChildProcess re-executes the slaw binary as the MotherDuck child, hands it
// req on stdin (the credential never touches argv or the environment) and
// returns the last line of its stdout: MotherDuck may write other text there.
// Errors are generic and never carry the credential.
func runChildProcess(ctx context.Context, req childRequest, o testOpts) ([]byte, error) {
	exe := o.exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("failed to find the slaw executable: %w", err)
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, ChildCommand)
	cmd.Env = childEnv(os.Environ())
	cmd.WaitDelay = 500 * time.Millisecond // do not wait on grandchildren holding the pipes
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timed out after %s", o.timeout)
		}
		return nil, fmt.Errorf("the MotherDuck test process failed: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	return []byte(lines[len(lines)-1]), nil
}

// RunChild is the subprocess side of the MotherDuck test: it reads one JSON
// request from in, runs the test in this process (which holds exactly one
// MotherDuck token) and writes one JSON line to out. The caller is the hidden
// `slaw __conn-test` command.
func RunChild(ctx context.Context, in io.Reader, out io.Writer) error {
	var req childRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("failed to read the test request: %w", err)
	}
	if req.Action == childActionListDatabases {
		return runListChild(ctx, req, out)
	}
	to := time.Duration(req.TimeoutMS) * time.Millisecond
	if to <= 0 || to > 2*TestTimeout {
		to = TestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	red := secrets.NewRedactor()
	red.AddCredential(req.Cred)
	var res TestResult
	if req.Def.Type == "motherduck" {
		req.Def.ReadOnly = false
	}
	if err := Validate(req.Def); err != nil {
		res = TestResult{Status: session.StatusError, Error: red.Redact(err.Error())}
	} else if req.Def.Type != "motherduck" {
		res = TestResult{Status: session.StatusError, Error: "the subprocess test only handles MotherDuck"}
	} else {
		res = runTest(ctx, req.Def, req.Cred, testOpts{extDir: req.ExtensionDr, install: req.Install, timeout: to}, red)
	}
	return json.NewEncoder(out).Encode(runClassified(req.Def, func() TestResult { return res }))
}

func duckdbProbe(ctx context.Context, sess session.Session, s session.Spec) (int, string, error) {
	var n int
	err := sess.DB().QueryRowContext(ctx, `SELECT count(*) FROM duckdb_tables() WHERE database_name = $1`, s.Name).Scan(&n)
	if err != nil {
		return 0, "", fmt.Errorf("attached, but listing tables failed: %w", err)
	}
	return n, fmt.Sprintf("%d tables", n), nil
}

// authFailure recognises a rejected key: S3-compatible stores answer 403 with
// InvalidAccessKeyId, SignatureDoesNotMatch or AccessDenied; Azure with
// AuthenticationFailed, AuthorizationFailure or an AADSTS error; MySQL with
// "Access denied for user".
func authFailure(err error) bool {
	m := strings.ToLower(err.Error())
	for _, k := range []string{"403", "forbidden", "invalidaccesskeyid", "signaturedoesnotmatch", "accessdenied", "access denied", "not authenticated", "authentication failed", "authenticationfailed", "authorizationfailure", "authorizationpermissionmismatch", "aadsts", "invalid_client"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}
