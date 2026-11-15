package usersql

import "strings"

// TokenKind classifies a lexical token.
type TokenKind int

// Token kinds.
const (
	TokWord   TokenKind = iota // bare word or number; Text is upper-cased
	TokString                  // 'x', E'x' or $tag$x$tag$; Text is the decoded content
	TokIdent                   // "x"; Text is the content, case kept
	TokParen                   // "(" or ")"; Text is that character
)

// Token is one comment-free token of a statement.
type Token struct {
	Kind TokenKind
	Text string
}

// Tokens splits sqlText into statements and each statement into tokens, with
// comments removed. It follows the same lexical rules as Check (line and block
// comments, single and double quotes with doubled-quote escapes, E'...' with
// backslash escapes, $tag$...$tag$), so a semicolon or a keyword inside a
// literal or a comment is never mistaken for SQL. Punctuation other than
// parentheses is dropped. Empty statements are omitted.
func Tokens(sqlText string) [][]Token {
	var out [][]Token
	var cur []Token
	var word strings.Builder
	flushWord := func() {
		if word.Len() > 0 {
			cur = append(cur, Token{TokWord, strings.ToUpper(word.String())})
			word.Reset()
		}
	}
	end := func() {
		flushWord()
		if len(cur) > 0 {
			out = append(out, cur)
		}
		cur = nil
	}
	rs := []rune(sqlText)
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
			j := i + 1
			for j < len(rs) && isWordRune(rs[j]) {
				j++
			}
			if j < len(rs) && rs[j] == '$' && (j == i+1 || !(rs[i+1] >= '0' && rs[i+1] <= '9')) {
				delim := string(rs[i : j+1])
				dl := j + 1 - i // the tag's length in runes: rs indexes runes, len(delim) counts bytes
				flushWord()
				k := j + 1
				for k < len(rs) && !strings.HasPrefix(string(rs[k:min(k+dl, len(rs))]), delim) {
					k++
				}
				cur = append(cur, Token{TokString, string(rs[j+1 : min(k, len(rs))])})
				i = k + dl - 1
			} else {
				flushWord()
			}
		case r == '\'' || r == '"':
			escape := false
			if r == '\'' && (word.String() == "E" || word.String() == "e") {
				word.Reset()
				escape = true
			}
			flushWord()
			var b strings.Builder
			for i++; i < len(rs); i++ {
				if escape && rs[i] == '\\' && i+1 < len(rs) {
					i++
					b.WriteRune(rs[i])
					continue
				}
				if rs[i] == r {
					if i+1 < len(rs) && rs[i+1] == r {
						b.WriteRune(r)
						i++
						continue
					}
					break
				}
				b.WriteRune(rs[i])
			}
			kind := TokString
			if r == '"' {
				kind = TokIdent
			}
			cur = append(cur, Token{kind, b.String()})
		case r == ';':
			end()
		case r == '(' || r == ')':
			flushWord()
			cur = append(cur, Token{TokParen, string(r)})
		case isWordRune(r):
			word.WriteRune(r)
		default:
			flushWord()
		}
	}
	end()
	return out
}
