package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/artifacts"
	"github.com/SandwichLabs/slaw/internal/queries"
)

// --- saved queries: list_queries, save_query, run_saved_query ---------------
//
// A saved query is queries/<id>.sql: a header (title, description, connection,
// parameters) over one SQL statement. save_query and build_query are content
// tools like write_artifact: the SQL is vetted at save under the same read-only
// policy a run uses (so a bad column or a write is refused with the binder's
// own words), a new query never silently forks an existing title, and an update
// names its id and the rev the agent read. Parameters are $name placeholders
// declared with a type and a default, bound exactly as an artifact's are.

func (h *Handler) handleListQueries(ctx context.Context, req *Request) *Response {
	list, err := h.access.Queries().List(ctx)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInternal, fmt.Sprintf("failed to list queries: %v", err))
	}
	rows := make([]map[string]any, 0, len(list))
	for _, q := range list {
		row := map[string]any{
			"id":         q.ID,
			"rev":        q.Rev,
			"name":       q.Title,
			"sql":        q.SQL,
			"engine":     q.Engine,
			"parameters": nonNilParams(q.Parameters),
		}
		if q.Description != "" {
			row["description"] = q.Description
		}
		if q.Connection != "" {
			row["connection"] = q.Connection
		}
		if q.Error != "" {
			row["error"] = scrubStoreText(q.Error)
		}
		rows = append(rows, row)
	}
	return NewResponse(req.ID, map[string]any{"queries": rows})
}

func nonNilParams(ps []queries.Param) []queries.Param {
	if ps == nil {
		return []queries.Param{}
	}
	return ps
}

// savedQuerySpec is a save request after its shape was read.
type savedQuerySpec struct {
	id, rev string
	q       queries.Query
}

// handleSaveQuery persists a named, already-written read-only SQL string.
func (h *Handler) handleSaveQuery(ctx context.Context, req *Request) *Response {
	spec, errResp := savedQuerySpecOf(req)
	if errResp != nil {
		return errResp
	}
	return h.storeSavedQuery(ctx, req, spec, "title")
}

func savedQuerySpecOf(req *Request) (savedQuerySpec, *Response) {
	var spec savedQuerySpec
	for _, f := range []string{"title", "sql", "description", "connection", "id", "rev"} {
		if v, present := req.Params[f]; present && v != nil {
			if _, ok := v.(string); !ok {
				return spec, NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("%s must be a string", f))
			}
		}
	}
	params, err := declaredParams(req.Params["parameters"])
	if err != nil {
		return spec, NewErrorResponse(req.ID, ErrInvalidParams, "parameters: "+err.Error())
	}
	spec.id = strings.TrimSpace(stringParam(req.Params["id"]))
	spec.rev = stringParam(req.Params["rev"])
	spec.q = queries.Query{
		Title:       strings.TrimSpace(stringParam(req.Params["title"])),
		SQL:         stringParam(req.Params["sql"]),
		Description: strings.TrimSpace(stringParam(req.Params["description"])),
		Connection:  strings.TrimSpace(stringParam(req.Params["connection"])),
		Engine:      "duckdb",
		Parameters:  params,
	}
	return spec, nil
}

