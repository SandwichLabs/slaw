package connections

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

const testARN = "arn:aws:s3tables:us-east-1:111122223333:bucket/sales"

func s3tDef() Def {
	return Def{Name: "s3t", Type: "iceberg_catalog", ReadOnly: true, Options: map[string]string{
		"endpoint_type": "s3_tables", "warehouse": testARN, "storage": "aws_lake"}}
}

func restDef(auth string) Def {
	d := Def{Name: "polaris", Type: "iceberg_catalog", ReadOnly: true, Options: map[string]string{
		"endpoint_type": "rest", "uri": "https://catalog.example.com/api/catalog", "warehouse": "analytics"}}
	if auth != "" {
		d.Options["auth"] = auth
	}
	return d
}

func with(d Def, f func(*Def)) Def {
	o := map[string]string{}
	for k, v := range d.Options {
		o[k] = v
	}
	d.Options = o
	f(&d)
	return d
}

func setOpt(k, v string) func(*Def) { return func(d *Def) { d.Options[k] = v } }

func TestIcebergCatalogValidAccepted(t *testing.T) {
	for name, d := range map[string]Def{
		"s3_tables":      s3tDef(),
		"rest default":   restDef(""),
		"rest oauth2":    with(restDef("oauth2"), setOpt("oauth2_server_uri", "https://catalog.example.com/api/catalog/v1/oauth/tokens")),
		"rest bearer":    restDef("bearer"),
		"rest none":      restDef("none"),
		"rest loopback":  with(restDef("none"), setOpt("uri", "http://127.0.0.1:8181")),
		"rest localhost": with(restDef("none"), setOpt("uri", "http://localhost:8181/x")),
		"rest ipv6 lo":   with(restDef("none"), setOpt("uri", "http://[::1]:8181")),
		"trusted":        with(s3tDef(), setOpt("trust_writers", "true")),
		"cn arn":         with(s3tDef(), setOpt("warehouse", "arn:aws-cn:s3tables:cn-north-1:111122223333:bucket/sales")),
		"gov arn":        with(s3tDef(), setOpt("warehouse", "arn:aws-us-gov:s3tables:us-gov-west-1:111122223333:bucket/sales")),
	} {
		assert.NoError(t, Validate(d), name)
	}
}

