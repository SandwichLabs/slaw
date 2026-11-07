package connections

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

func myDef() Def {
	return Def{Name: "my", Type: "mysql", Host: "db.internal", Port: 3307, Database: "shop", ReadOnly: true, Options: map[string]string{"ssl_mode": "required"}}
}

// secretSQLOf renders a step the way the session does, to check what would
// reach DuckDB (the session's own renderer is unexported, so this mirrors its
// quoting with the exported helpers).
func renderSecret(st session.SecretStep) string {
	sql := "CREATE OR REPLACE TEMPORARY SECRET " + session.QuoteIdent(st.Name) + " (TYPE " + st.Type
	for _, p := range st.Params {
		sql += ", " + p.Key + " "
		switch v := p.Value.(type) {
		case string:
			sql += session.QuoteLiteral(v)
		case int:
			sql += itoa(v)
		case bool:
			if v {
				sql += "true"
			} else {
				sql += "false"
			}
		}
	}
	return sql + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestMySQLEmittedSQL(t *testing.T) {
	typ := registry["mysql"]
	stmts, err := typ.AttachSQL(myDef().Spec())
	require.NoError(t, err)
	assert.Equal(t, []string{`ATTACH '' AS "my" (TYPE mysql, SECRET "slaw_my", READ_ONLY)`}, stmts)

	d := myDef()
	d.ReadOnly = false
	d.Options = nil
	stmts, err = typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Equal(t, []string{`ATTACH '' AS "my" (TYPE mysql, SECRET "slaw_my")`}, stmts)

	steps, err := asCred(typ).SecretSteps(myDef().Spec(), secrets.Credential{User: "rosa", Password: "pw'1"})
	require.NoError(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, session.SecretStep{Name: "slaw_my", Type: "mysql", Params: []session.SecretParam{
		{Key: "HOST", Value: "db.internal"},
		{Key: "PORT", Value: 3307},
		{Key: "DATABASE", Value: "shop"},
		{Key: "USER", Value: "rosa"},
		{Key: "PASSWORD", Value: "pw'1", Secret: true},
		{Key: "SSL_MODE", Value: "required"},
	}}, steps[0])
	assert.Equal(t, `CREATE OR REPLACE TEMPORARY SECRET "slaw_my" (TYPE mysql, HOST 'db.internal', PORT 3307, DATABASE 'shop', USER 'rosa', PASSWORD 'pw''1', SSL_MODE 'required')`, renderSecret(steps[0]))

	// The default port, and no SSL option unless one is set.
	d = Def{Name: "my", Type: "mysql", Host: "h", Database: "d"}
	steps, err = asCred(typ).SecretSteps(d.Spec(), secrets.Credential{User: "u", Password: "p"})
	require.NoError(t, err)
	assert.Contains(t, renderSecret(steps[0]), "PORT 3306")
	assert.NotContains(t, renderSecret(steps[0]), "SSL_MODE")

	// A user is required; the password may be empty (a trust-authenticated dev server).
	_, err = asCred(typ).SecretSteps(d.Spec(), secrets.Credential{Password: "p"})
	require.Error(t, err)
	_, err = asCred(typ).SecretSteps(d.Spec(), secrets.Credential{User: "u"})
	require.NoError(t, err)

	r, ok := typ.(session.Refresher)
	require.True(t, ok)
	assert.Equal(t, "CALL mysql_clear_cache()", r.RefreshSQL())
	assert.Equal(t, []string{"mysql"}, asCred(typ).Extensions())
}

func TestMySQLFieldRules(t *testing.T) {
	assert.NoError(t, Validate(myDef()))
	for _, m := range mysqlSSLModes {
		d := myDef()
		d.Options = map[string]string{"ssl_mode": m}
		assert.NoError(t, Validate(d), m)
	}
	bad := map[string]func(*Def){
		"no host":          func(d *Def) { d.Host = "" },
		"host with space":  func(d *Def) { d.Host = "a b" },
		"host with quote":  func(d *Def) { d.Host = "a'b" },
		"no database":      func(d *Def) { d.Database = "" },
		"port too big":     func(d *Def) { d.Port = 70000 },
		"unknown option":   func(d *Def) { d.Options = map[string]string{"ssl_ca": "/etc/ca.pem"} },
		"cert file option": func(d *Def) { d.Options = map[string]string{"ssl_cert": "/x"} },
		"socket option":    func(d *Def) { d.Options = map[string]string{"socket": "/var/run/mysqld.sock"} },
		"bad ssl mode":     func(d *Def) { d.Options = map[string]string{"ssl_mode": "bogus"} },
		"sql in ssl mode":  func(d *Def) { d.Options = map[string]string{"ssl_mode": "required', PASSWORD 'x"} },
	}
	for n, f := range bad {
		d := myDef()
		f(&d)
		assert.Error(t, Validate(d), n)
	}
}

func TestMySQLHostileValues(t *testing.T) {
	for _, v := range evil {
		d := myDef()
		d.Database = v
		if Validate(d) == nil {
			steps, err := asCred(registry["mysql"]).SecretSteps(d.Spec(), secrets.Credential{User: "u", Password: "p"})
			require.NoError(t, err)
			assert.Len(t, statements(renderSecret(steps[0])), 1, "database %q", v)
			stmts, _ := registry["mysql"].AttachSQL(d.Spec())
			assert.Len(t, statements(stmts[0]), 1)
		}
		// A user or password with any character stays inside its literal.
		steps, err := asCred(registry["mysql"]).SecretSteps(myDef().Spec(), secrets.Credential{User: v, Password: v})
		if v != "" && err == nil {
			assert.Len(t, statements(renderSecret(steps[0])), 1, "credential %q", v)
		}
		bad := myDef()
		bad.Host = v
		assert.Error(t, Validate(bad), "host %q", v)
		bad = myDef()
		bad.Options = map[string]string{"ssl_mode": v}
		assert.Error(t, Validate(bad), "ssl_mode %q", v)
	}
}

func azDef(auth string) Def {
	d := Def{Name: "az", Type: "azure", Scope: "az://raw/events/", Options: map[string]string{"sign_in": auth}}
	switch auth {
	case "account_key":
		d.Options["account_name"] = "acmelake"
	case "service_principal":
		d.Options["account_name"] = "acmelake"
		d.Options["tenant_id"] = "11111111-2222-3333-4444-555555555555"
	}
	return d
}

func TestAzureEmittedSQL(t *testing.T) {
	typ := registry["azure"]
	stmts, err := typ.AttachSQL(azDef("account_key").Spec())
	require.NoError(t, err)
	assert.Empty(t, stmts, "azure attaches no catalog")
	assert.True(t, asCatless(typ).Catalogless())
	assert.True(t, typ.Remote())

	step := func(d Def, c secrets.Credential) session.SecretStep {
		steps, err := asCred(typ).SecretSteps(d.Spec(), c)
		require.NoError(t, err)
		require.Len(t, steps, 1)
		return steps[0]
	}
	// Account key: the connection string is built here and is entirely secret.
	assert.Equal(t, session.SecretStep{Name: "slaw_az", Type: "azure", Params: []session.SecretParam{
		{Key: "CONNECTION_STRING", Value: "DefaultEndpointsProtocol=https;AccountName=acmelake;AccountKey=a2V5Ck5lZWRsZQ==;EndpointSuffix=core.windows.net", Secret: true},
		{Key: "SCOPE", Value: "az://raw/events/"},
	}}, step(azDef("account_key"), secrets.Credential{Secret: "a2V5Ck5lZWRsZQ=="}))
	// The default sign-in is the account key.
	d := azDef("account_key")
	delete(d.Options, "sign_in")
	assert.Equal(t, "CONNECTION_STRING", step(d, secrets.Credential{Secret: "a2V5Ck5lZWRsZQ=="}).Params[0].Key)
	// A pasted connection string goes through untouched.
	cs := "DefaultEndpointsProtocol=https;AccountName=x;AccountKey=k;EndpointSuffix=core.windows.net"
	assert.Equal(t, session.SecretStep{Name: "slaw_az", Type: "azure", Params: []session.SecretParam{
		{Key: "CONNECTION_STRING", Value: cs, Secret: true},
		{Key: "SCOPE", Value: "az://raw/events/"},
	}}, step(azDef("connection_string"), secrets.Credential{Secret: cs}))
	// Service principal.
	assert.Equal(t, session.SecretStep{Name: "slaw_az", Type: "azure", Params: []session.SecretParam{
		{Key: "PROVIDER", Value: "service_principal"},
		{Key: "TENANT_ID", Value: "11111111-2222-3333-4444-555555555555"},
		{Key: "CLIENT_ID", Value: "app-1234"},
		{Key: "CLIENT_SECRET", Value: "s3cr'et", Secret: true},
		{Key: "ACCOUNT_NAME", Value: "acmelake"},
		{Key: "SCOPE", Value: "az://raw/events/"},
	}}, step(azDef("service_principal"), secrets.Credential{KeyID: "app-1234", Secret: "s3cr'et"}))

	// Every secret-bearing value is flagged Secret, whichever variant.
	for _, a := range azureAuths {
		var c secrets.Credential
		switch a {
		case "account_key":
			c = secrets.Credential{Secret: "a2V5Ck5lZWRsZQ=="}
		case "connection_string":
			c = secrets.Credential{Secret: cs}
		default:
			c = secrets.Credential{KeyID: "app", Secret: "needle-secret"}
		}
		st := step(azDef(a), c)
		for _, p := range st.Params {
			if strings.Contains(strings.ToUpper(p.Key), "SECRET") || strings.Contains(strings.ToUpper(p.Key), "CONNECTION_STRING") {
				assert.True(t, p.Secret, "%s %s", a, p.Key)
			}
		}
	}

	// Missing pieces are named, and never echo a value.
	_, err = asCred(typ).SecretSteps(azDef("account_key").Spec(), secrets.Credential{})
	require.Error(t, err)
	_, err = asCred(typ).SecretSteps(azDef("service_principal").Spec(), secrets.Credential{Secret: "s"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client id")
	_, err = asCred(typ).SecretSteps(azDef("account_key").Spec(), secrets.Credential{Secret: "not base64!; AccountKey=x"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not base64")
	assert.Equal(t, []string{"azure"}, asCred(typ).Extensions())
}

func TestAzureFieldRules(t *testing.T) {
	for _, auth := range azureAuths {
		assert.NoError(t, Validate(azDef(auth)), auth)
	}
	for _, scope := range []string{"az://c/", "azure://c/p/", "abfss://c@acmelake.dfs.core.windows.net/p/", "az://c", "az://acmelake.blob.core.windows.net/c/p/", "azure://acmelake.blob.core.windows.net/c/"} {
		d := azDef("account_key")
		d.Scope = scope
		assert.NoError(t, Validate(d), scope)
	}
	bad := map[string]func(*Def){
		"no scope":            func(d *Def) { d.Scope = "" },
		"wrong scheme":        func(d *Def) { d.Scope = "s3://b/" },
		"https scope":         func(d *Def) { d.Scope = "https://acct.blob.core.windows.net/c/" },
		"no container":        func(d *Def) { d.Scope = "az://" },
		"wildcard":            func(d *Def) { d.Scope = "az://c/*" },
		"sas token in scope":  func(d *Def) { d.Scope = "az://c/p?sv=2020&sig=abc" },
		"fragment":            func(d *Def) { d.Scope = "az://c/p#x" },
		"space":               func(d *Def) { d.Scope = "az://c/a b" },
		"credential_chain":    func(d *Def) { d.Options["sign_in"] = "credential_chain" },
		"unknown option":      func(d *Def) { d.Options["provider"] = "credential_chain" },
		"endpoint option":     func(d *Def) { d.Options["endpoint"] = "evil.example" },
		"account name caps":   func(d *Def) { d.Options["account_name"] = "Acme" },
		"account name quote":  func(d *Def) { d.Options["account_name"] = "a'b" },
		"account key no acct": func(d *Def) { delete(d.Options, "account_name") },
		"tenant with quote":   func(d *Def) { d.Options["tenant_id"] = "t'; DROP" },
		"tenant with newline": func(d *Def) { d.Options["tenant_id"] = "t\nx" },
		"control char scope":  func(d *Def) { d.Scope = "az://c/\x00" },
	}
	for n, f := range bad {
		d := azDef("account_key")
		f(&d)
		assert.Error(t, Validate(d), n)
	}
	sp := azDef("service_principal")
	delete(sp.Options, "tenant_id")
	assert.Error(t, Validate(sp), "service principal needs a tenant")
	sp = azDef("service_principal")
	delete(sp.Options, "account_name")
	assert.Error(t, Validate(sp), "service principal needs an account")
	cs := azDef("connection_string")
	assert.NoError(t, Validate(cs), "a connection string needs no account name")
}

func TestAzureHostileValues(t *testing.T) {
	for _, v := range evil {
		for _, mk := range []func(string) Def{
			func(v string) Def { d := azDef("account_key"); d.Scope = "az://c/" + v; return d },
			func(v string) Def { d := azDef("account_key"); d.Options["account_name"] = v; return d },
			func(v string) Def { d := azDef("service_principal"); d.Options["tenant_id"] = v; return d },
		} {
			d := mk(v)
			if err := Validate(d); err != nil {
				continue
			}
			steps, err := asCred(registry["azure"]).SecretSteps(d.Spec(), secrets.Credential{KeyID: "app", Secret: "a2V5Ck5lZWRsZQ=="})
			if err == nil {
				assert.Len(t, statements(renderSecret(steps[0])), 1, "%q", v)
			}
		}
	}
	// A client id or key that tries to add a field.
	sp := azDef("service_principal")
	for _, v := range evil {
		_, err := asCred(registry["azure"]).SecretSteps(sp.Spec(), secrets.Credential{KeyID: v, Secret: "s"})
		assert.Error(t, err, "client id %q", v)
	}
}

func lakeDef(t *testing.T) Def {
	dir := t.TempDir()
	return Def{Name: "lake", Type: "ducklake", Path: filepath.Join(dir, "lake.ducklake"), Options: map[string]string{"data_path": filepath.Join(dir, "files")}}
}

func TestDuckLakeEmittedSQL(t *testing.T) {
	d := lakeDef(t)
	typ := registry["ducklake"]
	stmts, err := typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	sep := string(filepath.Separator)
	assert.Equal(t, []string{`ATTACH 'ducklake:` + d.Path + `' AS "lake" (DATA_PATH '` + d.Options["data_path"] + sep + `')`}, stmts)
	assert.Len(t, statements(stmts[0]), 1)

	// A read-only attach of a lake that does not exist yet is refused with a
	// clear message; an existing one is attached READ_ONLY.
	d.ReadOnly = true
	_, err = typ.AttachSQL(d.Spec())
	require.Error(t, err)
	require.NoError(t, os.WriteFile(d.Path, nil, 0o600))
	stmts, err = typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(stmts[0], `, READ_ONLY)`), stmts[0])

	// A storage data path keeps its slash style.
	d = lakeDef(t)
	d.Options["data_path"] = "s3://lake-bucket/ducklake"
	stmts, err = typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Contains(t, stmts[0], `DATA_PATH 's3://lake-bucket/ducklake/'`)

	// A path with a quote and a semicolon stays inside its literal.
	d = lakeDef(t)
	d.Path = filepath.Join(filepath.Dir(d.Path), "it's;a lake.ducklake")
	stmts, err = typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Len(t, statements(stmts[0]), 1)
	assert.Contains(t, stmts[0], "it''s;a lake.ducklake")

	// No secret and no credential: a lake attaches a file.
	steps, err := asCred(typ).SecretSteps(d.Spec(), secrets.Credential{})
	require.NoError(t, err)
	assert.Empty(t, steps)
	assert.False(t, asCred(typ).NeedsCredential(d.Spec()))
	assert.Equal(t, []string{"ducklake"}, asCred(typ).Extensions())

	// An unresolved (relative) path is a programming error, not a guess.
	rel := lakeDef(t)
	rel.Path = "data/lake.ducklake"
	_, err = typ.AttachSQL(rel.Spec())
	require.Error(t, err)
}

func TestDuckLakeFieldRules(t *testing.T) {
	good := []Def{
		lakeDef(t),
		{Name: "l", Type: "ducklake", Path: "data/lake.ducklake", Options: map[string]string{"data_path": "data/lake_files"}},
		{Name: "l", Type: "ducklake", Path: "data/lake.ducklake", Options: map[string]string{"data_path": "s3://b/lake/"}},
		{Name: "l", Type: "ducklake", Path: "data/lake.ducklake", Options: map[string]string{"data_path": "abfss://c@a.dfs.core.windows.net/lake/"}},
		{Name: "l", Type: "ducklake", Path: "data/lake.ducklake", Options: map[string]string{"data_path": "az://c/lake/"}},
	}
	for _, d := range good {
		assert.NoError(t, Validate(d), d.Options["data_path"])
	}
	bad := map[string]Def{
		"no path":           {Name: "l", Type: "ducklake", Options: map[string]string{"data_path": "data/f"}},
		"no data path":      {Name: "l", Type: "ducklake", Path: "data/l.ducklake"},
		"postgres metadata": {Name: "l", Type: "ducklake", Path: "postgres:dbname=x host=h password=p", Options: map[string]string{"data_path": "data/f"}},
		"sqlite metadata":   {Name: "l", Type: "ducklake", Path: "sqlite:/tmp/m.db", Options: map[string]string{"data_path": "data/f"}},
		"duckdb prefix":     {Name: "l", Type: "ducklake", Path: "duckdb:/tmp/m.db", Options: map[string]string{"data_path": "data/f"}},
		"https data path":   {Name: "l", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "https://x.example/lake/"}},
		"file:// data":      {Name: "l", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "file:///etc/"}},
		"query in url":      {Name: "l", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "s3://b/l?x=1"}},
		"unknown option":    {Name: "l", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "data/f", "metadata_schema": "x"}},
		"scheme-ish data":   {Name: "l", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "postgres:x"}},
		"control char":      {Name: "l", Type: "ducklake", Path: "data/l\n.ducklake", Options: map[string]string{"data_path": "data/f"}},
	}
	for n, d := range bad {
		assert.Error(t, Validate(d), n)
	}
}

