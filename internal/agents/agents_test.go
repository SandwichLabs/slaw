package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/SandwichLabs/slaw/internal/artifacts"
	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/semantics/proposals"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/session/sessiontest"
	"github.com/SandwichLabs/slaw/internal/skills"
	"github.com/SandwichLabs/slaw/internal/watches"

	_ "github.com/duckdb/duckdb-go/v2" // registers "duckdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/engine"
)

// --- Protocol tests ---

func TestParseRequest_Valid(t *testing.T) {
	data := `{"jsonrpc":"2.0","id":1,"method":"introspect_schema","params":{}}`
	req, err := ParseRequest([]byte(data))
	require.NoError(t, err)
	assert.Equal(t, "2.0", req.JSONRPC)
	assert.Equal(t, float64(1), req.ID) // JSON numbers unmarshal to float64
	assert.Equal(t, "introspect_schema", req.Method)
}

func TestParseRequest_StringID(t *testing.T) {
	data := `{"jsonrpc":"2.0","id":"abc-123","method":"list_queries"}`
	req, err := ParseRequest([]byte(data))
	require.NoError(t, err)
	assert.Equal(t, "abc-123", req.ID)
	assert.Equal(t, "list_queries", req.Method)
}

func TestParseRequest_InvalidJSON(t *testing.T) {
	data := `{not valid json}`
	_, err := ParseRequest([]byte(data))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse JSON-RPC request")
}

func TestParseRequest_WrongVersion(t *testing.T) {
	data := `{"jsonrpc":"1.0","id":1,"method":"test"}`
	_, err := ParseRequest([]byte(data))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON-RPC version")
}

func TestParseRequest_MissingMethod(t *testing.T) {
	data := `{"jsonrpc":"2.0","id":1}`
	_, err := ParseRequest([]byte(data))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing method")
}

func TestResponseMarshal(t *testing.T) {
	resp := NewResponse(1, map[string]any{"tables": []string{"users"}})
	data, err := resp.Marshal()
	require.NoError(t, err)

	var parsed Response
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)
	assert.Equal(t, "2.0", parsed.JSONRPC)
	assert.Nil(t, parsed.Error)
}

func TestErrorResponseMarshal(t *testing.T) {
	resp := NewErrorResponse(1, ErrMethodNotFound, "unknown method")
	data, err := resp.Marshal()
	require.NoError(t, err)

	var parsed Response
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)
	assert.Equal(t, "2.0", parsed.JSONRPC)
	assert.NotNil(t, parsed.Error)
	assert.Equal(t, ErrMethodNotFound, parsed.Error.Code)
}

// --- Tools tests ---

func TestGetTool_Exists(t *testing.T) {
	tool := GetTool("introspect_schema")
	require.NotNil(t, tool)
	assert.Equal(t, "introspect", tool.Category)
}

func TestGetTool_NotFound(t *testing.T) {
	tool := GetTool("nonexistent_method")
	assert.Nil(t, tool)
}

func TestListTools(t *testing.T) {
	tools := ListTools()
	assert.Len(t, tools, len(Tools))
	// Verify we got a copy, not a reference.
	tools[0].Name = "modified"
	assert.NotEqual(t, "modified", Tools[0].Name)
}

// --- Sandbox tests ---

func TestSandbox_ReadOnlyBlocksWrite(t *testing.T) {
	sc := DefaultSandboxConfig()
	assert.Equal(t, ModeReadOnly, sc.Mode)

	// Read operations should be allowed.
	assert.NoError(t, sc.Check("introspect_schema"))
	assert.NoError(t, sc.Check("describe_table"))
	assert.NoError(t, sc.Check("list_queries"))

	// run_query is exempt from the coarse category gate — its permission is
	// per-statement (see TestCheckSQLClass / TestHandleRunQuery_SandboxGating
	// in sql_classify_test.go): read-only allows SELECT but denies DML/DDL at
	// dispatch time, inside handleRunQuery.
	assert.NoError(t, sc.Check("run_query"))
	// Structured data write operations should still be blocked outright.
	assert.Error(t, sc.Check("create_record"))
	assert.Error(t, sc.Check("update_record"))
	assert.Error(t, sc.Check("delete_record"))
	assert.Error(t, sc.Check("bulk_insert"))

	// Content operations should be blocked.
	assert.Error(t, sc.Check("create_or_update_skill"))
	assert.Error(t, sc.Check("add_query_cell"))
	assert.Error(t, sc.Check("create_dashboard"))

	// Schema operations should be blocked.
	assert.Error(t, sc.Check("create_table"))
	assert.Error(t, sc.Check("add_column"))
	assert.Error(t, sc.Check("drop_table"))
}

