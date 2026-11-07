package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

func TestConnShared_MetadataDrivenTypesWarn(t *testing.T) {
	root := newWS(t)
	out, stderr, err := runSlaw("conn", "add", "sales", "--type", "delta", "--path", "data/sales", "--scope", "shared", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Warning: "+workspace.MetadataWarning)

	out, stderr, err = runSlaw("conn", "add", "mine", "--type", "delta", "--path", "data/mine", "--scope", "local", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.NotContains(t, out, "Warning:")

	out, stderr, err = runSlaw("conn", "list", "--json", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, stderr, "warning: sales is shared in slaw.yaml. Table metadata")
	assert.Equal(t, 1, strings.Count(out, `"warning"`))

	out, stderr, err = runSlaw("conn", "move", "mine", "--to", "shared", "--workspace", root)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, "Warning: "+workspace.MetadataWarning)
}
