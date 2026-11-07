package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

// memWorkspace opens a fresh workspace whose credentials live in memory.
func memWorkspace(t *testing.T, env map[string]string) (Workspace, *secrets.MemoryStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "acme")
	res, err := Init(dir)
	require.NoError(t, err)
	_ = res.Workspace.Close()
	store := secrets.NewMemoryStore()
	r := secrets.NewResolver(secrets.WithKeychain(store), secrets.WithEnv(func(k string) string { return env[k] }))
	cfg := t.TempDir()
	ws, err := Open(res.Workspace.Root(), WithConfigDir(cfg), WithUserConfig(UserConfig{Version: ConfigVersion}), WithResolver(r))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Close() })
	return ws, store, cfg
}

func pgConn(name string) ConnectionDef {
	return ConnectionDef{Name: name, Type: "postgres", Host: "db.internal", Port: 5432, Database: "analytics"}
}

func TestAddUpdateRemoveInEachScope(t *testing.T) {
	ws, _, cfg := memWorkspace(t, nil)
	ctx := context.Background()

	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("shared_pg")))
	require.NoError(t, ws.AddConnection(ScopeLocal, pgConn("local_pg")))
	require.NoError(t, ws.AddConnection(ScopeUser, ConnectionDef{Name: "user_lite", Type: "sqlite", Path: filepath.Join(t.TempDir(), "x.sqlite")}))

	scopes := map[string]Scope{}
	for _, c := range ws.Connections() {
		scopes[c.Name] = c.Scope
	}
	assert.Equal(t, ScopeShared, scopes["shared_pg"])
	assert.Equal(t, ScopeLocal, scopes["local_pg"])
	assert.Equal(t, ScopeUser, scopes["user_lite"])

	// slaw.yaml carries the definition and no username or secret.
	b, err := os.ReadFile(filepath.Join(ws.Root(), "slaw.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "db.internal")
	assert.NotContains(t, string(b), "password:")
	assert.FileExists(t, filepath.Join(cfg, "connections.yaml"))

	// Duplicates within a scope are refused; the same name in another scope is a shadow.
	require.Error(t, ws.AddConnection(ScopeShared, pgConn("shared_pg")))
	require.NoError(t, ws.AddConnection(ScopeLocal, pgConn("shared_pg")))
	for _, c := range ws.Connections() {
		if c.Name == "shared_pg" {
			assert.Equal(t, ScopeLocal, c.Scope)
			assert.Equal(t, []Scope{ScopeShared}, c.Shadows)
		}
	}
	require.NoError(t, ws.RemoveConnection(ctx, ScopeLocal, "shared_pg"))

	up := pgConn("shared_pg")
	up.Host = "db-new.internal"
	require.NoError(t, ws.UpdateConnection(ScopeShared, up))
	for _, c := range ws.Connections() {
		if c.Name == "shared_pg" {
			assert.Equal(t, "db-new.internal", c.Host)
		}
	}
	require.Error(t, ws.UpdateConnection(ScopeShared, pgConn("nope")))

	require.NoError(t, ws.RemoveConnection(ctx, ScopeShared, "shared_pg"))
	require.Error(t, ws.RemoveConnection(ctx, ScopeShared, "shared_pg"))
	require.NoError(t, ws.RemoveConnection(ctx, ScopeUser, "user_lite"))
}

func TestAddValidatesByScopeAndType(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	abs := filepath.Join(t.TempDir(), "x.sqlite")
	for name, c := range map[string]struct {
		scope Scope
		def   ConnectionDef
	}{
		"shared absolute path": {ScopeShared, ConnectionDef{Name: "a", Type: "sqlite", Path: abs}},
		"shared dotdot":        {ScopeShared, ConnectionDef{Name: "a", Type: "sqlite", Path: "../x.sqlite"}},
		"shared env":           {ScopeShared, ConnectionDef{Name: "a", Type: "postgres", Host: "${PGHOST}", Database: "d"}},
		"user relative path":   {ScopeUser, ConnectionDef{Name: "a", Type: "sqlite", Path: "x.sqlite"}},
		"bad type field":       {ScopeLocal, ConnectionDef{Name: "a", Type: "postgres", Host: "bad host", Database: "d"}},
		"unknown option":       {ScopeLocal, ConnectionDef{Name: "a", Type: "postgres", Host: "h", Database: "d", Options: map[string]string{"application_name": "x"}}},
		"password in host":     {ScopeLocal, ConnectionDef{Name: "a", Type: "postgres", Host: "postgres://u:pw@h", Database: "d"}},
		"secret-ish option":    {ScopeLocal, ConnectionDef{Name: "a", Type: "postgres", Host: "h", Database: "d", Options: map[string]string{"password": "x"}}},
		"s3 wrong scheme":      {ScopeLocal, ConnectionDef{Name: "a", Type: "s3", Scope: "gs://b/"}},
		"bad name":             {ScopeLocal, ConnectionDef{Name: "Bad", Type: "sqlite", Path: "p"}},
		"reserved name":        {ScopeLocal, ConnectionDef{Name: "memory", Type: "sqlite", Path: "p"}},
		"unknown type":         {ScopeLocal, ConnectionDef{Name: "a", Type: "oracle", Host: "h"}},
		"unknown scope":        {Scope("team"), pgConn("a")},
	} {
		assert.Error(t, ws.AddConnection(c.scope, c.def), name)
	}
	// ${ENV} is fine in your own scope (checked when it is resolved at open).
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "envhost", Type: "postgres", Host: "${PGHOST}", Database: "d"}))
	// And a relative sqlite path inside a shared scope is fine.
	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "rel", Type: "sqlite", Path: "data/x.sqlite"}))
}

