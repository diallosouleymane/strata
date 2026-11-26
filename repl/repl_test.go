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
	s := NewSession(db)
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
		{"COMMIT", "ERR no transaction in progress"},
		{"BEGIN", "OK"},
		{"BEGIN", "ERR already in a transaction"},
		{"SET pomme 99", "OK"},
		{"DEL ananas", "1"},
		{"SCAN", "banane = 2\ncerise = 7\npomme = 99"},
		{"ROLLBACK", "OK"},
		{"SCAN", "ananas = 1\nbanane = 2\ncerise = 7\npomme = 3"},
		{"begin", "OK"},
		{"SET kiwi 5", "OK"},
		{"commit", "OK"},
		{"GET kiwi", "5"},
	}
	for _, c := range cases {
		if got := s.Execute(c.in); got != c.want {
			t.Errorf("Execute(%q) = %q ; attendu %q", c.in, got, c.want)
		}
	}
}

// Une autre session ne voit pas les écritures d'une transaction en cours.
func TestSessionsAreIsolated(t *testing.T) {
	db, err := btree.Open(filepath.Join(t.TempDir(), "strata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	alice, bob := NewSession(db), NewSession(db)
	alice.Execute("BEGIN")
	alice.Execute("SET secret 42")
	if got := bob.Execute("GET secret"); got != "(nil)" {
		t.Fatalf("bob voit %q avant le commit d'alice", got)
	}
	alice.Execute("COMMIT")
	if got := bob.Execute("GET secret"); got != "42" {
		t.Fatalf("bob voit %q après le commit d'alice ; attendu 42", got)
	}
	alice.Execute("BEGIN")
	alice.Execute("SET secret 0")
	alice.Close() // déconnexion sans COMMIT
	if got := bob.Execute("GET secret"); got != "42" {
		t.Fatalf("une transaction abandonnée a fuité : %q", got)
	}
}

func TestShowEscapesControlBytes(t *testing.T) {
	if got := show([]byte("abc")); got != "abc" {
		t.Fatalf("show(abc) = %q", got)
	}
	if got := show([]byte("\x02users\x00")); got != `"\x02users\x00"` {
		t.Fatalf("show = %s", got)
	}
}
