package agents

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session"
)

// #3: the render-time read-only gate hides the classifier's reason (it can name
// a tier requirement that is false at a render-time gate), except a reason that
// is the same in every tier: an empty statement, or a statement DuckDB cannot
// parse or bind.

func TestReadOnlyGateShowsTierIndependentReasons(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			fx := newPolicyFixture(t, mode)
			ctx := context.Background()
			run := func(q string) error {
				_, _, err := fx.h.RunReadOnlySQLIn(ctx, "", q, nil, 10)
				return err
			}

			for _, empty := range []string{"", "   \n\t", " ; "} {
				err := run(empty)
				require.Error(t, err, "%q", empty)
				assert.Equal(t, "the statement is empty", err.Error(), "%q", empty)
				assert.ErrorIs(t, err, ErrReadOnlyBindingDenied, "callers' not-a-read branch still matches")
			}

			err := run("SELECT no_such_column FROM (SELECT 1 AS a)")
			require.Error(t, err)
			assert.NotEqual(t, ErrReadOnlyBindingDenied.Error(), err.Error())
			assert.Contains(t, err.Error(), "no_such_column")
			assert.Contains(t, err.Error(), "Binder Error")
			assert.ErrorIs(t, err, ErrReadOnlyBindingDenied)

			err = run("SELECT * FROM (SELECT 1")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "Parser Error")
		})
	}
}

// Every other denial keeps the one fixed message, in every tier: a write, DDL, a
// file outside data/.
func TestReadOnlyGateStillMasksTierDependentReasons(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			fx := newPolicyFixture(t, mode)
			for _, q := range []string{
				"CREATE TABLE zz_new (a INTEGER)",
				"DROP TABLE IF EXISTS zz_new",
				"SELECT * FROM read_csv('" + filepath.Join(fx.outside, "secret.csv") + "')",
			} {
				_, _, err := fx.h.RunReadOnlySQLIn(context.Background(), "", q, nil, 10)
				require.Error(t, err, q)
				assert.Same(t, ErrReadOnlyBindingDenied, err, q)
			}
		})
	}
}

func TestCheckSQLClassReadOnlyUsesTheVerdictMark(t *testing.T) {
	sc := &SandboxConfig{Mode: ModeFull}
	assert.NoError(t, sc.checkSQLClassReadOnly(&sqlVerdict{Class: sqlClassRead}))
	// An unmarked reason (it could name a tier) is dropped, even one that looks like a statement problem.
	assert.Same(t, ErrReadOnlyBindingDenied, sc.checkSQLClassReadOnly(&sqlVerdict{Class: sqlClassOther, Reason: "the statement is empty"}))
	assert.Same(t, ErrReadOnlyBindingDenied, sc.checkSQLClassReadOnly(deny("could not validate query: Binder Error: x")))
	// A marked reason with no text has nothing to show.
	assert.Same(t, ErrReadOnlyBindingDenied, sc.checkSQLClassReadOnly(&sqlVerdict{Class: sqlClassOther, Kind: defStatement}))
	err := sc.checkSQLClassReadOnly(denyDefinition("the statement is empty"))
	assert.EqualError(t, err, "the statement is empty")
	assert.ErrorIs(t, err, ErrReadOnlyBindingDenied)
	var dp interface{ DefinitionProblem() bool }
	assert.True(t, errors.As(err, &dp))
}

// An I/O failure while planning is not about the statement: it is not marked.
func TestIsStatementErrorIsTypedNotTextual(t *testing.T) {
	assert.Equal(t, defUnchecked, failureKind(errors.New("Binder Error: looks like one but is not typed")))
	fx := newPolicyFixture(t, ModeReadOnly)
	_, err := fx.sess.DB().Exec("SELECT no_such_column")
	require.Error(t, err)
	assert.NotZero(t, failureKind(err)&defStatement)
	_, err = fx.sess.DB().Exec("SELEC 1")
	require.Error(t, err)
	assert.NotZero(t, failureKind(err)&defStatement)
}

