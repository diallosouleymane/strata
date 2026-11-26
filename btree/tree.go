package btree

import (
	"bytes"
	"errors"
	"fmt"
)

// pageStore est tout ce que l'arbre sait du stockage : des numéros de page.
// L'arbre ne modifie jamais une page : il en alloue une nouvelle et libère l'ancienne.
type pageStore interface {
	get(id uint64) node
	alloc(n node) uint64
	free(id uint64)
}

var (
	ErrEmptyKey   = errors.New("btree: clé vide")
	ErrKeyTooLong = fmt.Errorf("btree: clé de plus de %d octets", MaxKeySize)
	ErrValTooLong = fmt.Errorf("btree: valeur de plus de %d octets", MaxValSize)
)

// La page 0 n'est jamais un nœud (c'est une page méta sur disque) : root == 0 veut dire « arbre vide ».
type tree struct {
	root  uint64
	pages pageStore
}

func checkKV(key, val []byte) error {
	switch {
	case len(key) == 0:
		return ErrEmptyKey
	case len(key) > MaxKeySize:
		return ErrKeyTooLong
	case len(val) > MaxValSize:
		return ErrValTooLong
	}
	return nil
}

// lookup est lookupLE pendant une descente : la clé sentinelle vide garantit un résultat ≥ 0.
func lookup(n node, key []byte) uint16 {
	i := n.lookupLE(key)
	if i < 0 {
		panic("btree: clé sentinelle absente du nœud le plus à gauche")
	}
	return uint16(i)
}

func (t *tree) get(key []byte) ([]byte, bool) {
	if len(key) == 0 || t.root == 0 {
		return nil, false
	}
	n := t.pages.get(t.root)
	for n.kind() == nodeInternal {
		n = t.pages.get(n.ptr(lookup(n, key)))
	}
	i := lookup(n, key)
	if !bytes.Equal(n.key(i), key) {
		return nil, false
	}
	// La valeur pointe dans la page : une copie évite que l'appelant garde une vue sur un tampon recyclé.
	return bytes.Clone(n.val(i)), true
}

func (t *tree) set(key, val []byte) error {
	if err := checkKV(key, val); err != nil {
		return err
	}
	if t.root == 0 {
		// Toute clé est > "", donc avec cette sentinelle en tête la descente trouve toujours un enfant.
		root := make(node, headerSize+pairCost(nil, nil)+pairCost(key, val))
		root.setHeader(nodeLeaf, 2)
		appendKV(root, 0, 0, nil, nil)
		appendKV(root, 1, 0, key, val)
		t.root = t.pages.alloc(root)
		return nil
	}
	updated := t.insert(t.pages.get(t.root), key, val)
	t.pages.free(t.root)
	t.root = t.newRoot(split(updated))
	return nil
}

func (t *tree) insert(n node, key, val []byte) node {
	i := lookup(n, key)
	switch n.kind() {
	case nodeLeaf:
		if bytes.Equal(n.key(i), key) {
			return leafUpdate(n, i, key, val)
		}
		return leafInsert(n, i+1, key, val)
	case nodeInternal:
		kidID := n.ptr(i)
		kid := t.insert(t.pages.get(kidID), key, val)
		t.pages.free(kidID)
		return t.replaceKids(n, i, 1, split(kid))
	}
	panic(fmt.Sprintf("btree: type de nœud inconnu %d", n.kind()))
}

// newRoot fait grandir l'arbre par le haut tant que la racine ne tient pas dans une page.
func (t *tree) newRoot(parts []node) uint64 {
	for len(parts) > 1 {
		size := headerSize
		for _, p := range parts {
			size += pairCost(p.key(0), nil)
		}
		root := make(node, size)
		root.setHeader(nodeInternal, uint16(len(parts)))
		for i, p := range parts {
			appendKV(root, uint16(i), t.pages.alloc(p), p.key(0), nil)
		}
		parts = split(root)
	}
	return t.pages.alloc(parts[0])
}

// split coupe un nœud trop gros en morceaux qui tiennent chacun dans une page.
func split(n node) []node {
	if n.nbytes() <= PageSize {
		return []node{n}
	}
	cnt := n.nkeys()
	best, bestMax := uint16(0), 0
	for k := uint16(1); k < cnt; k++ {
		l, r := rangeSize(n, 0, k), rangeSize(n, k, cnt)
		if l <= PageSize && r <= PageSize && (best == 0 || max(l, r) < bestMax) {
			best, bestMax = k, max(l, r)
		}
	}
	if best > 0 {
		return []node{sliceNode(n, 0, best), sliceNode(n, best, cnt)}
	}
	// Une grosse paire au milieu peut rendre toute coupe en deux impossible : on remplit des pages de gauche à droite.
	var parts []node
	for from := uint16(0); from < cnt; {
		to := from + 1
		for to < cnt && rangeSize(n, from, to+1) <= PageSize {
			to++
		}
		parts = append(parts, sliceNode(n, from, to))
		from = to
	}
	return parts
}

