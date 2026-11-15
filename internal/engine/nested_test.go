package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"testing"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/session/sessiontest"
)

func nestedEngine(t *testing.T) *DuckDBEngine {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewDuckDBEngine(sessiontest.ForTests(db))
}

// TestNestedValuesThroughDuckDB runs every nested type through the real driver
// and pins the column type, the wire value (as JSON) and the DuckDB-style text
// of the one cell.
func TestNestedValuesThroughDuckDB(t *testing.T) {
	eng := nestedEngine(t)
	const u = "'6d1e5f2a-0b3c-4d5e-8f70-123456789abc'::UUID"
	for _, tc := range []struct {
		name, sql, wantType, wantJSON, wantText string
	}{
		{"list of struct (the #18 report)", `SELECT [{'foo': 'bar'}] AS x`, `STRUCT("foo" VARCHAR)[]`, `[{"foo":"bar"}]`, `[{'foo': bar}]`},
		{"struct keeps declared order in text", `SELECT {'b': 1, 'a': 2} AS x`, `STRUCT("b" INTEGER, "a" INTEGER)`, `{"a":2,"b":1}`, `{'b': 1, 'a': 2}`},
		{"map", `SELECT MAP {'k': 1, 'j': 2} AS x`, `MAP(VARCHAR, INTEGER)`, `{"j":2,"k":1}`, `{j=2, k=1}`},
		{"map with a date key and list value", `SELECT MAP {DATE '2024-01-02': [1]} AS x`, `MAP(DATE, INTEGER[])`, `{"2024-01-02":[1]}`, `{2024-01-02=[1]}`},
		{"array", `SELECT [1,2]::INT[2] AS x`, `INTEGER[2]`, `[1,2]`, `[1, 2]`},
		{"list of lists with NULL", `SELECT [[1,NULL],[3]] AS x`, `INTEGER[][]`, `[[1,null],[3]]`, `[[1, NULL], [3]]`},
		{"union", `SELECT union_value(num := 2) AS x`, `UNION("num" INTEGER)`, `2`, `2`},
		{"union member that is a date", `SELECT [union_value(a := DATE '2024-01-02')::UNION(a DATE, b INT)] AS x`, `UNION("a" DATE, "b" INTEGER)[]`, `["2024-01-02"]`, `[2024-01-02]`},
		{"json object", `SELECT '{"a":[1,2]}'::JSON AS x`, `JSON`, `{"a":[1,2]}`, `{"a":[1,2]}`},
		{"json string stays the decoded string", `SELECT '"abc"'::JSON AS x`, `JSON`, `"abc"`, `abc`},
		{"uuid in a list", `SELECT [` + u + `] AS x`, `UUID[]`, `["6d1e5f2a-0b3c-4d5e-8f70-123456789abc"]`, `[6d1e5f2a-0b3c-4d5e-8f70-123456789abc]`},
		{"date and timestamp in a struct", `SELECT {'d': DATE '2024-01-02', 't': TIMESTAMP '2024-01-02 03:04:05.5'} AS x`, `STRUCT("d" DATE, "t" TIMESTAMP)`, `{"d":"2024-01-02","t":"2024-01-02 03:04:05.5"}`, `{'d': 2024-01-02, 't': 2024-01-02 03:04:05.5}`},
		{"decimal keeps its scale in a list", `SELECT [12.50::DECIMAL(10,2), 3::DECIMAL(10,2)] AS x`, `DECIMAL(10,2)[]`, `[12.50,3.00]`, `[12.50, 3.00]`},
		{"huge int in a struct is exact text", `SELECT {'h': 170141183460469231731687303715884105727::HUGEINT, 'b': 9007199254740993::BIGINT} AS x`, `STRUCT("h" HUGEINT, "b" BIGINT)`, `{"b":"9007199254740993","h":"170141183460469231731687303715884105727"}`, `{'h': 170141183460469231731687303715884105727, 'b': 9007199254740993}`},
		{"quoted field names", `SELECT {'a b': 1, 'q"r': 2} AS x`, `STRUCT("a b" INTEGER, "q""r" INTEGER)`, `{"a b":1,"q\"r":2}`, `{'a b': 1, 'q"r': 2}`},
		{"empty list", `SELECT []::INT[] AS x`, `INTEGER[]`, `[]`, `[]`},
		{"NULL inside a struct", `SELECT {'a': NULL::INT} AS x`, `STRUCT("a" INTEGER)`, `{"a":null}`, `{'a': NULL}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs, err := eng.Execute(context.Background(), tc.sql, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, rs.Columns[0].Type)
			b, err := json.Marshal(rs.Rows[0][0])
			require.NoError(t, err)
			assert.JSONEq(t, tc.wantJSON, string(b))
			assert.Equal(t, tc.wantText, CellTextTyped(rs.Rows[0][0], "NULL", rs.Columns[0].Type))
		})
	}
}

func TestNormalizeColumnValueNestedUnits(t *testing.T) {
	uuid := []byte{0x6d, 0x1e, 0x5f, 0x2a, 0x0b, 0x3c, 0x4d, 0x5e, 0x8f, 0x70, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc}
	day := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	t.Run("struct field types", func(t *testing.T) {
		got := NormalizeColumnValue(map[string]any{"u": uuid, "d": day}, `STRUCT("u" UUID, "d" DATE)`)
		assert.Equal(t, map[string]any{"u": "6d1e5f2a-0b3c-4d5e-8f70-123456789abc", "d": "2024-01-02"}, got)
	})
	t.Run("duckdb.Map with typed keys and values", func(t *testing.T) {
		got := NormalizeColumnValue(duckdb.Map{day: []any{day}}, "MAP(DATE, DATE[])")
		assert.Equal(t, map[string]any{"2024-01-02": []any{"2024-01-02"}}, got)
	})
	t.Run("duckdb.Union with a tag", func(t *testing.T) {
		got := NormalizeColumnValue(duckdb.Union{Tag: "a", Value: day}, `UNION("a" DATE, "b" INTEGER)`)
		assert.Equal(t, "2024-01-02", got)
	})
	t.Run("bare union value picks the member that fits", func(t *testing.T) {
		assert.Equal(t, "2024-01-02", NormalizeColumnValue(day, `UNION("n" INTEGER, "d" DATE)`))
		assert.Equal(t, int32(7), NormalizeColumnValue(int32(7), `UNION("n" INTEGER, "d" DATE)`))
	})
	t.Run("a shape the type does not describe falls back", func(t *testing.T) {
		assert.Equal(t, "x", NormalizeColumnValue("x", "INTEGER[]"))
		assert.Equal(t, map[string]any{"k": "nan"}, NormalizeColumnValue(map[string]any{"k": math.NaN()}, `STRUCT("k" DOUBLE)`))
	})
	t.Run("spelled-out zone names", func(t *testing.T) {
		got := NormalizeColumnValue([]any{day}, "TIMESTAMP WITH TIME ZONE[]")
		assert.Equal(t, []any{timestampTZText(day)}, got)
	})
}

func TestParseColType(t *testing.T) {
	ty := parseColType(`STRUCT("a" MAP(VARCHAR, DECIMAL(10,2)[]), "b, c" UNION("x" INTEGER, "y" DATE))[3]`)
	require.Equal(t, kindList, ty.kind)
	st := ty.elem
	require.Equal(t, kindStruct, st.kind)
	require.Len(t, st.fields, 2)
	assert.Equal(t, "a", st.fields[0].name)
	m := st.fields[0].typ
	require.Equal(t, kindMap, m.kind)
	assert.Equal(t, "VARCHAR", m.key.base)
	assert.Equal(t, kindList, m.elem.kind)
	assert.Equal(t, "DECIMAL(10,2)", m.elem.elem.base)
	assert.Equal(t, "b, c", st.fields[1].name)
	assert.Equal(t, kindUnion, st.fields[1].typ.kind)
	assert.Nil(t, compositeType("DECIMAL(10,2)"))
	assert.Nil(t, compositeType("VARCHAR"))
}

func TestNestedTextUntyped(t *testing.T) {
	assert.Equal(t, `[{'a': 1, 'b': NULL}, x]`, NestedText([]any{map[string]any{"b": nil, "a": int64(1)}, "x"}, ""))
	assert.Equal(t, `{k=1}`, NestedText(map[string]any{"k": int64(1)}, "MAP(VARCHAR, INTEGER)"))
}

// DuckDB 1.5's VARIANT cannot be scanned by duckdb-go. A statement that returns
// one fails ONCE, naming the column and the cast; it is never run a second time
// (a sequence must not advance twice, a remote query must not cost double).
func TestExecuteVariantColumnFailsOnceWithAClearError(t *testing.T) {
	eng := nestedEngine(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE v (id INT, payload VARIANT, "odd col" VARIANT, nested STRUCT(x VARIANT))`,
		`INSERT INTO v VALUES (1, 42::VARIANT, 1::VARIANT, {'x': 1::VARIANT})`,
		`CREATE SEQUENCE s`,
	} {
		_, err := eng.Execute(ctx, q, nil)
		require.NoError(t, err, q)
	}

	_, err := eng.Execute(ctx, `SELECT nextval('s') AS n, id, payload FROM v`, nil)
	require.Error(t, err)
	assert.Equal(t, "Column payload is VARIANT, which Slaw can't show yet; select it as payload::JSON.", err.Error())
	rs, err := eng.Execute(ctx, `SELECT nextval('s')`, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2, rs.Rows[0][0], "the failed statement ran once and DESCRIBE ran nothing")

	_, err = eng.Execute(ctx, `SELECT id, payload, "odd col" FROM v WHERE id = $id`, map[string]any{"id": 1})
	require.Error(t, err)
	assert.Equal(t, `Columns payload, odd col are VARIANT, which Slaw can't show yet; select them as payload::JSON, "odd col"::JSON.`, err.Error())

	// DuckDB's FROM-first form is a SELECT too.
	_, err = eng.Execute(ctx, `FROM v SELECT id, payload`, nil)
	require.Error(t, err)
	assert.Equal(t, "Column payload is VARIANT, which Slaw can't show yet; select it as payload::JSON.", err.Error())

	// A column named like a keyword gets a hint that runs.
	_, err = eng.Execute(ctx, `CREATE TABLE k ("select" VARIANT)`, nil)
	require.NoError(t, err)
	_, err = eng.Execute(ctx, `INSERT INTO k VALUES (1::VARIANT)`, nil)
	require.NoError(t, err)
	_, err = eng.Execute(ctx, `SELECT * FROM k`, nil)
	require.Error(t, err)
	assert.Equal(t, `Column select is VARIANT, which Slaw can't show yet; select it as "select"::JSON.`, err.Error())

	_, err = eng.Execute(ctx, `SELECT nested FROM v`, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested inside a struct or list")

	_, err = eng.Execute(ctx, `SELECT 1; SELECT payload FROM v`, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can't be shown yet")

	rs, err = eng.Execute(ctx, `SELECT payload::JSON AS payload FROM v`, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 42, rs.Rows[0][0])
}

func TestVariantCastProjection(t *testing.T) {
	assert.Equal(t, "*", VariantCastProjection(nil))
	assert.Equal(t, `* REPLACE (CAST("p" AS JSON) AS "p", CAST("we""ird" AS JSON) AS "we""ird")`, VariantCastProjection([]string{"p", `we"ird`}))
	assert.True(t, IsVariantType(" variant "))
	assert.False(t, IsVariantType("VARIANT[]"))
}
