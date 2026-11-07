package workspace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

func icebergRest(name, auth string, extra map[string]string) ConnectionDef {
	o := map[string]string{"endpoint_type": "rest", "uri": "https://catalog.example.com/api", "warehouse": "analytics"}
	if auth != "" {
		o["auth"] = auth
	}
	for k, v := range extra {
		o[k] = v
	}
	return ConnectionDef{Name: name, Type: "iceberg_catalog", ReadOnly: Bool(true), Options: o}
}

func icebergS3Tables(name string) ConnectionDef {
	return ConnectionDef{Name: name, Type: "iceberg_catalog", ReadOnly: Bool(true), Options: map[string]string{
		"endpoint_type": "s3_tables", "warehouse": "arn:aws:s3tables:us-east-1:111122223333:bucket/sales", "storage": "aws_lake"}}
}

// An iceberg_catalog may be defined in any scope, a committed slaw.yaml
// included, and its options named "auth" and "oauth2_server_uri" are settings,
// not credentials.
func TestIcebergCatalogIsSavedInEveryScope(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	for _, d := range []ConnectionDef{
		icebergRest("c_none", "none", nil),
		icebergRest("c_bearer", "bearer", nil),
		icebergRest("c_oauth", "oauth2", map[string]string{"oauth2_server_uri": "https://catalog.example.com/api/v1/oauth/tokens"}),
		icebergRest("c_default", "", nil),
		icebergS3Tables("c_s3t"),
	} {
		require.NoError(t, ws.AddConnection(ScopeShared, d), "shared: %s", d.Name)
		l := d
		l.Name += "_l"
		require.NoError(t, ws.AddConnection(ScopeLocal, l), "local: %s", d.Name)
		u := d
		u.Name += "_u"
		require.NoError(t, ws.AddConnection(ScopeUser, u), "user: %s", d.Name)
		c, ok := ws.Connection(d.Name)
		require.True(t, ok)
		_, err := ResolveConnection(ws, c, nil)
		require.NoError(t, err, d.Name)
	}
	// read_only defaults to true and false is allowed.
	rw := icebergRest("c_rw", "none", nil)
	rw.ReadOnly = Bool(false)
	require.NoError(t, ws.AddConnection(ScopeLocal, rw))
	def := icebergRest("c_ro", "none", nil)
	def.ReadOnly = nil
	assert.True(t, def.IsReadOnly())

	// Credentials still never go in a definition: the credential-name rule
	// guards every other option of the type, and another type's "auth" option.
	for _, extra := range []map[string]string{{"client_secret": "x"}, {"api_token": "x"}} {
		require.Error(t, ws.AddConnection(ScopeLocal, icebergRest("c_bad", "none", extra)))
	}
	other := ConnectionDef{Name: "pg_auth", Type: "postgres", Host: "h", Database: "d", Options: map[string]string{"auth": "x"}}
	require.Error(t, ws.AddConnection(ScopeLocal, other))
}

// Option names are not case-sensitive, so two that differ only in case are one
// option written twice and are refused (the value that won used to depend on map order).
func TestOptionNamesThatDifferOnlyInCaseAreRefused(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	d := icebergRest("c_case", "none", map[string]string{"warehouse": "w2", "Warehouse": "w3"})
	err := ws.AddConnection(ScopeLocal, d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "written twice")
	pg := ConnectionDef{Name: "pg_case", Type: "postgres", Host: "h", Database: "d", Options: map[string]string{"sslmode": "require", "SSLMode": "disable"}}
	require.Error(t, ws.AddConnection(ScopeLocal, pg))
	// One spelling in any case is fine.
	require.NoError(t, ws.AddConnection(ScopeLocal, icebergRest("c_upper", "none", map[string]string{"Oauth2_Server_Uri": ""})))
}

// Only a catalog that signs in has credentials: s3_tables signs with its
// storage connection, and auth none has nothing to sign with.
func TestIcebergCatalogCredentialsFollowTheDefinition(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	ctx := context.Background()
	for name, want := range map[string]bool{"c_s3t": false, "c_none": false, "c_bearer": true, "c_oauth": true, "c_default": true} {
		var d ConnectionDef
		switch name {
		case "c_s3t":
			d = icebergS3Tables(name)
		case "c_none":
			d = icebergRest(name, "none", nil)
		case "c_bearer":
			d = icebergRest(name, "bearer", nil)
		case "c_oauth":
			d = icebergRest(name, "oauth2", nil)
		default:
			d = icebergRest(name, "", nil)
		}
		assert.Equal(t, want, d.NeedsCredential(), name)
		require.NoError(t, ws.AddConnection(ScopeLocal, d), name)
		err := ws.SetCredentials(ctx, name, secrets.Credential{Token: "t", KeyID: "k", Secret: "s"}, true)
		if want {
			require.NoError(t, err, name)
		} else {
			require.Error(t, err, name)
			assert.Contains(t, err.Error(), "no credentials", name)
		}
		c, ok := ws.Connection(name)
		require.True(t, ok)
		rc, err := ResolveConnection(ws, c, nil)
		require.NoError(t, err)
		// TestCredential never touches the keychain for a catalog that signs nothing.
		cred, err := TestCredential(ctx, ws, rc, "", secrets.Credential{})
		if !want {
			require.NoError(t, err, name)
			assert.False(t, cred.HasSecret(), name)
		}
	}
}

// A shared (committed or pulled) iceberg_catalog carries the metadata warning on
// its status, like a shared DuckLake, Iceberg or Delta definition; a writable
// one says so too, and a definition in your own scopes carries none.
func TestSharedIcebergCatalogShowsTheMetadataWarning(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	assert.True(t, MetadataDrivenType("iceberg_catalog"))
	require.NoError(t, ws.AddConnection(ScopeShared, icebergRest("shared_cat", "none", nil)))
	rw := icebergRest("shared_rw", "none", nil)
	rw.ReadOnly = Bool(false)
	require.NoError(t, ws.AddConnection(ScopeShared, rw))
	require.NoError(t, ws.AddConnection(ScopeLocal, icebergRest("mine", "none", nil)))
	warn := map[string]string{}
	for _, st := range ConnectionStates(ws, nil, nil) {
		warn[st.Name] = st.Warning
	}
	assert.Equal(t, MetadataWarning, warn["shared_cat"])
	assert.Equal(t, MetadataWarning+" "+WritableSharedWarning, warn["shared_rw"])
	assert.Empty(t, warn["mine"])
}