func TestSignInIsPerPersonAndOutsideTheFolder(t *testing.T) {
	ws, _, cfg := memWorkspace(t, nil)
	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("pg")))
	require.Error(t, ws.SetSignIn("pg", "plaintext"))
	require.Error(t, ws.SetSignIn("nope", "keychain"))
	require.NoError(t, ws.SetSignIn("pg", "env:PG_PROD"))
	for _, c := range ws.Connections() {
		if c.Name == "pg" {
			assert.Equal(t, "env:PG_PROD", c.SignIn)
		}
	}
	b, _ := os.ReadFile(filepath.Join(ws.Root(), "slaw.yaml"))
	assert.NotContains(t, string(b), "PG_PROD", "the committed file never says how you sign in")
	assert.Contains(t, readLocalYAML(t, ws), "PG_PROD")
	assert.True(t, strings.HasPrefix(ws.LocalConfigPath(), cfg+string(filepath.Separator)))

	// A user connection keeps its sign-in in the user file.
	require.NoError(t, ws.AddConnection(ScopeUser, ConnectionDef{Name: "u", Type: "sqlite", Path: filepath.Join(t.TempDir(), "u.sqlite")}))
	ud := ConnectionDef{Name: "umd", Type: "motherduck", Database: "d"}
	require.NoError(t, ws.AddConnection(ScopeUser, ud))
	require.NoError(t, ws.SetSignIn("umd", "env:MOTHERDUCK"))
	data, _ := os.ReadFile(filepath.Join(cfg, "connections.yaml"))
	assert.Contains(t, string(data), "env:MOTHERDUCK")
}

func readLocalYAML(t *testing.T, ws Workspace) string {
	t.Helper()
	b, err := os.ReadFile(ws.LocalConfigPath())
	require.NoError(t, err)
	return string(b)
}

func TestCredentialsAreWriteOnlyBoundAndClearedOnRemove(t *testing.T) {
	ws, store, _ := memWorkspace(t, nil)
	ctx := context.Background()
	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("pg")))
	require.NoError(t, ws.SetCredentials(ctx, "pg", secrets.Credential{User: "rosa", Password: "pw_needle_1"}, false))

	st, err := ws.CredentialStatus(ctx, "pg")
	require.NoError(t, err)
	assert.True(t, st.SecretSet)
	assert.Equal(t, "rosa", st.Username)
	assert.True(t, st.FingerprintMatches)

	cred, rc, err := ConnectionCredential(ctx, ws, "pg", nil)
	require.NoError(t, err)
	assert.Equal(t, "pw_needle_1", cred.Password)
	assert.NotContains(t, cred.String(), "pw_needle_1", "a credential never prints its secret")
	assert.Equal(t, "db.internal:5432/analytics", ServerDescription(rc.Resolved))

	// Changing the server makes the saved credential stop applying.
	up := pgConn("pg")
	up.Host = "other.internal"
	require.NoError(t, ws.UpdateConnection(ScopeShared, up))
	_, _, err = ConnectionCredential(ctx, ws, "pg", nil)
	var fp *secrets.ErrFingerprintChanged
	require.ErrorAs(t, err, &fp)
	st, _ = ws.CredentialStatus(ctx, "pg")
	assert.False(t, st.FingerprintMatches)

	require.NoError(t, ws.RemoveConnection(ctx, ScopeShared, "pg"))
	_, err = store.Get(ctx, secrets.WorkspaceRef(ws.ID(), "pg"))
	assert.ErrorIs(t, err, secrets.ErrNotFound, "removing a connection clears its credentials")

	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "lite", Type: "sqlite", Path: "x.sqlite"}))
	require.Error(t, ws.SetCredentials(ctx, "lite", secrets.Credential{Password: "x"}, false), "sqlite has no credentials")
}