// storeSavedQuery is the one save path of save_query and build_query: vet the
// SQL, resolve create-or-update against the store, stop there in a dry run,
// then write. titleParam names the request field holding the title, for
// messages.
func (h *Handler) storeSavedQuery(ctx context.Context, req *Request, spec savedQuerySpec, titleParam string) *Response {
	q := &spec.q
	if q.Title == "" {
		return NewErrorResponse(req.ID, ErrInvalidParams, titleParam+" is required")
	}
	if strings.TrimSpace(q.SQL) == "" {
		return NewErrorResponse(req.ID, ErrInvalidParams, "sql is required")
	}
	store := h.access.Queries()
	existing, errResp := savedQueryTarget(ctx, req, store, spec)
	if errResp != nil {
		return errResp
	}
	cols, vf := h.vetSavedQuery(ctx, q)
	if vf != nil {
		return NewErrorResponse(req.ID, vf.code, vf.msg)
	}
	if isDryRun(ctx) {
		if cols == nil {
			return dryRunOK(req.ID)
		}
		return dryRunOKWith(req.ID, map[string]any{"columns": cols})
	}
	verb := "created"
	if existing != nil {
		q.ID, q.CreatedAt = existing.ID, existing.CreatedAt
		err := store.Update(ctx, q, queries.WithRev(spec.rev))
		if errors.Is(err, queries.ErrConflict) {
			return NewErrorResponse(req.ID, ErrInvalidParams, staleQueryMessage(q.ID))
		}
		if err != nil {
			return NewErrorResponse(req.ID, ErrInternal, "failed to save query: "+scrubStoreText(err.Error()))
		}
		verb = "updated"
	} else if err := store.Create(ctx, q); err != nil {
		return NewErrorResponse(req.ID, ErrInternal, "failed to save query: "+scrubStoreText(err.Error()))
	}
	h.logger.Info("agent saved query", "id", q.ID, "action", verb)
	res := map[string]any{"id": q.ID, "title": q.Title, "saved": true, "created": verb == "created", "rev": q.Rev,
		"parameters": nonNilParams(q.Parameters)}
	if cols != nil {
		res["columns"] = cols
	}
	return NewResponse(req.ID, res)
}

// savedQueryTarget settles whether a save creates or replaces. A create never
// reuses a title (it names the existing id instead of forking "-2"); a replace
// needs the id and the rev the agent read. It returns the stored query to
// replace, or nil to create.
func savedQueryTarget(ctx context.Context, req *Request, store queries.Store, spec savedQuerySpec) (*queries.Query, *Response) {
	id := req.ID
	fail := func(msg string) (*queries.Query, *Response) {
		return nil, NewErrorResponse(id, ErrInvalidParams, msg)
	}
	list, err := store.List(ctx)
	if err != nil {
		return fail("failed to read the saved queries: " + scrubStoreText(err.Error()))
	}
	var cur *queries.Query
	for i := range list {
		if spec.id != "" && list[i].ID == spec.id {
			cur = &list[i]
		}
	}
	if spec.id == "" {
		for _, o := range list {
			if strings.EqualFold(strings.TrimSpace(o.Title), spec.q.Title) {
				return fail(fmt.Sprintf("a saved query titled %q already exists (id %q), so nothing was saved. To change it, send %s with id %q and the \"rev\" list_queries returned for it; or pick another title.", o.Title, o.ID, req.Method, o.ID))
			}
		}
		return nil, nil
	}
	if cur == nil {
		return fail(fmt.Sprintf("there is no saved query with id %q (see list_queries). Omit id to create a new one.", spec.id))
	}
	if spec.rev == "" {
		return fail(fmt.Sprintf("saved query %q exists, so nothing was saved. Send the \"rev\" list_queries returned for it to replace it.", spec.id))
	}
	if cur.Rev != spec.rev {
		return fail(staleQueryMessage(spec.id))
	}
	for _, o := range list {
		if o.ID != spec.id && strings.EqualFold(strings.TrimSpace(o.Title), spec.q.Title) {
			return fail(fmt.Sprintf("another saved query is already titled %q (id %q), so nothing was saved. Pick another title.", o.Title, o.ID))
		}
	}
	return cur, nil
}

func staleQueryMessage(id string) string {
	return fmt.Sprintf("saved query %q changed since the rev you sent (a person or another tool edited it), so nothing was saved. Call list_queries again, merge your change into what it returns, and send the new \"rev\".", id)
}

// --- parameters --------------------------------------------------------------

// savedQueryTypes are the parameter types a saved query may declare: an
// artifact's, less enum (which needs a list of options a query file has no
// place for).
var savedQueryTypes = map[string]bool{
	artifacts.TypeText: true, artifacts.TypeNumber: true, artifacts.TypeInteger: true,
	artifacts.TypeDate: true, artifacts.TypeBoolean: true,
}

