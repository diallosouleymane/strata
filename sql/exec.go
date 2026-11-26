package sql

import (
	"bytes"
	"fmt"

	"github.com/diallosouleymane/strata/btree"
)

// KV est ce que l'exécuteur attend du stockage. *btree.Tx et *btree.DB le satisfont tous les deux.
type KV interface {
	Get(key []byte) ([]byte, bool, error)
	Set(key, val []byte) error
	Delete(key []byte) (bool, error)
	Scan(start, end []byte, fn func(key, val []byte) bool) error
}

// Result est soit un tableau (Columns et Rows, avec le plan d'exécution), soit un simple message.
type Result struct {
	Columns []string
	Rows    [][]Value
	Plan    string
	Message string
}

type table struct {
	name string
	cols []Column
}

func (t *table) index(col string) int {
	for i, c := range t.cols {
		if c.Name == col {
			return i
		}
	}
	return -1
}

// Exec exécute une instruction. Les écritures sont validées avant d'être appliquées : une instruction qui
// échoue n'a rien modifié, même au milieu d'une transaction.
func Exec(kv KV, stmt Statement) (*Result, error) {
	switch s := stmt.(type) {
	case *CreateTable:
		return createTable(kv, s)
	case *Insert:
		return insert(kv, s)
	case *Select:
		return selectRows(kv, s)
	case *Update:
		return update(kv, s)
	case *Delete:
		return deleteRows(kv, s)
	}
	return nil, fmt.Errorf("unsupported statement %T", stmt)
}

func loadTable(kv KV, name string) (*table, error) {
	b, ok, err := kv.Get(schemaKey(name))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("table %s does not exist", name)
	}
	return decodeSchema(name, b)
}

func createTable(kv KV, s *CreateTable) (*Result, error) {
	if _, ok, err := kv.Get(schemaKey(s.Name)); err != nil || ok {
		if err == nil {
			err = fmt.Errorf("table %s already exists", s.Name)
		}
		return nil, err
	}
	t := &table{name: s.Name, cols: s.Columns}
	seen := map[string]bool{}
	for _, c := range t.cols {
		if seen[c.Name] {
			return nil, fmt.Errorf("column %s appears twice", c.Name)
		}
		seen[c.Name] = true
	}
	if err := kv.Set(schemaKey(s.Name), encodeSchema(t)); err != nil {
		return nil, err
	}
	return &Result{Message: "CREATE TABLE"}, nil
}

type write struct{ key, val []byte }

func checkSize(w write) error {
	if len(w.key) > btree.MaxKeySize {
		return fmt.Errorf("primary key too long (%d bytes, max %d)", len(w.key), btree.MaxKeySize)
	}
	if len(w.val) > btree.MaxValSize {
		return fmt.Errorf("row too large (%d bytes, max %d)", len(w.val), btree.MaxValSize)
	}
	return nil
}

func apply(kv KV, writes []write) error {
	for _, w := range writes {
		if err := kv.Set(w.key, w.val); err != nil {
			return err
		}
	}
	return nil
}

func insert(kv KV, s *Insert) (*Result, error) {
	t, err := loadTable(kv, s.Table)
	if err != nil {
		return nil, err
	}
	var writes []write
	seen := map[string]bool{}
	for _, exprs := range s.Rows {
		if len(exprs) != len(t.cols) {
			return nil, fmt.Errorf("table %s has %d columns but %d values were given", t.name, len(t.cols), len(exprs))
		}
		row := make([]Value, len(exprs))
		for i, e := range exprs {
			v, err := eval(e, nil, nil)
			if err != nil {
				return nil, err
			}
			if v.Type != t.cols[i].Type {
				return nil, fmt.Errorf("column %s is %s, got %s %s", t.cols[i].Name, t.cols[i].Type, v.Type, v)
			}
			row[i] = v
		}
		w := write{key: rowKey(t, row[0]), val: encodeRow(t, row)}
		if err := checkSize(w); err != nil {
			return nil, err
		}
		_, exists, err := kv.Get(w.key)
		if err != nil {
			return nil, err
		}
		if exists || seen[string(w.key)] {
			return nil, fmt.Errorf("duplicate primary key %s = %s", t.cols[0].Name, row[0])
		}
		seen[string(w.key)] = true
		writes = append(writes, w)
	}
	if err := apply(kv, writes); err != nil {
		return nil, err
	}
	return &Result{Message: fmt.Sprintf("INSERT %d", len(writes))}, nil
}

