package sql

import (
	"fmt"
	"strings"
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokKeyword
	tokIdent
	tokInt
	tokString
	tokSymbol
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

var keywords = map[string]bool{
	"CREATE": true, "TABLE": true, "INSERT": true, "INTO": true, "VALUES": true,
	"SELECT": true, "FROM": true, "WHERE": true, "LIMIT": true,
	"DELETE": true, "UPDATE": true, "SET": true,
	"AND": true, "OR": true, "NOT": true, "INT": true, "TEXT": true,
}

// lex découpe une requête en tokens. Les mots-clés sont mis en majuscules et les noms en minuscules,
// comme Postgres le fait pour les noms sans guillemets : "Users" et "users" désignent la même table.
func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case isLetter(c):
			j := i
			for j < len(src) && (isLetter(src[j]) || isDigit(src[j])) {
				j++
			}
			word := src[i:j]
			if up := strings.ToUpper(word); keywords[up] {
				toks = append(toks, token{tokKeyword, up, i})
			} else {
				toks = append(toks, token{tokIdent, strings.ToLower(word), i})
			}
			i = j
		case isDigit(c):
			j := i
			for j < len(src) && isDigit(src[j]) {
				j++
			}
			toks = append(toks, token{tokInt, src[i:j], i})
			i = j
		case c == '\'':
			s, end, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{tokString, s, i})
			i = end
		default:
			if i+1 < len(src) {
				switch two := src[i : i+2]; two {
				case "<=", ">=", "!=", "<>":
					if two == "<>" {
						two = "!="
					}
					toks = append(toks, token{tokSymbol, two, i})
					i += 2
					continue
				}
			}
			if strings.IndexByte("(),*=<>;-", c) < 0 {
				return nil, &SyntaxError{Pos: i, Msg: fmt.Sprintf("unexpected character %q", c)}
			}
			toks = append(toks, token{tokSymbol, string(c), i})
			i++
		}
	}
	return append(toks, token{tokEOF, "", len(src)}), nil
}

// lexString lit une chaîne entre apostrophes ; deux apostrophes à la suite y représentent une apostrophe, comme en SQL standard.
func lexString(src string, start int) (string, int, error) {
	var b strings.Builder
	for j := start + 1; j < len(src); j++ {
		if src[j] != '\'' {
			b.WriteByte(src[j])
			continue
		}
		if j+1 < len(src) && src[j+1] == '\'' {
			b.WriteByte('\'')
			j++
			continue
		}
		return b.String(), j + 1, nil
	}
	return "", 0, &SyntaxError{Pos: start, Msg: "unterminated string"}
}

func isLetter(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
