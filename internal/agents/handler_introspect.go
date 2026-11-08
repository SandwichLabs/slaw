package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/session"
)

// --- Introspect handlers ---

func (h *Handler) handleIntrospectSchema(ctx context.Context, req *Request) *Response {
	sess, errResp := h.sess(req.ID)
	if errResp != nil {
		return errResp
	}
	// Optional "connection": introspect that catalog instead of the default.
	// Tables of a non-default catalog come back qualified (catalog.schema.table)
	// so the agent can use them verbatim in every other tool.
	connection, _ := req.Params["connection"].(string)
	catalog := connection
	if catalog == "" {
		catalog = sess.DefaultCatalog()
	}
	// A registered connection that is not connected yet connects on first use,
	// as it does for run_query (#111).
	connectErr := h.connectReferenced(ctx, connection, "")
	tabs, err := h.listCatalogTables(ctx, sess, catalog)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, fmt.Sprintf("failed to query schema: %v", withConnectError(err, connectErr)))
	}
	tables := make([]string, 0, len(tabs))
	for _, t := range tabs {
		if catalog == sess.DefaultCatalog() {
			tables = append(tables, t.Name)
		} else {
			tables = append(tables, t.Ref().String())
		}
	}
	return NewResponse(req.ID, map[string]any{"tables": tables})
}

// listCatalogTables lists the tables of catalog's default schema.
func (h *Handler) listCatalogTables(ctx context.Context, sess session.Session, catalog string) ([]session.TableInfo, error) {
	return sess.ListTables(ctx, catalog, "")
}

func (h *Handler) handleDescribeTable(ctx context.Context, req *Request) *Response {
	table, _ := req.Params["table"].(string)
	sess, errResp := h.sess(req.ID)
	if errResp != nil {
		return errResp
	}
	if isPathOrURL(table) {
		return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf(
			"describe_table takes a table (name or catalog.schema.table), not a file path or URL (%q). To see a file's columns use run_query with DESCRIBE SELECT * FROM read_csv('<path>') (read_parquet or read_json for those formats); the file policy applies, so the path must be inside the workspace data/ folder or a storage connection's scope.", table))
	}

	// table may be qualified (catalog.schema.table); an unqualified name
	// resolves against the default catalog. A connection it names connects on
	// first use (#111).
	connectErr := h.connectReferenced(ctx, "", table)
	desc, err := sess.DescribeTable(ctx, table)
	if err != nil {
		// A missing table is an error that names it: an empty column list reads
		// as "a table with no columns" to an agent.
		if errors.Is(err, session.ErrNoSuchTable) && connectErr == nil {
			return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf(
				"table %q does not exist (or has no columns). Call introspect_schema (with connection for a table in another connection) for the table names, and qualify the name as catalog.schema.table.", table))
		}
		return NewErrorResponse(req.ID, ErrInternal, fmt.Sprintf("failed to describe table: %v", withConnectError(err, connectErr)))
	}

	pk := h.primaryKeyColumns(ctx, sess, desc.Ref)
	columns := make([]map[string]any, 0, len(desc.Columns))
	for _, c := range desc.Columns {
		columns = append(columns, map[string]any{
			"name":     c.Name,
			"type":     c.DataType,
			"not_null": c.NotNull,
			"pk":       pk[c.Name],
		})
	}
	return NewResponse(req.ID, map[string]any{"table": table, "columns": columns})
}

// primaryKeyColumns is the set of ref's primary-key columns as DuckDB's catalog
// reports them. A catalog that exposes no constraints (an attached Postgres or
// MySQL database) or a failed read gives an empty set: pk is then false, as
// "unknown", never a guess.
func (h *Handler) primaryKeyColumns(ctx context.Context, sess session.Session, ref session.TableRef) map[string]bool {
	db := h.access.DuckDBDB()
	if db == nil {
		return nil
	}
	// An Iceberg catalog's tables resolve lazily and hold no constraints DuckDB
	// can read: skip the lookup, as engine.TableColumns does.
	for _, c := range sess.ConnectionInfos() {
		if strings.EqualFold(c.Name, ref.Catalog) && c.Type == "iceberg_catalog" {
			return nil
		}
	}
	pk, err := engine.PrimaryKeyColumns(ctxQuerier{ctx, db}, ref)
	if err != nil {
		h.logger.Warn("describe_table: could not read primary keys", "table", ref.String(), "error", engine.ErrorForLog(err))
		return nil
	}
	return pk
}

// ctxQuerier makes a *sql.DB's reads honour ctx for engine.PrimaryKeyColumns.
type ctxQuerier struct {
	ctx context.Context //nolint:containedctx // a one-call adapter
	db  *sql.DB
}

func (q ctxQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	return q.db.QueryContext(q.ctx, query, args...)
}

// isPathOrURL reports whether a describe_table argument is a path or URL
// rather than a table reference.
func isPathOrURL(table string) bool {
	if strings.Contains(table, `"`) {
		return false // a quoted name may hold a slash; DescribeTable resolves it
	}
	return strings.ContainsAny(table, `/\`) || strings.Contains(table, "://") || strings.HasPrefix(table, "~")
}

// builtinCatalogName is DuckDB's own scratch and system catalogs, which the
// hub keeps beside the workspace's connections: not places data lives.
func builtinCatalogName(name string) bool {
	switch strings.ToLower(name) {
	case "memory", "system", "temp":
		return true
	}
	return false
}

func (h *Handler) handleHelpSearch(_ context.Context, req *Request) *Response {
	query := ""
	if q, ok := req.Params["query"].(string); ok {
		query = q
	}

	if strings.TrimSpace(query) == "" {
		return NewResponse(req.ID, helpIndexResult())
	}
	entries := SearchHelp(query)

	results := make([]map[string]any, len(entries))
	for i, e := range entries {
		results[i] = map[string]any{
			"topic":   e.Topic,
			"title":   e.Title,
			"content": e.Content,
		}
	}
	return NewResponse(req.ID, map[string]any{"results": results, "count": len(results)})
}

// helpIndexResult is the empty-query answer of help_search: a compact index
// (topic, title, one line each) instead of every topic in full.
func helpIndexResult() map[string]any {
	results := make([]map[string]any, len(HelpEntries))
	for i, e := range HelpEntries {
		results[i] = map[string]any{"topic": e.Topic, "title": e.Title, "summary": e.Summary}
	}
	return map[string]any{
		"results": results,
		"count":   len(results),
		"note":    "Index only. Pass a topic value (e.g. \"mcp-tools\") or keywords as help_search query for the full text.",
	}
}
