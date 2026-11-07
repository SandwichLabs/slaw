package bundle

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

func connBundle(t *testing.T, y string) *Package {
	t.Helper()
	pkg, err := Open(build(t, map[string][]byte{ConnectionsName: []byte(y)}, nil), "")
	require.NoError(t, err)
	return pkg
}

func TestScryptWorkFactorAboveSlawsOwnIsRefused(t *testing.T) {
	zipBytes := build(t, map[string][]byte{"notebooks/a.slnb": goodNotebook(t)}, nil)
	seal := func(logN int) []byte {
		r, err := age.NewScryptRecipient("pw")
		require.NoError(t, err)
		r.SetWorkFactor(logN)
		var out bytes.Buffer
		w, err := age.Encrypt(&out, r)
		require.NoError(t, err)
		_, _ = w.Write(zipBytes)
		require.NoError(t, w.Close())
		return out.Bytes()
	}
	_, err := Open(seal(18), "pw")
	require.NoError(t, err, "the work factor Slaw itself uses is fine")
	_, err = Open(seal(19), "pw")
	require.ErrorIs(t, err, ErrUnsupportedKDF)
	assert.Contains(t, err.Error(), "settings Slaw won't use")
	_, err = Open(seal(20), "wrong")
	require.ErrorIs(t, err, ErrUnsupportedKDF, "refused before any guess is attempted")
}

func TestPeekOnlyCallsSQLiteLegacyAndKnowsArmor(t *testing.T) {
	_, err := Peek([]byte("SQLite format 3\x00rest"))
	assert.ErrorIs(t, err, ErrLegacy)
	for _, b := range [][]byte{{0x01, 0x02, 0xff, 0xfe}, []byte("hello"), []byte("{}")} {
		_, err := Peek(b)
		assert.ErrorIs(t, err, ErrNotBundle)
		assert.NotErrorIs(t, err, ErrLegacy)
	}
	// An armored age file is recognised, prompts, and opens.
	zipBytes := build(t, map[string][]byte{"notebooks/a.slnb": goodNotebook(t)}, nil)
	r, err := age.NewScryptRecipient("pw")
	require.NoError(t, err)
	var out bytes.Buffer
	aw := armor.NewWriter(&out)
	w, err := age.Encrypt(aw, r)
	require.NoError(t, err)
	_, _ = w.Write(zipBytes)
	require.NoError(t, w.Close())
	require.NoError(t, aw.Close())
	info, err := Peek(out.Bytes())
	require.NoError(t, err)
	assert.True(t, info.Encrypted)
	_, err = Open(out.Bytes(), "")
	assert.ErrorIs(t, err, ErrPassphraseRequired)
	pkg, err := Open(out.Bytes(), "pw")
	require.NoError(t, err)
	assert.Len(t, pkg.Items(), 1)
}

