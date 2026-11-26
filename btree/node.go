package btree

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// Format d'un nœud, entiers en little-endian :
//
//	| type (2) | nkeys (2) | pointeurs (nkeys × 8) | offsets (nkeys × 2) | paires |
//	paire : | klen (2) | vlen (2) | clé | valeur |
//
// offsets[i] est la fin de la paire i dans la zone des paires. Le début de la paire 0
// vaut toujours 0 et n'est donc pas stocké. Cette table donne l'accès direct à la i-ème
// clé malgré des tailles variables, ce qui rend la recherche dichotomique possible.
const (
	PageSize   = 4096
	MaxKeySize = 1000
	MaxValSize = 3000

	headerSize = 4
	ptrSize    = 8
	offsetSize = 2
	pairHeader = 4
)

const (
	nodeInternal uint16 = 1
	nodeLeaf     uint16 = 2
)

// Un node n'est qu'une vue sur des octets : le même []byte est écrit tel quel sur disque.
// Il peut dépasser PageSize juste après une insertion, avant d'être coupé en deux.
type node []byte

func (n node) kind() uint16  { return binary.LittleEndian.Uint16(n[0:2]) }
func (n node) nkeys() uint16 { return binary.LittleEndian.Uint16(n[2:4]) }

func (n node) setHeader(kind, nkeys uint16) {
	binary.LittleEndian.PutUint16(n[0:2], kind)
	binary.LittleEndian.PutUint16(n[2:4], nkeys)
}

func (n node) checkIndex(i uint16) {
	if i >= n.nkeys() {
		panic(fmt.Sprintf("btree: indice %d hors du nœud (%d clés)", i, n.nkeys()))
	}
}

func (n node) ptrPos(i uint16) int { return headerSize + ptrSize*int(i) }

func (n node) ptr(i uint16) uint64 {
	n.checkIndex(i)
	return binary.LittleEndian.Uint64(n[n.ptrPos(i):])
}

func (n node) setPtr(i uint16, v uint64) {
	n.checkIndex(i)
	binary.LittleEndian.PutUint64(n[n.ptrPos(i):], v)
}

func (n node) offsetPos(i uint16) int {
	return headerSize + ptrSize*int(n.nkeys()) + offsetSize*int(i-1)
}

// offset(i) est le début de la paire i dans la zone des paires, pour 0 ≤ i ≤ nkeys.
func (n node) offset(i uint16) uint16 {
	if i == 0 {
		return 0
	}
	return binary.LittleEndian.Uint16(n[n.offsetPos(i):])
}

func (n node) setOffset(i uint16, v uint16) {
	binary.LittleEndian.PutUint16(n[n.offsetPos(i):], v)
}

func (n node) kvPos(i uint16) int {
	return headerSize + (ptrSize+offsetSize)*int(n.nkeys()) + int(n.offset(i))
}

func (n node) key(i uint16) []byte {
	n.checkIndex(i)
	pos := n.kvPos(i)
	klen := binary.LittleEndian.Uint16(n[pos:])
	return n[pos+pairHeader:][:klen]
}

func (n node) val(i uint16) []byte {
	n.checkIndex(i)
	pos := n.kvPos(i)
	klen := binary.LittleEndian.Uint16(n[pos:])
	vlen := binary.LittleEndian.Uint16(n[pos+2:])
	return n[pos+pairHeader+int(klen):][:vlen]
}

// nbytes est la taille utile du nœud : la fin de la dernière paire.
func (n node) nbytes() int { return n.kvPos(n.nkeys()) }

// lookupLE renvoie l'indice de la dernière clé ≤ key, ou -1 si key est plus petite que toutes.
func (n node) lookupLE(key []byte) int {
	i := sort.Search(int(n.nkeys()), func(i int) bool {
		return bytes.Compare(n.key(uint16(i)), key) > 0
	})
	return i - 1
}

// appendKV écrit la paire i. Les paires doivent être écrites dans l'ordre, car la position
// de la paire i se déduit de la fin de la paire i-1, et le header doit déjà porter le nkeys final.
func appendKV(n node, i uint16, ptr uint64, key, val []byte) {
	n.setPtr(i, ptr)
	pos := n.kvPos(i)
	binary.LittleEndian.PutUint16(n[pos:], uint16(len(key)))
	binary.LittleEndian.PutUint16(n[pos+2:], uint16(len(val)))
	copy(n[pos+pairHeader:], key)
	copy(n[pos+pairHeader+len(key):], val)
	n.setOffset(i+1, n.offset(i)+uint16(pairHeader+len(key)+len(val)))
}

// appendRange recopie count paires de src (à partir de srcStart) dans dst (à partir de dstStart).
func appendRange(dst, src node, dstStart, srcStart, count uint16) {
	for k := uint16(0); k < count; k++ {
		i := srcStart + k
		appendKV(dst, dstStart+k, src.ptr(i), src.key(i), src.val(i))
	}
}

func pairCost(key, val []byte) int {
	return ptrSize + offsetSize + pairHeader + len(key) + len(val)
}

// leafInsert renvoie une nouvelle feuille : old avec (key, val) insérée à l'indice i.
// old n'est jamais modifiée, c'est la base de la copie sur écriture.
func leafInsert(old node, i uint16, key, val []byte) node {
	n := make(node, old.nbytes()+pairCost(key, val))
	n.setHeader(nodeLeaf, old.nkeys()+1)
	appendRange(n, old, 0, 0, i)
	appendKV(n, i, 0, key, val)
	appendRange(n, old, i+1, i, old.nkeys()-i)
	return n
}

// leafUpdate renvoie une nouvelle feuille : old avec la valeur de la paire i remplacée.
func leafUpdate(old node, i uint16, key, val []byte) node {
	n := make(node, old.nbytes()-len(old.val(i))+len(val))
	n.setHeader(nodeLeaf, old.nkeys())
	appendRange(n, old, 0, 0, i)
	appendKV(n, i, 0, key, val)
	appendRange(n, old, i+1, i+1, old.nkeys()-i-1)
	return n
}

// leafDelete renvoie une nouvelle feuille : old sans la paire i.
func leafDelete(old node, i uint16) node {
	n := make(node, old.nbytes()-pairCost(old.key(i), old.val(i)))
	n.setHeader(nodeLeaf, old.nkeys()-1)
	appendRange(n, old, 0, 0, i)
	appendRange(n, old, i, i+1, old.nkeys()-i-1)
	return n
}

// rangeSize est la taille d'un nœud qui contiendrait les paires [from, to) de n.
func rangeSize(n node, from, to uint16) int {
	return headerSize + (ptrSize+offsetSize)*int(to-from) + int(n.offset(to)-n.offset(from))
}

func sliceNode(n node, from, to uint16) node {
	m := make(node, rangeSize(n, from, to))
	m.setHeader(n.kind(), to-from)
	appendRange(m, n, 0, from, to-from)
	return m
}

func mergeNodes(left, right node) node {
	n := make(node, left.nbytes()+right.nbytes()-headerSize)
	n.setHeader(left.kind(), left.nkeys()+right.nkeys())
	appendRange(n, left, 0, 0, left.nkeys())
	appendRange(n, right, left.nkeys(), 0, right.nkeys())
	return n
}
