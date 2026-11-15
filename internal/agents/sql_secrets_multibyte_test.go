package agents

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dollar-quote tag with multibyte characters must not make the token screen
// lose its place: the statement after the closing tag is still seen (a lexer
// offset bug in usersql once skipped runes there, merging two statements).
func TestTouchesSecretsSeesStatementsAfterAMultibyteDollarTag(t *testing.T) {
	bom := string(rune(0xFEFF)) // a literal BOM is not valid in Go source
	for _, q := range []string{
		"SELECT $é日日$x$é日日$;DROP SECRET s",
		"SELECT $日本$x$日本$;CREATE SECRET s (TYPE http)",
		"SELECT $é$x$é$;DROP SECRET s",
		"SELECT $ $x$ $;DROP SECRET s",
		"SELECT $" + bom + "$x$" + bom + "$;DROP SECRET s",
	} {
		assert.True(t, touchesSecrets(q), q)
		class, reason := classifySQL(context.Background(), newClassifierTestDB(t), q, nil)
		assert.Equal(t, sqlClassOther, class, q)
		for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
			require.Error(t, (&SandboxConfig{Mode: mode}).checkSQLClass(class, reason), "%s in %s", q, mode)
		}
	}
	assert.False(t, touchesSecrets("SELECT $é$x$é$, 2"))
}

// The later policy layers (the head never-list and the parse) are independent
// of the token screen, so the screen is not the only thing between a script and
// secret DDL. This runs the analyzer past the screen to record that.
func TestSecretDDLIsRefusedPastTheTokenScreen(t *testing.T) {
	for _, q := range []string{
		"SELECT $é日日$x$é日日$;DROP SECRET s",
		"SELECT 1;DROP SECRET s",
		"SELECT 1;CREATE SECRET s (TYPE http)",
	} {
		a := &analyzer{ctx: context.Background(), db: newClassifierTestDB(t), env: nil, vetted: map[string]bool{}}
		a.paths = (*sqlEnv)(nil).pathPolicy()
		v := a.script(q, nil)
		assert.Equal(t, sqlClassOther, v.Class, "%s: %s", q, v.Reason)
	}
}