func TestDuckLakeHostileValues(t *testing.T) {
	dir := t.TempDir()
	for _, v := range evil {
		d := Def{Name: "lake", Type: "ducklake", Path: filepath.Join(dir, v+".ducklake"), Options: map[string]string{"data_path": filepath.Join(dir, v)}}
		if Validate(d) != nil {
			continue
		}
		stmts, err := registry["ducklake"].AttachSQL(d.Spec())
		if err == nil {
			assert.Len(t, statements(stmts[0]), 1, "%q: %s", v, stmts[0])
		}
	}
}

func TestTableSourceTypes(t *testing.T) {
	for _, kind := range []string{"iceberg", "delta"} {
		typ := registry[kind]
		d := Def{Name: "tbl", Type: kind, Path: "data/sales"}
		require.NoError(t, Validate(d), kind)
		stmts, err := typ.AttachSQL(d.Spec())
		require.NoError(t, err)
		assert.Empty(t, stmts, "a table source attaches nothing")
		steps, err := asCred(typ).SecretSteps(d.Spec(), secrets.Credential{})
		require.NoError(t, err)
		assert.Empty(t, steps)
		assert.False(t, asCred(typ).NeedsCredential(d.Spec()))
		assert.True(t, asCatless(typ).Catalogless())
		want := []string{kind}
		if kind == "iceberg" {
			want = []string{"avro", "iceberg"} // DuckDB 1.5: iceberg reads its metadata through avro
		}
		assert.Equal(t, want, asCred(typ).Extensions())
		// The location is not a storage scope: no "scope" param is produced, so
		// it can never widen what the agent may read.
		assert.NotContains(t, d.Params(), "scope")

		for _, p := range []string{"s3://b/t/", "gcs://b/t", "r2://b/t", "az://c/t", "abfss://c@a.dfs.core.windows.net/t"} {
			assert.NoError(t, Validate(Def{Name: "tbl", Type: kind, Path: p}), p)
		}
		for n, p := range map[string]string{
			"empty": "", "https": "https://x.example/t", "file": "file:///etc/x", "wildcard": "s3://b/*",
			"query": "s3://b/t?x=1", "scheme": "postgres:x", "control": "data/t\n",
		} {
			assert.Error(t, Validate(Def{Name: "tbl", Type: kind, Path: p}), "%s %s", kind, n)
		}
		assert.Error(t, Validate(Def{Name: "tbl", Type: kind, Path: "data/t", Options: map[string]string{"x": "y"}}))
	}
}

