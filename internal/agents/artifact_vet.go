package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/artifacts"
	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/receipts"
)

// This file is the save-time vet of a data artifact (D5, epic #162). The
// structure of a manifest is internal/artifacts' job; whether its queries may
// run is the agent SQL policy's, so the vet lives here. The run-time gate
// (RunArtifactQuery) repeats every check below, because a file can arrive by
// git, an editor or a bundle without ever passing through the agent.

// ArtifactVetError is a manifest whose queries did not pass the agent's read
// policy. Problems has one entry per refused query, each naming the query and
// giving the policy's reason, so a caller can show them as a list.
type ArtifactVetError struct {
	Problems []string
}

func (e *ArtifactVetError) Error() string {
	return "the artifact's queries did not pass the agent's read policy:\n" + strings.Join(e.Problems, "\n")
}

// ArtifactColumn is one result column of a vetted query.
type ArtifactColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ArtifactQueryColumns is what one query returns, in column order.
type ArtifactQueryColumns struct {
	Name    string           `json:"name"`
	Columns []ArtifactColumn `json:"columns"`
}

// VetArtifact runs every query of m (structurally valid already) through the
// same read-only policy a run goes through, with each parameter at its default:
// the connection is validated, the lexical checks run, then wildcard expansion,
// classification and the read-only gate (readOnlyVerdict). A refusal is an
// *ArtifactVetError naming each failing query and the policy's reason.
func (h *Handler) VetArtifact(ctx context.Context, m *artifacts.Manifest) error {
	_, err := h.VetArtifactColumns(ctx, m)
	return err
}

// VetArtifactColumns is VetArtifact that also returns each passing query's
// result columns (name and DuckDB type), read by binding the query under the
// policy that just passed it with a zero-row limit: no row is read. A query
// whose columns could not be read is left out of the list (not an error: the
// policy passed it).
func (h *Handler) VetArtifactColumns(ctx context.Context, m *artifacts.Manifest) ([]ArtifactQueryColumns, error) {
	var problems []string
	var out []ArtifactQueryColumns
	for _, q := range m.Queries {
		cols, err := h.vetArtifactQuery(ctx, m, q.Name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("query %q: %s", q.Name, redactForAgent(err.Error())))
			continue
		}
		if cols != nil {
			out = append(out, ArtifactQueryColumns{Name: q.Name, Columns: cols})
		}
	}
	if len(problems) > 0 {
		return nil, &ArtifactVetError{Problems: problems}
	}
	return out, nil
}

// vetArtifactQuery vets one query with its defaults bound, on the connection
// it will run on, and reads its result columns.
func (h *Handler) vetArtifactQuery(ctx context.Context, m *artifacts.Manifest, name string) ([]ArtifactColumn, error) {
	sqlText, connection, params, err := artifacts.Bind(m, name, nil, time.Now())
	if err != nil {
		return nil, err
	}
	runConn, err := h.checkArtifactStatement(sqlText, connection)
	if err != nil {
		return nil, err
	}
	runSQL, params, runCtx, den, err := h.readOnlyVerdict(ctx, runConn, sqlText, params)
	if err != nil {
		if errors.Is(err, ErrReadOnlyBindingDenied) && den.Reason != "" {
			return nil, errors.New(den.Reason)
		}
		return nil, err
	}
	return h.describeQueryColumns(runCtx, runConn, runSQL, params), nil
}

// describeQueryColumns binds a statement the policy has passed and returns its
// result columns, or nil when they cannot be read. It runs the statement under
// "LIMIT 0", the shape a run wraps its row cap in, so the engine plans and
// returns the column list but reads no rows.
func (h *Handler) describeQueryColumns(ctx context.Context, connection, runSQL string, params map[string]any) []ArtifactColumn {
	exec, err := h.executorFor(connection)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, previewQueryTimeout)
	defer cancel()
	wrapped := fmt.Sprintf("SELECT * FROM (%s\n) AS _a2ui_cols LIMIT 0", strings.TrimSuffix(strings.TrimSpace(runSQL), ";"))
	rs, err := exec(ctx, wrapped, params)
	if err != nil || rs == nil {
		h.logger.Debug("artifact query columns not read", "error", engine.ErrorForLog(err))
		return nil
	}
	cols := make([]ArtifactColumn, len(rs.Columns))
	for i, c := range rs.Columns {
		cols[i] = ArtifactColumn{Name: c.Name, Type: c.Type}
	}
	return cols
}

