package agents

import (
	"sort"
	"strings"

	"github.com/SandwichLabs/slaw/internal/queries"
)

// The name lists behind the agent SQL policy (#116). Everything here is a
// closed list: a table function that is not named is refused, not guessed at.

func nameSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// readerFuncs are the table functions that read a file or URL. They are the
// only ones that take a path, and only a plain string literal path inside
// data/ (or a storage scope) is accepted. Everything else that reads files
// (read_xlsx, st_read, sniff_csv, parquet_bloom_probe, ...) is not listed and
// therefore refused.
//
// iceberg_scan, iceberg_metadata, iceberg_snapshots and delta_scan are listed
// (queries.ReaderFunctions) and take exactly the read_parquet rules. A table's
// own metadata (a `_delta_log` add.path, a manifest list URL) names the files
// it reads, so a crafted table inside an allowed path can reach beyond it; that
// is accepted and documented, and the options that redirect metadata lookup
// (version_name_format, allow_moved_paths, ...) are still not on the safe list.
var readerFuncs = nameSet(queries.ReaderFunctions...)

// lakeReaders are the table-format readers: they follow the reader rules and a
// further one (lakeReaderPathRefusal).
var lakeReaders = nameSet("iceberg_scan", "iceberg_metadata", "iceberg_snapshots", "delta_scan")

// safeReaderParams are the only named options a reader may be given. They
// change how a file is parsed, never which other file or URL is read or where
// anything is written. Everything else (version_name_format, allow_moved_paths,
// rejects_table, store_rejects, ...) is refused.
var safeReaderParams = nameSet(
	"header", "delim", "delimiter", "sep", "quote", "escape", "columns", "union_by_name", "auto_detect",
	"all_varchar", "skip", "null_padding", "ignore_errors", "sample_size", "dateformat", "timestampformat",
	"types", "dtypes", "names", "normalize_names", "filename", "hive_partitioning", "hive_types",
	"hive_types_autocast", "format", "records", "maximum_object_size", "compression", "newline", "comment",
	"decimal_separator", "encoding", "binary_as_string", "file_row_number", "parallel", "auto_type_candidates",
	"nullstr", "strict_mode", "max_line_size", "buffer_size", "maximum_depth", "field_appearance_threshold",
	"map_inference_threshold", "convert_strings_to_integers", "ignore_errors", "thousands",
)

func safeReaderParamList() string {
	n := sortedKeys(safeReaderParams)
	return strings.Join(n, ", ")
}

// readerPlanOps are the plan operator names a reader appears as in EXPLAIN. A
// plan may contain no more of them than the statement wrote out and vetted.
var readerPlanOps = nameSet(
	"READ_CSV", "READ_CSV_AUTO",
	"READ_PARQUET", "PARQUET_SCAN", "PARQUET_METADATA", "PARQUET_SCHEMA",
	"PARQUET_FILE_METADATA", "PARQUET_KV_METADATA",
	"READ_JSON", "READ_JSON_AUTO", "READ_NDJSON", "READ_NDJSON_AUTO",
	"READ_JSON_OBJECTS", "READ_JSON_OBJECTS_AUTO", "READ_NDJSON_OBJECTS",
	"READ_TEXT", "READ_BLOB", "GLOB",
	"ICEBERG_SCAN", "ICEBERG_METADATA", "ICEBERG_SNAPSHOTS", "DELTA_SCAN",
)

// allowedTableFunctions are pure catalog and generator table functions: they
// touch only DuckDB's own catalog or a number sequence, never a file, the
// network, a setting or a secret.
var allowedTableFunctions = nameSet(
	"range", "generate_series", "unnest", "generate_subscripts",
	"duckdb_tables", "duckdb_columns", "duckdb_views", "duckdb_schemas",
	"duckdb_constraints", "duckdb_indexes", "duckdb_sequences", "duckdb_keywords",
	"duckdb_functions", "duckdb_types", "pragma_table_info", "pragma_storage_info",
	// DuckLake's read-only metadata functions (#119). Every other ducklake_*
	// function (ducklake_set_option, _expire_snapshots, _merge_adjacent_files,
	// _rewrite_data_files, _cleanup_old_files, _delete_orphaned_files,
	// _add_data_files, _flush_inlined_data, _set_commit_message, _options,
	// _list_files, _table_insertions/_deletions/_changes and iceberg_to_ducklake)
	// changes the lake or reveals where its files live, and stays refused.
	"ducklake_snapshots", "ducklake_current_snapshot", "ducklake_last_committed_snapshot", "ducklake_table_info",
)

