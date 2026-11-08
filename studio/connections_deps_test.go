package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/workspace"
)

// A lake follows the storage connection it was resolved against: when that
// connection goes away the lake is registered again, so its scope check runs
// against what is left (an issue, not a stale spec that passed the old check).
func TestSyncReregistersDependentsOfAChangedStorageConnection(t *testing.T) {
	app, _ := connApp(t)
	ro := true
	f := &ro
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "store", Type: "s3", Scope: "s3://bucket/", Region: "us-east-1"}))
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "lake", Type: "ducklake", Path: "s3://bucket/lake/catalog.db", ReadOnly: f,
		Options: map[string]string{"catalog": "sqlite", "data_path": "s3://bucket/lake/data/"}}))
	app.syncConnections()
	app.connSigMu.Lock()
	assert.Equal(t, []string{"store"}, app.connDeps["lake"].Requires)
	app.connSigMu.Unlock()
	assert.Empty(t, app.issues)

	// Only the storage connection changes; the lake's own definition does not.
	require.NoError(t, app.ws.RemoveConnection(context.Background(), workspace.ScopeLocal, "store"))
	app.syncConnections()
	require.Len(t, app.issues, 1)
	assert.Equal(t, "lake", app.issues[0].Connection)
	assert.Contains(t, app.issues[0].Message, "not inside the scope")

	// Adding a storage connection again repairs the unresolved lake.
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "store2", Type: "s3", Scope: "s3://bucket/", Region: "us-east-1"}))
	app.syncConnections()
	assert.Empty(t, app.issues)
	app.connSigMu.Lock()
	assert.Equal(t, []string{"store2"}, app.connDeps["lake"].Requires)
	app.connSigMu.Unlock()
}

func TestDependentsOfIgnoresUnrelatedChanges(t *testing.T) {
	cur := map[string]workspace.Connection{"a": {}, "b": {}, "c": {}}
	old := map[string]connDeps{"a": {Requires: []string{"s1"}}, "b": {Requires: []string{"s2"}}, "c": {Unresolved: true}}
	got := dependentsOf(cur, map[string]string{"s1": "s3"}, old, old)
	assert.Equal(t, map[string]bool{"a": true, "c": true}, got)
	got = dependentsOf(cur, map[string]string{"s9": "postgres"}, old, old)
	assert.Empty(t, got)
}

// A table source and the storage connection that reads it, added together: the
// table must not be preflighted before its storage connection is registered
// (map order in a sync, name order at open).
func TestSyncRegistersBeforeItPreflights(t *testing.T) {
	app, _ := connApp(t)
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "events", Type: "delta", Path: "s3://bucket/t/"}))
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "warehouse", Type: "s3", Scope: "s3://bucket/", Region: "us-east-1"}))
	app.syncConnections()
	_, sess, _, err := app.snapshot()
	require.NoError(t, err)
	st, ok := sess.Status("events")
	require.True(t, ok)
	assert.NotContains(t, st.Message, "not defined")
	assert.Empty(t, app.issues)
}

// An s3_tables iceberg_catalog follows the s3 connection that signs it: removing
// that connection makes the catalog an issue, and adding one again repairs it,
// with no change to the catalog's own definition.
func TestSyncReregistersAnS3TablesCatalogWhenItsStorageConnectionChanges(t *testing.T) {
	app, _ := connApp(t)
	ctx := context.Background()
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "aws_lake", Type: "s3", Scope: "s3://lake/", Region: "us-east-1"}))
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "s3t", Type: "iceberg_catalog", ReadOnly: workspace.Bool(true),
		Options: map[string]string{"endpoint_type": "s3_tables", "warehouse": "arn:aws:s3tables:us-east-1:111122223333:bucket/sales", "storage": "aws_lake"}}))
	app.syncConnections()
	app.connSigMu.Lock()
	assert.Equal(t, []string{"aws_lake"}, app.connDeps["s3t"].Requires)
	app.connSigMu.Unlock()
	assert.Empty(t, app.issues)

	require.NoError(t, app.ws.RemoveConnection(ctx, workspace.ScopeLocal, "aws_lake"))
	app.syncConnections()
	require.Len(t, app.issues, 1)
	assert.Equal(t, "s3t", app.issues[0].Connection)
	assert.Contains(t, app.issues[0].Message, "aws_lake")

	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "aws_lake", Type: "s3", Scope: "s3://lake/", Region: "us-east-1"}))
	app.syncConnections()
	assert.Empty(t, app.issues)
	app.connSigMu.Lock()
	assert.Equal(t, []string{"aws_lake"}, app.connDeps["s3t"].Requires)
	app.connSigMu.Unlock()
}

// A catalog waiting on its signed-out storage connection says so in the rail's
// data (blockedBy) and carries no sign-in hint of its own, so the prompt and the
// sign-in banner point at the storage connection.
func TestConnectionInfoNamesTheStorageConnectionAHeldCatalogWaitsOn(t *testing.T) {
	app, _ := connApp(t)
	t.Setenv("L3_NOPE_KEY_ID", "")
	t.Setenv("L3_NOPE_SECRET", "")
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "aws_lake", Type: "s3", Scope: "s3://lake/", Region: "us-east-1"}))
	require.NoError(t, app.ws.SetSignIn("aws_lake", "env:L3_NOPE"))
	require.NoError(t, app.ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "s3t", Type: "iceberg_catalog", ReadOnly: workspace.Bool(true),
		Options: map[string]string{"endpoint_type": "s3_tables", "warehouse": "arn:aws:s3tables:us-east-1:111122223333:bucket/sales", "storage": "aws_lake"}}))
	app.syncConnections()
	_, sess, _, err := app.snapshot()
	require.NoError(t, err)
	require.Error(t, sess.Connect(context.Background(), "s3t"))

	app.mu.RLock()
	infos := app.connectionInfosLocked()
	app.mu.RUnlock()
	by := map[string]ConnectionInfo{}
	for _, ci := range infos {
		by[ci.Name] = ci
	}
	assert.Equal(t, "needs-credentials", by["s3t"].Status)
	assert.Equal(t, "aws_lake", by["s3t"].BlockedBy)
	assert.Nil(t, by["s3t"].SignInNeeded)
	assert.Empty(t, by["aws_lake"].BlockedBy)
}
