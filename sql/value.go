package sql

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

type Type uint8

const (
	TypeInt  Type = 1
	TypeText Type = 2
	typeBool Type = 3 // résultat d'une comparaison, jamais stocké
)

func (t Type) String() string {
	switch t {
	case TypeInt:
		return "INT"
	case TypeText:
		return "TEXT"
	case typeBool:
		return "BOOLEAN"
	}
	return fmt.Sprintf("type(%d)", t)
}

type Value struct {
	Type Type
	Int  int64
	Text string
	Bool bool
}

func IntValue(n int64) Value   { return Value{Type: TypeInt, Int: n} }
func TextValue(s string) Value { return Value{Type: TypeText, Text: s} }
func boolValue(b bool) Value   { return Value{Type: typeBool, Bool: b} }

func (v Value) String() string {
	switch v.Type {
	case TypeInt:
		return strconv.FormatInt(v.Int, 10)
	case TypeText:
		return v.Text
	case typeBool:
		return strings.ToUpper(strconv.FormatBool(v.Bool))
	}
	return "?"
}

func compareValues(a, b Value) (int, error) {
	if a.Type != b.Type {
		return 0, fmt.Errorf("cannot compare %s with %s", a.Type, b.Type)
	}
	switch a.Type {
	case TypeInt:
		return cmp.Compare(a.Int, b.Int), nil
	case TypeText:
		return strings.Compare(a.Text, b.Text), nil
	}
	return 0, fmt.Errorf("cannot compare %s values", a.Type)
}
