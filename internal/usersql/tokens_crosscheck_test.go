package usersql

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/SandwichLabs/slaw/internal/sqllex"
)

// project turns the policy lexer's statements into usersql's token form
// (punctuation dropped), so the two lexers can be compared directly. A
// statement that is only punctuation has no tokens and is dropped on purpose:
// Tokens omits empty statements too.
func project(stmts [][]sqllex.Tok) [][]Token {
	var out [][]Token
	for _, st := range stmts {
		var cur []Token
		for _, t := range st {
			switch t.Kind {
			case sqllex.Word:
				cur = append(cur, Token{TokWord, t.Text})
			case sqllex.Ident:
				cur = append(cur, Token{TokIdent, t.Text})
			case sqllex.String:
				cur = append(cur, Token{TokString, t.Text})
			case sqllex.Paren:
				cur = append(cur, Token{TokParen, t.Text})
			}
		}
		if len(cur) > 0 {
			out = append(out, cur)
		}
	}
	return out
}

// crossCheckCorpus is the shared input of TestLexersAgree. The editor's courtesy
// checks (usersql) and the agent policy (sqllex) must see the same statements,
// the same string/identifier/word spans and the same comment-free code (#22 O4).
var crossCheckCorpus = map[string]string{
	"plain":                  "SELECT 1; SELECT 2",
	"semicolon in string":    "SELECT 'a;b'; SELECT 2",
	"doubled quote":          "SELECT 'it''s; ok'; SELECT 2",
	"quoted ident":           `SELECT "a;b", "Q""x" FROM "t"`,
	"line comment":           "SELECT 1 -- ; DROP x\n; SELECT 2",
	"line comment cr":        "SELECT 1 -- ; x\r; SELECT 2",
	"block comment":          "SELECT /* ; 'x */ 1; SELECT 2",
	"nested block comment":   "SELECT /* a /* ; b */ ; c */ 1; SELECT 2",
	"comment glued":          "CREATE/**/SECRET s (TYPE http); -- c\nSELECT 1",
	"comment inside string":  "SELECT '-- not a comment', '/* nor this */'",
	"string inside comment":  "SELECT 1 /* ' */ , 2",
	"E string":               `SELECT E'it\'s; x', e'a\\b'; SELECT 2`,
	"E string backslash end": `SELECT E'a\\'; SELECT 2`,
	"E-ending word":          "SELECT ONE'x'; SELECT xE'y'",
	"dollar quote":           "SELECT $$a;b$$; SELECT $t$x ' ; $t$",
	"dollar quote mismatch":  "SELECT $a$ x $b$ y $a$; SELECT 2",
	"dollar param":           "SELECT $1, $name FROM t WHERE a = $2",
	"dollar in identifier":   "SELECT x$a$ FROM t; SELECT 1",
	"number then dollar":     "SELECT 1a$b$, 1e5$b$ x $b$",
	"numbers":                "SELECT 1_000, 1.5e-3, .5, 0x1F, 1e",
	"unicode word":           "SELECT é, 日本語 FROM \"ünï\"; SELECT 'ß;'",
	"unicode string":         "SELECT '日本;語'; SELECT 2",
	"parens":                 "SELECT f((1), (2)) FROM g()",
	"empty statements":       ";;SELECT 1;;  ; -- x\n",
	"only comment":           "  -- just a comment\n",
	"punct dropped":          "SELECT a::int, b->>'k', c || d FROM t",
	"unterminated string":    "SELECT 'abc; SELECT 2",
	"unterminated ident":     `SELECT "abc; SELECT 2`,
	"unterminated comment":   "SELECT 1 /* ; SELECT 2",
	"unterminated dollar":    "SELECT $t$ abc; SELECT 2",
	"adjacent strings":       "SELECT 'a' 'b'; SELECT 2",
	"multibyte dollar tag":   "SELECT $é$x$é$;SELECT 2",
	"cjk dollar tag":         "SELECT $日本$x$日本$;DROP SECRET s",
	"nbsp dollar tag":        "SELECT $\u00a0$x$\u00a0$;SELECT 2",
	"bom dollar tag":         "SELECT $\ufeff$x$\ufeff$;SELECT 2",
	"U& string":              "SELECT U&'d\\0061t\\+000061'; SELECT 2",
	"tabs and newlines":      "SELECT\t1\n;\n\tSELECT\r\n2",
}

// agree checks usersql.Tokens and statements() against the policy lexer. Invalid
// UTF-8 is normalised first: usersql decodes to runes (U+FFFD) while sqllex is
// bytewise, so raw invalid bytes are out of scope for this comparison.
func agree(t *testing.T, src string) bool {
	t.Helper()
	src = strings.ToValidUTF8(src, "\uFFFD")
	want := project(sqllex.Statements(src))
	ok := assert.Equal(t, want, Tokens(src), "input %q", src)
	return assert.Equal(t, firstWords(want), statementWords(src), "statements() words, input %q", src) && ok
}

// firstWords is what statements() records per statement: the first six words,
// a quoted token as "?", parentheses not at all.
func firstWords(stmts [][]Token) [][]string {
	var out [][]string
	for _, st := range stmts {
		var ws []string
		for _, tk := range st {
			switch tk.Kind {
			case TokWord:
				ws = append(ws, tk.Text)
			case TokString, TokIdent:
				ws = append(ws, "?")
			}
		}
		if len(ws) > 6 {
			ws = ws[:6]
		}
		if len(ws) > 0 {
			out = append(out, ws)
		}
	}
	return out
}

func statementWords(src string) [][]string {
	var out [][]string
	for _, st := range statements(src) {
		if len(st.words) > 0 {
			out = append(out, st.words)
		}
	}
	return out
}

func TestLexersAgree(t *testing.T) {
	for name, src := range crossCheckCorpus {
		t.Run(name, func(t *testing.T) { agree(t, src) })
	}
}

// TestLexersAgreeOnGeneratedInput glues random lexically awkward fragments
// together (fixed seed, so a failure reproduces) and requires the same answer.
func TestLexersAgreeOnGeneratedInput(t *testing.T) {
	frags := []string{"'", `"`, "$$", "$t$", "E", "e", "x", "1", "a$", "$", "-", "--", "/*", "*/", "\n", "\r", ";", " ", "(", ")", "\\", "é", "1e", ".", "$é$", "\u00a0", "\ufeff", "''", "$1", "U&", "+", "=", "\xff", "\xc3"}
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 20000; n++ {
		var b strings.Builder
		for i := rng.Intn(10) + 1; i > 0; i-- {
			b.WriteString(frags[rng.Intn(len(frags))])
		}
		src := b.String()
		if !agree(t, src) {
			return
		}
	}
}
