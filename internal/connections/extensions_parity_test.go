package connections

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/SandwichLabs/slaw/internal/session"
)

// Everything person-facing (the extensions list, the consent dialog, the CLI's
// --install) reads TypeInfo.Extensions, while the session gates on the type's
// Extensions(). They must be the same list, in the same order.
func TestInfoExtensionsMatchTheTypeForEveryType(t *testing.T) {
	for name, typ := range registry {
		ct, ok := typ.(session.CredentialedType)
		if !ok {
			continue
		}
		ti, err := TypeInfoFor(name)
		if !assert.NoError(t, err, name) {
			continue
		}
		assert.Equal(t, ct.Extensions(), ti.Extensions, name)
	}
}

// Every kind of iceberg_catalog loads the same three extensions, and no `aws`
// (lakehouse plan S0 item 1): httpfs, avro and iceberg are enough for REST and
// for S3 Tables signed with an explicit s3 secret.
func TestIcebergCatalogExtensionsAreTheSameForEveryKind(t *testing.T) {
	want := []string{"httpfs", "avro", "iceberg"}
	typ, ok := registry["iceberg_catalog"].(session.SpecExtensionsType)
	assert.True(t, ok)
	for _, d := range []Def{s3tDef(), restDef("none"), restDef("bearer")} {
		assert.Equal(t, want, typ.SpecExtensions(d.Spec()), d.Name)
		assert.Equal(t, want, ExtensionsFor(d), d.Name)
	}
	assert.NotContains(t, want, "aws")
}
