package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
)

// CatalogRequest asks for one level of a connection's tree: its schemas (no
// schema), a schema's tables and views, or a table's columns.
type CatalogRequest struct {
	Connection string `json:"connection"`
	Schema     string `json:"schema"`
	Table      string `json:"table"`
}

// CatalogNode is one schema, table, view or column.
type CatalogNode struct {
	Kind      string `json:"kind"` // schema | table | view | column
	Name      string `json:"name"`
	Qualified string `json:"qualified,omitempty"`
	// Rows is an estimate (DuckDB statistics) when known; -1 otherwise.
	Rows     int64  `json:"rows"`
	DataType string `json:"dataType,omitempty"`
	PK       bool   `json:"pk,omitempty"`
	FK       bool   `json:"fk,omitempty"`
}

// CatalogListing is one level of the tree. When the connection has only one
// schema (`main`), Flattened is true and Nodes are its tables directly (the
// schema level is hidden), with Schema naming it.
type CatalogListing struct {
	Kind      string        `json:"kind"` // schemas | tables | columns
	Schema    string        `json:"schema"`
	Flattened bool          `json:"flattened"`
	Nodes     []CatalogNode `json:"nodes"`
	// More counts the namespaces left out of a schemas listing because the
	// connection has more than MaxCatalogNamespaces (an Iceberg catalog); 0
	// when nothing was cut. The rail says so with a "more" line.
	More int `json:"more,omitempty"`
}

// IcebergCatalogType is the connection type whose listings are names only.
const IcebergCatalogType = "iceberg_catalog"

// MaxCatalogNamespaces bounds the namespaces listed for one iceberg_catalog, so
// a large S3 Tables bucket cannot stall the rail.
const MaxCatalogNamespaces = 200

// namesOnly reports whether the connection's tree is listed by names only:
// an iceberg_catalog, whose tables cost a LoadTable each to describe (S0 item
// 10: duckdb_tables() and information_schema make no fetch, DESCRIBE does).
func namesOnly(sess session.Session, conn string) bool {
	for _, i := range sess.ConnectionInfos() {
		if i.Name == conn {
			return i.Type == IcebergCatalogType
		}
	}
	return false
}

// UnknownConnectionError says the request named no valid connection. Its text is
// meant for the person; it carries no secret.
type UnknownConnectionError struct{ Name string }

func (e *UnknownConnectionError) Error() string {
	return fmt.Sprintf("There is no connection called %q.", e.Name)
}

// ListCatalog returns one lazy level of a connection's tree. It never dials: a
// connection that is not connected is an error the caller answers by connecting
// first. The errors are not redacted: that is the caller's boundary.
func ListCatalog(ctx context.Context, sess session.Session, req CatalogRequest) (*CatalogListing, error) {
	if err := session.ValidateName(req.Connection); err != nil {
		return nil, &UnknownConnectionError{Name: req.Connection}
	}
	if req.Table != "" {
		return catalogColumns(ctx, sess, req)
	}
	flattened := false
	if req.Schema == "" {
		schemas, err := catalogSchemas(ctx, sess, req.Connection)
		if err != nil {
			return nil, err
		}
		more := 0
		if namesOnly(sess, req.Connection) && len(schemas) > MaxCatalogNamespaces {
			more, schemas = len(schemas)-MaxCatalogNamespaces, schemas[:MaxCatalogNamespaces]
		}
		// The schema level is hidden only while a connection has just `main`
		// (SPEC 05): a Postgres database with only `public` still shows it.
		if len(schemas) != 1 || schemas[0] != "main" {
			out := &CatalogListing{Kind: "schemas", Nodes: []CatalogNode{}, More: more}
			for _, s := range schemas {
				out.Nodes = append(out.Nodes, CatalogNode{Kind: "schema", Name: s, Rows: -1})
			}
			return out, nil
		}
		req.Schema, flattened = schemas[0], true // only one: hide the level
	}
	return catalogTables(ctx, sess, req, flattened)
}

// catalogSchemas lists a connection's schemas, minus catalog plumbing that is
// not data (Postgres keeps pg_toast and per-session pg_temp_N schemas).
func catalogSchemas(ctx context.Context, sess session.Session, conn string) ([]string, error) {
	all, err := sess.ListSchemas(ctx, conn)
	if err != nil {
		return nil, err
	}
	schemas := []string{}
	for _, sc := range all {
		if sc == "pg_toast" || strings.HasPrefix(sc, "pg_temp_") || strings.HasPrefix(sc, "pg_toast_temp_") {
			continue
		}
		schemas = append(schemas, sc)
	}
	return schemas, nil
}

func catalogColumns(ctx context.Context, sess session.Session, req CatalogRequest) (*CatalogListing, error) {
	schema := req.Schema
	if schema == "" {
		var err error
		if schema, err = sess.DefaultSchema(ctx, req.Connection); err != nil {
			return nil, err
		}
	}
	cols, err := TableColumns(sess.DB(), session.TableRef{Catalog: req.Connection, Schema: schema, Name: req.Table})
	if err != nil {
		return nil, err
	}
	out := &CatalogListing{Kind: "columns", Schema: schema, Nodes: []CatalogNode{}}
	for _, c := range cols {
		out.Nodes = append(out.Nodes, CatalogNode{Kind: "column", Name: c.Name, DataType: c.Type, PK: c.PK, FK: c.FK, Rows: -1})
	}
	return out, nil
}

func catalogTables(ctx context.Context, sess session.Session, req CatalogRequest, flattened bool) (*CatalogListing, error) {
	tabs, err := sess.ListTables(ctx, req.Connection, req.Schema)
	if err != nil {
		return nil, err
	}
	est := map[session.TableKey]int64{}
	if !namesOnly(sess, req.Connection) {
		est = session.EstimatedRows(ctx, sess.DB(), req.Connection)
	}
	out := &CatalogListing{Kind: "tables", Schema: req.Schema, Flattened: flattened, Nodes: []CatalogNode{}}
	for _, t := range tabs {
		kind := RelationKind(t.Type)
		n := CatalogNode{Kind: kind, Name: t.Name, Qualified: t.Ref().String(), Rows: -1}
		if r, ok := est[session.TableKey{Schema: req.Schema, Name: t.Name}]; ok && kind == "table" {
			n.Rows = r
		}
		out.Nodes = append(out.Nodes, n)
	}
	return out, nil
}

// RelationKind is "view" for a view type name from the catalog ("VIEW",
// "MATERIALIZED VIEW", ...), "table" otherwise.
func RelationKind(typ string) string {
	if strings.Contains(strings.ToUpper(typ), "VIEW") {
		return "view"
	}
	return "table"
}
