package receipts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recDir(root string) string { return filepath.Join(root, ".slaw-local", "receipts") }

func openStore(t *testing.T, root string, p Policy) *FileStore {
	t.Helper()
	s, err := NewFileStore(root, p)
	require.NoError(t, err)
	return s
}

// bigReceipt is about 100 KB: a statement near the cap, so a size cap is reached
// with a few dozen files.
func bigReceipt(t *testing.T, n int) Receipt {
	t.Helper()
	r := sample(n)
	r.SQL = strings.Repeat("a", 50<<10) + strconv.Itoa(n)
	r.RunSQL = strings.Repeat("b", 50<<10) + strconv.Itoa(n)
	r.SQLHash, r.RunSQLHash = HashText(testKey, r.SQL), HashRunText(testKey, r.RunSQL)
	s, err := Seal(r)
	require.NoError(t, err)
	return s
}

func count(t *testing.T, dir string) (n int, bytes int64) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			fi, _ := d.Info()
			n++
			bytes += fi.Size()
		}
		return err
	}))
	return n, bytes
}

func age(t *testing.T, dir, id string, by time.Duration) {
	t.Helper()
	p := filepath.Join(dir, id[2:4], id+".json")
	old := time.Now().Add(-by)
	require.NoError(t, os.Chtimes(p, old, old))
}

func TestFileStore_PutGetRoundTripAndLayout(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, DefaultPolicy())
	r := sealed(t, 1)
	require.NoError(t, s.Put(r))
	require.NoError(t, s.Put(r), "the same receipt twice is one")
	got, err := s.Get(r.ID)
	require.NoError(t, err)
	assert.Equal(t, r, got)
	path := filepath.Join(dir, r.ID[2:4], r.ID+".json")
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	n, _ := count(t, dir)
	assert.Equal(t, 1, n)

	// Another store over the same folder (Studio and mcp serve) reads it.
	got2, err := openStore(t, root, DefaultPolicy()).Get(r.ID)
	require.NoError(t, err)
	assert.Equal(t, r, got2)
}

func TestFileStore_GetNotFoundAndBadIDs(t *testing.T) {
	s := openStore(t, t.TempDir(), DefaultPolicy())
	for _, id := range []string{"r_00000000000000000000", "", "../x", "r_../../../etc/passwd", "r_ABCDEF0123456789ABCD"} {
		_, err := s.Get(id)
		require.ErrorIs(t, err, ErrNotFound, id)
	}
}

func TestFileStore_PutRefusesWhatIsNotAValidSealedReceipt(t *testing.T) {
	s := openStore(t, t.TempDir(), DefaultPolicy())
	require.ErrorIs(t, s.Put(sample(1)), ErrInvalid, "unsealed")
	r := sealed(t, 1)
	r.DurationMS = 5
	require.ErrorIs(t, s.Put(r), ErrInvalid, "changed after sealing")
}

func TestFileStore_GetRefusesCorruptFiles(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, DefaultPolicy())
	r := sealed(t, 1)
	require.NoError(t, s.Put(r))
	path := filepath.Join(dir, r.ID[2:4], r.ID+".json")
	good, err := os.ReadFile(path)
	require.NoError(t, err)

	for name, content := range map[string][]byte{
		"unknown field": []byte(strings.Replace(string(good), `"duration_ms"`, `"x":1,"duration_ms"`, 1)),
		"trailing data": append(append([]byte{}, good...), []byte("{}")...),
		"oversize":      append(append([]byte{}, good...), make([]byte, MaxFileBytes)...),
		"edited":        []byte(strings.Replace(string(good), `"duration_ms":12`, `"duration_ms":13`, 1)),
		"empty":         nil,
	} {
		require.NoError(t, os.WriteFile(path, content, 0o600))
		_, err := s.Get(r.ID)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	// Another receipt's content under this id.
	other := sealed(t, 2)
	b, err := Encode(other)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
	_, err = s.Get(r.ID)
	require.ErrorIs(t, err, ErrInvalid)

	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "x"), path))
	_, err = s.Get(r.ID)
	require.ErrorIs(t, err, ErrInvalid, "a link is not followed")
}

