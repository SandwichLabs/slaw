package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	_ "github.com/duckdb/duckdb-go/v2" // DuckDB driver, for the dedicated parser connection below
	"github.com/duckdb/duckdb-go/v2/mapping"

	"github.com/SandwichLabs/slaw/internal/usersql"
)

// sqlClass is what a run_query statement is allowed to do, used to gate it per
// statement instead of by the blanket "data" category (#63, #116).
//
// The decision is an ALLOW-LIST (sql_policy.go): a statement is classified only
// when DuckDB's own parser and the policy both understand every part of it, and
// anything else is denied with a reason ("couldn't verify this query"). There
// is no tier in which the never-list (ATTACH, SET, secrets, passthroughs, ...)
// is allowed.
type sqlClass int

const (
	// sqlClassRead is a statement over attached catalogs that changes nothing:
	// SELECT / WITH / FROM-first / VALUES, DESCRIBE, SHOW, SUMMARIZE, EXPLAIN
	// (not ANALYZE), an allow-listed PRAGMA or CALL. Allowed in every tier.
	sqlClassRead sqlClass = iota
	// sqlClassWrite is row-level DML (INSERT, UPDATE, DELETE, MERGE) into
	// writable catalogs. Needs read-write or full.
	sqlClassWrite
	// sqlClassDDL changes a catalog's shape (CREATE, DROP, ALTER) or writes a
	// file (COPY ... TO a data/ path). Needs full.
	sqlClassDDL
	// sqlClassOther is denied in every tier: the never-list, anything the
	// policy could not verify, and anything not on the allow-list. Its verdict
	// always carries a reason.
	sqlClassOther
)

func (c sqlClass) String() string {
	switch c {
	case sqlClassRead:
		return "read"
	case sqlClassWrite:
		return "write"
	case sqlClassDDL:
		return "ddl"
	default:
		return "denied"
	}
}

// sqlTarget is one catalog a write statement changes.
type sqlTarget struct {
	Catalog  string // lower-case catalog (connection) name
	ReadOnly bool   // the catalog or its connection is read-only
	Local    bool   // a local duckdb/sqlite file (anything else always needs approval)
	System   bool   // DuckDB's own system catalog
}

// sqlVerdict is the policy's answer for one run_query call.
type sqlVerdict struct {
	Class   sqlClass
	Reason  string // set whenever Class is sqlClassOther, and for gate refusals
	Targets []sqlTarget

	readers readerCount // file/URL readers vetted in the statement (for a nested query())

	// Statements is how many statements the script held, and discardedReads how
	// many of those, before the last, are reads: run_query returns only the last
	// statement's result, so a read before it would be silently lost.
	Statements     int
	discardedReads int
	// Temp is set for a statement that creates a TEMP object (see tempObjectNote).
	Temp bool
}

func deny(reason string) *sqlVerdict { return &sqlVerdict{Class: sqlClassOther, Reason: reason} }

const (
	// externalAccessDeniedMsg is the generic refusal for something that reaches
	// outside the allow-list.
	externalAccessDeniedMsg = "this query reaches outside what the agent may read (files and URLs are limited to the workspace data/ folder and storage scopes)"
	couldntVerifyMsg        = "couldn't verify this query"
)

// secretsDeniedMsg is the reason for a statement that creates, drops, lists or
// attaches with a DuckDB secret. It is denied in EVERY sandbox mode, full
// included, and no approval can lift it: credentials are the session's to
// create (Session.CreateTemporarySecret), never the agent's (#114).
const secretsDeniedMsg = "run_query: secrets are managed by Slaw and cannot be created, listed or referenced from SQL"

// gateReason is the reason checkSQLClass is given: the verdict's own, or the TEMP
// explanation for the tier refusal of a TEMP create (kept out of Reason so no
// other surface, a notebook refusal for one, repeats it).
func (v *sqlVerdict) gateReason() string {
	if v.Temp {
		return tempObjectNote
	}
	return v.Reason
}

// tempObjectNote explains why CREATE TEMP ... still needs the full tier. A TEMP
// object changes no connection, but it lives in the hub's one in-memory catalog,
// which every later query shares (the person's notebooks and artifacts too), and
// a temp table named like a real table shadows it for all of them.
const tempObjectNote = "A TEMP table changes no connection, but it lives in the one session catalog that the person's notebooks and artifacts share, and it shadows a real table of the same name there, so it is held to the full tier; use a CTE (WITH name AS (...)) or a subquery instead."

var plainIdentifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// unresolvedTableDeniedMsg is the refusal for a table reference that did not
// resolve. An unqualified plain identifier is the ordinary "that table does
// not exist" typo and says so plainly, without suggesting a privilege problem.
//
// A three-part name whose first part is not an attached catalog names a
// connection that does not exist (#111: a MotherDuck database other than the
// one the connection attaches), which says so instead of blaming file access.
func unresolvedTableDeniedMsg(catalogName, schema, table string) string {
	if catalogName == "" && schema == "" && plainIdentifierRe.MatchString(table) {
		return fmt.Sprintf("table %q not found", table)
	}
	if catalogName != "" && !strings.EqualFold(catalogName, "system") {
		return fmt.Sprintf("there is no connection named %q, so %q can't be read (see list_connections; %s)",
			catalogName, dotJoin(catalogName, schema, table), oneDatabasePerConnectionHint)
	}
	return fmt.Sprintf("%s (%q)", externalAccessDeniedMsg, dotJoin(catalogName, schema, table))
}

// oneDatabasePerConnectionHint explains how to reach a database a connection
// does not attach. A table name has at most three parts, so a database inside
// a connection (motherduck.newsroom.raw.t) cannot be named.
const oneDatabasePerConnectionHint = "a connection reaches one database, so a MotherDuck database other than the connection's own needs a connection of its own, which the person adds with the same token"

func dotJoin(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, ".")
}

// --- the parser connections ------------------------------------------------
//
// Both are hardened: external access off, no extension loading, no secrets, and
// the configuration locked. They only ever PARSE agent SQL (json_serialize_sql
// and duckdb_extract_statements never bind, so they cannot touch a file or the
// network), but if a parser bug ever let one execute, it would have no door to
// open. They are separate from the hub on purpose (spike #107, finding B).

var hardenedParserSettings = [][2]string{
	{"enable_external_access", "false"},
	{"autoinstall_known_extensions", "false"},
	{"autoload_known_extensions", "false"},
	{"allow_community_extensions", "false"},
	{"allow_persistent_secrets", "false"},
	{"lock_configuration", "true"},
}

var (
	stmtConnOnce sync.Once
	stmtConnDB   mapping.Database
	stmtConn     mapping.Connection
	stmtConnErr  error
	stmtConnMu   sync.Mutex // DuckDB connections are not safe for concurrent use
)

func statementConn() (mapping.Connection, error) {
	stmtConnOnce.Do(func() {
		var cfg mapping.Config
		if mapping.CreateConfig(&cfg) == mapping.StateError {
			stmtConnErr = errors.New("failed to create the statement-extraction configuration")
			return
		}
		defer mapping.DestroyConfig(&cfg)
		for _, kv := range hardenedParserSettings {
			if mapping.SetConfig(cfg, kv[0], kv[1]) == mapping.StateError {
				stmtConnErr = fmt.Errorf("failed to harden the statement-extraction database (%s)", kv[0])
				return
			}
		}
		var errMsg string
		if mapping.OpenExt("", &stmtConnDB, cfg, &errMsg) == mapping.StateError {
			stmtConnErr = fmt.Errorf("failed to open statement-extraction database: %s", errMsg)
			return
		}
		if mapping.Connect(stmtConnDB, &stmtConn) == mapping.StateError {
			stmtConnErr = errors.New("failed to open statement-extraction connection")
		}
	})
	return stmtConn, stmtConnErr
}

// countStatements returns the number of SQL statements raw contains, via
// DuckDB's own duckdb_extract_statements (syntax only, no binding). It is the
// authority on the count: the policy's own lexer must agree with it, or the
// query is denied.
func countStatements(raw string) (int, error) {
	conn, err := statementConn()
	if err != nil {
		return 0, err
	}
	stmtConnMu.Lock()
	defer stmtConnMu.Unlock()

	var extracted mapping.ExtractedStatements
	n := mapping.ExtractStatements(conn, raw, &extracted)
	errStr := mapping.ExtractStatementsError(extracted)
	mapping.DestroyExtracted(&extracted)
	if errStr != "" {
		return 0, errors.New(errStr)
	}
	return int(n), nil
}

// queryDB is the read surface the policy needs from the user's database.
// *sql.DB satisfies it, and so does a pinned *sql.Conn: a statement bound to a
// connection (notebook format v2) is classified on a connection whose default
// catalog is that connection, so its unqualified names resolve the way they
// will when it runs.
type queryDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// nilDB reports whether q is nil or a nil *sql.DB.
func nilDB(q queryDB) bool {
	if q == nil {
		return true
	}
	d, ok := q.(*sql.DB)
	return ok && d == nil
}