// allowedCalls are the only things the agent may CALL: the catalog metadata
// functions, with literal arguments.
var allowedCalls = nameSet(
	"duckdb_tables", "duckdb_columns", "duckdb_views", "duckdb_schemas",
	"duckdb_constraints", "duckdb_indexes", "duckdb_sequences", "duckdb_keywords",
	"duckdb_types", "pragma_table_info",
)

// allowedPragmas are the read-only introspection PRAGMAs.
//
// database_list is deliberately absent: it exposes every attached database's
// file path.
var allowedPragmas = nameSet(
	"table_info", "show_tables", "show_tables_expanded", "database_size",
	"storage_info", "version", "platform",
)

// allowedPlanOperators are the physical-plan operators that never themselves
// read outside the database: relational combinators, the DML and DDL
// operators, and the generator and catalog functions allowedTableFunctions
// names. Scans of catalog tables (an operator carrying a "Table" entry) and
// readers (readerPlanOps) are handled in the plan check itself. Anything else
// is refused, so an operator this list does not know (a new DuckDB version, an
// extension's table function) is refused rather than let through.
var allowedPlanOperators = nameSet(
	"PROJECTION", "FILTER", "HASH_JOIN", "LEFT_DELIM_JOIN", "RIGHT_DELIM_JOIN",
	"CROSS_PRODUCT", "NESTED_LOOP_JOIN", "PIECEWISE_MERGE_JOIN", "IE_JOIN",
	"ASOF_JOIN", "BLOCKWISE_NL_JOIN", "POSITIONAL_JOIN", "POSITIONAL_SCAN",
	"HASH_GROUP_BY", "PERFECT_HASH_GROUP_BY", "UNGROUPED_AGGREGATE", "SIMPLE_AGGREGATE",
	"WINDOW", "STREAMING_WINDOW", "STREAMING_LIMIT", "LIMIT", "LIMIT_PERCENT", "TOP_N",
	"ORDER_BY", "UNION", "DISTINCT", "RESERVOIR_SAMPLE", "STREAMING_SAMPLE", "SAMPLE",
	"PIVOT", "UNPIVOT", "STRICT_UNPIVOT", "UNNEST",
	"RECURSIVE_CTE", "REC_CTE", "CTE", "CTE_SCAN", "RECURSIVE_CTE_SCAN", "REC_CTE_SCAN", "MATERIALIZED_CTE", "PARTITIONED_AGGREGATE",
	"DELIM_SCAN", "COLUMN_DATA_SCAN", "CHUNK_SCAN", "EXPRESSION_SCAN", "DUMMY_SCAN", "EMPTY_RESULT",
	"RESULT_COLLECTOR", "EXPLAIN",
	// DML.
	"INSERT", "BATCH_INSERT", "UPDATE", "DELETE", "MERGE_INTO",
	// DDL. These carry no read of their own; their queries show as children.
	"CREATE_TABLE", "CREATE_TABLE_AS", "BATCH_CREATE_TABLE_AS", "CREATE_VIEW",
	"CREATE_SCHEMA", "CREATE_SEQUENCE", "CREATE_MACRO", "CREATE_INDEX", "CREATE_TYPE",
	"DROP", "ALTER",
	// COPY ... TO a data/ file (the path itself is vetted from the SQL text).
	"COPY_TO_FILE", "BATCH_COPY_TO_FILE",
	// MotherDuck's hybrid execution moves rows between this computer and its
	// server for a query that touches an attached md: catalog. The names come
	// from the extension's binaries (the DuckDB 1.4.1 and 1.5.6 builds), not from a live run; the
	// BATCH_ sinks are the order-preserving forms 1.5.6 plans for an ORDER BY (one was reported live).
	"DOWNLOAD_BRIDGE", "DOWNLOAD_SINK", "DOWNLOAD_SOURCE", "UPLOAD_BRIDGE", "UPLOAD_SINK", "UPLOAD_SOURCE", "HYBRID",
	"BATCH_DOWNLOAD_SINK", "BATCH_UPLOAD_SINK",
	// Generator and catalog functions (mirrors allowedTableFunctions).
	"RANGE", "GENERATE_SERIES", "GENERATE_SUBSCRIPTS",
	"DUCKDB_TABLES", "DUCKDB_COLUMNS", "DUCKDB_VIEWS", "DUCKDB_SCHEMAS",
	"DUCKDB_CONSTRAINTS", "DUCKDB_INDEXES", "DUCKDB_SEQUENCES", "DUCKDB_KEYWORDS",
	"DUCKDB_FUNCTIONS", "DUCKDB_TYPES", "PRAGMA_TABLE_INFO", "PRAGMA_STORAGE_INFO",
	"DUCKLAKE_SNAPSHOTS", "DUCKLAKE_CURRENT_SNAPSHOT", "DUCKLAKE_LAST_COMMITTED_SNAPSHOT", "DUCKLAKE_TABLE_INFO",
)