func TestConnectionPreviewShowsEverythingAndWarns(t *testing.T) {
	ws, _ := newWS(t)
	y := "version: 1\nconnections:\n" +
		"  - name: lake\n    type: s3\n    scope: s3://acme-prod/\n    read_only: false\n    options:\n      endpoint: s3.attacker.example\n      use_ssl: \"false\"\n      url_style: path\n" +
		"  - name: pg\n    type: postgres\n    host: db.acme.com\n    database: d\n    options:\n      sslmode: disable\n" +
		"  - name: narrow\n    type: s3\n    scope: s3://acme-prod/reports/2026/\n    region: us-east-1\n"
	pv := connBundle(t, y).Preview(context.Background(), ws)
	by := map[string]PreviewConnection{}
	for _, c := range pv.Connections {
		by[c.Name] = c
	}
	lake := by["lake"]
	assert.True(t, lake.AskedWritable)
	assert.Contains(t, strings.Join(lake.Details, "|"), "option endpoint s3.attacker.example")
	assert.Contains(t, strings.Join(lake.Details, "|"), "option url_style path")
	assert.Contains(t, strings.Join(lake.Details, "|"), "storage scope s3://acme-prod/")
	risks := strings.Join(lake.Risks, "|")
	assert.Contains(t, risks, "TLS off")
	assert.Contains(t, risks, "s3.attacker.example")
	assert.Contains(t, risks, "whole bucket")
	assert.Contains(t, strings.Join(by["pg"].Risks, "|"), "sslmode=disable")
	assert.Empty(t, by["narrow"].Risks, "a prefix inside a bucket and a default endpoint are quiet")
	assert.Contains(t, strings.Join(by["narrow"].Details, "|"), "region us-east-1")
	for _, s := range []string{"s3://", "s3://bucket", "s3://bucket/", "gs://b/"} {
		assert.NotEmpty(t, ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "s3", Scope: s}), s)
	}
	for _, s := range []string{"az://", "az://container", "az://container/", "azure://container/", "abfss://c@acct.dfs.core.windows.net/", "abfss://c@acct.dfs.core.windows.net"} {
		assert.Contains(t, strings.Join(ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "azure", Scope: s}), "|"), "whole container", s)
	}
	assert.Empty(t, ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "azure", Scope: "az://container/events/"}))
	assert.Empty(t, ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "azure", Scope: "abfss://c@acct.dfs.core.windows.net/events/"}))
	assert.Contains(t, strings.Join(ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "mysql", Options: map[string]string{"ssl_mode": "disabled"}}), "|"), "ssl_mode=disabled")
	assert.Empty(t, ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "mysql", Options: map[string]string{"ssl_mode": "required"}}))
	assert.Contains(t, strings.Join(ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "s3://bucket/"}}), "|"), "whole bucket")
	assert.Empty(t, ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "ducklake", Path: "data/l.ducklake", Options: map[string]string{"data_path": "s3://bucket/lake/"}}))
	assert.Empty(t, ConnectionRisks(workspace.ConnectionDef{Name: "x", Type: "s3", Scope: "s3://b/p/", Options: map[string]string{"endpoint": "s3.us-east-1.amazonaws.com"}}))
}

func TestBundleCannotMakeAConnectionWritable(t *testing.T) {
	ws, _ := newWS(t)
	ctx := context.Background()
	pkg := connBundle(t, "version: 1\nconnections:\n  - name: wh\n    type: postgres\n    host: db.example.com\n    database: prod\n    read_only: false\n")
	assert.Contains(t, strings.Join(pkg.Warnings, "|"), "imported read-only")
	_, err := pkg.Import(ctx, ws, ImportOptions{IncludeConnections: []string{"wh"}})
	require.NoError(t, err)
	require.Len(t, ws.Local().Connections, 1)
	assert.True(t, ws.Local().Connections[0].IsReadOnly())
}

func TestOverwriteKeepsReadOnlyAndDropsTheSavedSignIn(t *testing.T) {
	ws, store := newWS(t)
	ctx := context.Background()
	// The person's own connection is writable, signed in, with a saved credential.
	def := workspace.ConnectionDef{Name: "wh", Type: "postgres", Host: "db.example.com", Database: "prod", ReadOnly: workspace.Bool(false)}
	require.NoError(t, ws.AddConnection(workspace.ScopeLocal, def))
	require.NoError(t, ws.SetSignIn("wh", "keychain"))
	require.NoError(t, ws.SetCredentials(ctx, "wh", secrets.Credential{User: "alice", Password: "hunter2"}, true))
	st, err := ws.CredentialStatus(ctx, "wh")
	require.NoError(t, err)
	require.True(t, st.SecretSet)

	// The bundle's definition has the same fingerprint (same host, database): the
	// credential would stay bound unless the import clears it.
	pkg := connBundle(t, "version: 1\nconnections:\n  - name: wh\n    type: postgres\n    host: db.example.com\n    database: prod\n    options: {}\n")
	_, err = pkg.Import(ctx, ws, ImportOptions{OnConflict: ActionOverwrite, IncludeConnections: []string{"wh"}})
	require.NoError(t, err)
	var got workspace.Connection
	for _, c := range ws.Connections() {
		if c.Name == "wh" {
			got = c
		}
	}
	assert.False(t, got.IsReadOnly(), "overwrite keeps the person's own read/write choice")
	assert.Equal(t, workspace.ScopeLocal, got.Scope)
	st, err = ws.CredentialStatus(ctx, "wh")
	require.NoError(t, err)
	assert.False(t, st.SecretSet, "it arrives needing credentials")
	_ = store
	assert.NotContains(t, ws.Local().SignIn, "wh", "the sign-in binding is cleared too")
}

