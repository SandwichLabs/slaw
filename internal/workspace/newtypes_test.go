package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

func resolveOne(t *testing.T, ws Workspace, name string) (ResolvedConnection, error) {
	t.Helper()
	for _, c := range ws.Connections() {
		if c.Name == name {
			return ResolveConnection(ws, c, nil)
		}
	}
	t.Fatalf("no connection %q", name)
	return ResolvedConnection{}, nil
}

func TestMetadataTrustingTypesAreOwnScopeOnly(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	for _, d := range []ConnectionDef{
		{Name: "lake", Type: "ducklake", Path: "data/lake.ducklake", Options: map[string]string{"data_path": "data/f"}},
		{Name: "ice", Type: "iceberg", Path: "data/ice"},
		{Name: "dl", Type: "delta", Path: "data/delta"},
	} {
		err := ws.AddConnection(ScopeShared, d)
		require.Error(t, err, d.Type)
		assert.Contains(t, err.Error(), "only you can add them", d.Type)
		require.NoError(t, ws.AddConnection(ScopeLocal, d), d.Type)
		ud := ConnectionDef{Name: d.Name + "_u", Type: d.Type, Path: filepath.Join(t.TempDir(), "x")}
		if d.Type == "ducklake" {
			ud.Options = map[string]string{"data_path": filepath.Join(t.TempDir(), "f")}
		}
		require.NoError(t, ws.AddConnection(ScopeUser, ud), d.Type)
		// Moving one into the team's file is refused too.
		require.Error(t, ws.MoveConnection(context.Background(), d.Name, ScopeLocal, ScopeShared), d.Type)
	}
	// A hand-edited slaw.yaml (a pull) is refused when resolved.
	_, err := ResolveConnection(ws, Connection{ConnectionDef: ConnectionDef{Name: "planted", Type: "delta", Path: "data/planted"}, Scope: ScopeShared}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only you can add them")
}

func TestDuckLakePathsResolveInsideTheWorkspace(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/lake.ducklake", Options: map[string]string{"data_path": "data/lake_files"}}))
	rc, err := resolveOne(t, ws, "lake")
	require.NoError(t, err)
	root, _ := filepath.EvalSymlinks(ws.Root())
	assert.Equal(t, filepath.Join(root, "data", "lake.ducklake"), rc.Path)
	assert.Equal(t, filepath.Join(root, "data", "lake_files"), rc.Resolved.Options["data_path"])
	def := rc.Def(true)
	assert.Equal(t, rc.Path, def.Path)
	assert.Equal(t, filepath.Join(root, "data", "lake_files"), def.Options["data_path"])
	// A relative path that leaves the workspace is refused, by path or data path.
	for name, d := range map[string]ConnectionDef{
		"dotdot path":      {Name: "a", Type: "ducklake", Path: "../x.ducklake", Options: map[string]string{"data_path": "data/f"}},
		"dotdot data path": {Name: "b", Type: "ducklake", Path: "data/b.ducklake", Options: map[string]string{"data_path": "../../f"}},
	} {
		require.NoError(t, ws.AddConnection(ScopeLocal, d), name)
		_, err := resolveOne(t, ws, d.Name)
		assert.Error(t, err, name)
	}
	// Your own scopes may name absolute paths.
	abs := t.TempDir()
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "mine", Type: "ducklake", Path: filepath.Join(abs, "m.ducklake"), Options: map[string]string{"data_path": filepath.Join(abs, "files")}}))
	rc, err = resolveOne(t, ws, "mine")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(abs, "files"), rc.Resolved.Options["data_path"])
	// A user connection belongs to no workspace: a relative data path has nothing to resolve against.
	assert.Error(t, ws.AddConnection(ScopeUser, ConnectionDef{Name: "u", Type: "ducklake", Path: filepath.Join(abs, "u.ducklake"), Options: map[string]string{"data_path": "files"}}))
}

