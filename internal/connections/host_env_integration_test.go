//go:build integration

package connections_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// #135: the host's AWS_* variables, even when they are valid for the very
// MinIO the workspace uses, never read a URL that no connection of the
// workspace covers; a covered URL is still read with the connection's own keys.
func TestHostAWSEnvDoesNotReadAnUncoveredURL(t *testing.T) {
	ep := startMinIO(t)
	ctx := context.Background()
	opts := map[string]string{"endpoint": ep, "url_style": "path", "use_ssl": "false"}
	own := secrets.Credential{KeyID: "minioadmin", Secret: minioSecret}

	ws, _ := memWS(t)
	for _, c := range []workspace.ConnectionDef{
		{Name: "raw", Type: "s3", Scope: "s3://lake/raw/", Region: "us-east-1", Options: opts},
		{Name: "other", Type: "s3", Scope: "s3://lake/other/", Region: "us-east-1", Options: opts},
	} {
		require.NoError(t, ws.AddConnection(workspace.ScopeShared, c))
		require.NoError(t, ws.SetCredentials(ctx, c.Name, own, false))
	}

	// The host environment holds valid keys for the same server.
	t.Setenv("AWS_ACCESS_KEY_ID", "minioadmin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", minioSecret)
	t.Setenv("AWS_SESSION_TOKEN", "hostSessionToken")
	t.Setenv("AWS_REGION", "us-east-1")

	sess, issues, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck
	require.Empty(t, issues)
	eng := engine.NewManager(sess)

	for _, u := range []string{"raw/a", "other/b"} {
		_, err = eng.Execute(ctx, "COPY (SELECT 1 AS x UNION ALL SELECT 2) TO 's3://lake/"+u+".parquet'", nil)
		require.NoError(t, err, u)
	}
	count := func(u string) (any, error) {
		rs, err := eng.Execute(ctx, "SELECT count(*) FROM read_parquet('s3://lake/"+u+".parquet')", nil)
		if err != nil {
			return nil, err
		}
		return rs.Rows[0][0], nil
	}
	n, err := count("raw/a")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "a covered URL is read with the connection's own keys")

	// Drop the second connection's secret: its URL is now covered by nothing.
	require.NoError(t, ws.ClearCredentials(ctx, "other"))
	sess.Preflight(ctx, "other")
	// Before the read: the deny secret must already be the match, so a
	// regression fails here and never signs a request with the host's keys.
	var matched string
	require.NoError(t, sess.DB().QueryRowContext(ctx, "SELECT name FROM which_secret('s3://lake/other/b.parquet', 's3')").Scan(&matched))
	require.Equal(t, "slaw-deny-s3", matched)
	_, err = count("other/b")
	require.Error(t, err, "an uncovered URL must not be read with the host's keys")
	var denied *session.StorageDenyError
	if !errors.As(err, &denied) {
		require.ErrorAs(t, session.MapStorageDenyError(err), &denied, "not a read refused by a deny secret: %v", err)
	}
	assert.Equal(t, "lake", denied.Bucket)
	assert.NotContains(t, err.Error(), minioSecret)

	// The covered one is unaffected.
	n, err = count("raw/a")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
}
