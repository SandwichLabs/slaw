//go:build integration

package icebergtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// paddedPath returns dir + name + ".parquet" with exactly n characters. The
// name starts with a letter that is unique per index (at most 26 files).
func paddedPath(t testing.TB, dir string, idx, n int) string {
	t.Helper()
	const ext = ".parquet"
	room := n - len(dir) - len(ext)
	require.GreaterOrEqual(t, room, 1,
		"data dir %q is too long: the manifest path is %d characters, so the dir may be at most %d (use a shorter temp dir)",
		dir, n, n-len(ext)-1)
	name := strings.Repeat("p", room)
	return dir + string(rune('a'+idx)) + name[1:] + ext
}

// Craft registers ns.<name> as a copy of the seeded ns.orders whose manifest,
// manifest list or location is hostile as h says. The seeded table is not
// touched. name must be 6 characters (the length of "orders"). With an empty
// DataDir the data paths still name the seeded table's files; only the
// metadata (manifest list, manifests, metadata.json) is copied. Craft verifies
// its rewrites twice, in bytes before upload and through iceberg_metadata
// after registering, and fails the test if either disagrees with DataPaths.
func (f *Fixture) Craft(t testing.TB, name string, h Hostile) *Table {
	t.Helper()
	require.Len(t, name, len(craftSource), "crafted table names are 6 characters, like %q", craftSource)
	db := f.shared(t)

	var load struct {
		MetadataLocation string `json:"metadata-location"`
	}
	require.NoError(t, json.Unmarshal(f.restDo(t, http.MethodGet, "/v1/namespaces/"+Namespace+"/tables/"+craftSource, nil), &load))
	// Only the metadata directory is renamed; data paths are rewritten (or not)
	// by DataDir alone.
	oldMeta := "/" + Namespace + "/" + craftSource + "/metadata/"
	newMeta := "/" + Namespace + "/" + name + "/metadata/"
	metaBytes := f.ReadObject(t, db, load.MetadataLocation)

	dataFiles := f.List(t, db, s3url(Warehouse, Namespace+"/"+craftSource+"/data/*.parquet"))
	require.NotEmpty(t, dataFiles, "seeded ns.orders has no data files; call Seed first")
	require.Less(t, len(dataFiles), 26, "too many data files to give each a unique padded name")
	tbl := &Table{Name: name}
	for i, orig := range dataFiles {
		np := orig
		if h.DataDir != "" {
			np = paddedPath(t, h.DataDir, i, len(orig))
		}
		tbl.DataPaths = append(tbl.DataPaths, np)
		tbl.DataSizes = append(tbl.DataSizes, f.ObjectSize(t, db, orig))
	}
	rewrite := func(b []byte) []byte {
		for i, orig := range dataFiles {
			b = bytes.ReplaceAll(b, []byte(orig), []byte(tbl.DataPaths[i]))
		}
		b = bytes.ReplaceAll(b, []byte(oldMeta), []byte(newMeta))
		for i, orig := range dataFiles {
			require.Contains(t, string(b), tbl.DataPaths[i], "manifest rewrite lost the data path")
			if tbl.DataPaths[i] != orig {
				require.NotContains(t, string(b), orig, "manifest rewrite left the original data path")
			}
		}
		return b
	}

	var meta map[string]any
	dec := json.NewDecoder(bytes.NewReader(bytes.ReplaceAll(metaBytes, []byte(oldMeta), []byte(newMeta))))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&meta))
	if loc, _ := meta["location"].(string); strings.HasSuffix(loc, "/"+Namespace+"/"+craftSource) {
		meta["location"] = strings.TrimSuffix(loc, craftSource) + name
	}

	// Manifest lists and manifests, copied under the new table's metadata dir.
	snaps, _ := meta["snapshots"].([]any)
	for _, s := range snaps {
		snap, ok := s.(map[string]any)
		require.True(t, ok)
		mlNew, ok := snap["manifest-list"].(string)
		require.True(t, ok)
		ml := strings.Replace(mlNew, newMeta, oldMeta, 1)
		mlBytes := f.ReadObject(t, db, ml)
		// A manifest list names its manifests: copy each, then the list itself.
		copied := 0
		for _, m := range f.List(t, db, s3url(Warehouse, Namespace+"/"+craftSource+"/metadata/*.avro")) {
			if bytes.Contains(mlBytes, []byte(m)) {
				f.PutObject(t, strings.Replace(m, oldMeta, newMeta, 1), rewrite(f.ReadObject(t, db, m)))
				copied++
			}
		}
		require.Positive(t, copied, "manifest list %s names no manifest", ml)
		newList := bytes.ReplaceAll(mlBytes, []byte(oldMeta), []byte(newMeta))
		require.NotContains(t, string(newList), oldMeta)
		listURL := strings.Replace(ml, oldMeta, newMeta, 1)
		if h.ManifestListDir != "" {
			local := filepath.Join(h.ManifestListDir, path.Base(ml))
			require.NoError(t, os.MkdirAll(h.ManifestListDir, 0o755))
			require.NoError(t, os.WriteFile(local, newList, 0o600))
			listURL = local
		} else {
			f.PutObject(t, listURL, newList)
		}
		snap["manifest-list"] = listURL
		tbl.ManifestList = listURL
	}

	if h.Location != "" {
		meta["location"] = h.Location
	}
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	u := hex.EncodeToString(uuid[:])
	meta["table-uuid"] = u[:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:]
	if h.NameMapping {
		props, _ := meta["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		props["schema.name-mapping.default"] = NameMapping
		meta["properties"] = props
	}
	out, err := json.Marshal(meta)
	require.NoError(t, err)
	tbl.MetadataLocation = strings.Replace(load.MetadataLocation, oldMeta, newMeta, 1)
	f.PutObject(t, tbl.MetadataLocation, out)

	f.restDo(t, http.MethodPost, "/v1/namespaces/"+Namespace+"/register", map[string]string{
		"name": name, "metadata-location": tbl.MetadataLocation,
	})
	f.verifyCrafted(t, tbl)
	return tbl
}