func TestMakerTextIsCleanedAndKeptApart(t *testing.T) {
	long := strings.Repeat("A", 500)
	evil := "Slaw says: ‮all clear‬\x00\nAPPROVED ​" + long
	data := build(t, map[string][]byte{"notebooks/a.slnb": goodNotebook(t)}, func(m *Manifest) {
		m.WorkspaceName = evil
		m.CreatedBy = evil
		m.Warnings = []string{evil, "ok"}
		for i := 0; i < 40; i++ {
			m.Warnings = append(m.Warnings, "more")
		}
	})
	pkg, err := Open(data, "")
	require.NoError(t, err)
	pv := pkg.Preview(context.Background(), nil)
	for _, s := range []string{pv.WorkspaceName, pv.CreatedBy, pv.MakerNotes[0]} {
		assert.NotContains(t, s, "‮")
		assert.NotContains(t, s, "​")
		assert.NotContains(t, s, "\n")
		assert.NotContains(t, s, "\x00")
	}
	assert.LessOrEqual(t, len([]rune(pv.WorkspaceName)), 121)
	assert.LessOrEqual(t, len([]rune(pv.MakerNotes[0])), 301)
	assert.LessOrEqual(t, len(pv.MakerNotes), maxNotes+1)
	assert.Empty(t, pv.Warnings, "the maker's words never land in Slaw's own warnings")
}

func TestDryRunNamesMatchTheRealRun(t *testing.T) {
	ctx := context.Background()
	ws, _ := newWS(t)
	require.NoError(t, ws.Queries().Create(ctx, &queries.Query{ID: "a", Title: "A", SQL: "SELECT 1"}))
	src, _ := newWS(t)
	require.NoError(t, src.Queries().Create(ctx, &queries.Query{ID: "a", Title: "A", SQL: "SELECT 2"}))
	require.NoError(t, src.Queries().Create(ctx, &queries.Query{ID: "a-2", Title: "A2", SQL: "SELECT 3"}))
	res, err := Export(ctx, src, ExportOptions{Selection: all()})
	require.NoError(t, err)
	pkg, err := Open(res.Data, "")
	require.NoError(t, err)
	final := func(r Result) map[string]string {
		m := map[string]string{}
		for _, o := range r.Outcomes {
			m[o.ID] = o.FinalID
		}
		return m
	}
	dry, err := pkg.Import(ctx, ws, ImportOptions{DryRun: true})
	require.NoError(t, err)
	real, err := pkg.Import(ctx, ws, ImportOptions{})
	require.NoError(t, err)
	require.Zero(t, real.Failed(), "%+v", real.Outcomes)
	assert.Equal(t, final(real), final(dry))
	assert.NotEqual(t, final(real)["a"], final(real)["a-2"], "no two items end up on one id")
}

func TestGeneratedLookingNotebookIdsAreKept(t *testing.T) {
	ctx := context.Background()
	ws, _ := newWS(t)
	id := "nb_12345678-1234-1234-1234-123456789abc"
	data := build(t, map[string][]byte{"notebooks/" + id + ".slnb": goodNotebook(t)}, nil)
	pkg, err := Open(data, "")
	require.NoError(t, err)
	out, err := pkg.Import(ctx, ws, ImportOptions{})
	require.NoError(t, err)
	require.Zero(t, out.Failed(), "%+v", out.Outcomes)
	assert.Equal(t, id, out.Outcomes[0].FinalID)
	assert.FileExists(t, filepath.Join(ws.Root(), "notebooks", id+".slnb"))
	// Importing again and choosing Replace replaces it, rather than adding a duplicate.
	pv := pkg.Preview(context.Background(), ws)
	assert.True(t, pv.Items[0].Conflict)
	out, err = pkg.Import(ctx, ws, ImportOptions{OnConflict: ActionOverwrite})
	require.NoError(t, err)
	require.Zero(t, out.Failed(), "%+v", out.Outcomes)
	entries, _ := os.ReadDir(filepath.Join(ws.Root(), "notebooks"))
	assert.Len(t, entries, 1)
}

