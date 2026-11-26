package btree

import (
	"errors"
	"sync"
	"time"
)

var ErrBusy = errors.New("btree: another write transaction is in progress")

// busyTimeout borne l'attente d'une transaction d'écriture : un client qui laisse un BEGIN ouvert
// ne doit pas bloquer les autres indéfiniment.
var busyTimeout = 5 * time.Second

// DB est un B+tree stocké dans un fichier, utilisable depuis plusieurs goroutines.
//
// Un seul écrivain à la fois (writer), mais autant de lecteurs qu'on veut, même pendant une écriture :
// chacun lit la dernière version validée, que la copie sur écriture laisse intacte.
type DB struct {
	p      *pager
	mu     sync.RWMutex  // protège la version validée : lecteurs en RLock, bascule d'un commit en Lock
	writer chan struct{} // jeton de l'unique transaction d'écriture
}

func Open(path string) (*DB, error) {
	p, err := openPager(path)
	if err != nil {
		return nil, err
	}
	return &DB{p: p, writer: make(chan struct{}, 1)}, nil
}

// Close attend la fin des lectures en cours. Une transaction encore ouverte échouera à son Commit.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.p.f.Close()
}

// Begin démarre une transaction d'écriture, en attendant au plus busyTimeout que la précédente se termine.
func (db *DB) Begin() (*Tx, error) {
	select {
	case db.writer <- struct{}{}:
	case <-time.After(busyTimeout):
		return nil, ErrBusy
	}
	// Tant qu'on détient le jeton, personne d'autre ne peut valider : la version validée ne bougera pas.
	c := db.p.committed
	tx := &Tx{db: db, dirty: map[uint64]node{}, reusable: cloneIDs(db.p.reusable), npages: c.npages}
	tx.t = tree{root: c.root, pages: tx}
	return tx, nil
}

// view exécute fn sur la dernière version validée. Le RLock est tenu pendant toute la lecture : un commit
// ne peut pas basculer, et donc aucune page de cette version ne peut être recyclée, tant qu'elle dure.
func (db *DB) view(fn func(t *tree)) (err error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	defer recoverPageError(&err)
	fn(&tree{root: db.p.committed.root, pages: snapshot{db.p}})
	return nil
}

func (db *DB) Get(key []byte) (val []byte, ok bool, err error) {
	err = db.view(func(t *tree) { val, ok = t.get(key) })
	return val, ok, err
}

// Scan parcourt les clés de [start, end) dans l'ordre ; start == nil part du début, end == nil va jusqu'à la fin.
// fn renvoie false pour arrêter. key et val ne sont valables que pendant l'appel : les copier pour les garder.
func (db *DB) Scan(start, end []byte, fn func(key, val []byte) bool) error {
	return db.view(func(t *tree) { t.scan(start, end, fn) })
}

// Set et Delete sont des transactions d'une seule écriture.
func (db *DB) Set(key, val []byte) error {
	return db.update(func(tx *Tx) error { return tx.Set(key, val) })
}

func (db *DB) Delete(key []byte) (deleted bool, err error) {
	err = db.update(func(tx *Tx) (err error) {
		deleted, err = tx.Delete(key)
		return err
	})
	return deleted && err == nil, err
}

func (db *DB) update(fn func(tx *Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if !tx.done {
			tx.Rollback()
		}
		return err
	}
	return tx.Commit()
}

func recoverPageError(err *error) {
	if r := recover(); r != nil {
		pe, ok := r.(pageError)
		if !ok {
			panic(r)
		}
		*err = pe.err
	}
}
