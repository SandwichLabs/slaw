package storageglob

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeKeepsThePatternNotTheKeys(t *testing.T) {
	keys := make([]string, 0, DefaultMax)
	for i := 0; i < DefaultMax; i++ {
		keys = append(keys, fmt.Sprintf("s3://b/big/part-%05d.csv", i))
	}
	f := &fakeLister{keys: map[string][]string{"s3://b/big/*.csv": keys}}
	text := `SELECT count(*) FROM read_csv('s3://b/big/*.csv', header = true)`
	out, rw, err := Expand(context.Background(), text, Options{Lister: f})
	require.NoError(t, err)
	require.True(t, rw.Changed())
	require.Greater(t, len(out), 200000, "the statement that runs holds every key")

	sum, n := rw.Summarize(out)
	assert.Equal(t, DefaultMax, n)
	assert.Equal(t, `SELECT count(*) FROM read_csv('s3://b/big/*.csv' /* expanded to 10000 objects */, header = true)`, sum)
	assert.NotContains(t, sum, "part-0")
}

func TestSummarizeLeavesPlainTextAlone(t *testing.T) {
	var none *Rewrite
	sum, n := none.Summarize("SELECT 1")
	assert.Equal(t, "SELECT 1", sum)
	assert.Zero(t, n)

	_, rw, err := Expand(context.Background(), `SELECT * FROM read_csv('s3://b/*.csv')`, Options{Lister: newFake()})
	require.NoError(t, err)
	// Text that is not what the rewrite produced has nothing to put back.
	sum, n = rw.Summarize("SELECT 2")
	assert.Equal(t, "SELECT 2", sum)
	assert.Zero(t, n)
}
