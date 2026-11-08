// Package usersql holds the courtesy checks the CLI and the TUI run on SQL a
// person types, before it reaches the session.
//
// These are not a security boundary. The person at the keyboard is the user
// acting directly, not the agent, and DuckDB's configuration lock does not stop
// ATTACH or INSTALL by itself. The checks catch the common slips (attaching a
// database by hand, changing a setting, creating a secret, writing a file from
// a read-only run) with an explanation of what to do instead. The agent's own
// SQL policy lives in internal/agents and is separate.
package usersql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
)

// Check refuses the statements Slaw manages for you: ATTACH/DETACH,
// INSTALL/LOAD, SET/RESET and CREATE/DROP SECRET. With readOnly it also
// refuses COPY ... TO and EXPORT DATABASE, which write files. It scans each
// statement's leading words, skipping comments, quoted strings (single, double,
// E'...' and $tag$...$tag$), so a semicolon inside a literal is not a boundary.
func Check(sqlText string, readOnly bool) error {
	for _, st := range statements(sqlText) {
		if len(st.words) == 0 {
			continue
		}
		first := st.words[0]
		switch first {
		case "ATTACH", "DETACH":
			return fmt.Errorf("%s is not available here: connections are defined in the workspace (slaw.yaml, or your own connections file), and Slaw attaches them", first)
		case "INSTALL", "LOAD":
			return fmt.Errorf("%s is not available here: the session loads the DuckDB extensions it needs itself", first)
		case "SET", "RESET":
			return fmt.Errorf("%s is not available here: DuckDB settings are fixed when the session opens", first)
		case "PRAGMA":
			// A read PRAGMA (table_info, show_tables, version) is fine; an
			// assignment is SET in disguise.
			if st.hasEq || guardedSetting(st.words[1:]) {
				return fmt.Errorf("PRAGMA of a setting is not available here: DuckDB settings are fixed when the session opens")
			}
		case "COPY":
			if readOnly && st.hasTo {
				return fmt.Errorf("COPY ... TO writes a file, which --read-only does not allow")
			}
		case "EXPORT":
			if readOnly {
				return fmt.Errorf("EXPORT writes files, which --read-only does not allow")
			}
		case "CREATE", "DROP":
			if err := secretDDL(first, st.words[1:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckManaged is the narrow form of Check for the Studio query editor, which
// has always allowed ATTACH, SET and COPY. It refuses only what would undo
// Slaw's own protections: INSTALL/LOAD (a hand-loaded extension), and
// FORCE INSTALL and UPDATE EXTENSIONS, SET/RESET/PRAGMA of the S3 URL-parameter setting (#138), and CREATE/DROP SECRET (dropping the catch-all deny secrets that stop an
// uncovered storage URL being read with the host's credentials, #135).
func CheckManaged(sqlText string) error {
	for _, st := range statements(sqlText) {
		if len(st.words) == 0 {
			continue
		}
		switch first := st.words[0]; first {
		case "INSTALL", "LOAD", "FORCE":
			return fmt.Errorf("%s is not available here: the session loads the DuckDB extensions it needs itself", first)
		case "UPDATE":
			if len(st.words) > 1 && st.words[1] == "EXTENSIONS" {
				return fmt.Errorf("UPDATE EXTENSIONS is not available here: the session manages the DuckDB extensions it needs itself")
			}
		case "CREATE", "DROP":
			if err := secretDDL(first, st.words[1:]); err != nil {
				return err
			}
		case "SET", "RESET", "PRAGMA":
			// PRAGMA name = value is SET in disguise.
			if guardedSetting(st.words[1:]) {
				return fmt.Errorf("%s of that setting is not available here: it keeps S3 URLs from carrying credentials to another host", first)
			}
		}
	}
	return nil
}

// CheckInteractive is the guard the Studio query editor and the console run on
// SQL a person types: CheckManaged, then, only while httpfs has not loaded
// (session.LazyStorage; afterwards the configuration lock refuses the setting
// itself), the parser-based CheckParsed. It is not a security boundary (see the
// package doc); it is the one place both surfaces share.
func CheckInteractive(ctx context.Context, sess session.Session, sqlText string) error {
	if err := CheckManaged(sqlText); err != nil {
		return err
	}
	if !session.LazyStorage(sess) {
		return nil
	}
	return CheckParsed(ctx, sess.DB(), sqlText)
}

// guardedSetting reports whether the words after SET/RESET name the S3 URL
// setting (#138), or a quoted name, which cannot be told from it: the lock lets
// that one setting through after httpfs loads, so it is checked here.
func guardedSetting(words []string) bool {
	for len(words) > 0 && (words[0] == "GLOBAL" || words[0] == "SESSION" || words[0] == "LOCAL") {
		words = words[1:]
	}
	return len(words) > 0 && (words[0] == "S3_URL_COMPATIBILITY_MODE" || words[0] == "HTTP_TIMEOUT" || words[0] == "HTTP_RETRIES" || words[0] == "?" || words[0] == "ALL")
}

func secretDDL(first string, rest []string) error {
	for _, w := range rest {
		if w == "SECRET" || w == "SECRETS" {
			return fmt.Errorf("%s SECRET is not available here: credentials live in your OS keychain, never in SQL", first)
		}
		if w != "OR" && w != "REPLACE" && w != "PERSISTENT" && w != "TEMPORARY" && w != "TEMP" && w != "IF" && w != "NOT" && w != "EXISTS" {
			break
		}
	}
	return nil
}

// RequireSelect fails unless sqlText is exactly one SELECT statement (including
// WITH ... SELECT, VALUES and FROM-first queries), judged by DuckDB's own
// parser: json_serialize_sql parses without running anything and serialises
// only a single SELECT. Callers use it before placing text inside COPY (...).
func RequireSelect(ctx context.Context, db *sql.DB, sqlText string) error {
	var out string
	if err := db.QueryRowContext(ctx, "SELECT CAST(json_serialize_sql("+session.QuoteLiteral(sqlText)+") AS VARCHAR)").Scan(&out); err != nil {
		return fmt.Errorf("the query is not a single SELECT statement: %w", err)
	}
	var res struct {
		Error        bool   `json:"error"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return fmt.Errorf("could not check the query: %w", err)
	}
	if res.Error {
		return fmt.Errorf("the query must be exactly one SELECT statement (%s)", strings.TrimSpace(res.ErrorMessage))
	}
	return nil
}

type statement struct {
	words []string // first words, upper-cased; "?" stands for a quoted token
	hasTo bool     // a bare TO keyword appears anywhere in the statement
	hasEq bool     // a bare "=" appears outside quotes (PRAGMA name = value)
	text  string   // the statement's own text, without its ";"
}

// isWordRune reports whether r can continue an identifier (and be part of a
// dollar-quote tag): letters, digits, "_" and any non-ASCII rune, as in DuckDB
// and PostgreSQL. A "$" also continues an identifier (x$a$ is one name) but is
// handled by the callers, because it starts a dollar quote or a parameter
// anywhere else.
func isWordRune(r rune) bool {
	return r == '_' || r >= 0x80 || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// scanNumber returns the index just after the numeric literal that starts at
// rs[i] (a digit): digits with "_" only between two digits (never after a "."
// or an exponent mark: "1._5" is 1. then the name _5), an optional fraction, and an
// exponent only when digits follow it. What follows ("0x1f" -> 0 then x1f,
// "1e$" -> 1 then e$) is the next token, as DuckDB lexes it, so a "$" after a
// number starts a dollar quote while a "$" inside a name does not.
func scanNumber(rs []rune, i int) int {
	digits := func(j int) int {
		for j < len(rs) && (isDigit(rs[j]) || (rs[j] == '_' && j > 0 && isDigit(rs[j-1]) && j+1 < len(rs) && isDigit(rs[j+1]))) {
			j++
		}
		return j
	}
	j := digits(i)
	if j < len(rs) && rs[j] == '.' {
		j = digits(j + 1)
	}
	if j < len(rs) && (rs[j] == 'e' || rs[j] == 'E') {
		k := j + 1
		if k < len(rs) && (rs[k] == '+' || rs[k] == '-') {
			k++
		}
		if k < len(rs) && isDigit(rs[k]) {
			j = digits(k)
		}
	}
	return j
}

func statements(s string) []statement {
	var out []statement
	var cur statement
	var word strings.Builder
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		w := strings.ToUpper(word.String())
		word.Reset()
		if w == "TO" {
			cur.hasTo = true
		}
		if len(cur.words) < 6 {
			cur.words = append(cur.words, w)
		}
	}
	quoted := func() {
		if len(cur.words) < 6 {
			cur.words = append(cur.words, "?") // a quoted token is never a keyword
		}
	}
	rs := []rune(s)
	start := 0
	end := func(at int) {
		flushWord()
		cur.text = strings.TrimSpace(string(rs[start:at]))
		start = at + 1
		out = append(out, cur)
		cur = statement{}
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			flushWord()
			for i < len(rs) && rs[i] != '\n' && rs[i] != '\r' {
				i++
			}
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			flushWord()
			i = skipBlockComment(rs, i) - 1
		case r == '$' && word.Len() > 0:
			word.WriteRune(r) // x$a$ is one identifier
		case isDigit(r) && word.Len() == 0:
			j := scanNumber(rs, i)
			word.WriteString(string(rs[i:j]))
			flushWord()
			i = j - 1
		case r == '$':
			// $tag$ ... $tag$ (tag may be empty); $1 and the like are parameters.
			j := i + 1
			for j < len(rs) && isWordRune(rs[j]) {
				j++
			}
			if j < len(rs) && rs[j] == '$' && (j == i+1 || !(rs[i+1] >= '0' && rs[i+1] <= '9')) {
				delim := string(rs[i : j+1])
				flushWord()
				quoted()
				k := j + 1
				for k < len(rs) && !strings.HasPrefix(string(rs[k:min(k+len(delim), len(rs))]), delim) {
					k++
				}
				i = k + len(delim) - 1
			} else {
				flushWord()
			}
		case r == '\'' || r == '"':
			escape := false
			if r == '\'' && (word.String() == "E" || word.String() == "e") {
				word.Reset() // the E of E'...' is a prefix, not a word
				escape = true
			}
			flushWord()
			quoted()
			for i++; i < len(rs); i++ {
				if escape && rs[i] == '\\' {
					i++
					continue
				}
				if rs[i] == r {
					if i+1 < len(rs) && rs[i+1] == r {
						i++
						continue
					}
					break
				}
			}
		case r == ';':
			end(i)
		case r == '=':
			cur.hasEq = true
			flushWord()
		case isWordRune(r):
			word.WriteRune(r)
		default:
			flushWord()
		}
	}
	end(len(rs))
	return out
}

// skipBlockComment returns the index just after the block comment that opens
// at rs[i] ("/*"). Block comments nest, as in DuckDB and PostgreSQL, so
// "/* a /* b */ c */" is one comment. An unterminated or mis-nested comment
// swallows the rest of the text (fail closed: nothing after it is read as
// SQL). Dollar-quoted strings ($tag$...$tag$) and E'...' strings are lexed
// identically by statements and Tokens, in their own cases.
func skipBlockComment(rs []rune, i int) int {
	depth := 0
	for i < len(rs) {
		switch {
		case rs[i] == '/' && i+1 < len(rs) && rs[i+1] == '*':
			depth++
			i += 2
		case rs[i] == '*' && i+1 < len(rs) && rs[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(rs)
}