func TestSandbox_ReadWriteAllowsDataAndContentButBlocksSchema(t *testing.T) {
	sc := SandboxConfig{Mode: ModeReadWrite}

	// Introspect and data should be allowed.
	assert.NoError(t, sc.Check("introspect_schema"))
	assert.NoError(t, sc.Check("run_query"))
	assert.NoError(t, sc.Check("create_record"))
	assert.NoError(t, sc.Check("update_record"))
	assert.NoError(t, sc.Check("delete_record"))

	// Content (notebook cells, skills) is allowed, so a read-write agent can
	// write up what it finds (#111).
	assert.NoError(t, sc.Check("add_query_cell"))
	assert.NoError(t, sc.Check("create_dashboard"))
	assert.NoError(t, sc.Check("create_or_update_skill"))

	// Schema stays blocked.
	assert.Error(t, sc.Check("create_table"))
	assert.Error(t, sc.Check("drop_table"))
}

func TestSandbox_FullAllowsEverything(t *testing.T) {
	sc := SandboxConfig{Mode: ModeFull}

	assert.NoError(t, sc.Check("introspect_schema"))
	assert.NoError(t, sc.Check("run_query"))
	assert.NoError(t, sc.Check("create_record"))
	assert.NoError(t, sc.Check("add_query_cell"))
	assert.NoError(t, sc.Check("create_dashboard"))
	assert.NoError(t, sc.Check("create_table"))
	assert.NoError(t, sc.Check("drop_table"))
}

func TestSandbox_BlockedOperations(t *testing.T) {
	sc := SandboxConfig{
		Mode:              ModeFull,
		BlockedOperations: []string{"drop_table"},
	}

	assert.NoError(t, sc.Check("create_table"))
	assert.Error(t, sc.Check("drop_table"))
	assert.Contains(t, sc.Check("drop_table").Error(), "blocked")
}

func TestSandbox_TableAllowlist(t *testing.T) {
	sc := SandboxConfig{
		Mode:          ModeReadWrite,
		AllowedTables: []string{"users", "orders"},
	}

	assert.True(t, sc.IsTableAllowed("users"))
	assert.True(t, sc.IsTableAllowed("orders"))
	assert.False(t, sc.IsTableAllowed("secrets"))
}

func TestSandbox_EmptyAllowlistAllowsAll(t *testing.T) {
	sc := SandboxConfig{Mode: ModeReadWrite}
	assert.True(t, sc.IsTableAllowed("anything"))
}

// --- Mock implementations for handler tests ---

// mockAccess is a WorkspaceAccess over an in-memory DuckDB handle with
// in-memory content stores (the notebooks/queries/skills Mocks). Set nbs, qs
// or sks to swap in another Store (a FileStore, say).
type mockAccess struct {
	db     *sql.DB
	duckDB *sql.DB

	mu    sync.Mutex
	nbs   notebooks.Store
	qs    queries.Store
	sks   skills.Store
	sem   semantics.Mock
	props proposals.Mock
	ars   artifacts.Store
	wts   watches.Mock
	whist watches.HistoryReader
}

func (m *mockAccess) Watches() watches.WriteStore { return &m.wts }

// WatchHistory is a history with nothing in it until a test sets whist.
func (m *mockAccess) WatchHistory() watches.HistoryReader {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.whist == nil {
		m.whist = emptyHistory{}
	}
	return m.whist
}

type emptyHistory struct{}

func (emptyHistory) Entries(context.Context, string) ([]watches.Entry, error) {
	return []watches.Entry{}, nil
}

