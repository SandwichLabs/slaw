package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

// A table whose files sit outside every storage scope fails the datasheet's read;
// the binding answers in plain words (the bucket, what to do), never DuckDB's
// text, and the same for the filtered read. DuckDB's error() raises the message
// a deny secret's endpoint gives, so no network or extension is needed.
func TestTableReadsExplainADeniedBucket(t *testing.T) {
	app := newOpenApp(t)
	_, err := app.sess.DB().Exec(`CREATE VIEW remote AS SELECT 1 AS id, error('IO Error: Could not resolve hostname error for HTTP GET to ''https://other.deny.invalid/data/secret-key.parquet''') AS v`)
	require.NoError(t, err)

	_, err = app.GetTableData("remote", 1, 50, "", "")
	require.Error(t, err)
	var denied *session.StorageDenyError
	require.ErrorAs(t, err, &denied, err.Error())
	assert.Equal(t, "other", denied.Bucket)
	assert.Equal(t, "data at s3://other/ is outside every storage connection's scope: add one whose scope contains it", err.Error())
	assert.NotContains(t, err.Error(), "secret-key")

	_, err = app.GetTableDataFiltered("remote", FilterSpec{Conditions: []FilterCondition{{Column: "id", Op: "eq", Value: "1"}}}, 1, 50, "", "")
	require.ErrorAs(t, err, &denied, "the filtered read is mapped too")

	// Any other failure is left alone.
	_, err = app.sess.DB().Exec(`CREATE VIEW broken AS SELECT error('boom') AS v`)
	require.NoError(t, err)
	_, err = app.GetTableData("broken", 1, 50, "", "")
	require.Error(t, err)
	assert.NotErrorAs(t, err, &denied)
}
