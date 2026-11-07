package queries

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/SandwichLabs/slaw/internal/engine"
	"github.com/SandwichLabs/slaw/internal/sqllex"
)

// ReaderFunctions are the table functions that read a file or URL: the only
// ones that take a path, and the ones whose argument a parameter may never be
// (the agent SQL policy builds its reader allow-list from the same names).
//
// iceberg_scan, iceberg_metadata, iceberg_snapshots and delta_scan are here
// too: a table's own metadata chooses which files they read (see
// docs/reference/agent-policy.md), a risk the product accepts and warns about,
// and the literal path they are given is held to the same rules as read_parquet.
var ReaderFunctions = []string{
	"read_csv", "read_csv_auto",
	"read_parquet", "parquet_scan", "parquet_metadata", "parquet_schema",
	"parquet_file_metadata", "parquet_kv_metadata",
	"read_json", "read_json_auto", "read_ndjson", "read_ndjson_auto",
	"read_json_objects", "read_json_objects_auto", "read_ndjson_objects",
	"read_text", "read_blob", "glob",
	"iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "delta_scan",
}

var readerOrQueryArgs = func() map[string]bool {
	m := map[string]bool{"query": true, "query_table": true}
	for _, n := range ReaderFunctions {
		m[strings.ToLower(n)] = true
	}
	return m
}()

// CheckParamShape holds a statement that is about to be bound to the two
// lexical rules of D5, on the policy's own lexer (internal/sqllex):
//
//   - a query is exactly one statement (the lexer's count; the policy
//     cross-checks it against DuckDB's before any classification);
//   - every $name placeholder is a parameter token. engine.BindParams rewrites
//     every "$name" it finds in the raw text, string literals, quoted names,
//     $$...$$ bodies and comments included, and the database would quietly
//     ignore the extra argument, so a placeholder in any of those places is
//     refused, as are a bare "?" and a positional "$1" (they would take another
//     parameter's argument);
//   - a parameter is never the argument of query(), query_table() or a reader.
//
// Bind runs it, so every surface that binds a saved query (the agent, Studio,
// the CLI) holds the text to it. It is cheap and does no I/O.
func CheckParamShape(sqlText string) error {
	var seen []string
	stmts := sqllex.Statements(sqlText)
	if len(stmts) != 1 {
		return errors.New("a query is exactly one statement")
	}
	for _, toks := range stmts {
		for i, t := range toks {
			if t.Kind != sqllex.Punct {
				continue
			}
			switch t.Text {
			case "?":
				return errors.New("a bare ? is not allowed: write the parameter as $name")
			case "$":
				if i+1 >= len(toks) || toks[i+1].Kind != sqllex.Word || toks[i+1].Start != t.End {
					return errors.New("a $ must start a parameter name such as $from")
				}
				name := toks[i+1].Raw
				if name[0] >= '0' && name[0] <= '9' {
					return fmt.Errorf("$%s is a positional parameter: use a declared name such as $from", name)
				}
				seen = append(seen, name)
			}
		}
		if err := checkParamInReaderArgs(toks); err != nil {
			return err
		}
	}
	if !slices.Equal(seen, engine.ParamOccurrences(sqlText)) {
		return errors.New("a $name placeholder appears inside a string literal, a quoted name, a $$...$$ body or a comment, where it would be rewritten too: " +
			"keep $name only where a value belongs, and write literal text without it")
	}
	return nil
}

// checkParamInReaderArgs refuses a parameter inside the argument list of
// query(...), query_table(...) or a file reader in one statement's tokens.
func checkParamInReaderArgs(toks []sqllex.Tok) error {
	for i := 0; i+1 < len(toks); i++ {
		if !toks[i].IsName() || !toks[i+1].IsParen("(") || !readerOrQueryArgs[strings.ToLower(toks[i].Text)] {
			continue
		}
		end := sqllex.MatchParen(toks, i+1)
		if end < 0 {
			end = len(toks)
		}
		for _, t := range toks[i+2 : end] {
			if t.Kind == sqllex.Punct && t.Text == "$" {
				return fmt.Errorf("a parameter cannot be an argument of %s(...): it would choose what is read or run", strings.ToLower(toks[i].Text))
			}
		}
	}
	return nil
}