func (m *mockAccess) Artifacts() artifacts.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ars == nil {
		m.ars = &artifacts.Mock{}
	}
	return m.ars
}

func (m *mockAccess) Notebooks() notebooks.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nbs == nil {
		m.nbs = &notebooks.Mock{}
	}
	return m.nbs
}

func (m *mockAccess) Queries() queries.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.qs == nil {
		m.qs = &queries.Mock{}
	}
	return m.qs
}

func (m *mockAccess) Semantics() semantics.ReadStore { return &m.sem }

func (m *mockAccess) Proposals() proposals.Proposer { return &m.props }

func (m *mockAccess) Skills() skills.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sks == nil {
		m.sks = &skills.Mock{}
	}
	return m.sks
}

func (m *mockAccess) DuckDBDB() *sql.DB {
	if m.duckDB != nil {
		return m.duckDB
	}
	return m.db
}

type mockEngine struct{}

func (m *mockEngine) Execute(_ context.Context, sqlStr string, params map[string]any) (*engine.ResultSet, error) {
	return &engine.ResultSet{
		Columns: []engine.Column{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}},
		Rows:    [][]any{{1, "test"}},
	}, nil
}

func newTestHandler(t *testing.T, mode SandboxMode) (*Handler, *sql.DB) {
	t.Helper()

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name VARCHAR, email VARCHAR);
		INSERT INTO users (id, name, email) VALUES (1, 'Alice', 'alice@example.com');
	`)
	require.NoError(t, err)

	sandbox := &SandboxConfig{
		Mode:         mode,
		MaxQueryRows: 100,
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	container := &mockAccess{db: db}
	require.NoError(t, container.Queries().Create(context.Background(), &queries.Query{
		ID: "q1", Title: "all_users", SQL: "SELECT * FROM users", Engine: "duckdb", Parameters: []queries.Param{},
	}))
	engine := &mockEngine{}

	handler := NewHandler(container, engine, sandbox, logger)

	t.Cleanup(func() {
		db.Close()
	})

	return handler, db
}

// --- Handler dispatch tests ---

func TestHandler_DispatchIntrospectSchema(t *testing.T) {
	// Only that the handler dispatches without panicking.
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "introspect_schema",
		Params:  map[string]any{},
	}
	resp := handler.Handle(ctx, req)

	require.NotNil(t, resp)
	require.Nil(t, resp.Error, "%+v", resp.Error)
	out, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	assert.Contains(t, string(out), "users")
}

func TestHandler_DispatchDescribeTable(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "describe_table",
		Params:  map[string]any{"table": "users"},
	}
	resp := handler.Handle(ctx, req)

	require.NotNil(t, resp)
	require.Nil(t, resp.Error, "%+v", resp.Error)
	out, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	assert.Contains(t, string(out), "email")
}

// --- present_data (#67) ---

func TestHandler_PresentData_ValidPageAllowedInReadOnly(t *testing.T) {
	// present_data is "introspect" category and writes nothing, so it must
	// be available even in the most restricted sandbox.
	handler, _ := newTestHandler(t, ModeReadOnly)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "present_data",
		Params: map[string]any{
			"page": map[string]any{
				"title": "Customers",
				"components": []any{
					map[string]any{"id": "h", "type": "heading", "text": "Customers"},
				},
			},
		},
	}
	resp := handler.Handle(ctx, req)
	require.Nil(t, resp.Error)
	out, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"title":"Customers"`)
}

func TestHandler_PresentData_InvalidPageDenied(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "present_data",
		Params: map[string]any{
			"page": map[string]any{
				"title":      "Bad",
				"components": []any{map[string]any{"id": "a", "type": "pieChart"}},
			},
		},
	}
	resp := handler.Handle(ctx, req)
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "pieChart")
}

