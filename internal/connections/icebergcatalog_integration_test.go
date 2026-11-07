//go:build integration

package connections_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/connections"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
)

// fixtureStore is an s3 connection that reads the fixture's warehouse bucket.
func fixtureStore(f *icebergtest.Fixture) (connections.Def, secrets.Credential) {
	def := connections.Def{Name: "minio", Type: "s3", Scope: "s3://" + icebergtest.Warehouse + "/", Region: "us-east-1",
		Options: map[string]string{"endpoint": f.S3Endpoint, "url_style": "path", "use_ssl": "false"}}
	return def, secrets.Credential{KeyID: f.KeyID, Secret: f.Secret}
}

func restCatalog(uri, auth string) connections.Def {
	opts := map[string]string{"endpoint_type": "rest", "uri": uri, "warehouse": icebergtest.Warehouse, "auth": auth}
	return connections.Def{Name: "cat", Type: "iceberg_catalog", ReadOnly: true, Options: opts}
}

// captureLogs sends every slog record of the test into the returned buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func resultText(res connections.TestResult) string {
	b, _ := json.Marshal(res)
	return string(b)
}

// Test lists namespaces and tables through a REST catalog that wants a bearer
// token, and describes one table. A wrong token is a redacted auth failure, and
// neither token appears in the result or in a log line.
func TestIcebergCatalogTestWithBearerToken(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	logs := captureLogs(t)
	proxy := f.Proxy(t, icebergtest.ProxyOptions{RequireBearer: true})
	ctx := context.Background()
	ext := connections.WithTestExtensionDir(extDir(t))
	store, storeCred := fixtureStore(f)
	def := restCatalog(proxy.URL, "bearer")

	t.Run("lists namespaces, tables and one table's columns", func(t *testing.T) {
		red := secrets.NewRedactor()
		res := connections.Test(ctx, def, secrets.Credential{Token: icebergtest.BearerToken}, ext, connections.WithInstall(true),
			connections.WithTestRedactor(red), connections.WithRequiredStorage(store, storeCred))
		require.True(t, res.OK, resultText(res))
		assert.Equal(t, 2, res.Count, "ns.orders and ns.events")
		assert.Contains(t, res.Detail, "1 namespaces, 2 tables")
		assert.Contains(t, res.Detail, "columns")
		for _, s := range []string{resultText(res), logs.String()} {
			assert.NotContains(t, s, icebergtest.BearerToken)
			assert.NotContains(t, s, f.Secret)
		}
		// Names only, plus one table's LoadTable: not a LoadTable per table.
		loads := 0
		for _, r := range proxy.Requests() {
			if strings.HasPrefix(r, "GET /v1/") && strings.Contains(r, "/tables/") {
				loads++
			}
		}
		assert.LessOrEqual(t, loads, 3, "%v", proxy.Requests())
	})

	t.Run("a wrong token is a redacted authentication failure", func(t *testing.T) {
		const wrong = "iceberg_wrong_token_needle2"
		res := connections.Test(ctx, def, secrets.Credential{Token: wrong}, ext,
			connections.WithRequiredStorage(store, storeCred))
		require.False(t, res.OK)
		assert.NotEmpty(t, res.Error)
		assert.Contains(t, res.Error, connections.MetadataWarning, "a failed Test carries the warning too")
		for _, s := range []string{resultText(res), logs.String()} {
			assert.NotContains(t, s, wrong)
			assert.NotContains(t, s, icebergtest.BearerToken)
			assert.NotContains(t, s, f.Secret)
		}
		t.Logf("status=%s code=%s error=%q", res.Status, res.Code, res.Error)
	})

	t.Run("no token at all", func(t *testing.T) {
		res := connections.Test(ctx, def, secrets.Credential{}, ext, connections.WithRequiredStorage(store, storeCred))
		require.False(t, res.OK)
		assert.NotContains(t, resultText(res), icebergtest.BearerToken)
	})
}

// A catalog with no authentication reads with no secret of its own.
func TestIcebergCatalogTestWithNoAuthentication(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	store, storeCred := fixtureStore(f)
	res := connections.Test(context.Background(), restCatalog(f.RESTURI, "none"), secrets.Credential{},
		connections.WithTestExtensionDir(extDir(t)), connections.WithInstall(true), connections.WithRequiredStorage(store, storeCred))
	require.True(t, res.OK, resultText(res))
	assert.Equal(t, 2, res.Count)
}

