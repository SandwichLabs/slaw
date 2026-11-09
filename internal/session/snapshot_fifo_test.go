//go:build unix

package session

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A FIFO where the log folder should be would block an open forever: it is
// refused by its mode, never opened, so the collector returns at once and holds
// no goroutine or slot.
func TestSnapshot_ADeltaLogThatIsAFifoIsNeverOpened(t *testing.T) {
	h, _ := snapHub(t)
	table := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(table, "_delta_log"), 0o600))
	_, err := os.Lstat(filepath.Join(table, "_delta_log"))
	require.NoError(t, err)

	before := len(snapSlots)
	start := time.Now()
	got := collectAll(context.Background(), h, []snapTarget{{
		info: ConnectionInfo{Name: "dl", Type: "delta", Kind: StatusOK}, spec: Spec{Path: table},
	}}, SnapshotBudget)
	assert.Less(t, time.Since(start), SnapshotBudget)
	assert.Equal(t, SnapshotNotRecorded, got[0].Snapshot.Label)
	assert.Equal(t, snapshotValueMissing, got[0].Snapshot.Value)
	assert.Eventually(t, func() bool { return len(snapSlots) == before }, time.Second, 5*time.Millisecond, "no slot is held")
}
