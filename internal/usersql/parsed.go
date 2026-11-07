package usersql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

// CheckParsed is the parser-based second layer of CheckManaged (#138). Each
// statement is prepared, never run, and DuckDB's own statement type is read:
// DuckDB parses `PRAGMA x = y`, `SET x = y` and `RESET x` all as a SET
// statement, whatever the spelling, so a SET that names the S3 URL setting is
// refused here even if the textual check missed a spelling.
//
// Only driver.Conn.Prepare is used: unlike PrepareContext it refuses text with
// more than one statement instead of running all but the last, and the text is
// split into single statements first. A statement that cannot be prepared
// (it names a table that does not exist) is not a SET, so it is let through to
// fail on its own.
func CheckParsed(ctx context.Context, db *sql.DB, sqlText string) error {
	var texts []string
	for _, st := range statements(sqlText) {
		if st.text != "" {
			texts = append(texts, st.text)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return refuseIfNamed(sqlText)
	}
	defer conn.Close() //nolint:errcheck
	for _, text := range texts {
		if err := checkPiece(conn, text); err != nil {
			return err
		}
	}
	return nil
}

// ParsedType prepares text on conn (never runs it) and returns DuckDB's own
// statement type. An error means DuckDB could not prepare it (a syntax error,
// a missing table, more than one statement).
func ParsedType(conn *sql.Conn, text string) (duckdb.StmtType, error) {
	var typ duckdb.StmtType
	err := conn.Raw(func(dc any) error {
		c, ok := dc.(driver.Conn)
		if !ok {
			return fmt.Errorf("not a duckdb connection")
		}
		stmt, err := c.Prepare(text)
		if err != nil {
			return err
		}
		defer stmt.Close() //nolint:errcheck
		ds, ok := stmt.(*duckdb.Stmt)
		if !ok {
			return fmt.Errorf("not a duckdb statement")
		}
		typ, err = ds.StatementType()
		return err
	})
	return typ, err
}

// checkPiece prepares one piece of text (never runs it) and refuses a
// SET-type statement that names the setting, or any piece that cannot be
// prepared and names it. (Also what a piece holding several statements gives,
// our splitter and DuckDB's disagreeing: never let it pass when it names the
// setting.)
func checkPiece(conn *sql.Conn, text string) error {
	typ, err := ParsedType(conn, text)
	if err != nil {
		return refuseIfNamed(text)
	}
	if typ != duckdb.STATEMENT_TYPE_SET {
		return nil
	}
	return refuseIfNamed(text)
}

// namesGuardedSetting reports whether text mentions the setting once quotes
// and escapes are stripped (so "s3_url_compatibility_mode" and 's3_...' count).
func namesGuardedSetting(text string) bool {
	t := strings.ToLower(text)
	t = strings.NewReplacer(`"`, "", `'`, "", "\\", "", "`", "").Replace(t)
	return strings.Contains(t, "s3_url_compatibility_mode") || strings.Contains(t, "s3_url_compat") ||
		strings.Contains(t, "http_timeout") || strings.Contains(t, "http_retries")
}

// refuseIfNamed is the refusal for a statement that names the guarded setting.
func refuseIfNamed(text string) error {
	if namesGuardedSetting(text) {
		return fmt.Errorf("that statement changes a setting that keeps S3 URLs from carrying credentials to another host; it is not available here")
	}
	return nil
}