func vendedSecrets(t *testing.T, sess session.Session) []string {
	t.Helper()
	rows, err := sess.DB().QueryContext(context.Background(), `SELECT name FROM duckdb_secrets() WHERE provider = 'iceberg'`)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

// On a hardened hub session a catalog always attaches with delegation 'none':
// even behind a proxy that vends credentials in every LoadTable response, no
// vended secret is created, so the catalog cannot choose a scope or endpoint
// for the hub's reads; data is read through the storage connections.
func TestIcebergCatalogDelegationOnTheHub(t *testing.T) {
	f := icebergtest.Start(t)
	f.Seed(t)
	proxy := f.Proxy(t, icebergtest.ProxyOptions{Vended: f.VendedConfig()})
	ctx := context.Background()
	store, storeCred := fixtureStore(f)

	open := func(t *testing.T, def connections.Def, requireStore bool) session.Session {
		sess, err := session.New(session.WithConnectionTypes(connections.Types()...), session.WithExtensionDirectory(extDir(t)))
		require.NoError(t, err)
		t.Cleanup(func() { _ = sess.Close() })
		for _, e := range []string{"httpfs", "avro", "iceberg"} {
			require.NoError(t, sess.InstallExtension(ctx, e))
		}
		spec := def.Spec()
		if requireStore {
			ss := store.Spec()
			ss.Credential = func(context.Context) (secrets.Credential, error) { return storeCred, nil }
			require.NoError(t, sess.Register(ss))
			spec.Requires = []string{store.Name}
		}
		require.NoError(t, sess.Register(spec))
		require.NoError(t, sess.Connect(ctx, def.Name))
		return sess
	}
	count := func(t *testing.T, sess session.Session) int {
		var n int
		require.NoError(t, sess.DB().QueryRowContext(ctx, "SELECT max(id) FROM cat.ns.orders").Scan(&n))
		return n
	}

	t.Run("no vended secret, read through the person's storage connection", func(t *testing.T) {
		sess := open(t, restCatalog(proxy.URL, "none"), true)
		assert.Equal(t, 299, count(t, sess))
		assert.Empty(t, vendedSecrets(t, sess))
	})

	t.Run("without a storage connection it fails closed", func(t *testing.T) {
		sess := open(t, restCatalog(proxy.URL, "none"), false)
		var n int
		err := sess.DB().QueryRowContext(ctx, "SELECT max(id) FROM cat.ns.orders").Scan(&n)
		require.Error(t, err)
		assert.Empty(t, vendedSecrets(t, sess))
	})

	// A writable catalog (read_only: false) is attached without READ_ONLY and
	// still asks for no credentials.
	t.Run("writable: still no vended secret", func(t *testing.T) {
		def := restCatalog(proxy.URL, "none")
		def.ReadOnly = false
		sess := open(t, def, true)
		assert.Equal(t, 299, count(t, sess))
		assert.Empty(t, vendedSecrets(t, sess))
	})
}

// The secret DDL of every REST authentication parses in DuckDB 1.5.6 with the
// iceberg extension, and creates a secret of type iceberg.
func TestIcebergCatalogSecretDDLSpelling(t *testing.T) {
	ctx := context.Background()
	db, err := session.OpenDB("", session.WithExtensionDirectory(extDir(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, e := range []string{"httpfs", "avro", "iceberg"} {
		_, err := db.ExecContext(ctx, "INSTALL "+e)
		require.NoError(t, err, e)
		_, err = db.ExecContext(ctx, "LOAD "+e)
		require.NoError(t, err, e)
	}
	var ct session.CredentialedType
	for _, typ := range connections.Types() {
		if typ.Name() == "iceberg_catalog" {
			var ok bool
			ct, ok = typ.(session.CredentialedType)
			require.True(t, ok)
		}
	}
	require.NotNil(t, ct)
	// DuckDB fetches an OAuth2 token when the secret is created, so a loopback
	// token endpoint answers it.
	var tokenPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenPaths = append(tokenPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t","token_type":"bearer","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	api := srv.URL + "/api"
	for name, c := range map[string]struct {
		def  connections.Def
		cred secrets.Credential
	}{
		"bearer":       {restCatalog(api, "bearer"), secrets.Credential{Token: "tok'en"}},
		"oauth2":       {restCatalog(api, "oauth2"), secrets.Credential{KeyID: "cid", Secret: "cs'ecret"}},
		"oauth2 w uri": {withOpt(restCatalog(api, "oauth2"), "oauth2_server_uri", api+"/custom/token"), secrets.Credential{KeyID: "cid", Secret: "cs"}},
	} {
		steps, err := ct.SecretSteps(c.def.Spec(), c.cred)
		require.NoError(t, err, name)
		require.Len(t, steps, 1, name)
		ddl := "CREATE OR REPLACE TEMPORARY SECRET " + session.QuoteIdent(steps[0].Name) + " (TYPE " + steps[0].Type
		for _, p := range steps[0].Params {
			v, ok := p.Value.(string)
			require.True(t, ok, p.Key)
			ddl += ", " + p.Key + " " + session.QuoteLiteral(v)
		}
		_, err = db.ExecContext(ctx, ddl+")")
		require.NoError(t, err, name)
		var typ string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT type FROM duckdb_secrets() WHERE name = $1`, steps[0].Name).Scan(&typ), name)
		assert.Equal(t, "iceberg", typ, name)
	}
	// The default token URL is derived from the catalog URI; a given one is used as is.
	assert.Contains(t, tokenPaths, "/api/v1/oauth/tokens")
	assert.Contains(t, tokenPaths, "/api/custom/token")
}

func withOpt(d connections.Def, k, v string) connections.Def {
	o := map[string]string{}
	for kk, vv := range d.Options {
		o[kk] = vv
	}
	o[k] = v
	d.Options = o
	return d
}