func TestMoveKeepsTheCredentialBound(t *testing.T) {
	ws, store, _ := memWorkspace(t, nil)
	ctx := context.Background()
	require.NoError(t, ws.AddConnection(ScopeLocal, pgConn("pg")))
	require.NoError(t, ws.SetCredentials(ctx, "pg", secrets.Credential{User: "rosa", Password: "pw_needle_2"}, false))

	// local -> shared: same workspace reference, the credential does not move.
	require.NoError(t, ws.MoveConnection(ctx, "pg", ScopeLocal, ScopeShared))
	cred, _, err := ConnectionCredential(ctx, ws, "pg", nil)
	require.NoError(t, err)
	assert.Equal(t, "pw_needle_2", cred.Password)
	assert.Equal(t, ScopeShared, mustConn(t, ws, "pg").Scope)

	// shared -> user: a different keychain reference, so the entry migrates.
	require.NoError(t, ws.MoveConnection(ctx, "pg", ScopeShared, ScopeUser))
	assert.Equal(t, ScopeUser, mustConn(t, ws, "pg").Scope)
	_, err = store.Get(ctx, secrets.UserRef("pg"))
	require.NoError(t, err)
	_, err = store.Get(ctx, secrets.WorkspaceRef(ws.ID(), "pg"))
	assert.ErrorIs(t, err, secrets.ErrNotFound)
	cred, _, err = ConnectionCredential(ctx, ws, "pg", nil)
	require.NoError(t, err)
	assert.Equal(t, "pw_needle_2", cred.Password)

	require.Error(t, ws.MoveConnection(ctx, "pg", ScopeUser, ScopeUser))
	require.Error(t, ws.MoveConnection(ctx, "pg", ScopeLocal, ScopeShared), "not defined there")
}

func TestMoveToSharedAppliesSharedRules(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	ctx := context.Background()
	abs := filepath.Join(t.TempDir(), "x.sqlite")
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "lite", Type: "sqlite", Path: abs}))
	err := ws.MoveConnection(ctx, "lite", ScopeLocal, ScopeShared)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
	assert.Equal(t, ScopeLocal, mustConn(t, ws, "lite").Scope, "a refused move changes nothing")
}

func mustConn(t *testing.T, ws Workspace, name string) Connection {
	t.Helper()
	for _, c := range ws.Connections() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no connection %s", name)
	return Connection{}
}

func TestEnvSignInDoesNotNeedTheKeychain(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"PGP_USER": "svc", "PGP_PASSWORD": "pw_needle_3"})
	ctx := context.Background()
	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("pg")))
	require.NoError(t, ws.SetSignIn("pg", "env:PGP"))
	cred, _, err := ConnectionCredential(ctx, ws, "pg", nil)
	require.NoError(t, err)
	assert.Equal(t, "svc", cred.User)
	require.Error(t, ws.SetCredentials(ctx, "pg", secrets.Credential{Password: "x"}, false), "env credentials are set outside Slaw")
}

// --- OpenSession ---

func TestOpenSessionDialsNothingRemoteAndReportsNeedsCredentials(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	ctx := context.Background()
	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("pg")))
	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "lake", Type: "s3", Scope: "s3://acme/raw/", Region: "us-east-1"}))
	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "md", Type: "motherduck", Database: "d"}))
	sess, issues, err := OpenSession(ctx, ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	defer sess.Close()
	assert.Empty(t, issues)
	st := map[string]session.Status{}
	for _, s := range sess.Connections() {
		st[s.Name] = s
	}
	// Extension first (nothing is installed in an empty extension directory),
	// and never connected.
	for _, n := range []string{"pg", "lake", "md"} {
		assert.Equal(t, session.StatusExtensionMissing, st[n].Kind, n)
	}
	assert.Equal(t, session.StatusOK, st["local"].Kind)
}