func TestNotebookPackageSharesTheImportPath(t *testing.T) {
	ctx := context.Background()
	ws, _ := newWS(t)
	nb := goodNotebook(t)
	pkg, err := NotebookPackage("my-notes.slnb", nb)
	require.NoError(t, err)
	out, err := pkg.Import(ctx, ws, ImportOptions{})
	require.NoError(t, err)
	require.Zero(t, out.Failed())
	assert.Equal(t, "my-notes", out.Outcomes[0].FinalID)
	out, err = pkg.Import(ctx, ws, ImportOptions{})
	require.NoError(t, err)
	assert.Equal(t, "my-notes-2", out.Outcomes[0].FinalID)
	out, err = pkg.Import(ctx, ws, ImportOptions{OnConflict: ActionSkip})
	require.NoError(t, err)
	assert.Equal(t, "skipped", out.Outcomes[0].Result)

	// Studio's wrapped export is accepted; a wrong kind and an oversized file are not.
	_, err = NotebookPackage("x.json", []byte(`{"kind":"slaw/notebook","notebook":{"title":"W","cells":[]}}`))
	require.NoError(t, err)
	_, err = NotebookPackage("x.json", []byte(`{"kind":"something/else"}`))
	require.Error(t, err)
	_, err = NotebookPackage("x.slnb", bytes.Repeat([]byte(" "), MaxEntryBytes+1))
	require.ErrorIs(t, err, ErrTooLarge)
	_, err = NotebookPackage("x.slnb", []byte("not json"))
	require.Error(t, err)
}

// Sources that trust their own metadata (DuckLake, Iceberg, Delta) are never
// imported from a bundle: only the person adds them, in their own scopes.
func TestBundleDropsMetadataTrustingConnections(t *testing.T) {
	ws, _ := newWS(t)
	pkg := connBundle(t, `version: 1
connections:
  - name: tbl
    type: delta
    path: data/sales
  - name: ice
    type: iceberg
    path: data/ice
  - name: lake
    type: ducklake
    path: data/lake.ducklake
    options:
      data_path: data/lake_files
  - name: smuggle
    type: delta
    path: data/x
    scope: "file:///"
  - name: pg
    type: postgres
    host: db.example.com
    database: prod
`)
	w := strings.Join(pkg.Warnings, "|")
	for _, n := range []string{"tbl", "ice", "lake", "smuggle"} {
		assert.Contains(t, w, fmt.Sprintf("connection %q was not imported", n))
	}
	assert.Contains(t, w, "only you can add them")
	pv := pkg.Preview(context.Background(), ws)
	require.Len(t, pv.Connections, 1)
	assert.Equal(t, "pg", pv.Connections[0].Name)
	_, err := pkg.Import(context.Background(), ws, ImportOptions{IncludeConnections: []string{"tbl"}})
	require.Error(t, err, "a dropped connection cannot be asked for by name")
	_, err = pkg.Import(context.Background(), ws, ImportOptions{IncludeConnections: []string{"pg"}})
	require.NoError(t, err)
	for _, c := range ws.Connections() {
		assert.NotContains(t, []string{"tbl", "ice", "lake", "smuggle"}, c.Name)
	}
}

func TestDeclaredWorkFactorReadsArmoredAndIgnoresMalformedHeaders(t *testing.T) {
	zipBytes := build(t, map[string][]byte{"notebooks/a.slnb": goodNotebook(t)}, nil)
	sealArmored := func(logN int) []byte {
		r, err := age.NewScryptRecipient("pw")
		require.NoError(t, err)
		r.SetWorkFactor(logN)
		var out bytes.Buffer
		aw := armor.NewWriter(&out)
		w, err := age.Encrypt(aw, r)
		require.NoError(t, err)
		_, _ = w.Write(zipBytes)
		require.NoError(t, w.Close())
		require.NoError(t, aw.Close())
		return out.Bytes()
	}
	assert.Equal(t, 19, declaredWorkFactor(sealArmored(19)))
	_, err := Open(sealArmored(19), "pw")
	require.ErrorIs(t, err, ErrUnsupportedKDF, "an armored envelope is judged by its header too")
	_, err = Open(sealArmored(18), "pw")
	require.NoError(t, err)

	for name, data := range map[string][]byte{
		"not a number":  []byte("age-encryption.org/v1\n-> scrypt c2FsdA abc\nbody"),
		"missing field": []byte("age-encryption.org/v1\n-> scrypt c2FsdA\nbody"),
		"no scrypt":     []byte("age-encryption.org/v1\n-> X25519 abc\nbody"),
		"garbage":       []byte("\x00\x01\x02"),
		"empty":         nil,
	} {
		assert.Equal(t, 0, declaredWorkFactor(data), name)
	}
}