func TestPrune_HoldsTheCountCapOldestFirst(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, DefaultPolicy())
	var ids []string
	for i := 0; i < 130; i++ {
		r := sealed(t, i)
		require.NoError(t, s.Put(r))
		age(t, dir, r.ID, time.Duration(130-i)*time.Minute) // i=0 is the oldest
		ids = append(ids, r.ID)
	}
	require.NoError(t, s.Prune(Policy{KeepParams: true, MaxCount: 100}))
	n, _ := count(t, dir)
	assert.Equal(t, 100, n)
	for i, id := range ids {
		_, err := s.Get(id)
		if i < 30 {
			require.ErrorIs(t, err, ErrNotFound, "the oldest go first")
		} else {
			require.NoError(t, err)
		}
	}
	// A cap below the floor is raised to it, never honoured.
	require.NoError(t, s.Prune(Policy{MaxCount: 1}))
	n, _ = count(t, dir)
	assert.Equal(t, 100, n)
}

func TestPrune_HoldsTheAgeCap(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, DefaultPolicy())
	oldR, freshR := sealed(t, 1), sealed(t, 2)
	require.NoError(t, s.Put(oldR))
	require.NoError(t, s.Put(freshR))
	age(t, dir, oldR.ID, 3*24*time.Hour)
	require.NoError(t, s.Prune(Policy{MaxAge: 48 * time.Hour}))
	_, err := s.Get(oldR.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.Get(freshR.ID)
	require.NoError(t, err)
	// Below the floor of one day is raised to it: a receipt an hour old stays.
	age(t, dir, freshR.ID, time.Hour)
	require.NoError(t, s.Prune(Policy{MaxAge: time.Second}))
	_, err = s.Get(freshR.ID)
	require.NoError(t, err)
}

func TestPrune_HoldsTheByteCap(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, DefaultPolicy())
	var ids []string
	for i := 0; i < 30; i++ { // about 3 MB
		r := bigReceipt(t, i)
		require.NoError(t, s.Put(r))
		age(t, dir, r.ID, time.Duration(30-i)*time.Minute)
		ids = append(ids, r.ID)
	}
	_, before := count(t, dir)
	require.Greater(t, before, int64(2<<20))
	require.NoError(t, s.Prune(Policy{MaxBytes: 1 << 20}))
	n, after := count(t, dir)
	assert.LessOrEqual(t, after, int64(1<<20))
	assert.Greater(t, n, 0)
	_, err := s.Get(ids[0])
	require.ErrorIs(t, err, ErrNotFound, "the oldest goes first")
	_, err = s.Get(ids[29])
	require.NoError(t, err, "the newest stays")
}

func TestNewFileStore_ALoweredCapPrunesOnTheNextOpen(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, DefaultPolicy())
	for i := 0; i < 150; i++ {
		r := sealed(t, i)
		require.NoError(t, s.Put(r))
		age(t, dir, r.ID, time.Duration(150-i)*time.Minute)
	}
	n, _ := count(t, dir)
	require.Equal(t, 150, n)
	openStore(t, root, Policy{KeepParams: true, MaxCount: 100})
	n, _ = count(t, dir)
	assert.Equal(t, 100, n)
}

func TestFileStore_PrunesEveryFiveHundredPuts(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, Policy{KeepParams: true, MaxCount: 100})
	for i := 0; i < PruneEvery; i++ {
		require.NoError(t, s.Put(sealed(t, i)))
	}
	n, _ := count(t, dir)
	assert.LessOrEqual(t, n, 100, "the 500th Put pruned to the cap")
}

