package connections

import (
	"context"
	"github.com/SandwichLabs/slaw/internal/storageurl"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

func TestMain(m *testing.M) {
	// The MotherDuck subprocess test re-executes this test binary.
	if len(os.Args) > 1 && os.Args[1] == ChildCommand {
		if err := RunChild(context.Background(), os.Stdin, os.Stdout); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var evil = []string{
	"a'b", "a'; DROP TABLE x; --", "a\nb", "a\x00b", `a"b`, "ünï😀", "a b;c", "'", "''", `\'`,
}

// statements splits SQL on semicolons outside single-quoted literals and
// double-quoted identifiers: a correctly quoted value never adds one.
func statements(sql string) []string {
	var out []string
	var cur strings.Builder
	in := rune(0)
	for _, r := range sql {
		switch {
		case in != 0:
			if r == in {
				in = 0 // a doubled quote reopens on the next rune
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			in = r
			cur.WriteRune(r)
		case r == ';':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func pgDef() Def {
	return Def{Name: "pg", Type: "postgres", Host: "db.internal", Port: 5433, Database: "analytics", ReadOnly: true, Options: map[string]string{"sslmode": "require"}}
}

func TestPostgresEmittedSQL(t *testing.T) {
	sp := pgDef().Spec()
	typ := registry["postgres"]
	stmts, err := typ.AttachSQL(sp)
	require.NoError(t, err)
	assert.Equal(t, []string{`ATTACH 'connect_timeout=10 sslmode=require' AS "pg" (TYPE postgres, SECRET "slaw_pg", READ_ONLY)`}, stmts)

	d := pgDef()
	d.ReadOnly = false
	d.Options = nil
	stmts, err = registry["postgres"].AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Equal(t, []string{`ATTACH 'connect_timeout=10' AS "pg" (TYPE postgres, SECRET "slaw_pg")`}, stmts)

	steps, err := asCred(typ).SecretSteps(sp, secrets.Credential{User: "rosa", Password: "pw'1"})
	require.NoError(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, session.SecretStep{Name: "slaw_pg", Type: "postgres", Params: []session.SecretParam{
		{Key: "HOST", Value: "db.internal"},
		{Key: "PORT", Value: 5433},
		{Key: "DATABASE", Value: "analytics"},
		{Key: "USER", Value: "rosa"},
		{Key: "PASSWORD", Value: "pw'1", Secret: true},
	}}, steps[0])
}

func TestMotherDuckEmittedSQL(t *testing.T) {
	d := Def{Name: "md", Type: "motherduck", Database: "my-db_1"}
	stmts, err := registry["motherduck"].AttachSQL(d.Spec())
	require.NoError(t, err)
	// READ_ONLY is not supported on md: so it is never emitted, and the token is
	// never in a statement.
	assert.Equal(t, []string{`ATTACH 'md:my-db_1' AS "md"`}, stmts)
	steps, err := asCred(registry["motherduck"]).SecretSteps(d.Spec(), secrets.Credential{Token: "tok"})
	require.NoError(t, err)
	assert.Empty(t, steps, "no CREATE SECRET exists for motherduck on DuckDB 1.4.1")
	_, err = asCred(registry["motherduck"]).SecretSteps(d.Spec(), secrets.Credential{})
	require.Error(t, err)
	assert.True(t, asHub(registry["motherduck"]).UsesHubToken())
}

func TestSQLiteEmittedSQL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "it's;here.sqlite")
	require.NoError(t, os.WriteFile(p, nil, 0o600))
	d := Def{Name: "lite", Type: "sqlite", Path: p, ReadOnly: true}
	stmts, err := registry["sqlite"].AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Equal(t, []string{`ATTACH '` + strings.ReplaceAll(p, "'", "''") + `' AS "lite" (TYPE sqlite, READ_ONLY)`}, stmts)
	assert.Len(t, statements(stmts[0]), 1, "a ';' and a quote in the path stay inside the literal")
	d.ReadOnly = false
	stmts, _ = registry["sqlite"].AttachSQL(d.Spec())
	assert.True(t, strings.HasSuffix(stmts[0], `(TYPE sqlite)`))
	// A mistyped path is an error, not a silently created database.
	d.Path = filepath.Join(dir, "missing.sqlite")
	_, err = registry["sqlite"].AttachSQL(d.Spec())
	require.Error(t, err)
}

func TestStorageEmittedSQL(t *testing.T) {
	cred := secrets.Credential{KeyID: "AKIA123", Secret: "sk/needle", Token: "sess+needle"}
	cases := []struct {
		def  Def
		want session.SecretStep
	}{
		{
			Def{Name: "lake", Type: "s3", Scope: "s3://acme-lake/raw/", Region: "us-east-1", Options: map[string]string{"endpoint": "minio.local:9000", "url_style": "path", "use_ssl": "false"}},
			session.SecretStep{Name: "slaw_lake", Type: "s3", Params: []session.SecretParam{
				{Key: "SCOPE", Value: "s3://acme-lake/raw/"}, {Key: "KEY_ID", Value: "AKIA123"}, {Key: "SECRET", Value: "sk/needle", Secret: true},
				{Key: "SESSION_TOKEN", Value: "sess+needle", Secret: true}, {Key: "REGION", Value: "us-east-1"},
				{Key: "ENDPOINT", Value: "minio.local:9000"}, {Key: "URL_STYLE", Value: "path"}, {Key: "USE_SSL", Value: false},
			}},
		},
		{
			Def{Name: "gcp", Type: "gcs", Scope: "gs://bkt/p/"},
			session.SecretStep{Name: "slaw_gcp", Type: "gcs", Params: []session.SecretParam{
				{Key: "SCOPE", Value: "gs://bkt/p/"}, {Key: "KEY_ID", Value: "AKIA123"}, {Key: "SECRET", Value: "sk/needle", Secret: true},
				// Explicit and empty/default, so the host's AWS_SESSION_TOKEN and AWS_REGION never fill in (#135).
				{Key: "SESSION_TOKEN", Value: "", Secret: true}, {Key: "REGION", Value: "auto"},
			}},
		},
		{
			Def{Name: "cf", Type: "r2", Scope: "r2://bkt/", Options: map[string]string{"account_id": "abc123"}},
			session.SecretStep{Name: "slaw_cf", Type: "r2", Params: []session.SecretParam{
				{Key: "SCOPE", Value: "r2://bkt/"}, {Key: "KEY_ID", Value: "AKIA123"}, {Key: "SECRET", Value: "sk/needle", Secret: true},
				{Key: "SESSION_TOKEN", Value: "", Secret: true}, {Key: "REGION", Value: "auto"},
				{Key: "ACCOUNT_ID", Value: "abc123"},
			}},
		},
	}
	for _, c := range cases {
		typ := registry[c.def.Type]
		steps, err := asCred(typ).SecretSteps(c.def.Spec(), cred)
		require.NoError(t, err, c.def.Type)
		assert.Equal(t, []session.SecretStep{c.want}, steps, c.def.Type)
		stmts, err := typ.AttachSQL(c.def.Spec())
		require.NoError(t, err)
		assert.Empty(t, stmts, "storage attaches no catalog")
		assert.True(t, asCatless(typ).Catalogless())
	}
}

func TestNoCredentialOrServerInAnyAttachString(t *testing.T) {
	dir := t.TempDir()
	lite := filepath.Join(dir, "x.sqlite")
	require.NoError(t, os.WriteFile(lite, nil, 0o600))
	defs := []Def{
		pgDef(),
		{Name: "md", Type: "motherduck", Database: "db1"},
		{Name: "lite", Type: "sqlite", Path: lite},
		{Name: "lake", Type: "s3", Scope: "s3://b/"},
	}
	for _, d := range defs {
		stmts, err := registry[d.Type].AttachSQL(d.Spec())
		require.NoError(t, err, d.Type)
		for _, s := range stmts {
			for _, banned := range []string{"password", "PASSWORD", "token", "TOKEN", "secret_key", "user=", "host=", "postgresql://", "db.internal"} {
				assert.NotContains(t, s, banned, d.Type)
			}
		}
	}
}

// Every free-text field with a hostile value is either rejected or emitted
// quoted inside a single statement.
func TestHostileFieldValues(t *testing.T) {
	cred := secrets.Credential{User: "u", Password: "p", Token: "t", KeyID: "k", Secret: "s"}
	check := func(t *testing.T, d Def) {
		typ := registry[d.Type]
		ct := asCred(typ)
		if steps, err := ct.SecretSteps(d.Spec(), cred); err == nil {
			for _, st := range steps {
				for _, p := range st.Params {
					if v, ok := p.Value.(string); ok && strings.ContainsAny(v, "\x00\n") {
						// Values with control characters must have been rejected by Validate.
						assert.Fail(t, "control character reached a secret value", "%s %s", d.Type, p.Key)
					}
				}
			}
		}
		if stmts, err := typ.AttachSQL(d.Spec()); err == nil {
			for _, s := range stmts {
				assert.Len(t, statements(s), 1, "%s: %q", d.Type, s)
			}
		}
	}
	for _, v := range evil {
		pg := pgDef()
		pg.Host, pg.Database = v, v
		check(t, pg)
		pg = pgDef()
		pg.Database = v
		check(t, pg)
		md := Def{Name: "md", Type: "motherduck", Database: v}
		check(t, md)
		require.Error(t, Validate(md), "motherduck database %q", v)
		s3 := Def{Name: "lake", Type: "s3", Scope: "s3://b/" + v, Region: v, Options: map[string]string{"endpoint": v}}
		check(t, s3)
		r2 := Def{Name: "cf", Type: "r2", Scope: "r2://b/", Options: map[string]string{"account_id": v}}
		check(t, r2)
		require.Error(t, Validate(r2), "r2 account id %q", v)
		lite := Def{Name: "l", Type: "sqlite", Path: v}
		check(t, lite)
		// A hostile connection name never reaches SQL.
		bad := pgDef()
		bad.Name = v
		require.Error(t, Validate(bad), "name %q", v)
	}
	// A host that is not a host name is rejected.
	for _, h := range []string{"a b", "a'b", "a;b", "a\nb", "host/../x", "-lead"} {
		d := pgDef()
		d.Host = h
		assert.Error(t, Validate(d), "host %q", h)
	}
	// An unknown option is rejected instead of being emitted.
	d := pgDef()
	d.Options = map[string]string{"sslmode": "require", "application_name": "x"}
	require.Error(t, Validate(d))
	d.Options = map[string]string{"sslmode": "bogus"}
	require.Error(t, Validate(d))
}

func TestValidateFieldRules(t *testing.T) {
	good := []Def{
		pgDef(),
		{Name: "md", Type: "motherduck", Database: "my_db"},
		{Name: "l", Type: "sqlite", Path: "data/x.sqlite"},
		{Name: "lake", Type: "s3", Scope: "s3://b/p/", Region: "eu-west-1"},
		{Name: "g", Type: "gcs", Scope: "gcs://b/"},
		{Name: "c", Type: "r2", Scope: "r2://b/", Options: map[string]string{"account_id": "ab12"}},
		{Name: "f", Type: "duckdb", Path: "data/f.duckdb"},
	}
	for _, d := range good {
		assert.NoError(t, Validate(d), d.Type)
	}
	bad := map[string]Def{
		"pg without host":    {Name: "pg", Type: "postgres", Database: "d"},
		"pg without db":      {Name: "pg", Type: "postgres", Host: "h"},
		"pg port":            {Name: "pg", Type: "postgres", Host: "h", Database: "d", Port: 70000},
		"md without db":      {Name: "md", Type: "motherduck"},
		"md with query":      {Name: "md", Type: "motherduck", Database: "db?motherduck_token=x"},
		"sqlite no path":     {Name: "l", Type: "sqlite"},
		"s3 no scope":        {Name: "l", Type: "s3"},
		"s3 wrong scheme":    {Name: "l", Type: "s3", Scope: "gs://b/"},
		"s3 bucket missing":  {Name: "l", Type: "s3", Scope: "s3://"},
		"s3 wildcard":        {Name: "l", Type: "s3", Scope: "s3://b/*"},
		"s3 endpoint scheme": {Name: "l", Type: "s3", Scope: "s3://b/", Options: map[string]string{"endpoint": "https://x"}},
		"r2 no account":      {Name: "c", Type: "r2", Scope: "r2://b/"},
		"unknown type":       {Name: "x", Type: "oracle"},
		"bad name":           {Name: "Bad-Name", Type: "sqlite", Path: "p"},
	}
	for n, d := range bad {
		assert.Error(t, Validate(d), n)
	}
}

func TestS3InfoExposesUseSSL(t *testing.T) {
	i, err := TypeInfoFor("s3")
	require.NoError(t, err)
	var f *Field
	for k := range i.Fields {
		if i.Fields[k].Name == "use_ssl" {
			f = &i.Fields[k]
		}
	}
	require.NotNil(t, f, "the form must offer use_ssl")
	assert.Equal(t, KindEnum, f.Kind)
	assert.True(t, f.Option)
	assert.Equal(t, []string{"true", "false"}, f.Choices)
	assert.Equal(t, "true", f.Default)
	assert.Equal(t, "Use TLS", f.Label)
	assert.Contains(t, i.FingerprintFields, "option:use_ssl")
	for _, n := range []string{"gcs", "r2"} {
		g, _ := TypeInfoFor(n)
		for _, gf := range g.Fields {
			assert.NotEqual(t, "use_ssl", gf.Name, n)
		}
	}
}

func TestAzureSignInIsProminent(t *testing.T) {
	i, err := TypeInfoFor("azure")
	require.NoError(t, err)
	for _, f := range i.Fields {
		assert.Equal(t, f.Name == "sign_in", f.Prominent, f.Name)
	}
}

func TestListConnectionTypesMetadata(t *testing.T) {
	infos := ListConnectionTypes()
	var names []string
	for _, i := range infos {
		names = append(names, i.Name)
	}
	assert.Equal(t, []string{"duckdb", "azure", "delta", "ducklake", "gcs", "iceberg", "iceberg_catalog", "motherduck", "mysql", "postgres", "r2", "s3", "sqlite"}, names)
	for _, i := range infos {
		assert.NotEmpty(t, i.Label, i.Name)
		assert.Contains(t, i.FingerprintFields, "type", i.Name)
		for _, f := range i.Fields {
			assert.False(t, f.Secret, "%s.%s: a committed definition field is never secret", i.Name, f.Name)
			assert.False(t, f.PerPerson, "%s.%s: and never per person", i.Name, f.Name)
		}
		for _, f := range i.Credentials {
			assert.True(t, f.PerPerson, "%s.%s", i.Name, f.Name)
		}
		if ty, ok := registry[i.Name]; ok {
			assert.Equal(t, i.Capabilities.Remote, ty.Remote(), i.Name)
			assert.Equal(t, i.Capabilities.Catalog, !isCatalogless(ty), i.Name)
			assert.Equal(t, NeedsCredential(i.Name), len(i.Credentials) > 0, i.Name)
		}
	}
	pg, _ := TypeInfoFor("postgres")
	var secret []string
	for _, f := range pg.Credentials {
		if f.Secret {
			secret = append(secret, f.Name)
		}
	}
	assert.Equal(t, []string{"password"}, secret)
	assert.False(t, pg.Capabilities.ReadOnlyAttach, "postgres READ_ONLY is a client-side guard")
	md, _ := TypeInfoFor("motherduck")
	assert.False(t, md.Capabilities.ReadOnlyAttach)
	assert.True(t, md.Capabilities.Subprocess)
	_, err := TypeInfoFor("oracle")
	require.Error(t, err)
}

func isCatalogless(t Type) bool {
	c, ok := t.(session.CataloglessType)
	return ok && c.Catalogless()
}

func TestParamsFlattening(t *testing.T) {
	p := pgDef().Params()
	assert.Equal(t, map[string]string{"host": "db.internal", "port": "5433", "database": "analytics", "opt.sslmode": "require"}, p)
}

func asCred(t Type) session.CredentialedType {
	c, _ := t.(session.CredentialedType)
	return c
}

func asHub(t Type) session.HubTokenType {
	c, _ := t.(session.HubTokenType)
	return c
}

func asCatless(t Type) session.CataloglessType {
	c, _ := t.(session.CataloglessType)
	return c
}

func TestMotherDuckCannotBeReadOnly(t *testing.T) {
	d := Def{Name: "md", Type: "motherduck", Database: "db", ReadOnly: true}
	err := Validate(d)
	require.Error(t, err)
	assert.Equal(t, "MotherDuck connections can't be attached read-only; use a read-scaling token for read-only access", err.Error())
	d.ReadOnly = false
	require.NoError(t, Validate(d))
	// A read-only command that opens the workspace cannot connect it either.
	sp := d.Spec()
	sp.Params["forced_read_only"] = "true"
	_, err = registry["motherduck"].AttachSQL(sp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
	// A test never writes, so it does not trip the rule.
	res := Test(context.Background(), Def{Name: "md", Type: "motherduck", Database: "db", ReadOnly: true}, secrets.Credential{}, WithTestExtensionDir(t.TempDir()))
	assert.Equal(t, session.StatusNeedsCredentials, res.Status)
}

func TestStorageTypeForScheme(t *testing.T) {
	for in, want := range map[string]string{
		"s3://": "s3", "s3a://": "s3", "s3n://": "s3", "s3": "s3",
		"gs://": "gcs", "gcs://": "gcs", "r2://": "r2",
		"az://": "azure", "azure://": "azure", "abfss://": "azure",
	} {
		got, ok := StorageTypeForScheme(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"https://", "http://", "file://", "", "md:", "S3://"} {
		_, ok := StorageTypeForScheme(in)
		assert.False(t, ok, in)
	}
}

func TestStorageTypeForSchemeCoversEveryStorageScheme(t *testing.T) {
	for _, sch := range storageurl.Schemes {
		_, ok := StorageTypeForScheme(sch)
		assert.True(t, ok, sch)
	}
}

// A default s3 connection (no token, no region) still writes both fields, so
// the host's AWS_SESSION_TOKEN and AWS_REGION can never fill them in (#135).
func TestStorageDefaultS3WritesTokenAndRegionExplicitly(t *testing.T) {
	d := Def{Name: "plain", Type: "s3", Scope: "s3://b/"}
	steps, err := asCred(registry["s3"]).SecretSteps(d.Spec(), secrets.Credential{KeyID: "AKIA123", Secret: "sk/needle"})
	require.NoError(t, err)
	assert.Equal(t, []session.SecretStep{{Name: "slaw_plain", Type: "s3", Params: []session.SecretParam{
		{Key: "SCOPE", Value: "s3://b/"}, {Key: "KEY_ID", Value: "AKIA123"}, {Key: "SECRET", Value: "sk/needle", Secret: true},
		{Key: "SESSION_TOKEN", Value: "", Secret: true}, {Key: "REGION", Value: "us-east-1"},
	}}}, steps)
}
