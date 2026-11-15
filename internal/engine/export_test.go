package engine

import (
	"context"

	"github.com/SandwichLabs/slaw/internal/session"
)

// ResolveTable is ParseTable plus defaulting against the session: an
// unqualified name resolves to DefaultCatalog().<default schema>.name, so
// legacy callers that pass bare table names keep working while any
// catalog-qualified name addresses an attached connection.
func ResolveTable(ctx context.Context, sess session.Session, name string) (session.TableRef, error) {
	ref, err := ParseTable(name)
	if err != nil {
		return session.TableRef{}, err
	}
	if sess == nil {
		return ref, nil
	}
	return sess.Resolve(ctx, ref)
}

// NormalizeColumnValue is normalizeColumn for one value, parsing a nested
// type name on the spot (ScanRows parses it once per column).
func NormalizeColumnValue(v any, dbType string) any {
	return normalizeColumn(v, dbType, compositeType(dbType))
}