func TestMotherDuckTwoAccountsIsAnIssueOnTheSecond(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"A_TOKEN": "tok_account_a", "B_TOKEN": "tok_account_b"})
	ctx := context.Background()
	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "md_a", Type: "motherduck", Database: "da"}))
	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "md_b", Type: "motherduck", Database: "db"}))
	require.NoError(t, ws.SetSignIn("md_a", "env:A"))
	require.NoError(t, ws.SetSignIn("md_b", "env:B"))
	sess, issues, err := OpenSession(ctx, ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	defer sess.Close()
	require.Len(t, issues, 1)
	assert.Equal(t, "md_b", issues[0].Connection)
	assert.Contains(t, issues[0].Message, "one account")
	assert.NotContains(t, issues[0].Message, "tok_account")
}

func TestSameMotherDuckAccountTwiceIsFine(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"MOTHERDUCK_TOKEN": "tok_same"})
	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "md_a", Type: "motherduck", Database: "da"}))
	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "md_b", Type: "motherduck", Database: "db"}))
	require.NoError(t, ws.SetSignIn("md_a", "env:MOTHERDUCK"))
	require.NoError(t, ws.SetSignIn("md_b", "env:MOTHERDUCK"))
	sess, issues, err := OpenSession(context.Background(), ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	defer sess.Close()
	assert.Empty(t, issues)
}

