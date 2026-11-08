package workspace

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// catalogStorage is the requirement of an s3_tables iceberg_catalog: the s3
// connection (option storage) whose secret signs the catalog. It must exist in
// this workspace, be an s3 connection and resolve; the hub then connects it
// first and reports "needs the `x` connection: ..." while it is signed out.
// There is no URL to hold to a scope at open: the data locations come from the
// catalog at read time, and an uncovered one fails closed at the deny secret.
// Any other type, and a REST catalog, requires nothing here.
func catalogStorage(ws Workspace, d ConnectionDef, lookup LookupFunc) ([]string, error) {
	if d.Type != "iceberg_catalog" || d.Options["endpoint_type"] != "s3_tables" {
		return nil, nil
	}
	name := d.Options["storage"]
	if name == "" {
		return nil, errors.New("an s3_tables catalog needs a storage connection (option storage: the s3 connection that signs it)")
	}
	c, ok := ws.Connection(name)
	if !ok {
		return nil, fmt.Errorf("the storage connection %q is not defined in this workspace; add an s3 connection called %q", name, name)
	}
	if c.Type != "s3" {
		return nil, fmt.Errorf("the storage connection %q is a %s connection; an s3_tables catalog is signed by an s3 connection", name, c.Type)
	}
	sd, err := c.ConnectionDef.Resolve(lookup, c.Scope)
	if err != nil {
		return nil, fmt.Errorf("the storage connection %q does not resolve: %w", name, err)
	}
	if err := awsStorageProblem(name, sd); err != nil {
		return nil, err
	}
	return []string{name}, nil
}

// awsStorageProblem refuses a storage connection that is not AWS for an
// s3_tables catalog. S3 Tables is an AWS service: a connection to another
// S3-compatible store (MinIO, R2 through s3) would sign a request that still
// goes to AWS.
func awsStorageProblem(name string, storage ConnectionDef) error {
	if storage.Options["endpoint"] != "" {
		return fmt.Errorf("S3 Tables signs with AWS: %s has a custom endpoint, so it is not an AWS connection; use an s3 connection for AWS itself", name)
	}
	return nil
}

// checkCatalogStorageAtSave is the save-time half of catalogStorage: an
// s3_tables catalog is refused when the storage connection it names already
// exists and is not AWS. A storage connection that does not exist yet (or is
// written with ${ENV}) is checked when the workspace opens, so a catalog may
// still be saved before its storage connection.
func (w *workspace) checkCatalogStorageAtSave(d ConnectionDef) error {
	if d.Type != "iceberg_catalog" || d.Options["endpoint_type"] != "s3_tables" {
		return nil
	}
	name := d.Options["storage"]
	if name == "" || strings.Contains(name, "${") {
		return nil
	}
	c, ok := w.Connection(name)
	if !ok || c.Type != "s3" || strings.Contains(c.Options["endpoint"], "${") {
		return nil
	}
	return awsStorageProblem(name, c.ConnectionDef)
}

// mergeRequires returns the sorted union of two requirement lists.
func mergeRequires(a, b []string) []string {
	out := slices.Clone(a)
	for _, n := range b {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}