// remoteWriteOps matches the plan operators a remote catalog's own DML shows as
// (PG_INSERT, PG_UPDATE, PG_DELETE, PG_CREATE_TABLE_AS, MYSQL_INSERT,
// MYSQL_CREATE_TABLE_AS, DUCKLAKE_INSERT, DUCKLAKE_UPDATE, DUCKLAKE_DELETE, DUCKLAKE_INLINE_DATA (DuckLake 1.0
// stages small inserts in its catalog; a child of DUCKLAKE_INSERT / DUCKLAKE_CREATE_TABLE_AS), ...). The statement was
// already refused unless its target is a writable, approved connection.
func isRemoteWriteOp(name string) bool {
	for _, prefix := range []string{"PG_", "SQLITE_", "MYSQL_", "DUCKLAKE_", "MD_", "MOTHERDUCK_"} {
		if strings.HasPrefix(name, prefix) {
			switch strings.TrimPrefix(name, prefix) {
			case "INSERT", "UPDATE", "DELETE", "CREATE_TABLE", "CREATE_TABLE_AS", "ALTER", "DROP", "INLINE_DATA":
				return true
			}
		}
	}
	return false
}

// deniedFuncExact and deniedFuncPrefix are functions refused anywhere, as a
// table function or a scalar, in every tier. The value is the reason.
var deniedFuncExact = map[string]string{
	"query":                       "query() runs SQL from a string; write the SQL directly",
	"query_table":                 "query_table() reads a table named by a string; name the table directly",
	"json_execute_serialized_sql": "json_execute_serialized_sql() runs SQL from a string; write the SQL directly",
	"duckdb_secrets":              secretsDeniedMsg,
	"duckdb_secret_types":         secretsDeniedMsg,
	"which_secret":                secretsDeniedMsg,
	"duckdb_settings":             "settings and the environment are not readable by the agent",
	"current_setting":             "settings and the environment are not readable by the agent",
	"getenv":                      "settings and the environment are not readable by the agent",
	"duckdb_extensions":           "settings and the environment are not readable by the agent",
	"duckdb_variables":            "settings and the environment are not readable by the agent",
	"duckdb_databases":            "attached databases are listed by list_connections (duckdb_databases() exposes file paths)",
	"pragma_database_list":        "attached databases are listed by list_connections (it exposes file paths)",
	"duckdb_logs":                 "DuckDB's internal log is not readable by the agent",
	"duckdb_log_contexts":         "DuckDB's internal log is not readable by the agent",
	"enable_logging":              "DuckDB's internal log is not readable by the agent",
	"truncate_duckdb_logs":        "DuckDB's internal log is not readable by the agent",
	"duckdb_temporary_files":      "DuckDB's internal state is not readable by the agent",
	"duckdb_external_file_cache":  "DuckDB's internal state is not readable by the agent",
	"duckdb_memory":               "DuckDB's internal state is not readable by the agent",
	"duckdb_optimizers":           "DuckDB's internal state is not readable by the agent",
	"sql_auto_complete":           "DuckDB's internal state is not readable by the agent",
	"sleep_ms":                    "sleeping holds a connection and a worker for as long as the query asks; it is not available to the agent",
	"pg_sleep":                    "sleeping holds a connection and a worker for as long as the query asks; it is not available to the agent",
	"pg_clear_cache":              "remote catalogs are refreshed by Slaw, not by SQL",
	"get_md_token":                "MotherDuck account functions are not available to the agent",
	"print_md_token":              "MotherDuck account functions are not available to the agent",
	"prompt":                      "MotherDuck AI functions send data to a remote service and are not available to the agent",
	"prompt_query":                "MotherDuck AI functions send data to a remote service and are not available to the agent",
	"prompt_explain":              "MotherDuck AI functions send data to a remote service and are not available to the agent",
	"prompt_fixup":                "MotherDuck AI functions send data to a remote service and are not available to the agent",
	"prompt_fix_line":             "MotherDuck AI functions send data to a remote service and are not available to the agent",
	"embedding":                   "MotherDuck AI functions send data to a remote service and are not available to the agent",
}

