package btree

// DB est un B+tree stocké dans un fichier. Chaque Set ou Delete est validé sur disque
// avant de rendre la main : après un crash, on retrouve l'état du dernier appel réussi.
type DB struct {
	t tree
	p *pager
}

func Open(path string) (*DB, error) {
	p, err := openPager(path)
	if err != nil {
		return nil, err
	}
	return &DB{t: tree{root: p.committed.root, pages: p}, p: p}, nil
}

func (db *DB) Get(key []byte) (val []byte, ok bool, err error) {
	defer recoverPageError(&err)
	val, ok = db.t.get(key)
	return val, ok, nil
}

func (db *DB) Set(key, val []byte) error {
	return db.update(func() error { return db.t.set(key, val) })
}

func (db *DB) Delete(key []byte) (bool, error) {
	var deleted bool
	err := db.update(func() (err error) {
		deleted, err = db.t.del(key)
		return err
	})
	return deleted && err == nil, err
}

func (db *DB) Close() error { return db.p.f.Close() }

// update applique une modification puis la valide. En cas d'échec, l'arbre revient au dernier commit,
// qui est aussi ce qu'on relirait au prochain Open : la mémoire reste fidèle au disque.
func (db *DB) update(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			pe, ok := r.(pageError)
			if !ok {
				panic(r)
			}
			err = pe.err
		}
		if err != nil {
			db.t.root = db.p.committed.root
			db.p.rollback()
		}
	}()
	if err := fn(); err != nil {
		return err
	}
	if !db.p.hasChanges() {
		return nil
	}
	return db.p.commit(db.t.root)
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