func TestOpenSessionScrubsTheAmbientMotherDuckTokenOnlyForAMotherDuckWorkspace(t *testing.T) {
	t.Setenv("MOTHERDUCK_TOKEN", "ambient_needle")
	ws, _, _ := memWorkspace(t, nil)
	sess, _, err := OpenSession(context.Background(), ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	_ = sess.Close()
	assert.Equal(t, "ambient_needle", os.Getenv("MOTHERDUCK_TOKEN"), "no MotherDuck connection: the env is not touched")

	require.NoError(t, ws.AddConnection(ScopeShared, ConnectionDef{Name: "md", Type: "motherduck", Database: "d"}))
	require.NoError(t, ws.Resolver().SetConnectionCredentials(context.Background(), secrets.WorkspaceRef(ws.ID(), "md"), "keychain",
		secrets.Credential{Token: "keychain_tok"}, BindingFor(ConnectionDef{Name: "md", Type: "motherduck", Database: "d"})))
	sess, _, err = OpenSession(context.Background(), ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	defer sess.Close()
	_, still := os.LookupEnv("MOTHERDUCK_TOKEN")
	assert.False(t, still, "the ambient token is removed before the extension could pick it up")
}

func TestSnapshotEnvSurvivesAScrub(t *testing.T) {
	t.Setenv("MOTHERDUCK_TOKEN", "snap_tok")
	get := SnapshotEnv()
	session.ScrubMotherDuckEnv()
	assert.Equal(t, "snap_tok", get("MOTHERDUCK_TOKEN"))
	assert.Empty(t, os.Getenv("MOTHERDUCK_TOKEN"))
}

func TestLazyAttachThroughAQualifiedName(t *testing.T) {
	// A sqlite file attached lazily would need the extension; use the session's
	// own duckdb type through a remote-style stub is covered in package
	// session. Here: an unknown catalog is still an error.
	ws, _, _ := memWorkspace(t, nil)
	sess, _, err := OpenSession(context.Background(), ws, nil)
	require.NoError(t, err)
	defer sess.Close()
	_, err = sess.Resolve(context.Background(), session.TableRef{Catalog: "nope", Name: "t"})
	require.Error(t, err)
}

// --- fingerprint inputs ---

func TestEveryDeclaredFingerprintFieldChangesTheFingerprint(t *testing.T) {
	base := map[string]ConnectionDef{
		"postgres":        {Name: "x", Type: "postgres", Host: "h", Port: 5432, Database: "d", Options: map[string]string{"sslmode": "require"}},
		"motherduck":      {Name: "x", Type: "motherduck", Database: "d"},
		"sqlite":          {Name: "x", Type: "sqlite", Path: "data/x.sqlite"},
		"duckdb":          {Name: "x", Type: "duckdb", Path: "data/x.duckdb"},
		"s3":              {Name: "x", Type: "s3", Scope: "s3://b/", Region: "us-east-1", Options: map[string]string{"endpoint": "e", "url_style": "path", "use_ssl": "true"}},
		"gcs":             {Name: "x", Type: "gcs", Scope: "gs://b/"},
		"r2":              {Name: "x", Type: "r2", Scope: "r2://b/", Options: map[string]string{"account_id": "a1"}},
		"mysql":           {Name: "x", Type: "mysql", Host: "h", Port: 3306, Database: "d", Options: map[string]string{"ssl_mode": "required"}},
		"azure":           {Name: "x", Type: "azure", Scope: "az://c/", Options: map[string]string{"sign_in": "service_principal", "account_name": "acct", "tenant_id": "t1"}},
		"ducklake":        {Name: "x", Type: "ducklake", Path: "data/x.ducklake", Options: map[string]string{"data_path": "data/files"}},
		"iceberg":         {Name: "x", Type: "iceberg", Path: "data/ice"},
		"delta":           {Name: "x", Type: "delta", Path: "data/delta"},
		"iceberg_catalog": {Name: "x", Type: "iceberg_catalog", Options: map[string]string{"endpoint_type": "rest", "uri": "https://c.example.com/api", "warehouse": "w"}},
	}
	for _, info := range connections.ListConnectionTypes() {
		d := base[info.Name]
		require.NotEmpty(t, d.Type, "no base definition for %s", info.Name)
		f0 := Fingerprint(d)
		for _, field := range info.FingerprintFields {
			m := d
			m.Options = map[string]string{}
			for k, v := range d.Options {
				m.Options[k] = v
			}
			switch {
			case field == "type":
				m.Type += "x"
			case field == "host":
				m.Host += "x"
			case field == "port":
				m.Port++
			case field == "database":
				m.Database += "x"
			case field == "scope":
				m.Scope += "x"
			case field == "path":
				m.Path += "x"
			case field == "region":
				m.Region += "x"
			case len(field) > 7 && field[:7] == "option:":
				m.Options[field[7:]] += "x"
			default:
				t.Fatalf("%s: unknown fingerprint field %q", info.Name, field)
			}
			assert.NotEqual(t, f0, Fingerprint(m), "%s: %s", info.Name, field)
		}
		// Naming and read-only are not part of what a credential is bound to.
		m := d
		m.Name, m.ReadOnly = "renamed", Bool(false)
		assert.Equal(t, f0, Fingerprint(m), info.Name)
	}
}

// --- review #115 D-1: a committed credentials: value is only a hint ---

func TestSharedCredentialsValueIsOnlyAHint(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"PG_PROD_USER": "alice", "PG_PROD_PASSWORD": "ExfilNeedle_777"})
	ctx := context.Background()
	d := pgConn("pg")
	d.Credentials = "env:PG_PROD" // what a pulled commit says
	require.NoError(t, ws.AddConnection(ScopeShared, d))
	c := mustConn(t, ws, "pg")
	assert.Equal(t, "keychain", c.SignIn, "the effective method is the person's own, defaulting to the keychain")
	assert.Equal(t, "env:PG_PROD", c.SignInHint)

	_, _, err := ConnectionCredential(ctx, ws, "pg", nil)
	var nc *secrets.ErrNeedsCredentials
	require.ErrorAs(t, err, &nc, "the environment is not read for a method the person never chose")

	st, err := ws.CredentialStatus(ctx, "pg")
	require.NoError(t, err)
	assert.Equal(t, "keychain", st.Method)
	assert.False(t, st.SecretSet)
}

func TestEnvSignInIsBoundToTheDefinitionItWasChosenFor(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"PGP_USER": "svc", "PGP_PASSWORD": "pw_needle_bound"})
	ctx := context.Background()
	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("pg")))
	require.NoError(t, ws.SetSignIn("pg", "env:PGP"))
	_, _, err := ConnectionCredential(ctx, ws, "pg", nil)
	require.NoError(t, err)
	assert.Contains(t, readLocalYAML(t, ws), "sign_in_fingerprint")

	// A pull re-points the connection: the environment password is withheld.
	up := pgConn("pg")
	up.Host = "attacker.example"
	require.NoError(t, ws.UpdateConnection(ScopeShared, up))
	cred, _, err := ConnectionCredential(ctx, ws, "pg", nil)
	var fp *secrets.ErrFingerprintChanged
	require.ErrorAs(t, err, &fp)
	assert.Empty(t, cred.Password)
	st, _ := ws.CredentialStatus(ctx, "pg")
	assert.False(t, st.FingerprintMatches)
	assert.False(t, st.SecretSet)

	// Choosing the method again does not silently follow the pulled host...
	require.ErrorIs(t, ws.SetSignIn("pg", "env:PGP"), ErrRebindNeedsConfirm)
	// ...a confirmed re-bind does.
	require.NoError(t, ws.RebindSignIn("pg", "env:PGP"))
	_, _, err = ConnectionCredential(ctx, ws, "pg", nil)
	require.NoError(t, err)
}

