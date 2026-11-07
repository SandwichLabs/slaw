package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// ducklakeType attaches a DuckLake: a lakehouse whose catalog (the metadata)
// lives in a database and whose rows live in Parquet files under a data path.
//
//	ATTACH 'ducklake:/abs/lake.ducklake' AS "x" (DATA_PATH '/abs/data/' [, READ_ONLY])
//
// The metadata catalog (Path) is a DuckDB file (option catalog "duckdb", the
// default) or a SQLite file (catalog "sqlite"), either one on this computer or a
// plain storage URL inside the scope of a storage connection of the workspace
// (read-only: a catalog in object storage cannot be written). The data path
// (option data_path) is a local folder or such a URL. A Postgres metadata
// catalog would need a credential in the ATTACH string, so it is not offered;
// the metadata catalog's own internals (__ducklake_metadata_<name>) are hidden
// from the rail and denied to the agent.
//
// The type is remote in the session's sense (lazy attach; every write needs the
// person's approval) because a write creates Parquet files and a snapshot
// that other readers of the lake see.
type ducklakeType struct{}

func (ducklakeType) Name() string { return "ducklake" }
func (ducklakeType) Remote() bool { return true }

// Extensions is the extension every lake needs. What else a particular lake needs
// depends on its spec (SpecExtensions: sqlite for a SQLite catalog, httpfs for
// a catalog or data path in object storage). It stays the base list on purpose:
// TypeInfo.Extensions (the consent dialog, the CLI's --install) and the
// session's InstallExtension allow-list read it, and sqlite and httpfs are
// already allowed by the sqlite and storage types.
func (ducklakeType) Extensions() []string { return []string{"ducklake"} }

// SpecExtensions implements session.SpecExtensionsType: ducklake first, then
// the extension a SQLite catalog is read with, then httpfs for a URL. They
// install one at a time in this order, so the dependency comes after the lake.
func (ducklakeType) SpecExtensions(s session.Spec) []string {
	exts := []string{"ducklake"}
	if catalogKind(s) == "sqlite" {
		exts = append(exts, "sqlite")
	}
	if IsDataURL(s.Path) || IsDataURL(opt(s, "data_path")) {
		exts = append(exts, "httpfs")
	}
	return exts
}

func (ducklakeType) NeedsCredential(session.Spec) bool { return false }