// checkArtifactStatement is the part of the vet that needs no database and
// that the run repeats first: the lexical checks on the SQL, and the query's
// connection resolved to its canonical registered name ("" is the session
// default). Vet and run both resolve the connection here, so a statement is
// never vetted on one connection and run on another.
func (h *Handler) checkArtifactStatement(sqlText, connection string) (string, error) {
	if err := checkArtifactSQLShape(sqlText); err != nil {
		return "", err
	}
	return h.validConnection(connection)
}

// checkArtifactSQLShape holds a query to the two lexical rules of D5 (one
// statement; every $name a parameter token, never the argument of query() or a
// reader). The rule is queries.CheckParamShape, shared with Studio and the CLI.
func checkArtifactSQLShape(sqlText string) error { return queries.CheckParamShape(sqlText) }

// artifactQueryRowCap bounds the rows one artifact query returns, the cap of a
// live A2UI binding. The cap is pushed into the query (RunReadOnlySQLIn).
const artifactQueryRowCap = 500

// ArtifactQueryContext limits a run of an artifact's query, a live A2UI
// binding or a notebook SQL cell (RunA2UIQueryIn serves all of them, and a
// pulled or bundled notebook re-runs every cell on open) to
// artifacts.MaxQueryResultBytes of result, checked while the rows are read.
// Every such path goes through RunReadOnlySQLIn, so a caller outside this
// package (Studio's A2UI binding) wraps its context with it.
func ArtifactQueryContext(ctx context.Context) context.Context {
	return engine.WithMaxResultBytes(ctx, artifacts.MaxQueryResultBytes)
}

// RunArtifactQuery runs one named query of a stored artifact with the given
// parameter values, and is the ONE path that does: Studio's binding, the
// level-2 bridge and any later tool call it. It never trusts the caller for
// anything but names and values:
//
//   - the artifact is loaded from the store (not from the caller), so what
//     runs is what is on disk;
//   - the lexical checks of the save-time vet repeat here, and the query's
//     connection is re-validated, because a file edited by hand or imported
//     in a bundle never passed the vet;
//   - artifacts.Bind turns the values into prepared-statement arguments (a
//     value is never SQL text) and refuses an unknown name or a bad value;
//   - RunReadOnlySQLIn then expands, classifies with the ACTUAL values and
//     applies the read-only gate, so the plan check sees what will run, not
//     the defaults. A denial there is the fixed ErrReadOnlyBindingDenied.
//
// The result belongs to the caller's UI. No agent tool returns it.
func (h *Handler) RunArtifactQuery(ctx context.Context, name, query string, values map[string]any) (*engine.ResultSet, bool, error) {
	store, err := h.artifactStore()
	if err != nil {
		return nil, false, err
	}
	a, err := store.Get(ctx, name)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load artifact %q: %w", name, err)
	}
	return h.RunManifestQuery(ctx, name, &a.Manifest, query, values)
}

// RunManifestQuery is RunArtifactQuery over a manifest the caller already
// holds: the HTML page host renders the files that were hashed, so its queries
// must be the hashed manifest's too, not whatever artifact.yaml says by the
// time a lazy query runs. Every other check is the same: the lexical vet and
// the connection check, Bind, the read-only gate, the row cap. name is only a
// label for the log.
func (h *Handler) RunManifestQuery(ctx context.Context, name string, m *artifacts.Manifest, query string, values map[string]any) (*engine.ResultSet, bool, error) {
	sqlText, connection, params, err := artifacts.Bind(m, query, values, time.Now())
	if err != nil {
		return nil, false, err
	}
	runConn, err := h.checkArtifactStatement(sqlText, connection)
	if err != nil {
		h.logger.Debug("artifact query refused", "artifact", name, "query", query, "reason", engine.ErrorForLog(err))
		return nil, false, err
	}
	origin := ArtifactOrigin(name, query)
	return h.RunReadOnlySQLIn(receipts.WithOrigin(ArtifactQueryContext(ctx), origin), runConn, sqlText, params, artifactQueryRowCap)
}
