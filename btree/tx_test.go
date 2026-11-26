package btree

import (
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func newDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	t.Cleanup(func() { db.Close() })
	return db, path
}

func mustBegin(t *testing.T, db *DB) *Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin : %v", err)
	}
	return tx
}

func TestTxSeesItsOwnWritesOthersDoNot(t *testing.T) {
	db, _ := newDB(t)
	mustSetDB(t, db, "a", "avant")

	tx := mustBegin(t, db)
	tx.Set([]byte("a"), []byte("pendant"))
	tx.Set([]byte("b"), []byte("nouvelle"))
	tx.Delete([]byte("a"))
	tx.Set([]byte("a"), []byte("après"))

	if v, _, _ := tx.Get([]byte("a")); string(v) != "après" {
		t.Fatalf("dans la transaction, a = %q ; attendu \"après\"", v)
	}
	if v, _ := mustGet(t, db, "a"); v != "avant" {
		t.Fatalf("hors de la transaction, a = %q ; attendu \"avant\" tant que rien n'est validé", v)
	}
	if _, ok := mustGet(t, db, "b"); ok {
		t.Fatal("b ne doit pas être visible avant le commit")
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if v, _ := mustGet(t, db, "a"); v != "après" {
		t.Fatalf("après commit, a = %q", v)
	}
	if v, _ := mustGet(t, db, "b"); v != "nouvelle" {
		t.Fatalf("après commit, b = %q", v)
	}
}

func TestTxRollbackForgetsEverything(t *testing.T) {
	db, path := newDB(t)
	mustSetDB(t, db, "a", "1")
	version := db.p.committed.version

	tx := mustBegin(t, db)
	for i := 0; i < 500; i++ {
		tx.Set([]byte(fmt.Sprintf("k%03d", i)), []byte("x"))
	}
	tx.Delete([]byte("a"))
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	if db.p.committed.version != version {
		t.Fatal("un rollback ne doit créer aucune version")
	}
	if v, _ := mustGet(t, db, "a"); v != "1" {
		t.Fatalf("a = %q après rollback", v)
	}
	checkNoLeak(t, db)
	db.Close()
	db = openDB(t, path)
	defer db.Close()
	if _, ok := mustGet(t, db, "k000"); ok {
		t.Fatal("une écriture annulée ne doit pas réapparaître à la réouverture")
	}
}

func TestTxDoneAfterCommitOrRollback(t *testing.T) {
	db, _ := newDB(t)
	tx := mustBegin(t, db)
	tx.Commit()
	if err := tx.Set([]byte("a"), []byte("1")); err != ErrTxDone {
		t.Fatalf("Set après Commit : %v ; attendu ErrTxDone", err)
	}
	if err := tx.Commit(); err != ErrTxDone {
		t.Fatalf("second Commit : %v ; attendu ErrTxDone", err)
	}
	tx = mustBegin(t, db)
	tx.Rollback()
	if _, _, err := tx.Get([]byte("a")); err != ErrTxDone {
		t.Fatalf("Get après Rollback : %v ; attendu ErrTxDone", err)
	}
}

// Une transaction de plusieurs écritures coûte deux fsync, comme une seule écriture.
func TestTxManyWritesOneCommit(t *testing.T) {
	db, _ := newDB(t)
	before := db.p.committed.version
	tx := mustBegin(t, db)
	for i := 0; i < 1000; i++ {
		tx.Set([]byte(fmt.Sprintf("k%04d", i)), []byte("v"))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := db.p.committed.version - before; got != 1 {
		t.Fatalf("%d versions pour une transaction ; attendu 1", got)
	}
	checkNoLeak(t, db)
}

// Un crash au milieu du commit d'une grosse transaction ne laisse aucune de ses écritures.
func TestTxCrashIsAllOrNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strata.db")
	db := openDB(t, path)
	mustSetDB(t, db, "solde:alice", "100")
	mustSetDB(t, db, "solde:bob", "0")

	tx := mustBegin(t, db)
	tx.Set([]byte("solde:alice"), []byte("40"))
	tx.Set([]byte("solde:bob"), []byte("60"))
	tx.buildFreelist()
	tx.writePages()
	db.Close() // crash avant la méta

	db = openDB(t, path)
	defer db.Close()
	a, _ := mustGet(t, db, "solde:alice")
	b, _ := mustGet(t, db, "solde:bob")
	if a != "100" || b != "0" {
		t.Fatalf("alice = %s, bob = %s ; attendu 100 et 0 : un virement est tout ou rien", a, b)
	}
}

func TestTxSecondWriterIsBusy(t *testing.T) {
	db, _ := newDB(t)
	old := busyTimeout
	busyTimeout = 50 * time.Millisecond
	defer func() { busyTimeout = old }()

	tx := mustBegin(t, db)
	if _, err := db.Begin(); err != ErrBusy {
		t.Fatalf("second Begin : %v ; attendu ErrBusy", err)
	}
	if err := db.Set([]byte("a"), []byte("1")); err != ErrBusy {
		t.Fatalf("Set pendant une transaction : %v ; attendu ErrBusy", err)
	}
	// Les lectures, elles, ne sont jamais bloquées par l'écrivain.
	if _, _, err := db.Get([]byte("a")); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if _, err := db.Begin(); err != nil {
		t.Fatalf("Begin après la fin de la première : %v", err)
	}
}

// Des lecteurs tournent pendant que l'écrivain fait des virements. Chaque virement garde la somme
// à 1000 : un lecteur qui verrait une somme différente aurait lu une version à moitié validée,
// ou une page recyclée sous ses pieds. À lancer avec -race.
func TestTxReadersSeeConsistentSnapshots(t *testing.T) {
	db, _ := newDB(t)
	tx := mustBegin(t, db)
	for i := 0; i < 10; i++ {
		tx.Set([]byte(fmt.Sprintf("compte:%d", i)), []byte("100"))
	}
	tx.Commit()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sum := 0
				err := db.Scan([]byte("compte:"), []byte("compte;"), func(k, v []byte) bool {
					n, _ := strconv.Atoi(string(v))
					sum += n
					return true
				})
				if err != nil || sum != 1000 {
					errs <- fmt.Errorf("somme lue %d, erreur %v", sum, err)
					return
				}
			}
		}()
	}

	for i := 0; i < 300; i++ {
		from, to := fmt.Sprintf("compte:%d", i%10), fmt.Sprintf("compte:%d", (i*7+3)%10)
		if from == to {
			continue
		}
		tx := mustBegin(t, db)
		a, _, _ := tx.Get([]byte(from))
		b, _, _ := tx.Get([]byte(to))
		x, _ := strconv.Atoi(string(a))
		y, _ := strconv.Atoi(string(b))
		tx.Set([]byte(from), []byte(strconv.Itoa(x-1)))
		tx.Set([]byte(to), []byte(strconv.Itoa(y+1)))
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	checkNoLeak(t, db)
}