func TestHandWrittenEnvSignInWithoutABindingIsWithheld(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"PGP_PASSWORD": "pw_needle_unbound"})
	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("pg")))
	writeLocal(t, ws, "sign_in:\n  pg: env:PGP\n")
	_, _, err := ConnectionCredential(context.Background(), ws, "pg", nil)
	var fp *secrets.ErrFingerprintChanged
	require.ErrorAs(t, err, &fp)
}

func TestOpenSessionDoesNotSendAHintedEnvPassword(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"PG_PROD_USER": "alice", "PG_PROD_PASSWORD": "ExfilNeedle_777"})
	d := pgConn("pg")
	d.Credentials = "env:PG_PROD"
	require.NoError(t, ws.AddConnection(ScopeShared, d))
	sess, _, err := OpenSession(context.Background(), ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	defer sess.Close()
	for _, st := range sess.Connections() {
		if st.Name == "pg" {
			assert.NotContains(t, st.Message, "ExfilNeedle_777")
		}
	}
}

func TestMotherDuckReadOnlyRules(t *testing.T) {
	ws, _, _ := memWorkspace(t, map[string]string{"MOTHERDUCK_TOKEN": "tok_md_ro"})
	d := ConnectionDef{Name: "md", Type: "motherduck", Database: "d", ReadOnly: Bool(true)}
	err := ws.AddConnection(ScopeShared, d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-scaling token")

	d.ReadOnly = nil
	require.NoError(t, ws.AddConnection(ScopeShared, d))
	assert.False(t, mustConn(t, ws, "md").IsReadOnly(), "MotherDuck defaults to read_only: false")

	// A hand-written slaw.yaml that says true is an issue, not a silent write.
	writeLocal(t, ws, "connections:\n  - {name: md2, type: motherduck, database: d2, read_only: true}\n")
	require.NoError(t, ws.SetSignIn("md", "env:MOTHERDUCK"))
	sess, issues, err := OpenSession(context.Background(), ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	defer sess.Close()
	require.Len(t, issues, 1)
	assert.Equal(t, "md2", issues[0].Connection)
	assert.Contains(t, issues[0].Message, "can't be attached read-only")
}

// --- review #115: concurrent writers do not lose each other's edits ---

func TestStaleHandleAddDoesNotClobberAnotherWritersConnection(t *testing.T) {
	a, _, cfg := memWorkspace(t, nil)
	b, err := Open(a.Root(), WithConfigDir(cfg), WithUserConfig(UserConfig{Version: ConfigVersion}), WithResolver(a.Resolver()))
	require.NoError(t, err)
	defer b.Close()

	require.NoError(t, a.AddConnection(ScopeShared, pgConn("from_a")))
	// b still holds the file as it was before a's write.
	require.NoError(t, b.AddConnection(ScopeShared, pgConn("from_b")))
	require.NoError(t, b.Reload())
	names := map[string]bool{}
	for _, c := range b.Connections() {
		names[c.Name] = true
	}
	assert.True(t, names["from_a"] && names["from_b"], "both edits survive: %v", names)

	// Same for the local file and for sign-in choices.
	require.NoError(t, a.AddConnection(ScopeLocal, pgConn("la")))
	require.NoError(t, b.AddConnection(ScopeLocal, pgConn("lb")))
	require.NoError(t, a.SetSignIn("la", "prompt"))
	require.NoError(t, b.SetSignIn("lb", "prompt"))
	require.NoError(t, a.Reload())
	got := map[string]string{}
	for _, c := range a.Connections() {
		got[c.Name] = c.SignIn
	}
	assert.Equal(t, "prompt", got["la"])
	assert.Equal(t, "prompt", got["lb"])
}

func TestParallelAddsAllLand(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, ws.AddConnection(ScopeShared, pgConn(fmt.Sprintf("c%d", i))))
		}(i)
	}
	wg.Wait()
	require.NoError(t, ws.Reload())
	n := 0
	for _, c := range ws.Connections() {
		if strings.HasPrefix(c.Name, "c") && c.Name != "" && c.Scope == ScopeShared && c.Type == "postgres" {
			n++
		}
	}
	assert.Equal(t, 8, n)
}