func TestHandler_PresentData_RefusesViewOnlyComponents(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	resp := handler.Handle(context.Background(), &Request{
		JSONRPC: "2.0", ID: 1, Method: "present_data",
		Params: map[string]any{"page": map[string]any{
			"title":      "Controls",
			"components": []any{map[string]any{"id": "a", "type": "select", "param": "region"}},
		}},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "only allowed in a data artifact view")
}

func TestHandler_PresentData_ToleratesJSONStringPage(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "present_data",
		Params: map[string]any{
			"page": `{"title":"x","components":[{"id":"a","type":"text","text":"hi"}]}`,
		},
	}
	resp := handler.Handle(ctx, req)
	require.Nil(t, resp.Error)
}

func TestHandler_DispatchListQueries(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      5,
		Method:  "list_queries",
		Params:  map[string]any{},
	}
	resp := handler.Handle(ctx, req)

	assert.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	queries, _ := result["queries"].([]map[string]any)
	assert.Len(t, queries, 1)
	assert.Equal(t, "all_users", queries[0]["name"])
}

func TestHandler_DispatchRunQuery(t *testing.T) {
	// run_query is classified against the live DuckDB catalog (#116), so this one
	// uses the real-DuckDB handler.
	handler, _ := newRealDuckDBHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      6,
		Method:  "run_query",
		Params:  map[string]any{"sql": "SELECT * FROM t"},
	}
	resp := handler.Handle(ctx, req)

	assert.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	assert.NotNil(t, result["columns"])
	assert.NotNil(t, result["rows"])
}

func TestHandler_UnknownMethod(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      7,
		Method:  "nonexistent",
		Params:  map[string]any{},
	}
	resp := handler.Handle(ctx, req)

	assert.NotNil(t, resp.Error)
	assert.Equal(t, ErrMethodNotFound, resp.Error.Code)
}

func TestHandler_MissingRequiredParam(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      8,
		Method:  "describe_table",
		Params:  map[string]any{}, // missing "table"
	}
	resp := handler.Handle(ctx, req)

	assert.NotNil(t, resp.Error)
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)
}

func TestHandler_SandboxPermissionDenied(t *testing.T) {
	handler, _ := newTestHandler(t, ModeReadOnly)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      9,
		Method:  "create_record",
		Params:  map[string]any{"table": "users", "values": map[string]any{"name": "Bob"}},
	}
	resp := handler.Handle(ctx, req)

	assert.NotNil(t, resp.Error)
	assert.Equal(t, ErrPermission, resp.Error.Code)
}

func TestHandler_TableAllowlistEnforced(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	sb := *handler.sandbox.Load()
	sb.AllowedTables = []string{"orders"}
	handler.sandbox.Store(&sb)
	ctx := context.Background()

	req := &Request{
		JSONRPC: "2.0",
		ID:      10,
		Method:  "describe_table",
		Params:  map[string]any{"table": "users"},
	}
	resp := handler.Handle(ctx, req)

	assert.NotNil(t, resp.Error)
	assert.Equal(t, ErrPermission, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "not in the allowed tables list")
}

func TestHandler_CreateAndDeleteRecord(t *testing.T) {
	// create_record/delete_record now route through the real-DuckDB SQL
	// classifier (requireWriteStatement) — a bare fixture can't
	// satisfy classifySQL's EXPLAIN (FORMAT JSON)/catalog queries, so this
	// needs the real-DuckDB handler, same as the run_query tests.
	handler, _ := newRealDuckDBHandler(t, ModeFull)
	ctx := context.Background()

	// Create
	req := &Request{
		JSONRPC: "2.0",
		ID:      11,
		Method:  "create_record",
		Params:  map[string]any{"table": "users", "values": map[string]any{"name": "Bob", "email": "bob@example.com"}},
	}
	resp := handler.Handle(ctx, req)
	assert.Nil(t, resp.Error)
	created, _ := resp.Result.(map[string]any)
	assert.Equal(t, true, created["created"])
	assert.Equal(t, int64(1), created["rows_affected"])
	assert.NotContains(t, created, "last_insert_id", "DuckDB has no last-insert id; the field would always be 0")

	// Delete
	req = &Request{
		JSONRPC: "2.0",
		ID:      12,
		Method:  "delete_record",
		Params:  map[string]any{"table": "users", "where": "name = 'Bob'"},
	}
	resp = handler.Handle(ctx, req)
	assert.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	assert.Equal(t, int64(1), result["rows_affected"])
}

