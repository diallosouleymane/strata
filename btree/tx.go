package btree

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var ErrTxDone = errors.New("btree: transaction déjà validée ou annulée")

// Tx est une transaction d'écriture. Elle travaille sur sa propre racine et garde ses pages en mémoire :
// les autres lecteurs ne voient rien avant Commit, et Rollback n'a qu'à tout oublier.
// Une seule transaction d'écriture existe à la fois. Un Tx ne doit pas être partagé entre goroutines.
type Tx struct {
	db       *DB
	t        tree
	dirty    map[uint64]node
	freed    []uint64 // pages de la version validée que cette transaction n'utilise plus
	reusable []uint64 // pages réutilisables par cette transaction
	npages   uint64
	done     bool
}

func (tx *Tx) get(id uint64) node {
	if n, ok := tx.dirty[id]; ok {
		return n
	}
	return tx.db.p.readPage(id)
}

func (tx *Tx) alloc(n node) uint64 {
	if n.nbytes() > PageSize {
		panic(fmt.Sprintf("btree: nœud de %d octets alloué, plus qu'une page", n.nbytes()))
	}
	id := tx.takePage()
	tx.dirty[id] = n
	return id
}

// takePage réutilise une page libre si possible, sinon agrandit le fichier.
func (tx *Tx) takePage() uint64 {
	if k := len(tx.reusable); k > 0 {
		id := tx.reusable[k-1]
		tx.reusable = tx.reusable[:k-1]
		return id
	}
	id := tx.npages
	tx.npages++
	return id
}

func (tx *Tx) free(id uint64) {
	if _, ok := tx.dirty[id]; ok {
		// Allouée et libérée dans la même transaction : jamais validée, donc réutilisable tout de suite.
		delete(tx.dirty, id)
		tx.reusable = append(tx.reusable, id)
		return
	}
	tx.freed = append(tx.freed, id)
}

func (tx *Tx) Get(key []byte) (val []byte, ok bool, err error) {
	if tx.done {
		return nil, false, ErrTxDone
	}
	defer recoverPageError(&err)
	val, ok = tx.t.get(key)
	return val, ok, nil
}

func (tx *Tx) Scan(start, end []byte, fn func(key, val []byte) bool) (err error) {
	if tx.done {
		return ErrTxDone
	}
	defer recoverPageError(&err)
	tx.t.scan(start, end, fn)
	return nil
}

func (tx *Tx) Set(key, val []byte) error {
	return tx.modify(func() error { return tx.t.set(key, val) })
}

func (tx *Tx) Delete(key []byte) (bool, error) {
	var deleted bool
	err := tx.modify(func() (err error) {
		deleted, err = tx.t.del(key)
		return err
	})
	return deleted && err == nil, err
}

// modify annule la transaction si une lecture de page échoue en pleine opération : l'arbre de travail
// serait à moitié construit. Une erreur de validation (clé vide…) arrive avant toute modification
// et laisse la transaction utilisable.
func (tx *Tx) modify(fn func() error) (err error) {
	if tx.done {
		return ErrTxDone
	}
	defer func() {
		if r := recover(); r != nil {
			pe, ok := r.(pageError)
			if !ok {
				panic(r)
			}
			tx.finish()
			err = pe.err
		}
	}()
	return fn()
}

func (tx *Tx) Rollback() error {
	if tx.done {
		return ErrTxDone
	}
	tx.finish()
	return nil
}

// Commit rend toutes les écritures de la transaction durables d'un coup. L'ordre des fsync est ce qui
// garantit la reprise après crash : les nouvelles pages d'abord, la méta ensuite. Dans l'autre ordre,
// la méta pourrait pointer vers des pages pas encore écrites.
func (tx *Tx) Commit() error {
	if tx.done {
		return ErrTxDone
	}
	defer tx.finish()
	if len(tx.dirty) == 0 && len(tx.freed) == 0 {
		return nil
	}
	p := tx.db.p
	ids, pages := tx.buildFreelist()
	if err := tx.writePages(); err != nil {
		return err
	}
	var head uint64
	if len(pages) > 0 {
		head = pages[0]
	}
	m := meta{version: p.committed.version + 1, root: tx.t.root, npages: tx.npages, freelist: head}
	if err := p.writeMeta(m); err != nil {
		return err
	}

	// Les lecteurs n'attendent que cette bascule en mémoire, jamais les fsync : les pages écrites plus haut
	// ne font pas partie de la version qu'ils lisent.
	tx.db.mu.Lock()
	p.committed, p.reusable, p.freelistPages = m, ids, pages
	tx.db.mu.Unlock()
	return nil
}

func (tx *Tx) finish() {
	tx.done = true
	tx.dirty = nil
	<-tx.db.writer
}

// buildFreelist prépare les pages de la prochaine liste libre et les ajoute aux pages à écrire.
//
// Les pages libérées par cette transaction (et les anciennes pages de la liste) servent encore à la version
// validée, sur laquelle on retomberait après un crash : on les note, mais on ne les réutilise qu'au commit
// suivant. Les pages de la nouvelle liste sont prises parmi celles réutilisables tout de suite, sinon le
// fichier grossirait à chaque commit.
func (tx *Tx) buildFreelist() (ids, pages []uint64) {
	later := append(cloneIDs(tx.freed), tx.db.p.freelistPages...)
	for len(pages)*freelistPerPage < len(tx.reusable)+len(later) {
		pages = append(pages, tx.takePage())
	}
	ids = append(cloneIDs(tx.reusable), later...)

	for j, id := range pages {
		chunk := ids[j*freelistPerPage : min((j+1)*freelistPerPage, len(ids))]
		b := make(node, PageSize)
		binary.LittleEndian.PutUint16(b, nodeFreelist)
		binary.LittleEndian.PutUint16(b[2:], uint16(len(chunk)))
		if j+1 < len(pages) {
			binary.LittleEndian.PutUint64(b[4:], pages[j+1])
		}
		for i, free := range chunk {
			binary.LittleEndian.PutUint64(b[freelistHeader+8*i:], free)
		}
		tx.dirty[id] = b
	}
	return ids, pages
}

func (tx *Tx) writePages() error {
	buf := make([]byte, PageSize)
	for id, n := range tx.dirty {
		clear(buf)
		copy(buf, n)
		if _, err := tx.db.p.f.WriteAt(buf, int64(id)*PageSize); err != nil {
			return err
		}
	}
	return tx.db.p.f.Sync()
}
