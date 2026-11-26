package repl

import (
	"path/filepath"
	"testing"

	"github.com/diallosouleymane/strata/btree"
)

// Les cas s'enchaînent sur la même base : l'ordre compte.
func TestExecute(t *testing.T) {
	db, err := btree.Open(filepath.Join(t.TempDir(), "strata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
		{"SCAN", "(empty)"},
		{"SET pomme 3", "OK"},
		{"SET ananas 1", "OK"},
		{"SET cerise 7", "OK"},
		{"SET banane 2", "OK"},
		{"scan", "ananas = 1\nbanane = 2\ncerise = 7\npomme = 3"},
		{"SCAN b", "banane = 2\ncerise = 7\npomme = 3"},
		{"SCAN b d", "banane = 2\ncerise = 7"},
		{"SCAN x", "(empty)"},
		{"SCAN a b c", "ERR wrong number of arguments for 'SCAN'"},
	}
	for _, c := range cases {
		if got := Execute(db, c.in); got != c.want {
			t.Errorf("Execute(%q) = %q ; attendu %q", c.in, got, c.want)
		}
	}
}
