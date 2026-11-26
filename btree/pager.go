package btree

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

// Les pages 0 et 1 sont deux pages méta qui alternent ; les nœuds commencent à la page 2.
//
// Format d'une page méta (little-endian) :
//
//	| magic "STRATA01" (8) | version (8) | racine (8) | nombre de pages (8) | crc32 des 32 premiers octets (4) |
const (
	metaMagic = "STRATA01"
	metaPages = 2
	metaSize  = 36
)

type meta struct {
	version uint64
	root    uint64
	npages  uint64
}

func encodeMeta(m meta) []byte {
	b := make([]byte, PageSize)
	copy(b, metaMagic)
	binary.LittleEndian.PutUint64(b[8:], m.version)
	binary.LittleEndian.PutUint64(b[16:], m.root)
	binary.LittleEndian.PutUint64(b[24:], m.npages)
	binary.LittleEndian.PutUint32(b[32:], crc32.ChecksumIEEE(b[:32]))
	return b
}

func decodeMeta(b []byte) (meta, bool) {
	if len(b) < metaSize || string(b[:8]) != metaMagic {
		return meta{}, false
	}
	if crc32.ChecksumIEEE(b[:32]) != binary.LittleEndian.Uint32(b[32:]) {
		return meta{}, false
	}
	return meta{
		version: binary.LittleEndian.Uint64(b[8:]),
		root:    binary.LittleEndian.Uint64(b[16:]),
		npages:  binary.LittleEndian.Uint64(b[24:]),
	}, true
}

// pageError transporte une erreur d'entrée-sortie à travers l'arbre, qui ne renvoie pas d'erreurs ;
// DB la récupère avec recover et la renvoie comme une erreur normale.
type pageError struct{ err error }

// pager implémente pageStore sur un fichier. Les pages allouées restent en mémoire jusqu'au commit :
// le fichier n'est jamais modifié là où l'arbre déjà validé pourrait le lire.
type pager struct {
	f         *os.File
	committed meta
	npages    uint64
	dirty     map[uint64]node
	freed     []uint64
}

func openPager(path string) (*pager, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	p := &pager{f: f, dirty: map[uint64]node{}}
	if err := p.load(path); err != nil {
		f.Close()
		return nil, err
	}
	return p, nil
}

func (p *pager) load(path string) error {
	info, err := p.f.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		m := meta{version: 1, npages: metaPages}
		if err := p.writeMeta(m); err != nil {
			return err
		}
		// Le fichier vient d'être créé : son entrée dans le dossier doit aussi survivre à une coupure.
		if err := syncDir(filepath.Dir(path)); err != nil {
			return err
		}
		p.committed, p.npages = m, m.npages
		return nil
	}

	var best meta
	found := false
	for slot := int64(0); slot < metaPages; slot++ {
		b := make([]byte, metaSize)
		if _, err := p.f.ReadAt(b, slot*PageSize); err != nil {
			continue // emplacement absent ou tronqué : on se rabat sur l'autre
		}
		if m, ok := decodeMeta(b); ok && (!found || m.version > best.version) {
			best, found = m, true
		}
	}
	if !found {
		return errors.New("btree: aucune page méta valide, ce n'est pas un fichier strata ou il est corrompu")
	}
	if int64(best.npages)*PageSize > info.Size() {
		return fmt.Errorf("btree: la méta annonce %d pages mais le fichier n'en contient que %d", best.npages, info.Size()/PageSize)
	}
	p.committed, p.npages = best, best.npages
	return nil
}

func (p *pager) get(id uint64) node {
	if n, ok := p.dirty[id]; ok {
		return n
	}
	if id < metaPages || id >= p.npages {
		panic(pageError{fmt.Errorf("btree: page %d hors du fichier (%d pages)", id, p.npages)})
	}
	n := make(node, PageSize)
	if _, err := p.f.ReadAt(n, int64(id)*PageSize); err != nil {
		panic(pageError{fmt.Errorf("btree: lecture de la page %d : %w", id, err)})
	}
	if k := n.kind(); k != nodeLeaf && k != nodeInternal {
		panic(pageError{fmt.Errorf("btree: page %d corrompue, type de nœud %d", id, k)})
	}
	return n
}

// alloc ajoute toujours la page en fin de fichier : réutiliser les pages libérées viendra avec la liste libre.
func (p *pager) alloc(n node) uint64 {
	if n.nbytes() > PageSize {
		panic(fmt.Sprintf("btree: nœud de %d octets alloué, plus qu'une page", n.nbytes()))
	}
	id := p.npages
	p.npages++
	p.dirty[id] = n
	return id
}

func (p *pager) free(id uint64) {
	if _, ok := p.dirty[id]; ok {
		delete(p.dirty, id) // jamais écrite, rien à libérer sur disque
		return
	}
	p.freed = append(p.freed, id)
}

func (p *pager) hasChanges() bool { return len(p.dirty) > 0 || len(p.freed) > 0 }

// commit rend durable un nouvel arbre. L'ordre des fsync est ce qui garantit la reprise après crash :
// les nouvelles pages d'abord, la méta ensuite. Dans l'autre ordre, la méta pourrait pointer vers des
// pages pas encore écrites.
func (p *pager) commit(root uint64) error {
	if err := p.writePages(); err != nil {
		return err
	}
	m := meta{version: p.committed.version + 1, root: root, npages: p.npages}
	if err := p.writeMeta(m); err != nil {
		return err
	}
	p.committed = m
	p.dirty = map[uint64]node{}
	p.freed = nil
	return nil
}

func (p *pager) writePages() error {
	buf := make([]byte, PageSize)
	for id, n := range p.dirty {
		clear(buf)
		copy(buf, n[:n.nbytes()])
		if _, err := p.f.WriteAt(buf, int64(id)*PageSize); err != nil {
			return err
		}
	}
	return p.f.Sync()
}

// writeMeta écrit dans l'emplacement qui n'est pas en service : si cette écriture est interrompue,
// l'autre emplacement contient toujours la version précédente, intacte.
func (p *pager) writeMeta(m meta) error {
	if _, err := p.f.WriteAt(encodeMeta(m), int64(m.version%metaPages)*PageSize); err != nil {
		return err
	}
	return p.f.Sync()
}

func (p *pager) rollback() {
	p.dirty = map[uint64]node{}
	p.freed = nil
	p.npages = p.committed.npages
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
