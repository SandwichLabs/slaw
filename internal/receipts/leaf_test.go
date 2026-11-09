package receipts

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImportsStayLeaf pins the leaf rule: production code here imports only the
// standard library, jsonenc, filestore and filelock. Collecting a receipt's
// facts belongs to the callers (engine, session, agents), which import this
// package, never the other way round.
func TestImportsStayLeaf(t *testing.T) {
	const mod = "github.com/SandwichLabs/slaw/internal/"
	allowed := map[string]bool{mod + "jsonenc": true, mod + "filestore": true, mod + "filelock": true}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		parsed, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			first, _, _ := strings.Cut(path, "/")
			if !strings.Contains(first, ".") {
				continue // the standard library
			}
			require.True(t, allowed[path], "%s imports %s: receipts is a leaf", f, path)
		}
	}
}
