package workspace

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// B saves, then A (which loaded before) saves: A reloads, retries once and
// succeeds, and keeps what B wrote under other keys.
func TestSetLocalReceipts_ASaveAfterAnotherHandleSavedSucceeds(t *testing.T) {
	a, root := newWorkspace(t)
	b, err := Open(root, WithConfigDir(configDirOf(t, a)), WithUserConfig(UserConfig{Version: ConfigVersion}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	// B writes the file for the first time, with a key this section does not own.
	l := b.Local()
	l.MCPPort = 4321
	l.Receipts = LocalReceipts{Days: 7}
	require.NoError(t, b.SaveLocal(l))

	// A still holds the pre-B view (no file), so its save would conflict.
	require.NoError(t, SetLocalReceipts(a, ReceiptSettings{KeepParams: false, MaxCount: 500}))
	assert.Equal(t, ReceiptSettings{KeepParams: false, MaxCount: 500}, ReceiptSettingsNow(a))
	assert.Equal(t, 4321, a.Local().MCPPort, "keys B wrote are kept")

	// And again, after B has changed the file once more: still fine.
	require.NoError(t, b.Reload())
	l = b.Local()
	l.MCPPort = 4322
	require.NoError(t, b.SaveLocal(l))
	require.NoError(t, SetLocalReceipts(a, ReceiptSettings{KeepParams: true, Days: 9}))
	assert.Equal(t, 4322, a.Local().MCPPort)
	assert.Equal(t, 9, a.Local().Receipts.Days)
}

// A rewrite with the same size and the same mtime, by atomic rename, is a new
// file: the policy cache must not serve the old answer.
func TestReceiptPolicySource_ASameSizeRewriteWithTheMtimeRestoredIsSeen(t *testing.T) {
	ws, _ := newWorkspace(t)
	require.NoError(t, SetLocalReceipts(ws, ReceiptSettings{KeepParams: true, Days: 11}))
	w, ok := ws.(*workspace)
	require.True(t, ok)
	path := w.localPath(w.cfg.ID)

	p, ok := w.receiptPolicySource()
	require.True(t, ok)
	require.Equal(t, 11, int(p.MaxAge.Hours()/24))
	before, err := os.Stat(path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	changed := []byte(string(data))
	i := bytes.Index(changed, []byte("days: 11"))
	require.GreaterOrEqual(t, i, 0)
	copy(changed[i:], "days: 22")
	tmp := path + ".new"
	require.NoError(t, os.WriteFile(tmp, changed, 0o600))
	require.NoError(t, os.Chtimes(tmp, before.ModTime(), before.ModTime()))
	require.NoError(t, os.Rename(tmp, path))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before.Size(), after.Size())
	require.True(t, before.ModTime().Equal(after.ModTime()))

	p, ok = w.receiptPolicySource()
	require.True(t, ok)
	assert.Equal(t, 22, int(p.MaxAge.Hours()/24), "a new file under the same size and mtime is read again")
}