// No credential, host, user or key in any ATTACH statement of any new type; the
// secrets live only in the temporary secret.
func TestNewTypesKeepCredentialsOutOfAttach(t *testing.T) {
	banned := []string{"password", "PASSWORD", "needle", "db.internal", "user=", "host=", "AccountKey", "CONNECTION_STRING"}
	defs := []Def{myDef(), azDef("account_key"), lakeDef(t), {Name: "t", Type: "delta", Path: "data/t"}, {Name: "t2", Type: "iceberg", Path: "s3://b/t/"}, s3tDef(), restDef("bearer")}
	for _, d := range defs {
		stmts, err := registry[d.Type].AttachSQL(d.Spec())
		require.NoError(t, err, d.Type)
		for _, s := range stmts {
			for _, b := range banned {
				assert.NotContains(t, s, b, d.Type)
			}
		}
	}
}

func TestNewTypesMetadata(t *testing.T) {
	for _, n := range []string{"mysql", "azure", "ducklake", "iceberg", "delta", "iceberg_catalog"} {
		i, err := TypeInfoFor(n)
		require.NoError(t, err, n)
		assert.NotEmpty(t, i.Label)
		assert.NotEmpty(t, i.Extensions, n)
		assert.Contains(t, i.FingerprintFields, "type")
		assert.True(t, i.Capabilities.Test, n)
	}
	my, _ := TypeInfoFor("mysql")
	assert.Equal(t, []string{"type", "host", "port", "database", "option:ssl_mode"}, my.FingerprintFields)
	assert.False(t, my.Capabilities.ReadOnlyAttach, "MySQL READ_ONLY is a client-side guard")
	var secret []string
	for _, f := range my.Credentials {
		if f.Secret {
			secret = append(secret, f.Name)
		}
	}
	assert.Equal(t, []string{"password"}, secret)
	az, _ := TypeInfoFor("azure")
	assert.Equal(t, []string{"type", "scope", "option:sign_in", "option:account_name", "option:tenant_id"}, az.FingerprintFields)
	assert.False(t, az.Capabilities.Catalog)
	dl, _ := TypeInfoFor("ducklake")
	assert.True(t, dl.Capabilities.ReadOnlyAttach)
	assert.Equal(t, []string{"type", "path", "option:data_path", "option:catalog"}, dl.FingerprintFields)
	assert.Empty(t, dl.Credentials)
}