var deniedFuncPrefix = []struct{ prefix, reason string }{
	{"postgres_", "raw passthroughs to another database bypass the SQL policy; query the attached catalog instead"},
	{"mysql_", "raw passthroughs to another database bypass the SQL policy; query the attached catalog instead"},
	{"sqlite_", "raw passthroughs to another database bypass the SQL policy; query the attached catalog instead"},
	{"md_", "MotherDuck sharing and administration functions are not available to the agent"},
	{"motherduck_", "MotherDuck sharing and administration functions are not available to the agent"},
	{"non_md_", "MotherDuck sharing and administration functions are not available to the agent"},
	{"__", "internal functions are not available to the agent"},
}

// hardDeniedFunc returns the reason a function name is refused everywhere, or "".
func hardDeniedFunc(name string) string {
	if r, ok := deniedFuncExact[name]; ok {
		return r
	}
	for _, p := range deniedFuncPrefix {
		if strings.HasPrefix(name, p.prefix) {
			return p.reason
		}
	}
	return ""
}

// mutatingScalarFuncs advance persistent state while looking like a read.
var mutatingScalarFuncs = nameSet("nextval", "setval")

// Never-list statement heads: refused in every tier, with the reason shown.
var deniedHeads = map[string]string{
	"ATTACH":     "ATTACH is not allowed: connections come from the workspace's definitions, not from SQL",
	"DETACH":     "DETACH is not allowed: connections come from the workspace's definitions, not from SQL",
	"USE":        "USE is not allowed: name the connection in the table (connection.schema.table)",
	"INSTALL":    "INSTALL is not allowed: extensions are installed by the person, with consent",
	"LOAD":       "LOAD is not allowed: extensions are loaded by Slaw",
	"FORCE":      "INSTALL/LOAD are not allowed",
	"SET":        "SET is not allowed: settings and variables cannot be changed by the agent",
	"RESET":      "RESET is not allowed: settings cannot be changed by the agent",
	"EXPORT":     "EXPORT DATABASE is not allowed",
	"IMPORT":     "IMPORT DATABASE is not allowed",
	"CHECKPOINT": "CHECKPOINT is not allowed",
	"VACUUM":     "VACUUM is not allowed",
	"ANALYZE":    "ANALYZE is not allowed",
	"BEGIN":      "transactions are not allowed: each statement runs on its own",
	"START":      "transactions are not allowed: each statement runs on its own",
	"COMMIT":     "transactions are not allowed: each statement runs on its own",
	"ROLLBACK":   "transactions are not allowed: each statement runs on its own",
	"ABORT":      "transactions are not allowed: each statement runs on its own",
	"END":        "transactions are not allowed: each statement runs on its own",
	"PREPARE":    "PREPARE/EXECUTE are not allowed: they would run a statement the policy cannot see",
	"EXECUTE":    "PREPARE/EXECUTE are not allowed: they would run a statement the policy cannot see",
	"DEALLOCATE": "PREPARE/EXECUTE are not allowed",
	"GRANT":      "GRANT is not allowed",
	"REVOKE":     "REVOKE is not allowed",
	"TRUNCATE":   "TRUNCATE is not allowed: use DELETE FROM",
	"COMMENT":    "COMMENT ON is not allowed",
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PolicyReaderFunctions lists the table functions the agent may use to read a
// file or URL (a plain string literal path inside data/ or a storage scope).
// docs/reference/agent-policy.md must name each.
func PolicyReaderFunctions() []string { return sortedKeys(readerFuncs) }

// PolicyTableFunctions lists the catalog and generator table functions the
// agent may call in SELECT ... FROM. docs/reference/agent-policy.md must name each.
func PolicyTableFunctions() []string { return sortedKeys(allowedTableFunctions) }

// PolicyCalls lists what the agent may CALL.
func PolicyCalls() []string { return sortedKeys(allowedCalls) }

// PolicyPragmas lists the PRAGMAs the agent may run.
func PolicyPragmas() []string { return sortedKeys(allowedPragmas) }
