package connections

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func names(fs []Field) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// An iceberg_catalog's fields and credentials follow its endpoint_type and auth.
func TestIcebergCatalogFieldsAndCredentialsFollowTheKind(t *testing.T) {
	info, err := TypeInfoFor("iceberg_catalog")
	assert.NoError(t, err)
	visible := func(opts map[string]string) []string {
		var out []Field
		for _, f := range info.Fields {
			if f.VisibleFor(info.Fields, opts) {
				out = append(out, f)
			}
		}
		return names(out)
	}
	assert.Equal(t, []string{"endpoint_type", "warehouse", "storage", "default_namespace"}, visible(map[string]string{"endpoint_type": "s3_tables"}))
	assert.Equal(t, []string{"endpoint_type", "warehouse", "uri", "auth", "oauth2_server_uri", "default_namespace"}, visible(map[string]string{"endpoint_type": "rest"}), "auth unset is its default, oauth2")
	assert.Equal(t, []string{"endpoint_type", "warehouse", "uri", "auth", "default_namespace"}, visible(map[string]string{"endpoint_type": "rest", "auth": "bearer"}))

	cred := func(o map[string]string) []string {
		return names(CredentialFieldsFor(Def{Name: "c", Type: "iceberg_catalog", ReadOnly: true, Options: o}))
	}
	assert.Equal(t, []string{"key_id", "secret"}, cred(map[string]string{"endpoint_type": "rest"}))
	assert.Equal(t, []string{"token"}, cred(map[string]string{"endpoint_type": "rest", "auth": "bearer"}))
	assert.Empty(t, cred(map[string]string{"endpoint_type": "rest", "auth": "none"}))
	assert.Empty(t, cred(map[string]string{"endpoint_type": "s3_tables"}))
	assert.Equal(t, []string{"user", "password"}[:0], names(CredentialFieldsFor(Def{Type: "duckdb"})))
	pg := names(CredentialFieldsFor(Def{Type: "postgres"}))
	assert.NotEmpty(t, pg, "a type with no conditions keeps its whole list")
}