func TestIcebergCatalogValidateRefusals(t *testing.T) {
	type refusal struct {
		d    Def
		want string
	}
	bad := map[string]refusal{
		"writable":               {with(s3tDef(), func(d *Def) { d.ReadOnly = false }), "read-only"},
		"no endpoint type":       {with(s3tDef(), func(d *Def) { delete(d.Options, "endpoint_type") }), "endpoint_type"},
		"glue":                   {with(s3tDef(), setOpt("endpoint_type", "glue")), "not supported yet"},
		"unknown endpoint type":  {with(s3tDef(), setOpt("endpoint_type", "hive")), "not one of"},
		"unknown option":         {with(s3tDef(), setOpt("token", "x")), "unknown option"},
		"stray host":             {with(s3tDef(), func(d *Def) { d.Host = "h" }), "not a setting"},
		"stray scope":            {with(s3tDef(), func(d *Def) { d.Scope = "s3://b/" }), "not a setting"},
		"stray path":             {with(s3tDef(), func(d *Def) { d.Path = "/x" }), "not a setting"},
		"trust not bool":         {with(s3tDef(), setOpt("trust_writers", "yes")), "trust_writers"},
		"s3t no warehouse":       {with(s3tDef(), func(d *Def) { delete(d.Options, "warehouse") }), "warehouse"},
		"s3t not an arn":         {with(s3tDef(), setOpt("warehouse", "sales")), "ARN"},
		"s3t wrong service":      {with(s3tDef(), setOpt("warehouse", "arn:aws:s3:us-east-1:111122223333:bucket/sales")), "ARN"},
		"s3t short account":      {with(s3tDef(), setOpt("warehouse", "arn:aws:s3tables:us-east-1:1111:bucket/sales")), "ARN"},
		"s3t table arn":          {with(s3tDef(), setOpt("warehouse", testARN+"/table/x")), "ARN"},
		"s3t uppercase bucket":   {with(s3tDef(), setOpt("warehouse", "arn:aws:s3tables:us-east-1:111122223333:bucket/Sales")), "ARN"},
		"s3t no storage":         {with(s3tDef(), func(d *Def) { delete(d.Options, "storage") }), "storage"},
		"s3t bad storage name":   {with(s3tDef(), setOpt("storage", "a b")), "storage"},
		"s3t self storage":       {with(s3tDef(), setOpt("storage", "s3t")), "own storage"},
		"s3t with uri":           {with(s3tDef(), setOpt("uri", "https://x.example.com")), "only for a rest"},
		"s3t with auth":          {with(s3tDef(), setOpt("auth", "none")), "only for a rest"},
		"rest no warehouse":      {with(restDef(""), func(d *Def) { delete(d.Options, "warehouse") }), "warehouse"},
		"rest long warehouse":    {with(restDef(""), setOpt("warehouse", strings.Repeat("w", 201))), "longer"},
		"rest control warehouse": {with(restDef(""), setOpt("warehouse", "a\tb")), "control"},
		"rest nbsp warehouse":    {with(restDef(""), setOpt("warehouse", "a b")), "printable"},
		"rest storage":           {with(restDef(""), setOpt("storage", "aws_lake")), "only for an s3_tables"},
		"rest no uri":            {with(restDef(""), func(d *Def) { delete(d.Options, "uri") }), "uri"},
		"rest http remote":       {with(restDef(""), setOpt("uri", "http://catalog.example.com/x")), "localhost"},
		"rest http lookalike":    {with(restDef(""), setOpt("uri", "http://localhost.example.com/x")), "localhost"},
		"rest ftp":               {with(restDef(""), setOpt("uri", "ftp://catalog.example.com")), "https"},
		"rest no scheme":         {with(restDef(""), setOpt("uri", "catalog.example.com")), "uri"},
		"rest userinfo":          {with(restDef(""), setOpt("uri", "https://u:p@catalog.example.com")), "user name"},
		"rest query":             {with(restDef(""), setOpt("uri", "https://catalog.example.com/x?a=b")), "query"},
		"rest empty query":       {with(restDef(""), setOpt("uri", "https://catalog.example.com/x?")), "query"},
		"rest fragment":          {with(restDef(""), setOpt("uri", "https://catalog.example.com/x#f")), "fragment"},
		"rest space":             {with(restDef(""), setOpt("uri", "https://catalog.example.com/a b")), "plain URL"},
		"rest newline":           {with(restDef(""), setOpt("uri", "https://catalog.example.com/a\nb")), "control"},
		"rest no host":           {with(restDef(""), setOpt("uri", "https:///x")), "uri"},
		"rest bad auth":          {restDef("kerberos"), "auth"},
		"oauth2 uri w bearer":    {with(restDef("bearer"), setOpt("oauth2_server_uri", "https://x.example.com/t")), "only valid with auth oauth2"},
		"oauth2 uri w none":      {with(restDef("none"), setOpt("oauth2_server_uri", "https://x.example.com/t")), "only valid with auth oauth2"},
		"oauth2 uri http remote": {with(restDef("oauth2"), setOpt("oauth2_server_uri", "http://x.example.com/t")), "localhost"},
		"oauth2 uri userinfo":    {with(restDef("oauth2"), setOpt("oauth2_server_uri", "https://u@x.example.com/t")), "user name"},
	}
	for n, c := range bad {
		err := Validate(c.d)
		if assert.Error(t, err, n) {
			assert.Contains(t, err.Error(), c.want, n)
		}
	}
}

func TestIcebergCatalogHostileValuesStayInsideTheirLiterals(t *testing.T) {
	typ := registry["iceberg_catalog"]
	for _, v := range evil {
		d := with(restDef("none"), setOpt("warehouse", v))
		if Validate(d) != nil {
			continue
		}
		stmts, err := typ.AttachSQL(d.Spec())
		require.NoError(t, err)
		assert.Len(t, statements(stmts[0]), 1, "warehouse %q", v)
	}
	for _, v := range evil {
		assert.Error(t, Validate(with(s3tDef(), setOpt("storage", v))), "storage %q", v)
	}
}

