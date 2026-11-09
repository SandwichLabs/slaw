package bundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

// A workspace's run receipts live in .slaw-local/receipts/ and are never a
// bundle entry: not the receipt files, not the key, not a stray file beside
// them, and nothing of their content reaches the archive bytes.
func TestReceipts_AreNeverABundleEntry(t *testing.T) {
	src, _ := newWS(t)
	ctx := context.Background()
	require.NoError(t, src.Queries().Create(ctx, &queries.Query{ID: "orders", Title: "Orders", SQL: "SELECT 1"}))

	dir := filepath.Join(src.Root(), ".slaw-local", "receipts")
	key, err := receipts.LoadKey(src.Root())
	require.NoError(t, err)
	store, err := receipts.NewFileStore(src.Root(), receipts.DefaultPolicy())
	require.NoError(t, err)
	for i := 0; i < 25; i++ {
		h := receipts.NewResultHasher(key, nil)
		sql := "SELECT 'RECEIPTMARKER-" + strings.Repeat("x", i) + "'"
		r, err := receipts.Seal(receipts.Receipt{
			At: time.Now(), Surface: receipts.SurfaceQueryEditor, Actor: receipts.ActorPerson,
			Source: receipts.Source{Kind: receipts.SourceSQL}, SQL: sql, SQLHash: receipts.HashText(key, sql),
			RunSQL: sql, RunSQLHash: receipts.HashRunText(key, sql), Verdict: receipts.VerdictUnchecked,
			ResultHMAC: h.Sum(),
		})
		require.NoError(t, err)
		require.NoError(t, store.Put(r))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("RECEIPTMARKER"), 0o600))

	res, err := Export(ctx, src, ExportOptions{Selection: all()})
	require.NoError(t, err)
	require.NotEmpty(t, res.Manifest.Contents)
	for _, e := range res.Manifest.Contents {
		assert.NotContains(t, e.Path, "receipts", e.Path)
		assert.NotContains(t, e.Path, ".slaw-local", e.Path)
	}
	assert.NotContains(t, string(res.Data), "RECEIPTMARKER")
}
