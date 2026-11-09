package receipts

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The store asks its source for the policy before each prune round: a process
// whose person raised a limit elsewhere keeps what the new limit allows, and an
// unanswered source keeps the policy in force instead of the defaults.
func TestPolicySource_PruneRoundsUseTheFreshPolicy(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root, DefaultPolicy())
	old := sealed(t, 1)
	require.NoError(t, s.Put(old))
	when := time.Now().Add(-31 * 24 * time.Hour)
	oldPath := filepath.Join(recDir(root), old.ID[2:4], old.ID+".json")
	require.NoError(t, os.Chtimes(oldPath, when, when))

	// Control: with no source the stale 30-day policy removes it.
	s.pruneAsync()
	s.Wait()
	assert.NoFileExists(t, oldPath)

	require.NoError(t, s.Put(old))
	require.NoError(t, os.Chtimes(oldPath, when, when))
	answer := Policy{KeepParams: true, MaxAge: 365 * 24 * time.Hour}
	ok := true
	s.SetPolicySource(func() (Policy, bool) { return answer, ok })
	s.pruneAsync()
	s.Wait()
	assert.FileExists(t, oldPath, "a prune round keeps what the fresh 365-day policy allows")

	// An unreadable or untrusted settings file keeps the policy in force (365
	// days from the last answer), never the 30-day defaults.
	ok = false
	s.pruneAsync()
	s.Wait()
	assert.FileExists(t, oldPath)
}

func TestRecorderPolicySource_AppliesBeforeEachReceipt(t *testing.T) {
	s := openStore(t, t.TempDir(), DefaultPolicy())
	rec := NewRecorder(s, testKey, DefaultPolicy(), Checks{}, nil)
	assert.True(t, rec.Policy().KeepParams)
	rec.SetPolicySource(func() (Policy, bool) { return Policy{KeepParams: false}, true })
	assert.False(t, rec.Policy().KeepParams)
	assert.Equal(t, DefaultMaxAge, rec.Policy().MaxAge, "an unset limit is the default")
	rec.SetPolicySource(func() (Policy, bool) { return Policy{}, false })
	assert.False(t, rec.Policy().KeepParams, "no answer keeps the last policy")
}
