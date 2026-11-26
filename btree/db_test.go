package btree

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openDB(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	return db
}

func mustGet(t *testing.T, db *DB, key string) (string, bool) {
	t.Helper()
	v, ok, err := db.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get(%q) : %v", key, err)
	}
	return string(v), ok
}

func mustSetDB(t *testing.T, db *DB, key, val string) {
	t.Helper()
	if err := db.Set([]byte(key), []byte(val)); err != nil {
		t.Fatalf("Set(%q) : %v", key, err)
	}
}

func TestDBNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	defer db.Close()

	if info, _ := os.Stat(path); info.Size() != metaPages*PageSize {
		t.Fatalf("un fichier neuf doit contenir les deux emplacements méta, il fait %d octets", info.Size())
	}
	if _, ok := mustGet(t, db, "a"); ok {
		t.Fatal("une base neuve doit être vide")
	}
}

func TestDBPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	for i := 0; i < 2000; i++ {
		mustSetDB(t, db, fmt.Sprintf("k%04d", i), fmt.Sprint(i))
	}
	for i := 0; i < 2000; i += 3 {
		if ok, err := db.Delete([]byte(fmt.Sprintf("k%04d", i))); !ok || err != nil {
			t.Fatalf("Delete : %v, %v", ok, err)
		}
	}
	db.Close()

	db = openDB(t, path)
	defer db.Close()
	for i := 0; i < 2000; i++ {
		v, ok := mustGet(t, db, fmt.Sprintf("k%04d", i))
		if i%3 == 0 && ok {
			t.Fatalf("k%04d devait être supprimée", i)
		}
		if i%3 != 0 && v != fmt.Sprint(i) {
			t.Fatalf("Get(k%04d) = %q, %v", i, v, ok)
		}
	}
}

// Simule un crash entre les deux fsync : les nouvelles pages sont sur disque, mais pas la méta.
func TestDBCrashBeforeMetaKeepsLastCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	mustSetDB(t, db, "a", "1")

	if err := db.t.set([]byte("b"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	if err := db.p.writePages(); err != nil {
		t.Fatal(err)
	}
	db.Close() // la méta n'a jamais été écrite

	db = openDB(t, path)
	if v, _ := mustGet(t, db, "a"); v != "1" {
		t.Fatalf("Get(a) = %q ; attendu \"1\"", v)
	}
	if _, ok := mustGet(t, db, "b"); ok {
		t.Fatal("b n'a jamais été validée, elle ne doit pas exister")
	}
	// Les pages orphelines en fin de fichier sont simplement réécrites.
	mustSetDB(t, db, "c", "3")
	db.Close()

	db = openDB(t, path)
	defer db.Close()
	if v, _ := mustGet(t, db, "c"); v != "3" {
		t.Fatalf("Get(c) = %q ; attendu \"3\"", v)
	}
}

// Simule une page méta à moitié écrite : la base revient à la version précédente.
func TestDBCorruptLatestMetaFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	mustSetDB(t, db, "a", "1")
	mustSetDB(t, db, "a", "2")
	latest := db.p.committed.version
	db.Close()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("XX"), int64(latest%metaPages)*PageSize+20); err != nil {
		t.Fatal(err)
	}
	f.Close()

	db = openDB(t, path)
	defer db.Close()
	if db.p.committed.version != latest-1 {
		t.Fatalf("version %d ; attendu %d", db.p.committed.version, latest-1)
	}
	if v, _ := mustGet(t, db, "a"); v != "1" {
		t.Fatalf("Get(a) = %q ; attendu \"1\", la valeur de la version précédente", v)
	}
}

func TestDBRejectsForeignFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pas-une-base")
	if err := os.WriteFile(path, []byte(strings.Repeat("n'importe quoi ", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open doit refuser un fichier sans page méta valide")
	}
}

func TestDBRejectedWriteCommitsNothing(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "strata.db"))
	defer db.Close()
	mustSetDB(t, db, "a", "1")
	before := db.p.committed.version

	if err := db.Set(nil, []byte("v")); err != ErrEmptyKey {
		t.Fatalf("Set(clé vide) : %v ; attendu ErrEmptyKey", err)
	}
	if ok, err := db.Delete([]byte("absente")); ok || err != nil {
		t.Fatalf("Delete(absente) = %v, %v", ok, err)
	}
	if db.p.committed.version != before {
		t.Fatal("une écriture refusée ou sans effet ne doit pas créer de version")
	}
}

func TestDBCorruptPageReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	mustSetDB(t, db, "a", "1")
	root := db.p.committed.root
	db.Close()

	f, _ := os.OpenFile(path, os.O_RDWR, 0)
	f.WriteAt([]byte{0xFF, 0xFF}, int64(root)*PageSize) // type de nœud invalide
	f.Close()

	db = openDB(t, path)
	defer db.Close()
	if _, _, err := db.Get([]byte("a")); err == nil {
		t.Fatal("Get sur une page corrompue doit renvoyer une erreur, pas paniquer")
	}
	if err := db.Set([]byte("b"), []byte("2")); err == nil {
		t.Fatal("Set à travers une page corrompue doit renvoyer une erreur")
	}
}

func TestDBRandomOpsWithReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	want := map[string]string{}
	r := rand.New(rand.NewPCG(7, 8))
	for op := 0; op < 3000; op++ {
		k := fmt.Sprintf("k%03d", r.IntN(500))
		if r.IntN(3) == 0 {
			db.Delete([]byte(k))
			delete(want, k)
		} else {
			v := strings.Repeat("v", r.IntN(300))
			mustSetDB(t, db, k, v)
			want[k] = v
		}
		if op%500 == 499 {
			db.Close()
			db = openDB(t, path)
		}
	}
	defer db.Close()
	for i := 0; i < 500; i++ {
		k := fmt.Sprintf("k%03d", i)
		v, ok := mustGet(t, db, k)
		if wv, wok := want[k]; ok != wok || v != wv {
			t.Fatalf("Get(%q) = %d octets, %v ; attendu %d octets, %v", k, len(v), ok, len(wv), wok)
		}
	}
}

// checkNoLeak vérifie que chaque page du fichier a exactement un rôle : méta, nœud de l'arbre,
// page libre ou page de la liste libre.
func checkNoLeak(t *testing.T, db *DB) {
	t.Helper()
	owner := map[uint64]string{}
	claim := func(id uint64, role string) {
		if prev, ok := owner[id]; ok {
			t.Fatalf("page %d est à la fois %s et %s", id, prev, role)
		}
		owner[id] = role
	}
	var walk func(id uint64)
	walk = func(id uint64) {
		claim(id, "nœud")
		n := db.p.get(id)
		if n.kind() == nodeInternal {
			for i := uint16(0); i < n.nkeys(); i++ {
				walk(n.ptr(i))
			}
		}
	}
	if db.t.root != 0 {
		walk(db.t.root)
	}
	for _, id := range db.p.reusable {
		claim(id, "libre")
	}
	for _, id := range db.p.freelistPages {
		claim(id, "page de liste libre")
	}
	if got := uint64(len(owner)) + metaPages; got != db.p.npages {
		t.Fatalf("%d pages ont un rôle sur %d : %d pages perdues", got, db.p.npages, db.p.npages-got)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// Sans liste libre, 1000 SET sur la même clé faisaient un fichier de 1002 pages.
func TestFreelistStopsGrowth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	defer db.Close()
	for i := 0; i < 1000; i++ {
		mustSetDB(t, db, "compteur", fmt.Sprint(i))
	}
	if pages := fileSize(t, path) / PageSize; pages > 10 {
		t.Fatalf("1000 SET sur la même clé : %d pages ; la liste libre devrait les recycler", pages)
	}
	checkNoLeak(t, db)
}

func TestFreelistSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	for i := 0; i < 3000; i++ {
		mustSetDB(t, db, fmt.Sprintf("k%04d", i), strings.Repeat("v", 100))
	}
	for i := 0; i < 3000; i++ {
		db.Delete([]byte(fmt.Sprintf("k%04d", i)))
	}
	free := len(db.p.reusable)
	size := fileSize(t, path)
	db.Close()

	db = openDB(t, path)
	defer db.Close()
	if len(db.p.reusable) != free {
		t.Fatalf("%d pages libres après réouverture ; attendu %d", len(db.p.reusable), free)
	}
	checkNoLeak(t, db)
	// Remplir à nouveau doit réutiliser l'espace libéré au lieu d'agrandir le fichier.
	for i := 0; i < 3000; i++ {
		mustSetDB(t, db, fmt.Sprintf("k%04d", i), strings.Repeat("v", 100))
	}
	if after := fileSize(t, path); after > size {
		t.Fatalf("le fichier est passé de %d à %d octets malgré %d pages libres", size, after, free)
	}
	checkNoLeak(t, db)
}

// Reprend le crash entre les deux fsync, mais avec des pages recyclées : la transaction interrompue
// a écrit dans des pages libres, jamais dans celles de l'arbre validé.
func TestFreelistCrashKeepsCommittedTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	want := map[string]string{}
	for i := 0; i < 500; i++ {
		k := fmt.Sprintf("k%03d", i%50)
		mustSetDB(t, db, k, fmt.Sprint(i))
		want[k] = fmt.Sprint(i)
	}
	if len(db.p.reusable) == 0 {
		t.Fatal("le scénario suppose des pages libres à recycler")
	}
	for i := 0; i < 50; i++ {
		db.t.set([]byte(fmt.Sprintf("k%03d", i)), []byte("jamais validé"))
	}
	db.p.buildFreelist()
	if err := db.p.writePages(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db = openDB(t, path)
	defer db.Close()
	for k, v := range want {
		if got, _ := mustGet(t, db, k); got != v {
			t.Fatalf("Get(%q) = %q ; attendu %q", k, got, v)
		}
	}
	checkNoLeak(t, db)
}

func TestFreelistRandomOpsNoLeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	r := rand.New(rand.NewPCG(9, 10))
	for op := 0; op < 4000; op++ {
		k := fmt.Sprintf("k%03d", r.IntN(400))
		if r.IntN(3) == 0 {
			db.Delete([]byte(k))
		} else {
			mustSetDB(t, db, k, strings.Repeat("v", r.IntN(MaxValSize)))
		}
		if op%1000 == 999 {
			checkNoLeak(t, db)
			db.Close()
			db = openDB(t, path)
			checkNoLeak(t, db)
		}
	}
	db.Close()
}
