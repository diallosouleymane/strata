package repl

import (
	"path/filepath"
	"testing"

	"github.com/diallosouleymane/strata/kv"
)

// Les cas s'enchaînent sur le même store : l'ordre compte.
func TestExecute(t *testing.T) {
	s, err := kv.Open(filepath.Join(t.TempDir(), "db.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cases := []struct{ in, want string }{
		{"SET name Issa", "OK"},
		{"GET name", "Issa"},
		{"get name", "Issa"},
		{"  GET   name  ", "Issa"},
		{"GET absent", "(nil)"},
		{"DEL name", "1"},
		{"DEL name", "0"},
		{"GET name", "(nil)"},
		{"", ""},
		{"FOO bar", "ERR unknown command 'FOO'"},
		{"SET a", "ERR wrong number of arguments for 'SET'"},
		{"GET", "ERR wrong number of arguments for 'GET'"},
		{"DEL a b", "ERR wrong number of arguments for 'DEL'"},
		{"SET x 1", "OK"},
		{"compact", "OK"},
		{"GET x", "1"},
		{"COMPACT now", "ERR wrong number of arguments for 'COMPACT'"},
	}
	for _, c := range cases {
		if got := Execute(s, c.in); got != c.want {
			t.Errorf("Execute(%q) = %q ; attendu %q", c.in, got, c.want)
		}
	}
}