var (
	classifierDBOnce sync.Once
	classifierDBConn *sql.DB
	classifierDBErr  error
)

// classifierDB is the hardened, external-access-off, in-memory DuckDB the AST
// pre-check parses on. It is never the hub.
func classifierDB() (*sql.DB, error) {
	classifierDBOnce.Do(func() {
		dsn := "?"
		for i, kv := range hardenedParserSettings {
			if i > 0 {
				dsn += "&"
			}
			dsn += kv[0] + "=" + kv[1]
		}
		db, err := sql.Open("duckdb", dsn)
		if err != nil {
			classifierDBErr = err
			return
		}
		db.SetMaxOpenConns(1)
		var v string
		if err := db.QueryRow("SELECT current_setting('enable_external_access')").Scan(&v); err != nil || !strings.EqualFold(v, "false") {
			_ = db.Close()
			if err != nil {
				classifierDBErr = fmt.Errorf("the SQL parser connection is not hardened (enable_external_access=%q): %w", v, err)
			} else {
				classifierDBErr = fmt.Errorf("the SQL parser connection is not hardened (enable_external_access=%q)", v)
			}
			return
		}
		classifierDBConn = db
	})
	return classifierDBConn, classifierDBErr
}

// serializedSQLDoc is the shape of json_serialize_sql's output. On success
// (error: false) Statements holds the parsed statement(s) as generic trees;
// json_serialize_sql only succeeds for a batch of pure SELECT statements.
type serializedSQLDoc struct {
	Error      bool             `json:"error"`
	ErrorType  string           `json:"error_type"`
	Statements []map[string]any `json:"statements"`
}

// serializeSQL asks the parser connection to parse raw via json_serialize_sql.
// raw is a bound parameter, never concatenated into SQL text.
func serializeSQL(ctx context.Context, raw string) (*serializedSQLDoc, error) {
	db, err := classifierDB()
	if err != nil {
		return nil, fmt.Errorf("parser connection unavailable: %w", err)
	}
	var out string
	err = db.QueryRowContext(ctx, "SELECT CAST(json_serialize_sql(?::VARCHAR) AS VARCHAR)", raw).Scan(&out)
	if err != nil {
		return nil, fmt.Errorf("json_serialize_sql failed: %w", err)
	}
	var doc serializedSQLDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("failed to parse json_serialize_sql output: %w", err)
	}
	return &doc, nil
}

// --- secrets (a token screen: secret DDL is not serializable) ---------------

// touchesSecrets reports whether the statement(s) create, drop, list or use a
// DuckDB secret. It works on the comment-free token stream from usersql, so
// comments, quoting and string literals cannot hide a keyword, and it errs
// towards denying. It stays a token screen (not an AST check) because secret
// DDL is not a SELECT and so is never serializable; the AST policy refuses the
// secret table functions as well.
func touchesSecrets(raw string) bool { return tokensTouchSecrets(usersql.Tokens(raw), 0) }

var secretDDLModifiers = map[string]bool{
	"OR": true, "REPLACE": true, "PERSISTENT": true, "TEMPORARY": true, "TEMP": true,
	"IF": true, "NOT": true, "EXISTS": true,
}

func tokensTouchSecrets(stmts [][]usersql.Token, depth int) bool {
	if depth > 4 {
		return true
	}
	isName := func(t usersql.Token, name string) bool {
		return (t.Kind == usersql.TokWord || t.Kind == usersql.TokIdent) && strings.EqualFold(t.Text, name)
	}
	for _, st := range stmts {
		first := ""
		if len(st) > 0 && st[0].Kind == usersql.TokWord {
			first = st[0].Text
		}
		for i, t := range st {
			if isName(t, "duckdb_secrets") || isName(t, "which_secret") || isName(t, "duckdb_secret_types") {
				return true
			}
			if (t.Kind == usersql.TokWord || t.Kind == usersql.TokIdent) && strings.EqualFold(t.Text, "secret") {
				if first == "ATTACH" {
					return true
				}
				for j := i - 1; j >= 0; j-- {
					if st[j].Kind != usersql.TokWord {
						break
					}
					if secretDDLModifiers[st[j].Text] {
						continue
					}
					if st[j].Text == "CREATE" || st[j].Text == "DROP" || st[j].Text == "ALTER" {
						return true
					}
					break
				}
			}
			if (first == "SET" || first == "RESET") && strings.Contains(strings.ToUpper(t.Text), "SECRET") {
				return true
			}
			if (isName(t, "query") || isName(t, "query_table")) && i+1 < len(st) && st[i+1].Kind == usersql.TokParen && st[i+1].Text == "(" {
				// The SQL a literal argument carries is checked in turn. A
				// non-literal argument is refused by the policy itself, with its
				// own reason (it is not a secret problem).
				if i+3 < len(st) && st[i+2].Kind == usersql.TokString && st[i+3].Kind == usersql.TokParen && st[i+3].Text == ")" {
					if tokensTouchSecrets(usersql.Tokens(st[i+2].Text), depth+1) {
						return true
					}
				}
				continue
			}
		}
	}
	return false
}