func TestSetSignIn_NeverRebindsAnEnvBindingSilently(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	require.NoError(t, ws.AddConnection(ScopeShared, pgConn("pg")))
	require.NoError(t, ws.SetSignIn("pg", "env:PGPROD")) // a first binding is the person's choice

	// A pull points the connection somewhere else.
	moved := pgConn("pg")
	moved.Host = "db-new.internal"
	require.NoError(t, ws.UpdateConnection(ScopeShared, moved))

	err := ws.SetSignIn("pg", "env:PGPROD")
	require.ErrorIs(t, err, ErrRebindNeedsConfirm)
	c, _ := findConn(ws, "pg")
	assert.Equal(t, "db.internal:5432/analytics", c.SignInServer, "the binding still names the old server")
	assert.Error(t, signInGuard(secrets.NewResolver(), c, mustResolve(t, c)), "so the credential is still withheld")

	// Switching to a method that carries its own binding needs no confirmation.
	require.NoError(t, ws.SetSignIn("pg", "keychain"))
	// A confirmed re-bind goes through and records the new server.
	require.NoError(t, ws.RebindSignIn("pg", "env:PGPROD"))
	c, _ = findConn(ws, "pg")
	assert.Equal(t, "db-new.internal:5432/analytics", c.SignInServer)
	assert.NoError(t, signInGuard(secrets.NewResolver(), c, mustResolve(t, c)))

	// The guard's error names both servers, for env as for the keychain.
	require.NoError(t, ws.UpdateConnection(ScopeShared, pgConn("pg")))
	c, _ = findConn(ws, "pg")
	var fc *secrets.ErrFingerprintChanged
	require.ErrorAs(t, signInGuard(secrets.NewResolver(), c, mustResolve(t, c)), &fc)
	assert.Equal(t, "db-new.internal:5432/analytics", fc.Old)
	assert.Equal(t, "db.internal:5432/analytics", fc.New)
}

func findConn(ws Workspace, name string) (Connection, bool) {
	for _, c := range ws.Connections() {
		if c.Name == name {
			return c, true
		}
	}
	return Connection{}, false
}

func mustResolve(t *testing.T, c Connection) ConnectionDef {
	t.Helper()
	d, err := c.ConnectionDef.Resolve(nil, c.Scope)
	require.NoError(t, err)
	return d
}

// ConnectionCredential (test helper: the saved connection's credential by its sign-in method) resolves the person's credential for one connection of
// ws by their sign-in method. It is a Go-level helper for Test and the CLI; it
// is never bound across the Wails bridge, MCP or the agent (write-only). The
// result is registered with the resolver's redactor.
func ConnectionCredential(ctx context.Context, ws Workspace, name string, lookup LookupFunc) (secrets.Credential, ResolvedConnection, error) {
	if c, ok := ws.Connection(name); ok {
		rc, err := ResolveConnection(ws, c, lookup)
		if err != nil {
			return secrets.Credential{}, ResolvedConnection{}, err
		}
		if !connections.NeedsCredential(rc.Resolved.Type) {
			return secrets.Credential{}, rc, nil
		}
		if err := signInGuard(ws.Resolver(), c, rc.Resolved); err != nil {
			return secrets.Credential{}, rc, err
		}
		cred, err := ws.Resolver().Resolve(ctx, credentialRef(ws.ID(), c.Scope, c.Name), c.SignIn, rc.Binding())
		return cred, rc, err
	}
	return secrets.Credential{}, ResolvedConnection{}, fmt.Errorf("unknown connection %q", name)
}

func urlLake(_ string) ConnectionDef {
	return ConnectionDef{Name: "lake", Type: "ducklake", Path: "s3://bucket/lake/catalog.db", ReadOnly: boolPtr(true),
		Options: map[string]string{"data_path": "s3://bucket/lake/data/", "catalog": "sqlite"}}
}