// declaredParams reads and checks save_query's parameters: each is
// {name, type, default}, and the default is required (it is what the vet binds
// and what a run uses when the caller sends no value).
func declaredParams(raw any) ([]queries.Param, error) {
	out := []queries.Param{}
	if raw == nil {
		return out, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("must be a list of {name, type, default}")
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d must be an object {name, type, default}", i)
		}
		def, hasDef := m["default"]
		p := queries.Param{Name: stringParam(m["name"]), Type: stringParam(m["type"])}
		if hasDef && def != nil {
			s, ok := def.(string)
			if !ok {
				return nil, fmt.Errorf("parameter %q: default must be a string such as \"15\", \"true\" or \"2026-01-31\"", p.Name)
			}
			p.Default = s
		} else {
			return nil, fmt.Errorf("parameter %q needs a default (it is what the check and a run use when no value is sent)", p.Name)
		}
		if !savedQueryTypes[p.Type] {
			return nil, fmt.Errorf("parameter %q: type %q is not one of text, number, integer, date, boolean", p.Name, p.Type)
		}
		out = append(out, p)
	}
	if errs := artifacts.ValidateParameters(queries.ArtifactParams(out)); len(errs) > 0 {
		return nil, joinInvalid(errs)
	}
	return out, nil
}

func joinInvalid(errs []error) error {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = trimInvalid(e)
	}
	return errors.New(strings.Join(msgs, "; "))
}

func trimInvalid(err error) string {
	return strings.TrimPrefix(err.Error(), artifacts.ErrInvalid.Error()+": ")
}

// --- vetting -----------------------------------------------------------------

// vetSavedQuery holds a saved query's SQL to the policy a run applies: the
// parameters line up, the statement is one read, and it binds on its connection
// with every parameter at its default (a bad column fails with the binder's own
// error and its candidates). It returns the result columns it read.
func (h *Handler) vetSavedQuery(ctx context.Context, q *queries.Query) ([]ArtifactColumn, *sqlVetFailure) {
	fail := func(msg string) ([]ArtifactColumn, *sqlVetFailure) {
		return nil, &sqlVetFailure{ErrInvalidParams, redactForAgent(msg)}
	}
	params, err := queries.Bind(q, nil, queries.BindOptions{Strict: true})
	sqlText := q.SQL
	if err != nil {
		return fail("sql: " + err.Error())
	}
	conn, err := h.checkArtifactStatement(sqlText, q.Connection)
	if err != nil {
		return fail("sql: " + err.Error())
	}
	runSQL, params, runCtx, den, err := h.readOnlyVerdict(ctx, conn, sqlText, params)
	if err != nil {
		if errors.Is(err, ErrReadOnlyBindingDenied) {
			err = errors.New("not a plain read (no writes, no temporary objects, files only inside data/)")
			if den.Reason != "" {
				err = errors.New(den.Reason)
			}
		}
		return fail("saved queries are read-only queries: " + err.Error())
	}
	return h.describeQueryColumns(runCtx, conn, runSQL, params), nil
}

// --- run_saved_query ----------------------------------------------------------

// handleRunSavedQuery runs a saved query by id through run_query's own
// pipeline (every sandbox, connection and result gate) with its parameters
// bound by name. It runs reads only: a stored statement that has become a write
// (a hand edit, a git pull) is refused.
func (h *Handler) handleRunSavedQuery(ctx context.Context, req *Request) *Response {
	id := strings.TrimSpace(stringParam(req.Params["id"]))
	store := h.access.Queries()
	q, err := store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, queries.ErrNotFound) {
			return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("there is no saved query with id %q (see list_queries)", id))
		}
		return NewErrorResponse(req.ID, ErrInternal, "failed to read the saved query: "+scrubStoreText(err.Error()))
	}
	if q.Error != "" {
		return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("saved query %q cannot be run: %s", id, scrubStoreText(q.Error)))
	}
	var values map[string]any
	if raw, present := req.Params["params"]; present && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("params must be an object {name: value} (got %s)", jsonKind(raw)))
		}
		values = m
	}
	params, err := queries.Bind(q, values, queries.BindOptions{})
	sqlText := q.SQL
	if err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("saved query %q: %s", id, err))
	}
	conn, err := h.checkArtifactStatement(sqlText, q.Connection)
	if err != nil {
		return NewErrorResponse(req.ID, ErrInvalidParams, fmt.Sprintf("saved query %q: %s", id, err))
	}
	run := &Request{JSONRPC: req.JSONRPC, ID: req.ID, Method: "run_query", Params: map[string]any{"sql": sqlText, "params": params}}
	return h.runQuery(ctx, run, runTarget{connection: conn, readsOnly: true})
}
