package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceiptSettings_BindingsRoundTripAndClear(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec("CREATE TABLE r (id INTEGER); INSERT INTO r VALUES (1), (2)")
	require.NoError(t, err)

	info, err := app.GetReceiptSettings()
	require.NoError(t, err)
	assert.True(t, info.Available)
	assert.Equal(t, ReceiptSettings{KeepParams: true, Days: 30, MaxCount: 20000, MaxMB: 64}, info.Effective)
	assert.Equal(t, info.Defaults, info.Effective)
	assert.Equal(t, 1, info.Floors.Days)
	assert.Equal(t, 365, info.Ceilings.Days)
	assert.Zero(t, info.Usage.Count)
	assert.Empty(t, info.Usage.Oldest)

	for i := 0; i < 3; i++ {
		res, err := app.RunQuery(RunQueryRequest{SQL: "SELECT id FROM r ORDER BY id", TabID: "t1"})
		require.NoError(t, err)
		require.NotEmpty(t, res.ReceiptID)
	}
	info, err = app.GetReceiptSettings()
	require.NoError(t, err)
	// The same statement over the same rows is the same receipt only when the
	// time matches; either way at least one was kept.
	assert.GreaterOrEqual(t, info.Usage.Count, 1)
	assert.Positive(t, info.Usage.Bytes)
	oldest, err := time.Parse(time.RFC3339, info.Usage.Oldest)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), oldest, time.Minute)

	// Saving clamps, and the effective values show what is in force.
	require.NoError(t, app.SetReceiptSettings(ReceiptSettings{KeepParams: false, Days: 9999, MaxCount: 5, MaxMB: 8}))
	info, err = app.GetReceiptSettings()
	require.NoError(t, err)
	assert.Equal(t, ReceiptSettings{KeepParams: false, Days: 365, MaxCount: 100, MaxMB: 8}, info.Settings)
	assert.Equal(t, info.Settings, info.Effective)
	assert.False(t, app.ws.Receipts().Policy().KeepParams, "applied to the open recorder at once")

	require.NoError(t, app.ClearReceipts())
	info, err = app.GetReceiptSettings()
	require.NoError(t, err)
	assert.Zero(t, info.Usage.Count)
	assert.Zero(t, info.Usage.Bytes)
}

func TestReceiptSettings_NoWorkspaceIsAnError(t *testing.T) {
	app := newBareApp(t)
	_, err := app.GetReceiptSettings()
	require.Error(t, err)
	require.Error(t, app.SetReceiptSettings(ReceiptSettings{}))
	require.Error(t, app.ClearReceipts())
}