// replaceKids renvoie une copie du nœud interne n où les count enfants à partir de i sont remplacés par kids.
// La clé d'un enfant dans son parent est toujours sa première clé.
func (t *tree) replaceKids(n node, i, count uint16, kids []node) node {
	size := n.nbytes()
	for j := i; j < i+count; j++ {
		size -= pairCost(n.key(j), nil)
	}
	for _, k := range kids {
		size += pairCost(k.key(0), nil)
	}
	m := make(node, size)
	m.setHeader(nodeInternal, n.nkeys()-count+uint16(len(kids)))
	appendRange(m, n, 0, 0, i)
	for j, k := range kids {
		appendKV(m, i+uint16(j), t.pages.alloc(k), k.key(0), nil)
	}
	appendRange(m, n, i+uint16(len(kids)), i+count, n.nkeys()-i-count)
	return m
}

func (t *tree) del(key []byte) (bool, error) {
	if err := checkKV(key, nil); err != nil {
		return false, err
	}
	if t.root == 0 {
		return false, nil
	}
	updated := t.remove(t.pages.get(t.root), key)
	if updated == nil {
		return false, nil
	}
	t.pages.free(t.root)
	// Une racine interne à un seul enfant ne sert à rien : l'arbre perd un niveau.
	for updated.kind() == nodeInternal && updated.nkeys() == 1 {
		id := updated.ptr(0)
		updated = t.pages.get(id)
		t.pages.free(id)
	}
	t.root = t.newRoot(split(updated))
	return true, nil
}

// remove renvoie le nœud n sans key, ou nil si key n'y est pas.
func (t *tree) remove(n node, key []byte) node {
	i := lookup(n, key)
	switch n.kind() {
	case nodeLeaf:
		if !bytes.Equal(n.key(i), key) {
			return nil
		}
		return leafDelete(n, i)
	case nodeInternal:
		kidID := n.ptr(i)
		kid := t.remove(t.pages.get(kidID), key)
		if kid == nil {
			return nil
		}
		t.pages.free(kidID)
		return t.rebalance(n, i, kid)
	}
	panic(fmt.Sprintf("btree: type de nœud inconnu %d", n.kind()))
}

func fits(left, right node) bool {
	return left.nbytes()+right.nbytes()-headerSize <= PageSize
}

// rebalance remplace l'enfant i de n par kid, en le fusionnant avec un voisin s'il est devenu trop petit.
// Le seuil d'un quart de page évite de fusionner puis recouper sans cesse autour de la moitié.
func (t *tree) rebalance(n node, i uint16, kid node) node {
	if kid.nbytes() <= PageSize/4 {
		if i > 0 {
			leftID := n.ptr(i - 1)
			if left := t.pages.get(leftID); fits(left, kid) {
				t.pages.free(leftID)
				return t.replaceKids(n, i-1, 2, []node{mergeNodes(left, kid)})
			}
		}
		if i+1 < n.nkeys() {
			rightID := n.ptr(i + 1)
			if right := t.pages.get(rightID); fits(kid, right) {
				t.pages.free(rightID)
				return t.replaceKids(n, i, 2, []node{mergeNodes(kid, right)})
			}
		}
	}
	if kid.nkeys() == 0 {
		return t.replaceKids(n, i, 1, nil)
	}
	// Une suppression peut aussi faire grossir un nœud : la nouvelle première clé d'un enfant
	// peut être plus longue que l'ancienne, et elle remonte dans le parent.
	return t.replaceKids(n, i, 1, split(kid))
}

// scan appelle fn sur chaque clé de [start, end), dans l'ordre. end == nil veut dire « jusqu'à la fin ».
// fn renvoie false pour arrêter. key et val pointent dans la page : ils ne sont valables que pendant l'appel.
func (t *tree) scan(start, end []byte, fn func(key, val []byte) bool) {
	if t.root != 0 {
		t.scanNode(t.root, start, end, fn)
	}
}

// scanNode descend directement vers start, puis parcourt les enfants de gauche à droite :
// seules les pages qui contiennent des clés de la plage sont lues.
func (t *tree) scanNode(id uint64, start, end []byte, fn func(key, val []byte) bool) bool {
	n := t.pages.get(id)
	from := uint16(0)
	if i := n.lookupLE(start); i > 0 {
		from = uint16(i)
	}
	for i := from; i < n.nkeys(); i++ {
		k := n.key(i)
		if end != nil && bytes.Compare(k, end) >= 0 {
			return false
		}
		if n.kind() == nodeInternal {
			if !t.scanNode(n.ptr(i), start, end, fn) {
				return false
			}
			continue
		}
		if len(k) == 0 || bytes.Compare(k, start) < 0 {
			continue // la sentinelle, ou une clé avant le début de la plage
		}
		if !fn(k, n.val(i)) {
			return false
		}
	}
	return true
}
