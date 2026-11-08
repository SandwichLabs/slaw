package secrets

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvVarNamesFor(t *testing.T) {
	assert.Equal(t, EnvVarNames("P"), EnvVarNamesFor("P", nil))
	assert.Equal(t, []string{"P_TOKEN"}, EnvVarNamesFor("P", []string{"token"}))
	assert.Equal(t, []string{"P_KEY_ID", "P_SECRET"}, EnvVarNamesFor("P", []string{"key_id", "secret"}))
}

// A definition that uses one credential field says only that variable is missing,
// in both the resolver's reason and the status detail.
func TestEnvHintsNameOnlyTheDefinitionsVariables(t *testing.T) {
	r := NewResolver(WithKeychain(NewMemoryStore()), WithEnv(func(string) string { return "" }))
	b := Binding{Type: "iceberg_catalog", EnvFields: []string{"token"}}
	_, err := r.Resolve(context.Background(), Ref{}, "env:BEAR", b)
	var nc *ErrNeedsCredentials
	require.ErrorAs(t, err, &nc)
	assert.Equal(t, "none of BEAR_TOKEN is set", nc.Reason)
	st := r.Status(context.Background(), Ref{}, "env:BEAR", b)
	assert.Equal(t, "None of BEAR_TOKEN is set.", st.Detail)
	require.Len(t, st.Env, 1)
	assert.Equal(t, "BEAR_TOKEN", st.Env[0].Name)

	_, err = r.Resolve(context.Background(), Ref{}, "env:PG", Binding{Type: "postgres"})
	require.ErrorAs(t, err, &nc)
	assert.Contains(t, nc.Reason, "PG_USER", "a type with no narrowing keeps naming every variable")
}