func TestIcebergCatalogAttachSQL(t *testing.T) {
	typ := registry["iceberg_catalog"]
	attach := func(d Def) string {
		stmts, err := typ.AttachSQL(d.Spec())
		require.NoError(t, err)
		require.Len(t, stmts, 1)
		return stmts[0]
	}
	// The main hub's default: an untrusted catalog asks for no vended credentials.
	assert.Equal(t, `ATTACH 'arn:aws:s3tables:us-east-1:111122223333:bucket/sales' AS "s3t" (TYPE iceberg, ENDPOINT_TYPE 's3_tables', SECRET 'slaw_aws_lake', ACCESS_DELEGATION_MODE 'none', READ_ONLY)`, attach(s3tDef()))
	secretAttach := `ATTACH 'analytics' AS "polaris" (TYPE iceberg, ENDPOINT 'https://catalog.example.com/api/catalog', SECRET 'slaw_polaris', ACCESS_DELEGATION_MODE 'none', READ_ONLY)`
	assert.Equal(t, secretAttach, attach(restDef("")))
	assert.Equal(t, secretAttach, attach(restDef("bearer")))
	assert.Equal(t, `ATTACH 'analytics' AS "polaris" (TYPE iceberg, ENDPOINT 'https://catalog.example.com/api/catalog', AUTHORIZATION_TYPE 'none', ACCESS_DELEGATION_MODE 'none', READ_ONLY)`, attach(restDef("none")))

	// A trusted catalog vends on the main hub; an explicit false does not.
	trusted := with(s3tDef(), setOpt("trust_writers", "true"))
	assert.Contains(t, attach(trusted), `ACCESS_DELEGATION_MODE 'vended_credentials', READ_ONLY)`)
	assert.Contains(t, attach(with(s3tDef(), setOpt("trust_writers", "false"))), `ACCESS_DELEGATION_MODE 'none'`)

	// The attach target decides, not the definition: the lake reader asks for
	// vended credentials on an untrusted catalog, and WithDelegation leaves the
	// spec it was given alone.
	spec := restDef("none").Spec()
	stmts, err := typ.AttachSQL(WithDelegation(spec, DelegationVended))
	require.NoError(t, err)
	assert.Contains(t, stmts[0], `ACCESS_DELEGATION_MODE 'vended_credentials'`)
	assert.NotContains(t, spec.Params, delegationParam)
	stmts, err = typ.AttachSQL(WithDelegation(trusted.Spec(), DelegationNone))
	require.NoError(t, err)
	assert.Contains(t, stmts[0], `ACCESS_DELEGATION_MODE 'none'`)

	// An unknown mode is refused, never written.
	bad := restDef("none").Spec()
	bad.Params = map[string]string{delegationParam: "x', READ_ONLY", "opt.endpoint_type": "rest"}
	_, err = typ.AttachSQL(bad)
	require.Error(t, err)

	// A definition cannot set the mode: Def.Params writes only known keys and
	// opt.*, and an option of that name is an unknown option.
	forged := with(restDef("none"), setOpt(delegationParam, "vended_credentials"))
	assert.Error(t, Validate(forged))
	assert.NotContains(t, forged.Params(), delegationParam)

	// AttachSQL validates: a writable spec attaches nothing.
	w := restDef("none")
	w.ReadOnly = false
	_, err = typ.AttachSQL(w.Spec())
	require.Error(t, err)
}

