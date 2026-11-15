package bundle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withUnknown builds a bundle holding one good notebook plus the given extra
// entries, each listed in the manifest under the given kind.
func withUnknown(t *testing.T, extra map[string]ItemKind, bodies map[string][]byte) []byte {
	t.Helper()
	entries := map[string][]byte{"notebooks/ok.slnb": goodNotebook(t)}
	for name := range extra {
		entries[name] = bodies[name]
	}
	return build(t, entries, func(m *Manifest) {
		for name, kind := range extra {
			sum := sha256.Sum256(bodies[name])
			m.Contents = append(m.Contents, Entry{Path: name, Kind: kind, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bodies[name]))})
		}
	})
}

func TestUnknownKindsAreSkippedWithAWarning(t *testing.T) {
	bodies := map[string][]byte{"foo/a.yaml": []byte("a: 1\n"), "dashboards/b.json": []byte("{}")}
	data := withUnknown(t, map[string]ItemKind{"foo/a.yaml": "foo", "dashboards/b.json": "dashboard"}, bodies)
	pkg, err := Open(data, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"dashboard", "foo"}, pkg.UnsupportedKinds)
	require.Len(t, pkg.Items(), 1, "the known notebook still imports")

	want := "This bundle has entries this version of Slaw doesn't know (dashboard, foo). They were skipped; update Slaw to import them."
	pv := pkg.Preview(context.Background(), nil)
	assert.Contains(t, pv.Warnings, want)

	ws, _ := newWS(t)
	res, err := pkg.Import(context.Background(), ws, ImportOptions{OnConflict: ActionRename})
	require.NoError(t, err)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, "created", res.Outcomes[0].Result)
	assert.Contains(t, res.Warnings, want)
}

func TestUnknownKindStillMustBeSafeAndMatch(t *testing.T) {
	t.Run("traversal in an unknown kind", func(t *testing.T) {
		for _, name := range []string{"foo/../../etc/passwd", "/etc/passwd", `foo\x.yaml`, "foo/./x.yaml"} {
			body := []byte("x")
			data := build(t, map[string][]byte{name: body}, func(m *Manifest) {
				sum := sha256.Sum256(body)
				m.Contents = append(m.Contents, Entry{Path: name, Kind: "foo", SHA256: hex.EncodeToString(sum[:]), Size: 1})
			})
			_, err := Open(data, "")
			require.ErrorIs(t, err, ErrCorrupt, name)
		}
	})
	t.Run("unknown path not in the manifest", func(t *testing.T) {
		_, err := Open(build(t, map[string][]byte{"foo/a.yaml": []byte("x")}, nil), "")
		require.ErrorIs(t, err, ErrCorrupt)
	})
	t.Run("known kind on a path it does not own", func(t *testing.T) {
		body := []byte("x")
		data := build(t, map[string][]byte{"foo/a.yaml": body}, func(m *Manifest) {
			sum := sha256.Sum256(body)
			m.Contents = append(m.Contents, Entry{Path: "foo/a.yaml", Kind: KindNotebook, ID: "a", SHA256: hex.EncodeToString(sum[:]), Size: 1})
		})
		_, err := Open(data, "")
		require.ErrorIs(t, err, ErrCorrupt)
	})
	t.Run("unknown kind inside a known kind's folder is refused", func(t *testing.T) {
		for _, name := range []string{"notebooks/BAD ID.slnb", "queries/x.txt", "skills/a/run.sh", "semantics/other/x.yaml", "watches/x.txt", "connections.yaml/x"} {
			data := withUnknown(t, map[string]ItemKind{name: "foo"}, map[string][]byte{name: []byte("x")})
			_, err := Open(data, "")
			require.ErrorIs(t, err, ErrCorrupt, name)
		}
	})
	t.Run("unknown kind under artifacts stays tolerated", func(t *testing.T) {
		data := withUnknown(t, map[string]ItemKind{"artifacts/x/extra.txt": "foo"}, map[string][]byte{"artifacts/x/extra.txt": []byte("x")})
		pkg, err := Open(data, "")
		require.NoError(t, err)
		assert.Equal(t, []string{"foo"}, pkg.UnsupportedKinds)
	})
	t.Run("odd kind names are refused", func(t *testing.T) {
		for _, k := range []ItemKind{"", "Foo", "foo bar", "../x", ItemKind(fmt.Sprintf("a%040d", 1))} {
			data := withUnknown(t, map[string]ItemKind{"foo/a.yaml": k}, map[string][]byte{"foo/a.yaml": []byte("x")})
			_, err := Open(data, "")
			require.ErrorIs(t, err, ErrCorrupt, string(k))
		}
	})
	t.Run("checksum is still verified", func(t *testing.T) {
		data := withUnknown(t, map[string]ItemKind{"foo/a.yaml": "foo"}, map[string][]byte{"foo/a.yaml": []byte("x")})
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		require.NoError(t, err)
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, f := range zr.File {
			rc, _ := f.Open()
			var b bytes.Buffer
			_, _ = b.ReadFrom(rc)
			rc.Close()
			body := b.Bytes()
			if f.Name == "foo/a.yaml" {
				body = []byte("y")
			}
			w, _ := zw.Create(f.Name)
			_, _ = w.Write(body)
		}
		require.NoError(t, zw.Close())
		_, err = Open(buf.Bytes(), "")
		require.ErrorIs(t, err, ErrCorrupt)
	})
	t.Run("size cap still applies", func(t *testing.T) {
		big := bytes.Repeat([]byte{' '}, MaxEntryBytes+1)
		data := withUnknown(t, map[string]ItemKind{"foo/a.yaml": "foo"}, map[string][]byte{"foo/a.yaml": big})
		_, err := Open(data, "")
		require.ErrorIs(t, err, ErrTooLarge)
	})
}

func TestMalformedKnownEntryAlongsideUnknownIsStillAnError(t *testing.T) {
	entries := map[string][]byte{"notebooks/bad.slnb": []byte("not json"), "foo/a.yaml": []byte("x")}
	data := build(t, entries, func(m *Manifest) {
		sum := sha256.Sum256([]byte("x"))
		m.Contents = append(m.Contents, Entry{Path: "foo/a.yaml", Kind: "foo", SHA256: hex.EncodeToString(sum[:]), Size: 1})
	})
	_, err := Open(data, "")
	require.ErrorIs(t, err, ErrCorrupt)
}