func selectRows(kv KV, s *Select) (*Result, error) {
	t, err := loadTable(kv, s.Table)
	if err != nil {
		return nil, err
	}
	if err := checkColumns(s.Where, t); err != nil {
		return nil, err
	}
	var idx []int
	res := &Result{}
	if s.Columns == nil {
		for i, c := range t.cols {
			idx = append(idx, i)
			res.Columns = append(res.Columns, c.Name)
		}
	}
	for _, name := range s.Columns {
		i := t.index(name)
		if i < 0 {
			return nil, fmt.Errorf("column %s does not exist in table %s", name, t.name)
		}
		idx = append(idx, i)
		res.Columns = append(res.Columns, name)
	}

	start, end, plan := keyRange(t, s.Where)
	res.Plan = plan
	if s.Limit == 0 {
		return res, nil
	}
	err = scanRows(kv, t, start, end, s.Where, func(_ []byte, row []Value) bool {
		out := make([]Value, len(idx))
		for j, i := range idx {
			out[j] = row[i]
		}
		res.Rows = append(res.Rows, out)
		return s.Limit < 0 || len(res.Rows) < s.Limit
	})
	return res, err
}

func update(kv KV, s *Update) (*Result, error) {
	t, err := loadTable(kv, s.Table)
	if err != nil {
		return nil, err
	}
	if err := checkColumns(s.Where, t); err != nil {
		return nil, err
	}
	for _, a := range s.Set {
		switch i := t.index(a.Column); {
		case i < 0:
			return nil, fmt.Errorf("column %s does not exist in table %s", a.Column, t.name)
		case i == 0:
			// Changer la clé primaire déplacerait la ligne ailleurs dans l'arbre : ce serait un DELETE puis un INSERT.
			return nil, fmt.Errorf("cannot update the primary key column %s", a.Column)
		}
		if err := checkColumns(a.Value, t); err != nil {
			return nil, err
		}
	}

	start, end, plan := keyRange(t, s.Where)
	var writes []write
	var inner error
	err = scanRows(kv, t, start, end, s.Where, func(key []byte, row []Value) bool {
		next := append([]Value(nil), row...)
		for _, a := range s.Set {
			i := t.index(a.Column)
			v, err := eval(a.Value, t, row)
			if err == nil && v.Type != t.cols[i].Type {
				err = fmt.Errorf("column %s is %s, got %s %s", a.Column, t.cols[i].Type, v.Type, v)
			}
			if err != nil {
				inner = err
				return false
			}
			next[i] = v
		}
		w := write{key: bytes.Clone(key), val: encodeRow(t, next)}
		if inner = checkSize(w); inner != nil {
			return false
		}
		writes = append(writes, w)
		return true
	})
	if err == nil {
		err = inner
	}
	if err == nil {
		err = apply(kv, writes)
	}
	if err != nil {
		return nil, err
	}
	return &Result{Message: fmt.Sprintf("UPDATE %d", len(writes)), Plan: plan}, nil
}

func deleteRows(kv KV, s *Delete) (*Result, error) {
	t, err := loadTable(kv, s.Table)
	if err != nil {
		return nil, err
	}
	if err := checkColumns(s.Where, t); err != nil {
		return nil, err
	}
	start, end, plan := keyRange(t, s.Where)
	// On ne modifie pas l'arbre pendant qu'on le parcourt : on note les clés, puis on supprime.
	var keys [][]byte
	err = scanRows(kv, t, start, end, s.Where, func(key []byte, _ []Value) bool {
		keys = append(keys, bytes.Clone(key))
		return true
	})
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if _, err := kv.Delete(k); err != nil {
			return nil, err
		}
	}
	return &Result{Message: fmt.Sprintf("DELETE %d", len(keys)), Plan: plan}, nil
}