func TestStorageURLsMustLieInsideAStorageScope(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	add := func(d ConnectionDef) { require.NoError(t, ws.AddConnection(ScopeLocal, d), d.Name) }
	add(ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "s3://bucket/lake/"}})
	add(ConnectionDef{Name: "tbl", Type: "delta", Path: "az://c/tables/sales"})
	add(ConnectionDef{Name: "ice", Type: "iceberg", Path: "data/ice"})

	_, err := resolveOne(t, ws, "lake")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside the scope")
	_, err = resolveOne(t, ws, "tbl")
	require.Error(t, err)
	rc, err := resolveOne(t, ws, "ice")
	require.NoError(t, err)
	root, _ := filepath.EvalSymlinks(ws.Root())
	assert.Equal(t, filepath.Join(root, "data", "ice"), rc.Path)

	add(ConnectionDef{Name: "store", Type: "s3", Scope: "s3://bucket/lake", Region: "us-east-1"})
	add(ConnectionDef{Name: "blob", Type: "azure", Scope: "az://c/tables", Options: map[string]string{"sign_in": "connection_string"}})
	rc, err = resolveOne(t, ws, "lake")
	require.NoError(t, err, "inside the s3 scope (a scope without a slash is a folder)")
	assert.Equal(t, "s3://bucket/lake/", rc.Resolved.Options["data_path"])
	rc, err = resolveOne(t, ws, "tbl")
	require.NoError(t, err)
	assert.Equal(t, "az://c/tables/sales", rc.Path, "a storage URL is not resolved against the workspace")

	// The same rule as the agent's URL reads (one normaliser): look-alikes,
	// traversal in every spelling, empty segments, escapes, aliases, case.
	for name, u := range map[string]string{
		"look-alike prefix":  "az://c/tables2/sales",
		"dot dot":            "az://c/tables/../secret",
		"escaped dot dot":    "az://c/tables/%2e%2e/secret",
		"escaped dot dot up": "az://c/tables/%2E%2E/secret",
		"empty segment":      "az://c/tables//x",
		"dot segment":        "az://c/tables/./x",
		"query":              "az://c/tables/x?sv=1",
		"fragment":           "az://c/tables/x#f",
		"backslash":          "az://c/tables\\x",
		"alias scheme":       "azure://c/tables/x",
		"upper case scheme":  "AZ://c/tables/x",
		"upper case bucket":  "az://C/tables/x",
		"other bucket":       "s3://elsewhere/lake/x",
	} {
		if ws.AddConnection(ScopeLocal, ConnectionDef{Name: "bad", Type: "delta", Path: u}) != nil {
			continue // refused when it is added: just as good
		}
		_, err = resolveOne(t, ws, "bad")
		assert.Error(t, err, name)
		require.NoError(t, ws.RemoveConnection(context.Background(), ScopeLocal, "bad"), name)
	}
}

func TestFingerprintCoversTheNewTypesFields(t *testing.T) {
	base := map[string]ConnectionDef{
		"mysql":    {Name: "m", Type: "mysql", Host: "h", Port: 3306, Database: "d", Options: map[string]string{"ssl_mode": "required"}},
		"azure":    {Name: "a", Type: "azure", Scope: "az://c/p/", Options: map[string]string{"sign_in": "account_key", "account_name": "acct"}},
		"ducklake": {Name: "l", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "data/f"}},
	}
	change := map[string][]func(*ConnectionDef){
		"mysql": {
			func(d *ConnectionDef) { d.Host = "other" },
			func(d *ConnectionDef) { d.Port = 3307 },
			func(d *ConnectionDef) { d.Database = "other" },
			func(d *ConnectionDef) { d.Options = map[string]string{"ssl_mode": "disabled"} },
		},
		"azure": {
			func(d *ConnectionDef) { d.Scope = "az://other/p/" },
			func(d *ConnectionDef) {
				d.Options = map[string]string{"sign_in": "connection_string", "account_name": "acct"}
			},
			func(d *ConnectionDef) {
				d.Options = map[string]string{"sign_in": "account_key", "account_name": "evil"}
			},
		},
		"ducklake": {
			func(d *ConnectionDef) { d.Path = "data/other.ducklake" },
			func(d *ConnectionDef) { d.Options = map[string]string{"data_path": "data/elsewhere"} },
		},
	}
	for typ, d := range base {
		fp := Fingerprint(d)
		assert.Equal(t, fp, Fingerprint(d), typ)
		for i, f := range change[typ] {
			c := d
			c.Options = map[string]string{}
			for k, v := range d.Options {
				c.Options[k] = v
			}
			f(&c)
			assert.NotEqual(t, fp, Fingerprint(c), "%s change %d must change the fingerprint", typ, i)
		}
	}
	assert.Equal(t, "h:3306/d", ServerDescription(base["mysql"]))
	assert.Equal(t, "h:3306/d", ServerDescription(ConnectionDef{Type: "mysql", Host: "h", Database: "d"}))
	assert.Equal(t, "az://c/p/", ServerDescription(base["azure"]))
	assert.Equal(t, "l.ducklake", ServerDescription(base["ducklake"]))
}

