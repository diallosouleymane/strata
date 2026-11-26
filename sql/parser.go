package sql

import (
	"fmt"
	"strconv"
)

// Les instructions reconnues :
//
//	CREATE TABLE t (col INT|TEXT, ...)           la première colonne est la clé primaire
//	INSERT INTO t VALUES (v, ...), (v, ...)
//	SELECT * | col, ... FROM t [WHERE expr] [LIMIT n]
//	UPDATE t SET col = expr, ... [WHERE expr]
//	DELETE FROM t [WHERE expr]
//
// expr : comparaisons (= != <> < <= > >=) entre colonnes, entiers et 'chaînes', combinées par AND, OR, NOT
// et des parenthèses.
type Statement interface{ statement() }

type CreateTable struct {
	Name    string
	Columns []Column
}

type Column struct {
	Name string
	Type Type
}

type Insert struct {
	Table string
	Rows  [][]Expr
}

type Select struct {
	Table   string
	Columns []string // nil pour *
	Where   Expr     // nil sans WHERE
	Limit   int      // -1 sans LIMIT
}

type Update struct {
	Table string
	Set   []Assignment
	Where Expr
}

type Assignment struct {
	Column string
	Value  Expr
}

type Delete struct {
	Table string
	Where Expr
}

func (*CreateTable) statement() {}
func (*Insert) statement()      {}
func (*Select) statement()      {}
func (*Update) statement()      {}
func (*Delete) statement()      {}

type Expr interface{ expr() }

type Literal struct{ Value Value }
type ColumnRef struct{ Name string }
type Binary struct {
	Op          string // = != < <= > >= AND OR
	Left, Right Expr
}
type Not struct{ X Expr }

func (Literal) expr()   {}
func (ColumnRef) expr() {}
func (Binary) expr()    {}
func (Not) expr()       {}

type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("syntax error at position %d: %s", e.Pos, e.Msg)
}

type parser struct {
	toks []token
	i    int
}

// Parse est un parseur à descente récursive : une fonction par règle de la grammaire. Une erreur
// panique avec un *SyntaxError, récupéré ici, ce qui évite de vérifier une erreur après chaque token.
func Parse(src string) (stmt Statement, err error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	defer func() {
		if r := recover(); r != nil {
			se, ok := r.(*SyntaxError)
			if !ok {
				panic(r)
			}
			stmt, err = nil, se
		}
	}()
	stmt = p.statement()
	p.acceptSymbol(";")
	if p.peek().kind != tokEOF {
		p.fail("unexpected %s after the end of the statement", describe(p.peek()))
	}
	return stmt, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) fail(format string, args ...any) {
	panic(&SyntaxError{Pos: p.peek().pos, Msg: fmt.Sprintf(format, args...)})
}

func (p *parser) acceptKeyword(kw string) bool {
	if t := p.peek(); t.kind == tokKeyword && t.text == kw {
		p.i++
		return true
	}
	return false
}

func (p *parser) expectKeyword(kw string) {
	if !p.acceptKeyword(kw) {
		p.fail("expected %s, found %s", kw, describe(p.peek()))
	}
}

func (p *parser) acceptSymbol(s string) bool {
	if t := p.peek(); t.kind == tokSymbol && t.text == s {
		p.i++
		return true
	}
	return false
}

func (p *parser) expectSymbol(s string) {
	if !p.acceptSymbol(s) {
		p.fail("expected %q, found %s", s, describe(p.peek()))
	}
}

func (p *parser) ident() string {
	t := p.peek()
	if t.kind != tokIdent {
		p.fail("expected a name, found %s", describe(t))
	}
	p.i++
	return t.text
}

func describe(t token) string {
	switch t.kind {
	case tokEOF:
		return "end of input"
	case tokString:
		return "'" + t.text + "'"
	}
	return strconv.Quote(t.text)
}

func (p *parser) statement() Statement {
	switch {
	case p.acceptKeyword("CREATE"):
		return p.createTable()
	case p.acceptKeyword("INSERT"):
		return p.insert()
	case p.acceptKeyword("SELECT"):
		return p.selectStmt()
	case p.acceptKeyword("UPDATE"):
		return p.update()
	case p.acceptKeyword("DELETE"):
		return p.deleteStmt()
	}
	p.fail("expected CREATE, INSERT, SELECT, UPDATE or DELETE, found %s", describe(p.peek()))
	return nil
}

