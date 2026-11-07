package connections

import (
	"context"
	"fmt"

	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/storageurl"
)

// tableSourceType is an Iceberg or Delta Lake table. These are not catalogs:
// DuckDB reads a table with a table function over a folder or URL,
//
//	SELECT * FROM iceberg_scan('<table folder or URL>')
//	SELECT * FROM delta_scan('<table folder or URL>')
//
// so the connection attaches nothing and holds no credential. It exists to give
// the table function a home in the product: it names the extension, so the
// install-consent flow and the status line cover it (extension-missing, then
// loaded when the workspace opens), and it records where the table lives so Test
// can prove it is readable. A table in object storage is read with the secret of
// the s3 / gcs / r2 / azure connection whose scope contains it.
//
// Any scope may define one (workspace.MetadataWarning): a table's own metadata
// decides which files are read, so no check on the path can confine it. The
// agent may call these readers, but only on paths and URLs that pass the same
// vetting as read_parquet (agent-policy.md).
//
// The location is Def.Path (not Scope, which makes a storage scope the agent
// may read inside): a URL here does not widen what the agent can read.
type tableSourceType struct{ kind string }

func (t tableSourceType) Name() string                      { return t.kind }
func (t tableSourceType) Remote() bool                      { return true }
func (t tableSourceType) Catalogless() bool                 { return true }
func (t tableSourceType) NeedsCredential(session.Spec) bool { return false }

// Extensions: iceberg reads its metadata through the avro extension, which
// DuckDB 1.5 tries to auto-install when iceberg loads. The hub runs with
// autoinstall off, so avro is a named extension of its own.
func (t tableSourceType) Extensions() []string {
	if t.kind == "iceberg" {
		return []string{"avro", "iceberg"}
	}
	return []string{t.kind}
}

func (t tableSourceType) fn() string {
	if t.kind == "iceberg" {
		return "iceberg_scan"
	}
	return "delta_scan"
}

func (t tableSourceType) Validate(s session.Spec) error {
	if err := requireField(s, "a location (a table folder, or a storage URL)", s.Path); err != nil {
		return err
	}
	if err := noStrayFields(s, "path"); err != nil {
		return err
	}
	if err := unknownOptions(s); err != nil {
		return err
	}
	if IsDataURL(s.Path) {
		if err := storageurl.Validate(s.Path); err != nil {
			return fmt.Errorf("%s connection %q: location must be a folder or a plain storage URL: %w", t.kind, s.Name, err)
		}
	} else if metadataSchemeRE.MatchString(s.Path) {
		return fmt.Errorf("%s connection %q: location %q is not a folder or a storage URL", t.kind, s.Name, s.Path)
	}
	return nil
}

func (tableSourceType) SecretSteps(session.Spec, secrets.Credential) ([]session.SecretStep, error) {
	return nil, nil
}

// AttachSQL: a table source attaches nothing.
func (t tableSourceType) AttachSQL(s session.Spec) ([]string, error) {
	if err := t.Validate(s); err != nil {
		return nil, err
	}
	return []string{}, nil
}

// Probe reads the table's schema (no rows). A URL is read only when the session
// holds the covering storage connection's secret (s.Requires names it, as in
// the hub and in a Test that brought it up first); without one nothing is read,
// because DuckDB would read the URL anonymously.
func (t tableSourceType) Probe(ctx context.Context, sess session.Session, s session.Spec) (int, string, error) {
	if IsDataURL(s.Path) && len(s.Requires) == 0 {
		return 0, "a storage location is read with the matching S3, GCS, R2 or Azure connection", nil
	}
	var n int
	err := sess.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM (DESCRIBE SELECT * FROM `+t.fn()+`(`+session.QuoteLiteral(s.Path)+`))`).Scan(&n)
	if err != nil {
		return 0, "", fmt.Errorf("could not read the %s table: %w", t.kind, err)
	}
	return n, fmt.Sprintf("%d columns", n), nil
}

func (t tableSourceType) Info() TypeInfo {
	label := map[string]string{"iceberg": "Iceberg table", "delta": "Delta Lake table"}[t.kind]
	return TypeInfo{
		Name: t.kind, Label: label,
		Fields: []Field{
			{Name: "path", Label: "Table location", Kind: KindPath, Required: true,
				Help: "The table's folder, or a URL inside the scope of an S3, GCS, R2 or Azure connection. Read it with " + t.fn() + "('<location>')."},
		},
		Extensions:        t.Extensions(),
		Capabilities:      Capabilities{Catalog: false, Writable: false, ReadOnlyAttach: false, Remote: true, Test: true},
		FingerprintFields: []string{"type", "path"},
		SignInMethods:     []string{},
	}
}