func TestMySQLWrongPasswordIsNeedsCredentials(t *testing.T) {
	assert.True(t, authFailure(assertErr("Access denied for user 'root'@'1.2.3.4' (using password: YES)")))
	assert.True(t, authFailure(assertErr("AuthenticationFailed: Server failed to authenticate the request")))
	assert.True(t, authFailure(assertErr("AADSTS7000215: Invalid client secret provided")))
	assert.False(t, authFailure(assertErr("connection refused")))
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// A definition accepts exactly the top-level fields its type uses. A committed
// `scope: "file:///"` on a table source once made the agent able to read any file.
func TestTypesRejectFieldsTheyDoNotUse(t *testing.T) {
	stray := []func(*Def){
		func(d *Def) { d.Scope = "file:///" },
		func(d *Def) { d.Scope = "http://" },
		func(d *Def) { d.Scope = "s3://bucket/" },
		func(d *Def) { d.Host = "evil.example" },
		func(d *Def) { d.Port = 80 },
		func(d *Def) { d.Database = "x" },
		func(d *Def) { d.Region = "us-east-1" },
	}
	good := map[string]Def{
		"delta":           {Name: "t", Type: "delta", Path: "data/t"},
		"iceberg":         {Name: "t", Type: "iceberg", Path: "data/t"},
		"iceberg_catalog": {Name: "t", Type: "iceberg_catalog", ReadOnly: true, Options: map[string]string{"endpoint_type": "s3_tables", "warehouse": testARN, "storage": "aws_lake"}},
		"ducklake":        {Name: "t", Type: "ducklake", Path: "data/t.ducklake", Options: map[string]string{"data_path": "data/f"}},
		"sqlite":          {Name: "t", Type: "sqlite", Path: "data/t.sqlite"},
		"mysql":           {Name: "t", Type: "mysql", Host: "h", Database: "d"},
		"postgres":        {Name: "t", Type: "postgres", Host: "h", Database: "d"},
		"azure":           {Name: "t", Type: "azure", Scope: "az://c/", Options: map[string]string{"sign_in": "connection_string"}},
		"s3":              {Name: "t", Type: "s3", Scope: "s3://b/"},
	}
	for typ, g := range good {
		require.NoError(t, Validate(g), typ)
		for i, f := range stray {
			d := g
			d.Options = map[string]string{}
			for k, v := range g.Options {
				d.Options[k] = v
			}
			f(&d)
			// The type's own fields are legitimate (a MySQL host, an S3 region and
			// scope); every other one must be refused.
			legit := map[string][]int{"mysql": {3, 4, 5}, "postgres": {3, 4, 5}, "s3": {2, 6}, "azure": {}}
			ok := false
			for _, n := range legit[typ] {
				ok = ok || n == i
			}
			if err := Validate(d); err == nil {
				assert.True(t, ok, "%s accepted stray field #%d: %+v", typ, i, d)
			}
		}
	}
	// Specifically: the smuggled scope on every metadata-trusting type.
	for _, typ := range []string{"delta", "iceberg", "ducklake", "iceberg_catalog"} {
		d := good[typ]
		d.Scope = "file:///"
		require.Error(t, Validate(d), typ)
		sp := d.Spec()
		assert.Error(t, registry[typ].Validate(sp), typ)
	}
}

// Through a real session: only the storage types export a scope the agent
// policy reads, however a definition is spelled.
func TestOnlyStorageConnectionsExportAScope(t *testing.T) {
	sess, err := session.New(session.WithConnectionTypes(Types()...), session.WithExtensionDirectory(t.TempDir()))
	require.NoError(t, err)
	defer sess.Close()
	specs := []session.Spec{
		{Name: "tbl", Type: "delta", Path: "data/t", Params: map[string]string{"scope": "file:///"}},
		{Name: "ice", Type: "iceberg", Path: "data/t", Params: map[string]string{"scope": "http://"}},
		{Name: "lake", Type: "ducklake", Path: "/tmp/x.ducklake", Params: map[string]string{"scope": "file:///", "opt.data_path": "/tmp/f"}},
		{Name: "icat", Type: "iceberg_catalog", ReadOnly: true, Params: map[string]string{"scope": "s3://b/p/", "opt.endpoint_type": "s3_tables", "opt.warehouse": testARN, "opt.storage": "aws_lake"}},
		{Name: "s3c", Type: "s3", Params: map[string]string{"scope": "s3://b/p/"}},
	}
	for _, sp := range specs {
		// The types' own Validate runs at Register: a smuggled scope never gets in.
		err := sess.Register(sp)
		if sp.Type == "s3" {
			require.NoError(t, err)
		} else {
			require.Error(t, err, sp.Name)
		}
	}
	scope := map[string]string{}
	for _, ci := range sess.ConnectionInfos() {
		scope[ci.Name] = ci.Scope
	}
	for _, n := range []string{"tbl", "ice", "lake"} {
		_, registered := scope[n]
		assert.False(t, registered, "%s: registration refused it", n)
	}
	assert.Equal(t, "s3://b/p/", scope["s3c"])
}

// A pasted connection string must be for the account the scope and the account
// name say, and the refusal never echoes the string.
func TestAzureConnectionStringAccountMustMatch(t *testing.T) {
	typ := asCred(registry["azure"])
	mk := func(scope, acct string) session.Spec {
		d := Def{Name: "az", Type: "azure", Scope: scope, Options: map[string]string{"sign_in": "connection_string"}}
		if acct != "" {
			d.Options["account_name"] = acct
		}
		return d.Spec()
	}
	cs := "DefaultEndpointsProtocol=https;AccountName=otheracct;AccountKey=c2VjcmV0LWtleS1uZWVkbGU=;EndpointSuffix=core.windows.net"
	for _, sp := range []session.Spec{
		mk("abfss://c@acmelake.dfs.core.windows.net/", ""),
		mk("az://acmelake.blob.core.windows.net/c/", ""),
		mk("az://c/", "acmelake"),
	} {
		_, err := typ.SecretSteps(sp, secrets.Credential{Secret: cs})
		require.Error(t, err, sp.Params["scope"])
		assert.NotContains(t, err.Error(), "c2VjcmV0")
		assert.NotContains(t, err.Error(), "otheracct")
	}
	good := strings.Replace(cs, "otheracct", "acmelake", 1)
	for _, sp := range []session.Spec{
		mk("abfss://c@acmelake.dfs.core.windows.net/", ""),
		mk("az://c/", "acmelake"),
		mk("az://c/", ""),
	} {
		_, err := typ.SecretSteps(sp, secrets.Credential{Secret: good})
		require.NoError(t, err, sp.Params["scope"])
	}
	// No AccountName in the string: nothing to compare (e.g. a SAS-style string).
	_, err := typ.SecretSteps(mk("az://c/", "acmelake"), secrets.Credential{Secret: "BlobEndpoint=http://127.0.0.1:1/x;"})
	require.NoError(t, err)
	assert.Equal(t, "acmelake", connStringAccount("a=b; accountname = acmelake ;c=d"))
	assert.Equal(t, "", connStringAccount("nothing"))
}

func urlLake(path, catalog string, readOnly bool) Def {
	o := map[string]string{"data_path": "s3://b/lake/"}
	if catalog != "" {
		o["catalog"] = catalog
	}
	return Def{Name: "lake", Type: "ducklake", Path: path, ReadOnly: readOnly, Options: o}
}

func TestDuckLakeCatalogOptionAndURLCatalogRules(t *testing.T) {
	// Catalog values: duckdb (default, also spelled), sqlite; anything else is refused.
	for _, c := range []string{"", "duckdb", "sqlite"} {
		d := lakeDef(t)
		if c != "" {
			d.Options["catalog"] = c
		}
		assert.NoError(t, Validate(d), c)
	}
	for _, c := range []string{"postgres", "SQLite", "mysql", "sqlite:x", " "} {
		d := lakeDef(t)
		d.Options["catalog"] = c
		assert.Error(t, Validate(d), c)
	}

	// A URL catalog is a plain storage URL, only ever read, whatever its kind.
	for _, c := range []string{"duckdb", "sqlite"} {
		assert.NoError(t, Validate(urlLake("s3://b/lake/catalog.db", c, true)), c)
		assert.NoError(t, Validate(urlLake("abfss://c@a.dfs.core.windows.net/l/c.db", c, true)), c)
		err := Validate(urlLake("s3://b/lake/catalog.db", c, false))
		require.Error(t, err, c)
		assert.Contains(t, err.Error(), "can only be read")
	}
	// The URL test comes before the scheme test (s3: matches the scheme pattern),
	// and every other scheme is still refused, URL-shaped or not.
	for _, p := range []string{
		"postgres://u:p@h/db", "postgres:dbname=x host=h", "sqlite:/tmp/m.db", "duckdb:/tmp/m.db",
		"https://x.example/c.db", "http://x.example/c.db", "file:///etc/passwd", "md:db",
		"s3://b/lake/c.db?x=1", "s3://b/lake/*.db", "s3://b/../x.db",
	} {
		assert.Error(t, Validate(urlLake(p, "sqlite", true)), p)
		assert.Error(t, Validate(urlLake(p, "duckdb", true)), p)
	}
	// A local catalog may be writable for both kinds.
	d := lakeDef(t)
	d.Options["catalog"] = "sqlite"
	assert.NoError(t, Validate(d))
}

func TestDuckLakeAttachTargets(t *testing.T) {
	typ := registry["ducklake"]
	d := lakeDef(t)
	stmts, err := typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Contains(t, stmts[0], `ATTACH 'ducklake:`+d.Path+`'`)
	assert.NotContains(t, stmts[0], "ducklake:sqlite:")

	d.Options["catalog"] = "sqlite"
	stmts, err = typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Contains(t, stmts[0], `ATTACH 'ducklake:sqlite:`+d.Path+`' AS "lake"`)
	assert.Len(t, statements(stmts[0]), 1)

	// A URL catalog is passed as it is (not stat'ed, not made absolute), read-only.
	for target, c := range map[string]string{"ducklake:s3://b/lake/c.db": "", "ducklake:sqlite:s3://b/lake/c.db": "sqlite"} {
		stmts, err = typ.AttachSQL(urlLake("s3://b/lake/c.db", c, true).Spec())
		require.NoError(t, err, c)
		assert.Equal(t, []string{`ATTACH '` + target + `' AS "lake" (DATA_PATH 's3://b/lake/', READ_ONLY)`}, stmts)
	}
	// A writable URL catalog never reaches the ATTACH.
	_, err = typ.AttachSQL(urlLake("s3://b/lake/c.db", "sqlite", false).Spec())
	require.Error(t, err)

	// A quote in a SQLite path stays inside its literal.
	d = lakeDef(t)
	d.Options["catalog"] = "sqlite"
	d.Path = filepath.Join(filepath.Dir(d.Path), "it's;a lake.db")
	stmts, err = typ.AttachSQL(d.Spec())
	require.NoError(t, err)
	assert.Len(t, statements(stmts[0]), 1)
}

func TestDuckLakeSpecExtensions(t *testing.T) {
	typ := registry["ducklake"]
	se, ok := typ.(session.SpecExtensionsType)
	require.True(t, ok)
	local := func(c string) Def { d := lakeDef(t); d.Options["catalog"] = c; return d }
	for name, tc := range map[string]struct {
		d    Def
		want []string
	}{
		"local duckdb":         {local(""), []string{"ducklake"}},
		"local sqlite":         {local("sqlite"), []string{"ducklake", "sqlite"}},
		"url data path":        {Def{Name: "l", Type: "ducklake", Path: "/x/c.db", Options: map[string]string{"data_path": "s3://b/l/"}}, []string{"ducklake", "httpfs"}},
		"url catalog, duckdb":  {urlLake("s3://b/c.db", "", true), []string{"ducklake", "httpfs"}},
		"url catalog, sqlite":  {urlLake("s3://b/c.db", "sqlite", true), []string{"ducklake", "sqlite", "httpfs"}},
		"local sqlite, url dp": {Def{Name: "l", Type: "ducklake", Path: "/x/c.db", Options: map[string]string{"data_path": "s3://b/l/", "catalog": "sqlite"}}, []string{"ducklake", "sqlite", "httpfs"}},
	} {
		assert.Equal(t, tc.want, se.SpecExtensions(tc.d.Spec()), name)
		assert.Equal(t, tc.want, session.ExtensionsFor(asCred(typ), tc.d.Spec()), name)
	}
	// The type's base list is what person-facing flows and the allow-list read.
	assert.Equal(t, []string{"ducklake"}, asCred(typ).Extensions())
	// A type without the hook falls back to Extensions().
	assert.Equal(t, []string{"sqlite"}, session.ExtensionsFor(asCred(registry["sqlite"]), session.Spec{}))
}

func TestDuckLakeTestSkipsAttachForAURLCatalog(t *testing.T) {
	res := Test(context.Background(), urlLake("s3://b/lake/c.db", "sqlite", true), secrets.Credential{}, WithTestExtensionDir(t.TempDir()))
	assert.True(t, res.OK, res.Error)
	assert.Contains(t, res.Detail, "connect the lake from the rail")
	// Not "a new lake": a URL is never stat'ed.
	assert.NotContains(t, res.Detail, "new lake")
	res = Test(context.Background(), urlLake("s3://b/lake/c.db", "sqlite", false), secrets.Credential{}, WithTestExtensionDir(t.TempDir()))
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "can only be read")
}

// A catalog URL with glob characters would name a different object than the
// one that is checked (a[1].db is a1.db): Stage refuses it before any read, and
// so does Validate.
func TestDuckLakeStageRefusesGlobCharacters(t *testing.T) {
	typ, ok := registry["ducklake"].(session.Stager)
	require.True(t, ok)
	for _, u := range []string{"s3://lake/a[1].db", "s3://lake/a{1,2}.db", "s3://lake/a!.db"} {
		_, err := typ.Stage(context.Background(), nil, urlLake(u, "sqlite", true).Spec(), t.TempDir())
		require.Error(t, err, u)
		assert.Contains(t, err.Error(), "plain")
		assert.Error(t, Validate(urlLake(u, "sqlite", true)), u)
	}
}