// A pull can put anything in slaw.yaml. A definition the person may not have
// (a source that trusts its metadata, or one with fields its type does not use,
// such as a smuggled scope) is skipped with a visible issue; the workspace still
// opens and the other connections work.
func TestPlantedSharedDefinitionsAreSkippedWithAnIssue(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	cfg := ws.Config()
	cfg.Connections = append(cfg.Connections,
		ConnectionDef{Name: "planted_delta", Type: "delta", Path: "data/planted", Scope: "file:///"},
		ConnectionDef{Name: "planted_lake", Type: "ducklake", Path: "data/p.ducklake", Options: map[string]string{"data_path": "data/pf"}},
		ConnectionDef{Name: "stray_sqlite", Type: "sqlite", Path: "data/x.sqlite", Scope: "file:///"},
		ConnectionDef{Name: "fine", Type: "duckdb", Path: "data/fine.duckdb", ReadOnly: Bool(false)},
	)
	require.NoError(t, ws.SaveConfig(cfg), "slaw.yaml itself stays readable")

	sess, issues, err := OpenSession(context.Background(), ws, nil, WithSessionOptions(session.WithExtensionDirectory(t.TempDir())))
	require.NoError(t, err)
	defer sess.Close()
	byName := map[string]string{}
	for _, i := range issues {
		byName[i.Connection] = i.Message
	}
	require.Contains(t, byName, "planted_delta")
	require.Contains(t, byName, "planted_lake")
	require.Contains(t, byName, "stray_sqlite")
	assert.Contains(t, byName["planted_delta"], "ask the person who committed it to move it to their own connections")
	assert.Contains(t, byName["planted_lake"], "only you can add them")
	assert.Contains(t, byName["stray_sqlite"], "not a setting of this type")
	assert.NotContains(t, byName, "fine")
	infos := map[string]bool{}
	for _, ci := range sess.ConnectionInfos() {
		infos[ci.Name] = true
		assert.Empty(t, ci.Scope, ci.Name)
	}
	assert.True(t, infos["fine"], "the other connections work")
	assert.True(t, infos["local"])
	assert.False(t, infos["planted_delta"] || infos["planted_lake"] || infos["stray_sqlite"], "the refused ones are not registered")
	_, err = sess.Query(context.Background(), "SELECT 1")
	require.NoError(t, err)
}

func TestAgentAccessProtectsConnectionFiles(t *testing.T) {
	ws, _, _ := memWorkspace(t, nil)
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "lake", Type: "ducklake", Path: "data/cat.bin", Options: map[string]string{"data_path": "data/lake_files"}}))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "store", Type: "s3", Scope: "s3://b/", Region: "us-east-1"}))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "remote_lake", Type: "ducklake", Path: "data/r.ducklake", Options: map[string]string{"data_path": "s3://b/x/"}}))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "sqlake", Type: "ducklake", Path: "data/cat.sqlite", Options: map[string]string{"catalog": "sqlite", "data_path": "data/sq"}}))
	require.NoError(t, ws.AddConnection(ScopeLocal, ConnectionDef{Name: "urllake", Type: "ducklake", Path: "s3://b/x/cat.db", ReadOnly: boolPtr(true), Options: map[string]string{"catalog": "sqlite", "data_path": "s3://b/x/"}}))
	root, _ := filepath.EvalSymlinks(ws.Root())
	got := NewAgentAccess(ws, nil).ProtectedPaths()
	assert.Contains(t, got, filepath.Join(root, "data", "cat.sqlite"), "a local SQLite catalog is protected")
	assert.Contains(t, got, filepath.Join(root, "data", "cat.bin"))
	assert.Contains(t, got, filepath.Join(root, "data", "lake_files"))
	assert.Contains(t, got, filepath.Join(root, "data", "r.ducklake"))
	for _, p := range got {
		assert.NotContains(t, p, "s3://")
	}
}