func TestPrune_LeavesOtherFilesAndSweepsStaleTemps(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s := openStore(t, root, DefaultPolicy())
	r := sealed(t, 1)
	require.NoError(t, s.Put(r))
	sub := filepath.Join(dir, r.ID[2:4])
	stale := filepath.Join(sub, ".slaw-tmp-x.json.123")
	fresh := filepath.Join(sub, ".slaw-tmp-y.json.456")
	note := filepath.Join(sub, "notes.txt")
	for _, p := range []string{stale, fresh, note} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))
	_, err := LoadKey(root)
	require.NoError(t, err)
	require.NoError(t, s.Prune(DefaultPolicy()))
	assert.NoFileExists(t, stale)
	assert.FileExists(t, fresh)
	assert.FileExists(t, note)
	assert.FileExists(t, filepath.Join(dir, KeyFile), "the key is never pruned")
}

func TestFileStore_ConcurrentWritersInProcess(t *testing.T) {
	root := t.TempDir()
	dir := recDir(root)
	s1 := openStore(t, root, DefaultPolicy())
	s2 := openStore(t, root, DefaultPolicy())
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for w, s := range []*FileStore{s1, s2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				r, err := Seal(sample(w*1000 + i))
				if err == nil {
					err = s.Put(r)
				}
				if err == nil && i%25 == 0 {
					err = s.Prune(DefaultPolicy())
				}
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	n, _ := count(t, dir)
	assert.Equal(t, 200, n)
}

// TestFileStore_ConcurrentWritersAcrossProcesses re-runs the test binary as
// writers: two processes put and prune the same folder, and nothing is lost or
// torn.
func TestFileStore_ConcurrentWritersAcrossProcesses(t *testing.T) {
	if os.Getenv("RECEIPTS_HELPER_DIR") != "" {
		return
	}
	root := t.TempDir()
	dir := recDir(root)
	var cmds []*exec.Cmd
	var outs []*strings.Builder
	for p := 0; p < 2; p++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWriter$")
		cmd.Env = append(os.Environ(), "RECEIPTS_HELPER_DIR="+root, "RECEIPTS_HELPER_BASE="+strconv.Itoa(p*1000))
		out := &strings.Builder{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		cmds, outs = append(cmds, cmd), append(outs, out)
	}
	for i, cmd := range cmds {
		require.NoError(t, cmd.Wait(), outs[i].String())
	}
	s := openStore(t, root, floorPolicy)
	require.NoError(t, s.Prune(floorPolicy))
	n, _ := count(t, dir)
	assert.LessOrEqual(t, n, 100, "both writers pruned to the floor and nothing was left over")
	assert.Positive(t, n)
	// Every survivor is whole: a torn or half-pruned file would not decode.
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		_, gerr := s.Get(strings.TrimSuffix(d.Name(), ".json"))
		return gerr
	}))
}

var floorPolicy = Policy{KeepParams: true, MaxCount: 100}

func TestHelperWriter(t *testing.T) {
	root := os.Getenv("RECEIPTS_HELPER_DIR")
	if root == "" {
		t.Skip("helper process")
	}
	base, err := strconv.Atoi(os.Getenv("RECEIPTS_HELPER_BASE"))
	require.NoError(t, err)
	s := openStore(t, root, floorPolicy)
	for i := 0; i < 100; i++ {
		r, err := Seal(sample(base + i))
		require.NoError(t, err)
		require.NoError(t, s.Put(r))
		if i%10 == 0 {
			require.NoError(t, s.Prune(floorPolicy))
		}
	}
}

func TestMock_StoreContract(t *testing.T) {
	m := NewMock()
	r := sealed(t, 1)
	require.NoError(t, m.Put(r))
	got, err := m.Get(r.ID)
	require.NoError(t, err)
	assert.Equal(t, r, got)
	_, err = m.Get("r_00000000000000000000")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, m.Put(sample(2)), ErrInvalid)
	m.PutErr = errors.New("disk full")
	require.Error(t, m.Put(sealed(t, 3)))
	m.PutErr = nil
	for i := 10; i < 150; i++ {
		require.NoError(t, m.Put(sealed(t, i)))
	}
	require.NoError(t, m.Prune(Policy{MaxCount: 100}))
	assert.Len(t, m.All(), 100)
}