// A DuckLake catalog in object storage is resolved as a URL (never a path
// inside the workspace), checked against the scope of a storage connection.
func TestDuckLakeURLCatalogResolvesAsURL(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	d := urlLake("sqlite")
	err := ws.AddConnection(ScopeLocal, d)
	require.NoError(t, err, "a URL catalog saves (the scope is checked when it is opened)")
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "store", Type: "s3", Scope: "s3://bucket/", Region: "us-east-1"}))

	rc, err := ResolveConnection(ws, Connection{ConnectionDef: d, Scope: ScopeLocal}, nil)
	require.NoError(t, err)
	assert.Equal(t, "s3://bucket/lake/catalog.db", rc.Path)
	assert.Equal(t, "s3://bucket/lake/data/", rc.Resolved.Options["data_path"])
	assert.False(t, usesLocalPath(rc.Resolved))
	assert.Equal(t, "s3://bucket/lake/catalog.db", ServerDescription(rc.Resolved))
	_, statErr := os.Stat(filepath.Join(ws.Root(), "s3:"))
	assert.True(t, os.IsNotExist(statErr))

	// The read-only rule still holds at save.
	w := d
	w.ReadOnly = boolPtr(false)
	w.Name = "wlake"
	err = ws.AddConnection(ScopeLocal, w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read")
}

func TestDuckLakeCatalogOutsideEveryScopeIsRefused(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "store", Type: "s3", Scope: "s3://bucket/lake/data/", Region: "us-east-1"}))
	d := urlLake("sqlite")
	// The data path is inside the scope; the catalog is not.
	_, err := ResolveConnection(ws, Connection{ConnectionDef: d, Scope: ScopeLocal}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the catalog")
	assert.Contains(t, err.Error(), "not inside the scope")

	// And the other way round.
	d2 := d
	d2.Path = "s3://bucket/lake/data/catalog.db"
	d2.Options = map[string]string{"data_path": "s3://other/x/", "catalog": "sqlite"}
	_, err = ResolveConnection(ws, Connection{ConnectionDef: d2, Scope: ScopeLocal}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the data path")
}

// One hand-edited URL catalog outside every storage scope does not stop the
// workspace opening: that connection is skipped with an issue and the others work.
func TestHandEditedURLCatalogIsAnIssueNotAFailure(t *testing.T) {
	ws, store, cfg := memWorkspace(t, nil)
	root := ws.Root()
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/lake.ducklake", Options: map[string]string{"data_path": "data/files"}}))
	require.NoError(t, ws.AddConnection(ScopeLocal, pgConn("other")))
	require.NoError(t, ws.Close())
	var edited bool
	require.NoError(t, filepath.Walk(cfg, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == LocalFile {
			b, rerr := os.ReadFile(p)
			require.NoError(t, rerr)
			require.Contains(t, string(b), "data/lake.ducklake")
			require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(b), "data/lake.ducklake", "s3://bucket/lake/c.db", 1)), 0o600))
			edited = true
		}
		return nil
	}))
	require.True(t, edited)

	r := secrets.NewResolver(secrets.WithKeychain(store))
	ws2, err := Open(root, WithConfigDir(cfg), WithUserConfig(UserConfig{Version: ConfigVersion}), WithResolver(r))
	require.NoError(t, err, "the workspace still opens")
	defer ws2.Close() //nolint:errcheck
	sess, issues, err := OpenSession(context.Background(), ws2, nil)
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck
	require.Len(t, issues, 1)
	assert.Equal(t, "lake", issues[0].Connection)
	assert.Contains(t, issues[0].Message, "the catalog")
	assert.Contains(t, issues[0].Message, "not inside the scope")
	_, ok := sess.Status("other")
	assert.True(t, ok, "the other connection is registered")
}

// The storage connections that matched the lake's URLs become Spec.Requires
// (sorted, once each), straight from the scope check.
func TestStorageURLsFillRequires(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "zstore", Type: "s3", Scope: "s3://bucket/lake/data/", Region: "us-east-1"}))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "astore", Type: "s3", Scope: "s3://bucket/lake/", Region: "us-east-1"}))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "other", Type: "s3", Scope: "s3://unrelated/", Region: "us-east-1"}))
	d := urlLake("sqlite")
	require.NoError(t, ws.AddConnection(ScopeLocal, d))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "tbl", Type: "delta", Path: "s3://unrelated/t"}))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "loc", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "data/f"}}))

	spec, err := SessionSpec(ws, "lake")
	require.NoError(t, err)
	assert.Equal(t, []string{"astore", "zstore"}, spec.Requires, "the catalog is only inside astore; the data path is inside both and zstore is the more specific scope")

	spec, err = SessionSpec(ws, "tbl")
	require.NoError(t, err)
	assert.Equal(t, []string{"other"}, spec.Requires)

	spec, err = SessionSpec(ws, "loc")
	require.NoError(t, err)
	assert.Empty(t, spec.Requires, "local files require nothing")
}