func TestIcebergCatalogSecretSteps(t *testing.T) {
	typ := registry["iceberg_catalog"]
	steps := func(d Def, c secrets.Credential) []session.SecretStep {
		s, err := asCred(typ).SecretSteps(d.Spec(), c)
		require.NoError(t, err)
		return s
	}
	bearer := steps(restDef("bearer"), secrets.Credential{Token: "tok'en"})
	assert.Equal(t, []session.SecretStep{{Name: "slaw_polaris", Type: "iceberg", Params: []session.SecretParam{
		{Key: "TOKEN", Value: "tok'en", Secret: true},
	}}}, bearer)
	assert.Equal(t, `CREATE OR REPLACE TEMPORARY SECRET "slaw_polaris" (TYPE iceberg, TOKEN 'tok''en')`, renderSecret(bearer[0]))

	assert.Equal(t, []session.SecretStep{{Name: "slaw_polaris", Type: "iceberg", Params: []session.SecretParam{
		{Key: "CLIENT_ID", Value: "cid"},
		{Key: "CLIENT_SECRET", Value: "csecret", Secret: true},
		{Key: "ENDPOINT", Value: "https://catalog.example.com/api/catalog"}, // DuckDB derives the token URL from it
	}}}, steps(restDef(""), secrets.Credential{KeyID: "cid", Secret: "csecret"}))
	withURI := with(restDef("oauth2"), setOpt("oauth2_server_uri", "https://catalog.example.com/api/catalog/v1/oauth/tokens"))
	assert.Equal(t, `CREATE OR REPLACE TEMPORARY SECRET "slaw_polaris" (TYPE iceberg, CLIENT_ID 'cid', CLIENT_SECRET 'csecret', OAUTH2_SERVER_URI 'https://catalog.example.com/api/catalog/v1/oauth/tokens')`,
		renderSecret(steps(withURI, secrets.Credential{KeyID: "cid", Secret: "csecret"})[0]))

	// No secret for none, and none of its own for s3_tables (it signs with the storage connection's).
	assert.Empty(t, steps(restDef("none"), secrets.Credential{}))
	assert.Empty(t, steps(s3tDef(), secrets.Credential{Token: "ignored"}))

	// Missing pieces are named and never echo a value.
	_, err := asCred(typ).SecretSteps(restDef("bearer").Spec(), secrets.Credential{KeyID: "k", Secret: "s"})
	require.Error(t, err)
	_, err = asCred(typ).SecretSteps(restDef("oauth2").Spec(), secrets.Credential{KeyID: "k"})
	require.Error(t, err)
	_, err = asCred(typ).SecretSteps(restDef("oauth2").Spec(), secrets.Credential{Secret: "needle"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "needle")

	// NeedsCredential follows the kind and the authentication.
	ct := asCred(typ)
	assert.False(t, ct.NeedsCredential(s3tDef().Spec()))
	assert.False(t, ct.NeedsCredential(restDef("none").Spec()))
	assert.True(t, ct.NeedsCredential(restDef("bearer").Spec()))
	assert.True(t, ct.NeedsCredential(restDef("").Spec()))
}

// No credential reaches an ATTACH statement, whatever the kind or the mode.
func TestIcebergCatalogNoCredentialInAttachSQL(t *testing.T) {
	const needle = "iceberg_golden_needle"
	cred := secrets.Credential{Token: needle + "_token", KeyID: needle + "_id", Secret: needle + "_secret", Password: needle + "_pw", User: needle + "_user"}
	typ := registry["iceberg_catalog"]
	defs := []Def{s3tDef(), restDef(""), restDef("oauth2"), restDef("bearer"), restDef("none"),
		with(s3tDef(), setOpt("trust_writers", "true"))}
	for _, d := range defs {
		for _, mode := range []Delegation{"", DelegationNone, DelegationVended} {
			spec := d.Spec()
			if mode != "" {
				spec = WithDelegation(spec, mode)
			}
			spec.Credential = func(context.Context) (secrets.Credential, error) { return cred, nil }
			stmts, err := typ.AttachSQL(spec)
			require.NoError(t, err)
			for _, s := range stmts {
				assert.NotContains(t, s, needle, "%s/%s", d.Name, mode)
			}
		}
		// The credential is in the secret DDL only, and flagged Secret where it is one.
		steps, err := asCred(typ).SecretSteps(d.Spec(), cred)
		require.NoError(t, err)
		for _, st := range steps {
			for _, p := range st.Params {
				if p.Key == "TOKEN" || p.Key == "CLIENT_SECRET" {
					assert.True(t, p.Secret, p.Key)
				}
			}
		}
	}
}

func TestIcebergCatalogInfo(t *testing.T) {
	ti, err := TypeInfoFor("iceberg_catalog")
	require.NoError(t, err)
	assert.Equal(t, []string{"httpfs", "avro", "iceberg"}, ti.Extensions)
	assert.Equal(t, Capabilities{Catalog: true, Writable: false, ReadOnlyAttach: true, Remote: true, Test: true}, ti.Capabilities)
	assert.Contains(t, ti.FingerprintFields, "option:trust_writers")
	assert.True(t, IsRemote("iceberg_catalog"))
	byName := map[string]Field{}
	for _, f := range ti.Fields {
		byName[f.Name] = f
		assert.True(t, f.Option, f.Name)
	}
	tw := byName["trust_writers"]
	assert.Equal(t, "false", tw.Default)
	assert.Contains(t, tw.Help, "operator and writers can change where your own S3 reads go and the answers they get")
	assert.Contains(t, tw.Help, "local files and loopback http")
	for _, f := range ti.Credentials {
		assert.True(t, f.PerPerson, f.Name)
	}
	assert.Equal(t, []string{"httpfs", "avro", "iceberg"}, ExtensionsFor(restDef("none")))
}

func TestIcebergCatalogTestNeedsItsStorageConnection(t *testing.T) {
	// A Test without the storage connection does not attach: it says what to set up.
	res := Test(context.Background(), s3tDef(), secrets.Credential{})
	assert.True(t, res.OK)
	assert.Contains(t, res.Detail, "signs with aws_lake: set it up first")

	// Storage connections that do not include the named one are refused up front.
	other := Def{Name: "other", Type: "s3", Scope: "s3://b/"}
	res = Test(context.Background(), s3tDef(), secrets.Credential{}, WithRequiredStorage(other, secrets.Credential{KeyID: "k", Secret: "s"}))
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, `signs with the connection "aws_lake"`)
}