// scanRows parcourt les lignes de [start, end) qui satisfont where.
func scanRows(kv KV, t *table, start, end []byte, where Expr, fn func(key []byte, row []Value) bool) error {
	var inner error
	err := kv.Scan(start, end, func(k, v []byte) bool {
		row, err := decodeRow(t, v)
		if err == nil && where != nil {
			var ok bool
			if ok, err = matches(where, t, row); err == nil && !ok {
				return true
			}
		}
		if err != nil {
			inner = err
			return false
		}
		return fn(k, row)
	})
	if err != nil {
		return err
	}
	return inner
}

func matches(where Expr, t *table, row []Value) (bool, error) {
	v, err := eval(where, t, row)
	if err != nil {
		return false, err
	}
	if v.Type != typeBool {
		return false, fmt.Errorf("WHERE must be a condition, got %s %s", v.Type, v)
	}
	return v.Bool, nil
}

// checkColumns refuse une colonne inconnue avant de lire la moindre ligne : sans ça, l'erreur
// n'apparaîtrait que si la table contient au moins une ligne.
func checkColumns(e Expr, t *table) error {
	switch e := e.(type) {
	case ColumnRef:
		if t.index(e.Name) < 0 {
			return fmt.Errorf("column %s does not exist in table %s", e.Name, t.name)
		}
	case Binary:
		if err := checkColumns(e.Left, t); err != nil {
			return err
		}
		return checkColumns(e.Right, t)
	case Not:
		return checkColumns(e.X, t)
	}
	return nil
}

// eval calcule une expression sur une ligne. Sans ligne (row == nil), seules les constantes sont permises.
func eval(e Expr, t *table, row []Value) (Value, error) {
	switch e := e.(type) {
	case Literal:
		return e.Value, nil
	case ColumnRef:
		if row == nil {
			return Value{}, fmt.Errorf("column %s cannot be used here", e.Name)
		}
		i := t.index(e.Name)
		if i < 0 {
			return Value{}, fmt.Errorf("column %s does not exist in table %s", e.Name, t.name)
		}
		return row[i], nil
	case Not:
		v, err := evalBool(e.X, t, row, "NOT")
		return boolValue(!v), err
	case Binary:
		if e.Op == "AND" || e.Op == "OR" {
			l, err := evalBool(e.Left, t, row, e.Op)
			if err != nil {
				return Value{}, err
			}
			if e.Op == "AND" && !l || e.Op == "OR" && l {
				return boolValue(l), nil
			}
			r, err := evalBool(e.Right, t, row, e.Op)
			return boolValue(r), err
		}
		l, err := eval(e.Left, t, row)
		if err != nil {
			return Value{}, err
		}
		r, err := eval(e.Right, t, row)
		if err != nil {
			return Value{}, err
		}
		c, err := compareValues(l, r)
		if err != nil {
			return Value{}, err
		}
		switch e.Op {
		case "=":
			return boolValue(c == 0), nil
		case "!=":
			return boolValue(c != 0), nil
		case "<":
			return boolValue(c < 0), nil
		case "<=":
			return boolValue(c <= 0), nil
		case ">":
			return boolValue(c > 0), nil
		case ">=":
			return boolValue(c >= 0), nil
		}
		return Value{}, fmt.Errorf("unknown operator %s", e.Op)
	}
	return Value{}, fmt.Errorf("unsupported expression %T", e)
}

func evalBool(e Expr, t *table, row []Value, op string) (bool, error) {
	v, err := eval(e, t, row)
	if err != nil {
		return false, err
	}
	if v.Type != typeBool {
		return false, fmt.Errorf("%s expects conditions, got %s %s", op, v.Type, v)
	}
	return v.Bool, nil
}
