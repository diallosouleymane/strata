package btree

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
)

// Les pages 0 et 1 sont deux pages méta qui alternent ; les autres pages sont des nœuds
// ou des pages de la liste libre.
//
// Format d'une page méta (little-endian) :
//
//	| magic "STRATA02" (8) | version (8) | racine (8) | nombre de pages (8) | liste libre (8) | crc32 des 40 premiers octets (4) |
//
// Format d'une page de la liste libre :
//
//	| type = 3 (2) | nombre d'ids (2) | page suivante (8) | ids (nombre × 8) |
const (
	metaMagic = "STRATA02"
	metaPages = 2
	metaSize  = 44

	nodeFreelist    uint16 = 3
	freelistHeader         = 12
	freelistPerPage        = (PageSize - freelistHeader) / 8
)

type meta struct {
	version  uint64
	root     uint64
	npages   uint64
	freelist uint64
}

func encodeMeta(m meta) []byte {
	b := make([]byte, PageSize)
	copy(b, metaMagic)
	binary.LittleEndian.PutUint64(b[8:], m.version)
	binary.LittleEndian.PutUint64(b[16:], m.root)
	binary.LittleEndian.PutUint64(b[24:], m.npages)
	binary.LittleEndian.PutUint64(b[32:], m.freelist)
	binary.LittleEndian.PutUint32(b[40:], crc32.ChecksumIEEE(b[:40]))
	return b
}

func decodeMeta(b []byte) (meta, bool) {
	if len(b) < metaSize || string(b[:8]) != metaMagic {
		return meta{}, false
	}
	if crc32.ChecksumIEEE(b[:40]) != binary.LittleEndian.Uint32(b[40:]) {
		return meta{}, false
	}
	return meta{
		version:  binary.LittleEndian.Uint64(b[8:]),
		root:     binary.LittleEndian.Uint64(b[16:]),
		npages:   binary.LittleEndian.Uint64(b[24:]),
		freelist: binary.LittleEndian.Uint64(b[32:]),
	}, true
}

// pageError transporte une erreur d'entrée-sortie à travers l'arbre, qui ne renvoie pas d'erreurs ;
// DB et Tx la récupèrent avec recover et la renvoient comme une erreur normale.
type pageError struct{ err error }

// pager détient le fichier et la dernière version validée. Une transaction en cours garde ses
// modifications de son côté (voir Tx) : ici, rien ne change avant la bascule d'un commit.
type pager struct {
	f         *os.File
	committed meta
	// reusable : les pages hors de l'arbre validé et hors de sa liste libre.
	// freelistPages : les pages qui stockent la liste libre validée.
	reusable      []uint64
	freelistPages []uint64
}

func openPager(path string) (*pager, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	p := &pager{f: f}
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
		p.committed = m
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
		return errors.New("btree: no valid meta page, not a strata file or corrupted")
	}
	if int64(best.npages)*PageSize > info.Size() {
		return fmt.Errorf("btree: meta claims %d pages but the file holds only %d", best.npages, info.Size()/PageSize)
	}
	p.committed = best

	ids, pages, err := p.readFreelist(best.freelist)
	if err != nil {
		return err
	}
	p.reusable, p.freelistPages = ids, pages
	return nil
}

func (p *pager) validPage(id uint64) bool { return id >= metaPages && id < p.committed.npages }

func (p *pager) readFreelist(head uint64) (ids, pages []uint64, err error) {
	for id := head; id != 0; {
		if !p.validPage(id) || uint64(len(pages)) >= p.committed.npages {
			return nil, nil, fmt.Errorf("btree: corrupt free list at page %d", id)
		}
		b := make([]byte, PageSize)
		if _, err := p.f.ReadAt(b, int64(id)*PageSize); err != nil {
			return nil, nil, fmt.Errorf("btree: reading free list page %d: %w", id, err)
		}
		count := int(binary.LittleEndian.Uint16(b[2:]))
		if binary.LittleEndian.Uint16(b) != nodeFreelist || count > freelistPerPage {
			return nil, nil, fmt.Errorf("btree: page %d is not a free list page", id)
		}
		for i := 0; i < count; i++ {
			free := binary.LittleEndian.Uint64(b[freelistHeader+8*i:])
			if !p.validPage(free) {
				return nil, nil, fmt.Errorf("btree: free list references page %d, past the end of the file", free)
			}
			ids = append(ids, free)
		}
		pages = append(pages, id)
		id = binary.LittleEndian.Uint64(b[4:])
	}
	return ids, pages, nil
}

// readPage lit un nœud de la version validée.
func (p *pager) readPage(id uint64) node {
	if !p.validPage(id) {
		panic(pageError{fmt.Errorf("btree: page %d past the end of the file (%d pages)", id, p.committed.npages)})
	}
	n := make(node, PageSize)
	if _, err := p.f.ReadAt(n, int64(id)*PageSize); err != nil {
		panic(pageError{fmt.Errorf("btree: reading page %d: %w", id, err)})
	}
	if k := n.kind(); k != nodeLeaf && k != nodeInternal {
		panic(pageError{fmt.Errorf("btree: corrupt page %d, node type %d", id, k)})
	}
	return n
}

// writeMeta écrit dans l'emplacement qui n'est pas en service : si cette écriture est interrompue,
// l'autre emplacement contient toujours la version précédente, intacte.
func (p *pager) writeMeta(m meta) error {
	if _, err := p.f.WriteAt(encodeMeta(m), int64(m.version%metaPages)*PageSize); err != nil {
		return err
	}
	return p.f.Sync()
}

// snapshot est un pageStore en lecture seule sur la version validée.
type snapshot struct{ p *pager }

func (s snapshot) get(id uint64) node { return s.p.readPage(id) }
func (s snapshot) alloc(node) uint64  { panic("btree: write through a read-only snapshot") }
func (s snapshot) free(uint64)        { panic("btree: write through a read-only snapshot") }

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func cloneIDs(ids []uint64) []uint64 { return slices.Clone(ids) }