// --- tier gates -------------------------------------------------------------

// checkSQLClass gates a classified statement against the sandbox tier: reads
// are allowed everywhere, DML needs read-write or full, DDL and COPY ... TO
// need full, and a denied statement is refused in every tier. reason, when
// non-empty, is surfaced verbatim instead of the generic per-tier message.
//
// This is the first of the three write gates (tier). The other two are that
// the target connection is writable and that the person approves
// (Handler.gateWrite, handleRunQuery).
func (sc *SandboxConfig) checkSQLClass(class sqlClass, reason string) error {
	if reason == secretsDeniedMsg {
		return errors.New(secretsDeniedMsg)
	}
	if class == sqlClassOther {
		if reason == "" {
			reason = couldntVerifyMsg
		}
		return fmt.Errorf("run_query: %s", reason)
	}
	switch sc.Mode {
	case ModeReadOnly:
		if class != sqlClassRead {
			return fmt.Errorf("run_query: the agent is read-only here: only read statements (SELECT, WITH...SELECT, SHOW, DESCRIBE, EXPLAIN, SUMMARIZE, or a read-style PRAGMA) are allowed. Changing data needs the read-write tier. %s", tierFix(ModeReadWrite))
		}
	case ModeReadWrite:
		if class == sqlClassDDL {
			note := ""
			if reason == tempObjectNote {
				note = " " + tempObjectNote
			}
			return fmt.Errorf("run_query: this statement changes a connection's structure or writes a file, which needs the full tier (read-write allows INSERT, UPDATE and DELETE only).%s %s", note, tierFix(ModeFull))
		}
	case ModeFull:
	default:
		return fmt.Errorf("unknown sandbox mode: %q", sc.Mode)
	}
	return nil
}

// ErrReadOnlyBindingDenied is the ONE message checkSQLClassReadOnly returns for
// a denial: the render-time gate is read-only in every tier, so the reason the
// classifier gave (which can name a tier requirement) would be false for a
// full-tier caller. Callers log the reason themselves.
var ErrReadOnlyBindingDenied error = &policyRefusal{msg: "live bindings are read-only queries over this workspace's connections (no writes, and files only inside data/)"}

// policyRefusal is the policy's own "no": a statement that is not a plain read.
// PolicyRefused lets a caller that cannot import agents (the watch check) tell
// it from a database or connection failure, as ConnectFailure does for the
// connection.
type policyRefusal struct{ msg string }

func (e *policyRefusal) Error() string       { return e.msg }
func (e *policyRefusal) PolicyRefused() bool { return true }

// checkSQLClassReadOnly is the render-time gate for a live A2UI binding or a
// notebook cell: sqlClassRead regardless of the tier, because the statement
// re-runs on every render.
func (sc *SandboxConfig) checkSQLClassReadOnly(class sqlClass, _ string) error {
	if class == sqlClassRead {
		return nil
	}
	return ErrReadOnlyBindingDenied
}

// classifySQL classifies raw (with its $name params) with no workspace context:
// no data/ folder and no storage scopes, so no file or URL read classifies as a
// read. Handlers use Handler.classify, which supplies the workspace.
func classifySQL(ctx context.Context, userDB queryDB, raw string, params map[string]any) (sqlClass, string) {
	v := analyzeSQL(ctx, userDB, nil, raw, params)
	return v.Class, v.Reason
}

// IsReadOnlyStatement reports whether sql (with the given bound params)
// classifies as a pure read against db: exported so a caller outside this
// package that only has the original run_query request (the TUI's unsaved-
// changes tracker, the MCP debounced saver) can ask "did this statement
// write?" without duplicating classification logic.
func IsReadOnlyStatement(ctx context.Context, db *sql.DB, sqlText string, params map[string]any) bool {
	if db == nil {
		return false
	}
	class, _ := classifySQL(ctx, db, sqlText, params)
	return class == sqlClassRead
}
