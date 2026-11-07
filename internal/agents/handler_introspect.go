package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

	// table may be qualified (catalog.schema.table); an unqualified name
	// resolves against the default catalog. A connection it names connects on
	// first use (#111).
	connectErr := h.connectReferenced(ctx, "", table)
	desc, err := sess.DescribeTable(ctx, table)
	if err != nil {
		// A missing table is a success with an empty column list.
		if errors.Is(err, session.ErrNoSuchTable) && connectErr == nil {
			return NewResponse(req.ID, map[string]any{"table": table, "columns": []map[string]any{}})
		}
		return NewErrorResponse(req.ID, ErrInternal, fmt.Sprintf("failed to describe table: %v", withConnectError(err, connectErr)))
	}

	columns := make([]map[string]any, 0, len(desc.Columns))
	for _, c := range desc.Columns {
		columns = append(columns, map[string]any{
			"name":     c.Name,
			"type":     c.DataType,
			"not_null": c.NotNull,
			"pk":       false,
		})
	}
	return NewResponse(req.ID, map[string]any{"table": table, "columns": columns})
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
