package sql

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/diallosouleymane/strata/btree"
)

func newDB(t *testing.T) *btree.DB {
	t.Helper()
	db, err := btree.Open(filepath.Join(t.TempDir(), "strata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// run exécute une instruction dans sa propre transaction, comme le REPL hors BEGIN.
func run(t *testing.T, db *btree.DB, src string) (*Result, error) {
	t.Helper()
	stmt, err := Parse(src)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Exec(tx, stmt)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return res, tx.Commit()
}

func mustRun(t *testing.T, db *btree.DB, src string) *Result {
	t.Helper()
	res, err := run(t, db, src)
	if err != nil {
		t.Fatalf("%s : %v", src, err)
	}
	return res
}

// rows rend un résultat comparable : "1,Issa;2,Awa".
func rows(res *Result) string {
	var out []string
	for _, r := range res.Rows {
		var cells []string
		for _, v := range r {
			cells = append(cells, v.String())
		}
		out = append(out, strings.Join(cells, ","))
	}
	return strings.Join(out, ";")
}

func TestLex(t *testing.T) {
	toks, err := lex("select Name,age FROM Users where age>=-3 AND name <> 'l''été';")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tk := range toks {
		got = append(got, tk.text)
	}
	want := "SELECT name , age FROM users WHERE age >= - 3 AND name != l'été ; "
	if strings.Join(got, " ") != want {
		t.Fatalf("tokens :\n  %q\n  attendu %q", strings.Join(got, " "), want)
	}
	if _, err := lex("SELECT 'pas fini"); err == nil {
		t.Fatal("une chaîne non terminée doit être refusée")
	}
	if _, err := lex("SELECT # FROM t"); err == nil {
		t.Fatal("un caractère inconnu doit être refusé")
	}
}

func TestParsePrecedence(t *testing.T) {
	stmt, err := Parse("SELECT * FROM t WHERE a = 1 OR b = 2 AND NOT c = 3")
	if err != nil {
		t.Fatal(err)
	}
	w := stmt.(*Select).Where.(Binary)
	if w.Op != "OR" {
		t.Fatalf("racine %s ; OR doit lier moins fort que AND", w.Op)
	}
	if and := w.Right.(Binary); and.Op != "AND" {
		t.Fatalf("à droite du OR : %s ; attendu AND", and.Op)
	} else if _, ok := and.Right.(Not); !ok {
		t.Fatal("NOT doit s'appliquer à la comparaison c = 3")
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{
		"SELEC * FROM t",
		"SELECT * t",
		"SELECT * FROM",
		"CREATE TABLE t (id FLOAT)",
		"CREATE TABLE t (id INT,)",
		"INSERT INTO t VALUES (1, 2",
		"SELECT * FROM t WHERE",
		"SELECT * FROM t LIMIT x",
		"SELECT * FROM t extra",
		"INSERT INTO t VALUES (99999999999999999999)",
		"UPDATE t SET",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q) doit échouer", src)
		} else if _, ok := err.(*SyntaxError); !ok {
			t.Errorf("Parse(%q) : %T ; attendu *SyntaxError", src, err)
		}
	}
}

func setupUsers(t *testing.T) *btree.DB {
	db := newDB(t)
	mustRun(t, db, "CREATE TABLE users (id INT, name TEXT, age INT)")
	mustRun(t, db, "INSERT INTO users VALUES (3, 'Issa', 30), (-5, 'Awa', 25), (12, 'Koffi', 41), (7, 'Fatou', 25)")
	return db
}

func TestSelect(t *testing.T) {
	db := setupUsers(t)
	cases := []struct{ src, want, plan string }{
		{"SELECT * FROM users", "-5,Awa,25;3,Issa,30;7,Fatou,25;12,Koffi,41", planFull},
		{"SELECT name FROM users WHERE age = 25", "Awa;Fatou", planFull},
		{"SELECT name, id FROM users WHERE age > 25 AND name != 'Koffi'", "Issa,3", planFull},
		{"SELECT id FROM users WHERE age = 41 OR name = 'Awa'", "-5;12", planFull},
		{"SELECT id FROM users WHERE NOT (age = 25)", "3;12", planFull},
		{"SELECT name FROM users WHERE id = 7", "Fatou", planLookup},
		{"SELECT id FROM users WHERE id >= 0 AND id < 12", "3;7", planRange},
		{"SELECT id FROM users WHERE 3 < id", "7;12", planRange},
		{"SELECT id FROM users WHERE id <= 3 AND age = 25", "-5", planRange},
		{"SELECT id FROM users WHERE id > 100", "", planRange},
		{"SELECT id FROM users LIMIT 2", "-5;3", planFull},
		{"SELECT id FROM users LIMIT 0", "", planFull},
		{"select ID from USERS where Id = 3;", "3", planLookup},
	}
	for _, c := range cases {
		res := mustRun(t, db, c.src)
		if got := rows(res); got != c.want {
			t.Errorf("%s\n  = %q ; attendu %q", c.src, got, c.want)
		}
		if res.Plan != c.plan {
			t.Errorf("%s : plan %q ; attendu %q", c.src, res.Plan, c.plan)
		}
	}
	if res := mustRun(t, db, "SELECT name, age FROM users LIMIT 1"); strings.Join(res.Columns, ",") != "name,age" {
		t.Fatalf("colonnes %v", res.Columns)
	}
}

func TestUpdateDelete(t *testing.T) {
	db := setupUsers(t)
	if res := mustRun(t, db, "UPDATE users SET age = 26, name = 'Awa K' WHERE age = 25 AND id < 0"); res.Message != "UPDATE 1" {
		t.Fatalf("message %q", res.Message)
	}
	if got := rows(mustRun(t, db, "SELECT * FROM users WHERE id = -5")); got != "-5,Awa K,26" {
		t.Fatalf("après UPDATE : %q", got)
	}
	if res := mustRun(t, db, "DELETE FROM users WHERE age < 31"); res.Message != "DELETE 3" {
		t.Fatalf("message %q", res.Message)
	}
	if got := rows(mustRun(t, db, "SELECT id FROM users")); got != "12" {
		t.Fatalf("après DELETE : %q", got)
	}
	mustRun(t, db, "DELETE FROM users")
	if got := rows(mustRun(t, db, "SELECT id FROM users")); got != "" {
		t.Fatalf("après DELETE sans WHERE : %q", got)
	}
}

func TestTextPrimaryKey(t *testing.T) {
	db := newDB(t)
	mustRun(t, db, "CREATE TABLE villes (nom TEXT, habitants INT)")
	mustRun(t, db, "INSERT INTO villes VALUES ('Yamoussoukro', 400000), ('Abidjan', 6300000), ('Bouaké', 830000)")
	if got := rows(mustRun(t, db, "SELECT nom FROM villes")); got != "Abidjan;Bouaké;Yamoussoukro" {
		t.Fatalf("ordre %q", got)
	}
	res := mustRun(t, db, "SELECT habitants FROM villes WHERE nom = 'Bouaké'")
	if rows(res) != "830000" || res.Plan != planLookup {
		t.Fatalf("%q, %s", rows(res), res.Plan)
	}
}

// Deux tables dont l'une a pour nom le début de l'autre ne doivent pas se mélanger.
func TestTablesDoNotOverlap(t *testing.T) {
	db := newDB(t)
	mustRun(t, db, "CREATE TABLE a (id INT)")
	mustRun(t, db, "CREATE TABLE ab (id INT)")
	mustRun(t, db, "INSERT INTO a VALUES (1)")
	mustRun(t, db, "INSERT INTO ab VALUES (2)")
	if got := rows(mustRun(t, db, "SELECT * FROM a")); got != "1" {
		t.Fatalf("SELECT * FROM a = %q", got)
	}
}

func TestErrors(t *testing.T) {
	db := setupUsers(t)
	for _, c := range []struct{ src, want string }{
		{"SELECT * FROM nope", "table nope does not exist"},
		{"CREATE TABLE users (id INT)", "table users already exists"},
		{"CREATE TABLE t (a INT, a TEXT)", "column a appears twice"},
		{"INSERT INTO users VALUES (1, 'x')", "table users has 3 columns but 2 values were given"},
		{"INSERT INTO users VALUES ('x', 'y', 1)", "column id is INT, got TEXT x"},
		{"INSERT INTO users VALUES (3, 'Doublon', 1)", "duplicate primary key id = 3"},
		{"INSERT INTO users VALUES (1, 'a', age)", "column age cannot be used here"},
		{"SELECT nope FROM users", "column nope does not exist in table users"},
		{"SELECT * FROM users WHERE nope = 1", "column nope does not exist in table users"},
		{"SELECT * FROM users WHERE name > 3", "cannot compare TEXT with INT"},
		{"SELECT * FROM users WHERE age", "WHERE must be a condition, got INT 25"},
		{"SELECT * FROM users WHERE age = 30 AND 2", "AND expects conditions, got INT 2"},
		{"UPDATE users SET id = 1", "cannot update the primary key column id"},
		{"UPDATE users SET age = 'vieux'", "column age is INT, got TEXT vieux"},
		{"INSERT INTO users VALUES (99, '" + strings.Repeat("x", 3100) + "', 1)", "row too large"},
	} {
		_, err := run(t, db, c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%.60s\n  erreur %v ; attendu %q", c.src, err, c.want)
		}
	}
}

// Une instruction qui échoue ne laisse rien derrière elle, même dans une transaction ouverte.
func TestStatementIsAtomic(t *testing.T) {
	db := setupUsers(t)
	tx, _ := db.Begin()
	defer tx.Rollback()
	stmt, _ := Parse("INSERT INTO users VALUES (100, 'a', 1), (101, 'b', 2), (3, 'doublon', 3)")
	if _, err := Exec(tx, stmt); err == nil {
		t.Fatal("le doublon doit faire échouer l'instruction")
	}
	stmt, _ = Parse("SELECT id FROM users WHERE id >= 100")
	if res, _ := Exec(tx, stmt); len(res.Rows) != 0 {
		t.Fatalf("des lignes de l'INSERT raté sont restées : %v", rows(res))
	}
}

// La plage calculée par le planificateur ne doit jamais changer le résultat : on compare à un filtre bête.
func TestRangePlanMatchesFilter(t *testing.T) {
	db := newDB(t)
	mustRun(t, db, "CREATE TABLE n (id INT, v INT)")
	r := rand.New(rand.NewPCG(13, 14))
	ids := map[int]bool{}
	var values []string
	for len(ids) < 300 {
		id := r.IntN(2000) - 1000
		if !ids[id] {
			ids[id] = true
			values = append(values, fmt.Sprintf("(%d, %d)", id, id*2))
		}
	}
	mustRun(t, db, "INSERT INTO n VALUES "+strings.Join(values, ", "))
	var sorted []int
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Ints(sorted)

	ops := []string{"=", "<", "<=", ">", ">="}
	for i := 0; i < 200; i++ {
		op1, op2 := ops[r.IntN(5)], ops[r.IntN(5)]
		a, b := r.IntN(2200)-1100, r.IntN(2200)-1100
		q := fmt.Sprintf("SELECT id FROM n WHERE id %s %d AND %d %s id", op1, a, b, op2)
		var want []string
		for _, id := range sorted {
			if cmpOp(id, op1, a) && cmpOp(b, op2, id) {
				want = append(want, fmt.Sprint(id))
			}
		}
		if got := rows(mustRun(t, db, q)); got != strings.Join(want, ";") {
			t.Fatalf("%s\n  = %q\n  attendu %q", q, got, strings.Join(want, ";"))
		}
	}
}

func cmpOp(a int, op string, b int) bool {
	switch op {
	case "=":
		return a == b
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	}
	return a >= b
}

func TestEncodeKeyOrder(t *testing.T) {
	ns := []int64{-1 << 63, -1000, -1, 0, 1, 255, 256, 1<<63 - 1}
	for i := 1; i < len(ns); i++ {
		a, b := encodeKey(IntValue(ns[i-1])), encodeKey(IntValue(ns[i]))
		if string(a) >= string(b) {
			t.Fatalf("encodeKey(%d) ≥ encodeKey(%d) : l'ordre des octets doit suivre l'ordre des nombres", ns[i-1], ns[i])
		}
	}
}
