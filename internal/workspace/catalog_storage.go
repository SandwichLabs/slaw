package workspace

import (
	"errors"
	"fmt"
	"slices"
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
	if _, err := c.ConnectionDef.Resolve(lookup, c.Scope); err != nil {
		return nil, fmt.Errorf("the storage connection %q does not resolve: %w", name, err)
	}
	return []string{name}, nil
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