// IcebergFilePaths returns the data file paths iceberg_metadata reports for
// ns.<table> (through a catalog attach on a throwaway, unhardened DuckDB that
// reads only manifests, never data), sorted, and the distinct manifest paths.
func (f *Fixture) IcebergFilePaths(t testing.TB, table string) (data, manifests []string) {
	t.Helper()
	db := f.Open(t)
	Exec(t, db, AttachSQL("meta", f.RESTURI, ", READ_ONLY, ACCESS_DELEGATION_MODE 'none'"))
	rows, err := db.QueryContext(context.Background(),
		"SELECT file_path, manifest_path FROM iceberg_metadata('meta."+Namespace+"."+table+"') ORDER BY 1")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var fp, mp string
		require.NoError(t, rows.Scan(&fp, &mp))
		data = append(data, fp)
		if !seen[mp] {
			seen[mp] = true
			manifests = append(manifests, mp)
		}
	}
	require.NoError(t, rows.Err())
	return data, manifests
}

func (f *Fixture) verifyCrafted(t testing.TB, tbl *Table) {
	t.Helper()
	data, manifests := f.IcebergFilePaths(t, tbl.Name)
	want := append([]string(nil), tbl.DataPaths...)
	sort.Strings(want)
	require.Equal(t, want, data, "iceberg_metadata disagrees with the crafted data paths")
	for _, m := range manifests {
		require.Contains(t, m, "/"+Namespace+"/"+tbl.Name+"/metadata/", "manifest still in the seeded table's directory")
	}
	var load struct {
		Metadata struct {
			Snapshots []struct {
				ManifestList string `json:"manifest-list"`
			} `json:"snapshots"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(f.restDo(t, http.MethodGet, "/v1/namespaces/"+Namespace+"/tables/"+tbl.Name, nil), &load))
	require.NotEmpty(t, load.Metadata.Snapshots)
	for _, s := range load.Metadata.Snapshots {
		require.Equal(t, tbl.ManifestList, s.ManifestList)
	}
}
