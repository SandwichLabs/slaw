package mcp

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"

	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/semantics"
	"github.com/SandwichLabs/slaw/internal/semantics/proposals"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/session/sessiontest"
	"github.com/SandwichLabs/slaw/internal/skills"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/agents"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/llm"
)

type mockAccess struct {
	db *sql.DB

	nbs   notebooks.Mock
	qs    queries.Mock
	sks   skills.Mock
	sem   semantics.Mock
	props proposals.Mock
}

func (m *mockAccess) DuckDBDB() *sql.DB              { return m.db }
func (m *mockAccess) Notebooks() notebooks.Store     { return &m.nbs }
func (m *mockAccess) Queries() queries.Store         { return &m.qs }
func (m *mockAccess) Skills() skills.Store           { return &m.sks }
func (m *mockAccess) Semantics() semantics.ReadStore { return &m.sem }

func (m *mockAccess) Proposals() proposals.Proposer { return &m.props }

type mockEngine struct{}

func (m *mockEngine) Execute(_ context.Context, sqlStr string, params map[string]any) (*engine.ResultSet, error) {
	return &engine.ResultSet{
		Columns: []engine.Column{{Name: "id", Type: "INTEGER"}},
		Rows:    [][]any{{1}},
	}, nil
}

func TestToolRegistration(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()

	sandbox := &agents.SandboxConfig{Mode: agents.ModeFull, MaxQueryRows: 100}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := agents.NewHandler(&mockAccess{db: db}, &mockEngine{}, sandbox, logger)

	srv := New(handler, logger)
	require.NotNil(t, srv)

	allTools := agents.ListTools()
	assert.Greater(t, len(allTools), 0)

	for _, td := range allTools {
		mcpTool := toolDefToMCPTool(td)
		assert.Equal(t, td.Name, mcpTool.Name)
		assert.Equal(t, agents.ForMCP(td).Description, mcpTool.Description)
	}
}

// TestToolRegistration_SkillsToolsPresent (#62): list_skills and read_skill
// (and the authoring tool) flow onto the MCP tool list automatically because
// they're ToolDefs in the shared registry — no MCP-specific wiring needed.
func TestToolRegistration_SkillsToolsPresent(t *testing.T) {
	names := map[string]bool{}
	for _, td := range agents.ListTools() {
		names[td.Name] = true
	}
	assert.True(t, names["list_skills"])
	assert.True(t, names["read_skill"])
	assert.True(t, names["create_or_update_skill"])
}

func TestStartAndStop(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()

	sandbox := &agents.SandboxConfig{Mode: agents.ModeFull, MaxQueryRows: 100}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := agents.NewHandler(&mockAccess{db: db}, &mockEngine{}, sandbox, logger)

	srv := New(handler, logger)
	err = srv.Start("127.0.0.1:0")
	require.NoError(t, err)

	addr := srv.Addr()
	assert.NotEmpty(t, addr)
	t.Logf("MCP server listening on %s", addr)

	err = srv.Stop()
	assert.NoError(t, err)
	assert.Empty(t, srv.Addr())
}

// TestToolSchemas_MatchRunner asserts the MCP bridge and internal/llm's
// fantasy tool wrapper advertise byte-identical schemas for every tool in
// the shared registry — both now build from agents.ParamsToJSONSchema, so a
// divergence here would mean one of the two stopped using it (#63 review
// fix: "one tool surface" also means one schema).
func TestToolSchemas_MatchRunner(t *testing.T) {
	for _, td := range agents.ListTools() {
		mcpTool := toolDefToMCPTool(td)
		info := llm.ToolInfo(agents.ForMCP(td)) // same schema builder; only the Studio-only wording differs

		assert.Equal(t, td.Name, info.Name)
		assert.Equal(t, mcpTool.Description, info.Description, "tool %s: description diverged", td.Name)
		assert.Equal(t, mcpTool.InputSchema.Properties, info.Parameters, "tool %s: MCP and runner schemas diverged", td.Name)
		assert.ElementsMatch(t, mcpTool.InputSchema.Required, info.Required, "tool %s: required params diverged", td.Name)
	}
}

// TestToolDefToMCPTool_ArraysCarryItems guards against the Gemini function-
// calling rejection: every array-typed param in the shared registry must
// render a concrete "items" schema, never a bare {"type":"array"}.
func TestToolDefToMCPTool_ArraysCarryItems(t *testing.T) {
	for _, td := range agents.ListTools() {
		mcpTool := toolDefToMCPTool(td)
		for _, p := range td.Params {
			if p.Type != "array" {
				continue
			}
			prop, ok := mcpTool.InputSchema.Properties[p.Name].(map[string]any)
			require.Truef(t, ok, "%s.%s: expected a schema object", td.Name, p.Name)
			assert.Containsf(t, prop, "items", "%s.%s: array param must declare items (Gemini rejects a bare array schema)", td.Name, p.Name)
		}
	}
}

func (m *mockAccess) Session() session.Session {
	if m.db != nil {
		return sessiontest.ForTests(m.db)
	}
	return nil
}
