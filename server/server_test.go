package server

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/diallosouleymane/strata/btree"
)

func startServer(t *testing.T) string {
	t.Helper()
	db, err := btree.Open(filepath.Join(t.TempDir(), "strata.db"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0") // port 0 : le système en choisit un libre
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- Serve(ctx, ln, db) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve : %v", err)
		}
		db.Close()
	})
	return ln.Addr().String()
}

func dial(t *testing.T, addr string) *Client {
	t.Helper()
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func do(t *testing.T, c *Client, line string) string {
	t.Helper()
	out, _, err := c.Execute(line)
	if err != nil {
		t.Fatalf("%s : %v", line, err)
	}
	return out
}

func TestServerCommands(t *testing.T) {
	c := dial(t, startServer(t))
	cases := []struct{ in, want string }{
		{"SET a 1", "OK"},
		{"GET a", "1"},
		{"", ""},
		{"SET b 2", "OK"},
		{"SCAN", "a = 1\nb = 2"},
		{"CREATE TABLE t (id INT, v TEXT)", "CREATE TABLE"},
		{"INSERT INTO t VALUES (1, 'x')", "INSERT 1"},
		{"SELECT v FROM t", "v\n-\nx\n(1 row, full table scan)"},
		{"NOPE", "ERR unknown command 'NOPE'"},
	}
	for _, cs := range cases {
		if got := do(t, c, cs.in); got != cs.want {
			t.Errorf("%q = %q ; attendu %q", cs.in, got, cs.want)
		}
	}
	if _, inTx, _ := c.Execute("BEGIN"); !inTx {
		t.Fatal("après BEGIN, le serveur doit signaler une transaction ouverte")
	}
	if _, inTx, _ := c.Execute("ROLLBACK"); inTx {
		t.Fatal("après ROLLBACK, plus de transaction")
	}
}

func TestServerIsolatesClients(t *testing.T) {
	addr := startServer(t)
	alice, bob := dial(t, addr), dial(t, addr)
	do(t, alice, "BEGIN")
	do(t, alice, "SET secret 42")
	if got := do(t, bob, "GET secret"); got != "(nil)" {
		t.Fatalf("bob voit %q avant le COMMIT d'alice", got)
	}
	do(t, alice, "COMMIT")
	if got := do(t, bob, "GET secret"); got != "42" {
		t.Fatalf("bob voit %q après le COMMIT", got)
	}
}

// Un client qui se déconnecte au milieu d'une transaction ne doit ni la valider, ni bloquer les autres.
func TestServerDisconnectRollsBack(t *testing.T) {
	addr := startServer(t)
	quitter := dial(t, addr)
	do(t, quitter, "BEGIN")
	do(t, quitter, "SET fantome 1")
	quitter.Close()

	other := dial(t, addr)
	if got := do(t, other, "BEGIN"); got != "OK" {
		t.Fatalf("BEGIN après la déconnexion : %q", got)
	}
	if got := do(t, other, "GET fantome"); got != "(nil)" {
		t.Fatalf("la transaction abandonnée a été validée : %q", got)
	}
}

// Vingt clients incrémentent le même compteur en même temps. Comme une seule transaction d'écriture
// existe à la fois, aucune mise à jour n'est perdue : le compteur finit exactement à 20 × 10.
func TestServerConcurrentIncrementsLoseNothing(t *testing.T) {
	addr := startServer(t)
	do(t, dial(t, addr), "SET compteur 0")

	const clients, rounds = 20, 10
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Dial(addr)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			for r := 0; r < rounds; r++ {
				if out, _, err := c.Execute("BEGIN"); err != nil || out != "OK" {
					errs <- fmt.Errorf("BEGIN : %q, %v", out, err)
					return
				}
				v, _, _ := c.Execute("GET compteur")
				n, _ := strconv.Atoi(v)
				c.Execute(fmt.Sprintf("SET compteur %d", n+1))
				if out, _, err := c.Execute("COMMIT"); err != nil || out != "OK" {
					errs <- fmt.Errorf("COMMIT : %q, %v", out, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := do(t, dial(t, addr), "GET compteur"); got != strconv.Itoa(clients*rounds) {
		t.Fatalf("compteur = %s ; attendu %d", got, clients*rounds)
	}
}