func (p *parser) createTable() *CreateTable {
	p.expectKeyword("TABLE")
	s := &CreateTable{Name: p.ident()}
	p.expectSymbol("(")
	for {
		col := Column{Name: p.ident()}
		switch {
		case p.acceptKeyword("INT"):
			col.Type = TypeInt
		case p.acceptKeyword("TEXT"):
			col.Type = TypeText
		default:
			p.fail("expected a column type (INT or TEXT), found %s", describe(p.peek()))
		}
		s.Columns = append(s.Columns, col)
		if !p.acceptSymbol(",") {
			break
		}
	}
	p.expectSymbol(")")
	return s
}

func (p *parser) insert() *Insert {
	p.expectKeyword("INTO")
	s := &Insert{Table: p.ident()}
	p.expectKeyword("VALUES")
	for {
		p.expectSymbol("(")
		var row []Expr
		for {
			row = append(row, p.expr())
			if !p.acceptSymbol(",") {
				break
			}
		}
		p.expectSymbol(")")
		s.Rows = append(s.Rows, row)
		if !p.acceptSymbol(",") {
			return s
		}
	}
}

func (p *parser) selectStmt() *Select {
	s := &Select{Limit: -1}
	if !p.acceptSymbol("*") {
		for {
			s.Columns = append(s.Columns, p.ident())
			if !p.acceptSymbol(",") {
				break
			}
		}
	}
	p.expectKeyword("FROM")
	s.Table = p.ident()
	s.Where = p.where()
	if p.acceptKeyword("LIMIT") {
		t := p.peek()
		n, err := strconv.Atoi(t.text)
		if t.kind != tokInt || err != nil {
			p.fail("expected a number after LIMIT, found %s", describe(t))
		}
		p.i++
		s.Limit = n
	}
	return s
}

func (p *parser) update() *Update {
	s := &Update{Table: p.ident()}
	p.expectKeyword("SET")
	for {
		col := p.ident()
		p.expectSymbol("=")
		s.Set = append(s.Set, Assignment{Column: col, Value: p.expr()})
		if !p.acceptSymbol(",") {
			break
		}
	}
	s.Where = p.where()
	return s
}

func (p *parser) deleteStmt() *Delete {
	p.expectKeyword("FROM")
	s := &Delete{Table: p.ident()}
	s.Where = p.where()
	return s
}

func (p *parser) where() Expr {
	if p.acceptKeyword("WHERE") {
		return p.expr()
	}
	return nil
}

// La priorité des opérateurs vient de l'imbrication : OR appelle AND, qui appelle NOT, qui appelle
// les comparaisons. a OR b AND c se lit donc a OR (b AND c).
func (p *parser) expr() Expr {
	left := p.and()
	for p.acceptKeyword("OR") {
		left = Binary{Op: "OR", Left: left, Right: p.and()}
	}
	return left
}

func (p *parser) and() Expr {
	left := p.not()
	for p.acceptKeyword("AND") {
		left = Binary{Op: "AND", Left: left, Right: p.not()}
	}
	return left
}

func (p *parser) not() Expr {
	if p.acceptKeyword("NOT") {
		return Not{X: p.not()}
	}
	return p.comparison()
}

func (p *parser) comparison() Expr {
	left := p.primary()
	t := p.peek()
	if t.kind == tokSymbol {
		switch t.text {
		case "=", "!=", "<", "<=", ">", ">=":
			p.i++
			return Binary{Op: t.text, Left: left, Right: p.primary()}
		}
	}
	return left
}

func (p *parser) primary() Expr {
	t := p.peek()
	switch {
	case t.kind == tokInt:
		p.i++
		return Literal{Value: p.parseInt(t.text, t)}
	case t.kind == tokSymbol && t.text == "-":
		p.i++
		n := p.peek()
		if n.kind != tokInt {
			p.fail("expected a number after '-', found %s", describe(n))
		}
		p.i++
		return Literal{Value: p.parseInt("-"+n.text, n)}
	case t.kind == tokString:
		p.i++
		return Literal{Value: TextValue(t.text)}
	case t.kind == tokIdent:
		p.i++
		return ColumnRef{Name: t.text}
	case p.acceptSymbol("("):
		e := p.expr()
		p.expectSymbol(")")
		return e
	}
	p.fail("expected a value or a column, found %s", describe(t))
	return nil
}

func (p *parser) parseInt(text string, t token) Value {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		panic(&SyntaxError{Pos: t.pos, Msg: fmt.Sprintf("integer %s out of range", text)})
	}
	return IntValue(n)
}
