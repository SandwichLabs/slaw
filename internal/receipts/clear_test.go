package receipts

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsage_CountsSizeAndOldestWithoutDecoding(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root, DefaultPolicy())
	n, size, oldest := s.Usage()
	assert.Zero(t, n)
	assert.Zero(t, size)
	assert.True(t, oldest.IsZero())

	var want int64
	var first Receipt
	for i := 1; i <= 3; i++ {
		r := sealed(t, i)
		require.NoError(t, s.Put(r))
		b, err := Encode(r)
		require.NoError(t, err)
		want += int64(len(b))
		if i == 1 {
			first = r
		}
	}
	// A corrupt receipt file still counts: Usage reads names and sizes only.
	junk := filepath.Join(recDir(root), first.ID[2:4], "r_"+first.ID[2:4]+"000000000000000000.json")
	require.NoError(t, os.WriteFile(junk, []byte("not json"), 0o600))
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(filepath.Join(recDir(root), first.ID[2:4], first.ID+".json"), old, old))
	// Files that are not receipts do not count.
	require.NoError(t, os.WriteFile(filepath.Join(recDir(root), "notes.txt"), []byte("hi"), 0o600))

	n, size, oldest = s.Usage()
	assert.Equal(t, 4, n)
	assert.Equal(t, want+int64(len("not json")), size)
	assert.True(t, oldest.Equal(old), "oldest is the least recently modified receipt")
}

func TestClear_RemovesReceiptsOnly(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root, DefaultPolicy())
	key, err := LoadKey(root)
	require.NoError(t, err)
	var ids []string
	for i := 1; i <= 4; i++ {
		r := sealed(t, i)
		require.NoError(t, s.Put(r))
		ids = append(ids, r.ID)
	}
	stray := filepath.Join(recDir(root), "notes.txt")
	require.NoError(t, os.WriteFile(stray, []byte("mine"), 0o600))
	strayInShard := filepath.Join(recDir(root), ids[0][2:4], "keep-me.txt")
	require.NoError(t, os.WriteFile(strayInShard, []byte("mine"), 0o600))
	tmp := filepath.Join(recDir(root), ids[0][2:4], ".tmp-inflight")
	require.NoError(t, os.WriteFile(tmp, []byte("half"), 0o600))

	require.NoError(t, s.Clear())

	n, size, _ := s.Usage()
	assert.Zero(t, n)
	assert.Zero(t, size)
	for _, id := range ids {
		_, err := s.Get(id)
		assert.ErrorIs(t, err, ErrNotFound)
	}
	for _, p := range []string{stray, strayInShard, tmp} {
		assert.FileExists(t, p, "Clear removes receipts and nothing else")
	}
	again, err := LoadKey(root)
	require.NoError(t, err)
	assert.Equal(t, key, again, "Clear keeps the key")

	// The store still works afterwards.
	require.NoError(t, s.Put(sealed(t, 9)))
	n, _, _ = s.Usage()
	assert.Equal(t, 1, n)
}

func TestClear_NeverFollowsASymbolicLink(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root, DefaultPolicy())
	r := sealed(t, 1)
	require.NoError(t, s.Put(r))

	outside := t.TempDir()
	victim := sealed(t, 2)
	// A shard folder that is a link to a folder holding a receipt-named file.
	require.NoError(t, os.WriteFile(filepath.Join(outside, victim.ID+".json"), []byte("{}"), 0o600))
	link := filepath.Join(recDir(root), "ab")
	require.NoError(t, os.Symlink(outside, link))
	// A receipt-named link inside a real shard, pointing at a file outside.
	target := filepath.Join(outside, "precious.txt")
	require.NoError(t, os.WriteFile(target, []byte("precious"), 0o600))
	linkID := "r_" + r.ID[2:4] + "000000000000000000"
	require.True(t, ValidID(linkID), "the link is named like a receipt")
	fileLink := filepath.Join(recDir(root), r.ID[2:4], linkID+".json")
	require.NoError(t, os.Symlink(target, fileLink))

	require.NoError(t, s.Clear())

	assert.FileExists(t, filepath.Join(outside, victim.ID+".json"), "a linked shard is not followed")
	assert.FileExists(t, target, "a linked receipt file is not followed")
	lfi, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, lfi.Mode()&os.ModeSymlink, "the link itself is left in place")
	_, err = s.Get(r.ID)
	assert.ErrorIs(t, err, ErrNotFound, "the real receipt is gone")
}

func TestMock_UsageAndClear(t *testing.T) {
	m := NewMock()
	require.NoError(t, m.Put(sealed(t, 1)))
	require.NoError(t, m.Put(sealed(t, 2)))
	n, size, oldest := m.Usage()
	assert.Equal(t, 2, n)
	assert.Positive(t, size)
	assert.False(t, oldest.IsZero())
	require.NoError(t, m.Clear())
	n, _, oldest = m.Usage()
	assert.Zero(t, n)
	assert.True(t, oldest.IsZero())
}

// A limit lowered live through the recorder prunes the store without a reopen.
func TestRecorderSetPolicy_ALoweredLimitPrunesLive(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root, DefaultPolicy())
	rec := NewRecorder(s, testKey, DefaultPolicy(), Checks{}, nil)
	for i := 1; i <= 150; i++ {
		require.NoError(t, s.Put(sealed(t, i)))
	}
	n, _, _ := s.Usage()
	require.Equal(t, 150, n)

	p := rec.Policy()
	p.MaxCount = FloorMaxCount
	rec.SetPolicy(p)
	assert.Equal(t, FloorMaxCount, rec.Policy().MaxCount)
	s.Wait()
	n, _, _ = s.Usage()
	assert.Equal(t, FloorMaxCount, n)

	// A value under the floor is clamped to it, never to zero receipts.
	rec.SetPolicy(Policy{KeepParams: true, MaxCount: 1, MaxAge: time.Nanosecond, MaxBytes: 1})
	got := rec.Policy()
	assert.Equal(t, FloorMaxCount, got.MaxCount)
	assert.Equal(t, FloorMaxAge, got.MaxAge)
	assert.Equal(t, FloorMaxBytes, got.MaxBytes)
}
