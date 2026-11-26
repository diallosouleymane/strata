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
// DB la récupère avec recover et la renvoie comme une erreur normale.
type pageError struct{ err error }

// pager implémente pageStore sur un fichier. Les pages allouées restent en mémoire jusqu'au commit :
// le fichier n'est jamais modifié là où l'arbre déjà validé pourrait le lire.
type pager struct {
	f         *os.File
	committed meta
	npages    uint64
	dirty     map[uint64]node

	// reusable : pages hors de l'arbre validé, utilisables tout de suite.
	// freed : pages libérées par la transaction en cours ; l'arbre validé les utilise encore.
	// freelistPages : les pages qui stockent la liste libre validée.
	reusable          []uint64
	committedReusable []uint64
	freed             []uint64
	freelistPages     []uint64
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

	ids, pages, err := p.readFreelist(best.freelist)
	if err != nil {
		return err
	}
	p.reusable, p.committedReusable, p.freelistPages = ids, slices.Clone(ids), pages
	return nil
}

func (p *pager) validPage(id uint64) bool { return id >= metaPages && id < p.npages }

func (p *pager) readFreelist(head uint64) (ids, pages []uint64, err error) {
	for id := head; id != 0; {
		if !p.validPage(id) || uint64(len(pages)) >= p.npages {
			return nil, nil, fmt.Errorf("btree: liste libre corrompue à la page %d", id)
		}
		b := make([]byte, PageSize)
		if _, err := p.f.ReadAt(b, int64(id)*PageSize); err != nil {
			return nil, nil, fmt.Errorf("btree: lecture de la liste libre, page %d : %w", id, err)
		}
		count := int(binary.LittleEndian.Uint16(b[2:]))
		if binary.LittleEndian.Uint16(b) != nodeFreelist || count > freelistPerPage {
			return nil, nil, fmt.Errorf("btree: page %d : ce n'est pas une page de liste libre", id)
		}
		for i := 0; i < count; i++ {
			free := binary.LittleEndian.Uint64(b[freelistHeader+8*i:])
			if !p.validPage(free) {
				return nil, nil, fmt.Errorf("btree: la liste libre cite la page %d, hors du fichier", free)
			}
			ids = append(ids, free)
		}
		pages = append(pages, id)
		id = binary.LittleEndian.Uint64(b[4:])
	}
	return ids, pages, nil
}

func (p *pager) get(id uint64) node {
	if n, ok := p.dirty[id]; ok {
		return n
	}
	if !p.validPage(id) {
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

func (p *pager) alloc(n node) uint64 {
	if n.nbytes() > PageSize {
		panic(fmt.Sprintf("btree: nœud de %d octets alloué, plus qu'une page", n.nbytes()))
	}
	id := p.takePage()
	p.dirty[id] = n
	return id
}

// takePage réutilise une page libre si possible, sinon agrandit le fichier.
func (p *pager) takePage() uint64 {
	if k := len(p.reusable); k > 0 {
		id := p.reusable[k-1]
		p.reusable = p.reusable[:k-1]
		return id
	}
	id := p.npages
	p.npages++
	return id
}

func (p *pager) free(id uint64) {
	if _, ok := p.dirty[id]; ok {
		// Allouée et libérée dans la même transaction : jamais validée, donc réutilisable tout de suite.
		delete(p.dirty, id)
		p.reusable = append(p.reusable, id)
		return
	}
	p.freed = append(p.freed, id)
}

func (p *pager) hasChanges() bool { return len(p.dirty) > 0 || len(p.freed) > 0 }

// commit rend durable un nouvel arbre. L'ordre des fsync est ce qui garantit la reprise après crash :
// les nouvelles pages d'abord, la méta ensuite. Dans l'autre ordre, la méta pourrait pointer vers des
// pages pas encore écrites.
func (p *pager) commit(root uint64) error {
	ids, pages := p.buildFreelist()
	if err := p.writePages(); err != nil {
		return err
	}
	var head uint64
	if len(pages) > 0 {
		head = pages[0]
	}
	m := meta{version: p.committed.version + 1, root: root, npages: p.npages, freelist: head}
	if err := p.writeMeta(m); err != nil {
		return err
	}
	p.committed = m
	p.dirty = map[uint64]node{}
	p.freed = nil
	p.reusable, p.committedReusable, p.freelistPages = ids, slices.Clone(ids), pages
	return nil
}

// buildFreelist prépare les pages de la prochaine liste libre et les ajoute aux pages à écrire.
//
// Les pages libérées par cette transaction (et les anciennes pages de la liste) servent encore à l'arbre
// validé, sur lequel on retomberait après un crash : on les note, mais on ne les réutilise qu'au commit suivant.
// Les pages de la nouvelle liste sont prises parmi celles réutilisables tout de suite, sinon le fichier
// grossirait à chaque commit.
func (p *pager) buildFreelist() (ids, pages []uint64) {
	later := append(slices.Clone(p.freed), p.freelistPages...)
	for len(pages)*freelistPerPage < len(p.reusable)+len(later) {
		pages = append(pages, p.takePage())
	}
	ids = append(slices.Clone(p.reusable), later...)

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
		p.dirty[id] = b
	}
	return ids, pages
}

func (p *pager) writePages() error {
	buf := make([]byte, PageSize)
	for id, n := range p.dirty {
		clear(buf)
		copy(buf, n)
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
	p.reusable = slices.Clone(p.committedReusable)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