// metadataSchemeRE catches a metadata "path" that DuckLake would read as
// another kind of catalog (postgres:, sqlite:, duckdb:, ...). A Windows drive
// ("C:\") is one letter and is let through.
var metadataSchemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]+:`)

// IsDataURL reports whether a data path is a URL (a storage location) rather
// than a folder.
func IsDataURL(p string) bool { return strings.Contains(p, "://") }

var catalogKinds = []string{"duckdb", "sqlite"}

// maxCatalogBytes caps the catalog read into the snapshot: it is held in memory
// once on the way to the file.
var maxCatalogBytes int64 = 512 << 20

// sidecarSuffixes are the write-ahead logs and journals a copy of the catalog
// file alone would miss.
var sidecarSuffixes = []string{".wal", "-wal", "-journal", "-shm"}

// catalogSidecar returns the suffix of a write-ahead log or journal object that
// sits beside the catalog, or "". Each name is asked for by itself (a HEAD
// request): the hub reads S3-like URLs literally and cannot list a folder
// (#138). A 404 means absent; any other failure is an error, so a catalog is
// never copied on a guess.
func catalogSidecar(ctx context.Context, q session.QueryExecer, path string) (string, error) {
	for _, suffix := range sidecarSuffixes {
		var size int64
		found := false
		rows, err := q.QueryContext(ctx, `SELECT size FROM read_blob(`+session.QuoteLiteral(path+suffix)+`)`)
		if err == nil {
			if found = rows.Next(); found {
				err = rows.Scan(&size)
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
		}
		switch {
		case err == nil && found:
			return suffix, nil
		case err == nil:
			continue
		// duckdb-go has no typed HTTP status, so absence is the message's
		// "HTTP 404"; a reworded message fails closed (the lake does not attach).
		// MinIO integration tests pin the wording.
		case strings.Contains(err.Error(), "HTTP 404"):
			continue
		default:
			return "", fmt.Errorf("could not check the lake's catalog for a write-ahead log: %w", err)
		}
	}
	return "", nil
}

// Stage implements session.Stager. A catalog in object storage is read once into
// a private file (0600, in dir); the returned spec names that file, and that is
// what is vetted and attached (see the note in ducklake_vet.go). A local catalog
// is returned as it is. Sidecars that a copy of the main file alone would not
// carry (a DuckDB write-ahead log, a SQLite journal) make the catalog refused, so
// the lake never silently misses committed changes.
func (ducklakeType) Stage(ctx context.Context, q session.QueryExecer, s session.Spec, dir string) (session.Spec, error) {
	if !IsDataURL(s.Path) {
		return s, nil
	}
	if !s.ReadOnly {
		return s, fmt.Errorf("a catalog in object storage can only be read: set read_only")
	}
	if err := storageurl.Validate(s.Path); err != nil {
		return s, fmt.Errorf("the lake's catalog must be a plain storage URL: %w", err)
	}
	lit := session.QuoteLiteral(s.Path)
	if suffix, err := catalogSidecar(ctx, q, s.Path); err != nil {
		return s, err
	} else if suffix != "" {
		return s, fmt.Errorf("the lake's catalog in object storage has a %q file beside it (uncommitted changes that a copy would miss); refusing to read it. Checkpoint the catalog and remove it", suffix)
	}
	// One query: the size and (only when it is within the cap) the content come
	// from a single request, so a server cannot report a small size to one
	// request and a huge one to the next and have it all downloaded.
	var (
		size    int64
		content []byte
	)
	err := q.QueryRowContext(ctx, `SELECT size, CASE WHEN size <= `+strconv.FormatInt(maxCatalogBytes, 10)+` THEN content END FROM read_blob(`+lit+`)`).Scan(&size, &content)
	if err != nil {
		return s, fmt.Errorf("could not read the lake's catalog from object storage (is the storage connection connected, and does it cover this URL?): %w", err)
	}
	if content == nil || size > maxCatalogBytes || int64(len(content)) > maxCatalogBytes {
		return s, fmt.Errorf("the lake's catalog in object storage is over the %d MiB limit; refusing to read it", maxCatalogBytes>>20)
	}
	file := filepath.Join(dir, "catalog.db")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		return s, fmt.Errorf("could not copy the lake's catalog: %w", err)
	}
	s.Path = file
	return s, nil
}

// validateCatalogPath checks the catalog location. A URL is tested before the
// scheme pattern (s3: matches it), and one is only ever read.
func validateCatalogPath(s session.Spec) error {
	if IsDataURL(s.Path) {
		if err := storageurl.Validate(s.Path); err != nil {
			return fmt.Errorf("ducklake connection %q: the catalog must be a file or a plain storage URL: %w", s.Name, err)
		}
		if !s.ReadOnly {
			return fmt.Errorf("ducklake connection %q: a catalog in object storage can only be read: set read_only", s.Name)
		}
		return nil
	}
	if metadataSchemeRE.MatchString(s.Path) {
		return fmt.Errorf("ducklake connection %q: the metadata catalog must be a file on this computer or a storage URL (not %q)", s.Name, s.Path)
	}
	return nil
}

func (ducklakeType) Validate(s session.Spec) error {
	if err := noStrayFields(s, "path"); err != nil {
		return err
	}
	if err := requireField(s, "a metadata file (path)", s.Path); err != nil {
		return err
	}
	if err := unknownOptions(s, "data_path", "catalog"); err != nil {
		return err
	}
	if err := oneOf("catalog", opt(s, "catalog"), catalogKinds...); err != nil {
		return err
	}
	if err := validateCatalogPath(s); err != nil {
		return err
	}
	dp := opt(s, "data_path")
	if err := requireField(s, "a data path (option data_path)", dp); err != nil {
		return err
	}
	if IsDataURL(dp) {
		if err := storageurl.Validate(dp); err != nil {
			return fmt.Errorf("ducklake connection %q: data path must be a folder or a plain storage URL: %w", s.Name, err)
		}
	} else if metadataSchemeRE.MatchString(dp) {
		return fmt.Errorf("ducklake connection %q: data path %q is not a folder or a storage URL", s.Name, dp)
	}
	return nil
}

func (ducklakeType) SecretSteps(session.Spec, secrets.Credential) ([]session.SecretStep, error) {
	return nil, nil
}

func (t ducklakeType) AttachSQL(s session.Spec) ([]string, error) {
	if err := t.Validate(s); err != nil {
		return nil, err
	}
	urlCatalog := IsDataURL(s.Path)
	if !urlCatalog && !filepath.IsAbs(s.Path) {
		return nil, fmt.Errorf("ducklake connection %q: the metadata path was not resolved (it must be absolute here)", s.Name)
	}
	dp := opt(s, "data_path")
	if !IsDataURL(dp) && !filepath.IsAbs(dp) {
		return nil, fmt.Errorf("ducklake connection %q: the data path was not resolved (it must be absolute here)", s.Name)
	}
	if s.ReadOnly && !urlCatalog {
		// A read-only attach of a missing catalog is a confusing DuckLake error.
		// (A missing URL catalog is an error at PreAttach, where it is read.)
		if _, err := os.Stat(s.Path); err != nil {
			return nil, fmt.Errorf("the DuckLake metadata file for %q is not readable (a read-only connection cannot create it): %w", s.Name, err)
		}
	}
	sep := string(filepath.Separator)
	if IsDataURL(dp) {
		sep = "/"
	}
	if !strings.HasSuffix(dp, sep) {
		dp += sep
	}
	target := "ducklake:" + s.Path
	if catalogKind(s) == "sqlite" {
		target = "ducklake:sqlite:" + s.Path
	}
	stmt := "ATTACH " + session.QuoteLiteral(target) + " AS " + session.QuoteIdent(s.Name) +
		" (DATA_PATH " + session.QuoteLiteral(dp)
	if s.ReadOnly {
		stmt += ", READ_ONLY"
	}
	return []string{stmt + ")"}, nil
}

// PostAttach vets what the attached lake's own catalog says before the lake is
// usable. A DuckLake catalog names every data file it reads, and a crafted one
// (a committed metadata file, or one edited after the fact) can name any file on
// this computer. So the lake is refused, and detached, when
//
//   - the data path stored in the catalog is not the configured one, or
//   - any data file, delete file, scheduled-for-deletion file, table or schema
//     path is absolute, has a ".." segment, a scheme or a backslash.
//
// A catalog you were handed (a shared slaw.yaml, a bundle) is therefore checked
// before it is read, and a shared definition carries workspace.MetadataWarning.
func (t ducklakeType) PostAttach(ctx context.Context, q session.Querier, s session.Spec) error {
	meta := session.QuoteIdent("__ducklake_metadata_"+s.Name) + ".main."
	var stored sql.NullString
	err := q.QueryRowContext(ctx, `SELECT value FROM `+meta+`ducklake_metadata WHERE key = 'data_path' AND scope IS NULL`).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("could not verify the lake's catalog (data path): %w", err)
	}
	if stored.Valid {
		want := opt(s, "data_path")
		sep := string(filepath.Separator)
		if IsDataURL(want) {
			sep = "/"
		}
		if !strings.HasSuffix(want, sep) {
			want += sep
		}
		got := stored.String
		if !strings.HasSuffix(got, "/") && !strings.HasSuffix(got, sep) {
			got += sep
		}
		if got != want {
			return fmt.Errorf("the lake's catalog says its data lives somewhere other than the data path of this connection; refusing to read it")
		}
	}
	for _, tbl := range []string{"ducklake_data_file", "ducklake_delete_file", "ducklake_files_scheduled_for_deletion", "ducklake_table", "ducklake_schema"} {
		var bad int
		err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+meta+tbl+`
			WHERE NOT coalesce(path_is_relative, false)
			   OR path IS NULL
			   OR regexp_matches(path, '(^|[/\\])\.\.([/\\]|$)')
			   OR path LIKE '%://%' OR path LIKE '/%' OR path LIKE '%\%' OR path LIKE '%?%'`).Scan(&bad)
		if err != nil {
			return fmt.Errorf("could not verify the lake's catalog (%s): %w", tbl, err)
		}
		if bad > 0 {
			return fmt.Errorf("the lake's catalog (%s) names %d file(s) by an absolute path, a URL, a query string or a path that leaves the data folder; refusing to read it", tbl, bad)
		}
	}
	if !IsDataURL(opt(s, "data_path")) {
		if err := refuseSymlinksUnder(opt(s, "data_path")); err != nil {
			return err
		}
	}
	return nil
}

// refuseSymlinksUnder fails when anything under dir is a symbolic link. A
// catalog row such as "link/secret.parquet" is a clean relative path, but if
// "link" points out of the data folder the lake would read what is there. The
// folder itself may be a link (that is the person's own choice); what is inside
// it may not.
func refuseSymlinksUnder(dir string) error {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil //nolint:nilerr // no data folder yet: nothing to read
	}
	const maxEntries = 500000
	seen := 0
	var link string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > maxEntries {
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link = p
			return fs.SkipAll
		}
		return nil
	})
	switch {
	case link != "":
		return fmt.Errorf("the lake's data folder contains a symbolic link (%s); refusing to read it, because a link can lead out of the folder", filepath.Base(link))
	case seen > maxEntries:
		return fmt.Errorf("the lake's data folder has too many files to verify; refusing to read it")
	}
	return nil
}

func (ducklakeType) Probe(ctx context.Context, sess session.Session, s session.Spec) (int, string, error) {
	if IsDataURL(s.Path) && len(s.Requires) == 0 {
		return 0, "a catalog in object storage is read with the matching S3, GCS, R2 or Azure connection; connect the lake from the rail", nil
	}
	var n int
	err := sess.DB().QueryRowContext(ctx, `SELECT count(*) FROM duckdb_tables() WHERE database_name = $1`, s.Name).Scan(&n)
	if err != nil {
		return 0, "", fmt.Errorf("attached, but listing tables failed: %w", err)
	}
	return n, fmt.Sprintf("%d tables", n), nil
}

func (ducklakeType) Info() TypeInfo {
	return TypeInfo{
		Name: "ducklake", Label: "DuckLake",
		Fields: []Field{
			{Name: "path", Label: "Catalog (file or storage URL)", Kind: KindPath, Required: true, Help: "Catalog file, or a storage URL inside an S3/GCS/R2/Azure connection's scope (read-only)"},
			{Name: "catalog", Label: "Catalog kind", Kind: KindEnum, Choices: append([]string(nil), catalogKinds...), Default: "duckdb", Option: true, Prominent: true, Help: "The kind of database the catalog is: a DuckDB file or a SQLite file."},
			{Name: "data_path", Label: "Data folder", Kind: KindText, Required: true, Option: true, Help: "A folder for the Parquet files, or a URL inside the scope of an S3, GCS, R2 or Azure connection of this workspace."},
		},
		Extensions:        []string{"ducklake"},
		Capabilities:      Capabilities{Catalog: true, Writable: true, ReadOnlyAttach: true, Remote: true, Test: true},
		FingerprintFields: []string{"type", "path", "option:data_path", "option:catalog"},
		SignInMethods:     []string{},
	}
}