func TestHandler_CreateAndUpdateSkill(t *testing.T) {
	handler, _ := newTestHandler(t, ModeFull)
	ctx := context.Background()

	// Create
	req := &Request{
		JSONRPC: "2.0",
		ID:      13,
		Method:  "create_or_update_skill",
		Params:  map[string]any{"name": "about-us", "description": "About us", "instructions": "# About"},
	}
	resp := handler.Handle(ctx, req)
	assert.Nil(t, resp.Error)

	// Update (upsert by name)
	req = &Request{
		JSONRPC: "2.0",
		ID:      14,
		Method:  "create_or_update_skill",
		Params:  map[string]any{"name": "about-us", "description": "About us", "instructions": "# About Us"},
	}
	resp = handler.Handle(ctx, req)
	assert.Nil(t, resp.Error)

	// Verify
	req = &Request{
		JSONRPC: "2.0",
		ID:      15,
		Method:  "read_skill",
		Params:  map[string]any{"name": "about-us"},
	}
	resp = handler.Handle(ctx, req)
	assert.Nil(t, resp.Error)
	result, _ := resp.Result.(map[string]any)
	assert.Contains(t, result["instructions"], "# About Us")
}

// TestHandler_SetFilePolicyConcurrentWithHandle is a regression guard for a
// data race a reviewer caught live: studio/app.go's agentToolHandler used to
// mutate a *SandboxConfig's Mode field in place on every dispatch so a
// long-lived Handler (the one StartMCPServer builds for the whole life of
// the MCP server) would track a live SetAgentConfig change — but that shared
// mutable struct raced with concurrent Handle() calls reading the same
// fields. SetFilePolicy (atomic.Pointer swap) replaces that; this test
// hammers both concurrently under `go test -race` to prove there is no data
// race, and that every call still observes some valid, fully-formed sandbox
// (never a torn/partial config).
func TestHandler_SetFilePolicyConcurrentWithHandle(t *testing.T) {
	handler, _ := newTestHandler(t, ModeReadOnly)
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Flip the sandbox mode back and forth concurrently with dispatch.
	wg.Add(1)
	go func() {
		defer wg.Done()
		modes := []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull}
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				handler.SetFilePolicy(modes[i%len(modes)], false)
				i++
			}
		}
	}()

	// Concurrently dispatch a read tool (always allowed) and a write tool
	// (allowed only in read-write/full) — neither must ever panic or race.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				handler.Handle(ctx, &Request{JSONRPC: "2.0", ID: j, Method: "introspect_schema", Params: map[string]any{}})
				handler.Handle(ctx, &Request{JSONRPC: "2.0", ID: j, Method: "create_or_update_skill", Params: map[string]any{"name": "x", "description": "d", "instructions": "y"}})
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestTighter covers the strictness ordering Tighter relies on: read-only <
// read-write < full, symmetric in its two arguments, and an unrecognized
// mode is returned as-is (SandboxConfig.Check fails closed on it, so this
// can only ever deny more, never grant unintended access).
func TestTighter(t *testing.T) {
	cases := []struct {
		a, b SandboxMode
		want SandboxMode
	}{
		{ModeReadOnly, ModeFull, ModeReadOnly},
		{ModeFull, ModeReadOnly, ModeReadOnly},
		{ModeReadWrite, ModeFull, ModeReadWrite},
		{ModeFull, ModeReadWrite, ModeReadWrite},
		{ModeReadOnly, ModeReadWrite, ModeReadOnly},
		{ModeFull, ModeFull, ModeFull},
		{ModeReadOnly, ModeReadOnly, ModeReadOnly},
		{SandboxMode("bogus"), ModeFull, SandboxMode("bogus")},
		{ModeFull, SandboxMode("bogus"), SandboxMode("bogus")},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, Tighter(c.a, c.b), "Tighter(%q, %q)", c.a, c.b)
	}
}

func (m *mockAccess) Session() session.Session {
	if db := m.DuckDBDB(); db != nil {
		return sessiontest.ForTests(db)
	}
	return nil
}