// #176: "unknown connection" is for a name nothing defines. A defined
// connection that failed to connect reports its own status text.
func TestClassifyInSaysUnknownOnlyForAnUndefinedConnection(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	ctx := context.Background()
	missing := filepath.Join(fx.dir, "nope", "missing.duckdb")
	require.NoError(t, fx.sess.Register(session.Spec{Name: "gone", Type: "duckdb", Path: missing, ReadOnly: true}))
	// The failed dial gives the connection a status message of its own.
	require.Error(t, fx.sess.ConnectReferenced(ctx, "gone", ""))
	var msg string
	for _, st := range fx.sess.Connections() {
		if st.Name == "gone" {
			msg = st.Message
		}
	}
	require.NotEmpty(t, msg, "the failed connect left a status message")

	_, err := fx.h.classifyIn(ctx, "gone", "SELECT 1", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "unknown connection")
	assert.NotContains(t, err.Error(), "is not attached")
	assert.Equal(t, `connection "gone": `+fx.sess.Redact(msg), err.Error())
	var ce *ConnectError
	assert.ErrorAs(t, err, &ce)

	_, err = fx.h.classifyIn(ctx, "never_defined", "SELECT 1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown connection "never_defined"`)
	var unknown *session.UnknownCatalogError
	assert.ErrorAs(t, err, &unknown)
}

// A table or connection the statement names that is not there is the same in
// every tier; the system-catalog and external-access refusals stay masked.
func TestReadOnlyGateShowsMissingTablesButMasksSystemAndExternal(t *testing.T) {
	for _, mode := range []SandboxMode{ModeReadOnly, ModeReadWrite, ModeFull} {
		t.Run(string(mode), func(t *testing.T) {
			fx := newPolicyFixture(t, mode)
			run := func(q string) error {
				_, _, err := fx.h.RunReadOnlySQLIn(context.Background(), "", q, nil, 10)
				return err
			}
			err := run("SELECT * FROM ordrs")
			require.Error(t, err)
			assert.Equal(t, `table "ordrs" not found`, err.Error())
			var mo interface{ MissingObject() bool }
			require.ErrorAs(t, err, &mo)
			assert.True(t, mo.MissingObject())

			err = run("SELECT * FROM nosuch.main.t")
			require.Error(t, err)
			assert.Contains(t, err.Error(), `there is no connection named "nosuch"`)

			for _, q := range []string{"SELECT * FROM system.main.no_such_object", "SELECT * FROM read_csv('/etc/hostname')"} {
				assert.Same(t, ErrReadOnlyBindingDenied, run(q), q)
			}
		})
	}
}

// A parser error quotes the text near the failure: it is redacted like every
// other engine error.
func TestReadOnlyGateRedactsParserErrors(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	_, _, err := fx.h.RunReadOnlySQLIn(context.Background(), "", "SELECT * FROM t1 t2 'password=hunter2'", nil, 10)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")
	assert.Contains(t, err.Error(), "Parser Error")
}

// The person-side vet reports an engine-reported problem as a BindFailure, a
// policy-found one as a DefinitionError, both a DefinitionProblem.
func TestVetReadOnlySQLInTypesStatementProblems(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	ctx := context.Background()
	var bf interface{ BindFailure() bool }
	var dp interface{ DefinitionProblem() bool }

	err := fx.h.VetReadOnlySQLIn(ctx, "", "SELECT no_such_column FROM (SELECT 1 AS a)", nil)
	require.ErrorAs(t, err, &bf)
	assert.True(t, bf.BindFailure())

	for _, q := range []string{"SELECT * FROM ordrs", ""} {
		err = fx.h.VetReadOnlySQLIn(ctx, "", q, nil)
		require.Error(t, err, q)
		require.ErrorAs(t, err, &dp, q)
		assert.True(t, dp.DefinitionProblem())
		assert.False(t, errors.As(err, &bf), q)
	}
}

// preview_query: a statement that does not bind is a failed query, not a
// permission problem.
func TestPreviewQueryReportsBinderErrorsAsFailedQueries(t *testing.T) {
	fx := newPolicyFixture(t, ModeReadOnly)
	resp := fx.call("preview_query", map[string]any{"sql": "SELECT no_such_column FROM (SELECT 1 AS a)"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrInvalidParams, resp.Error.Code)
	assert.True(t, strings.HasPrefix(resp.Error.Message, "query failed: "), resp.Error.Message)
	assert.Contains(t, resp.Error.Message, "no_such_column")

	resp = fx.call("preview_query", map[string]any{"sql": "CREATE TABLE zz_new (a INTEGER)"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrPermission, resp.Error.Code)
}
