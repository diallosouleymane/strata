package btree

import (
	"bytes"
	"fmt"
	"testing"
)

type pair struct{ key, val string }

func buildLeaf(pairs ...pair) node {
	size := headerSize
	for _, p := range pairs {
		size += pairCost([]byte(p.key), []byte(p.val))
	}
	n := make(node, size)
	n.setHeader(nodeLeaf, uint16(len(pairs)))
	for i, p := range pairs {
		appendKV(n, uint16(i), 0, []byte(p.key), []byte(p.val))
	}
	return n
}

func assertPairs(t *testing.T, n node, want ...pair) {
	t.Helper()
	if int(n.nkeys()) != len(want) {
		t.Fatalf("nkeys = %d ; attendu %d", n.nkeys(), len(want))
	}
	for i, p := range want {
		if k := string(n.key(uint16(i))); k != p.key {
			t.Errorf("key(%d) = %q ; attendu %q", i, k, p.key)
		}
		if v := string(n.val(uint16(i))); v != p.val {
			t.Errorf("val(%d) = %q ; attendu %q", i, v, p.val)
		}
	}
	if n.nbytes() != len(n) {
		t.Errorf("nbytes = %d mais le nœud fait %d octets", n.nbytes(), len(n))
	}
}

// Fige le format sur disque : si ce test casse, les fichiers déjà écrits deviennent illisibles.
func TestLayoutBytes(t *testing.T) {
	n := buildLeaf(pair{"a", "1"}, pair{"bc", ""})
	want := []byte{
		0x02, 0x00, 0x02, 0x00, // type = feuille, nkeys = 2
		0, 0, 0, 0, 0, 0, 0, 0, // pointeur 0 (inutilisé dans une feuille)
		0, 0, 0, 0, 0, 0, 0, 0, // pointeur 1
		0x06, 0x00, // fin de la paire 0 = 4 + 1 + 1
		0x0c, 0x00, // fin de la paire 1 = 6 + 4 + 2 + 0
		0x01, 0x00, 0x01, 0x00, 'a', '1', // paire 0
		0x02, 0x00, 0x00, 0x00, 'b', 'c', // paire 1
	}
	if !bytes.Equal(n, want) {
		t.Fatalf("octets :\n  obtenu  % x\n  attendu % x", []byte(n), want)
	}
}

func TestReadBack(t *testing.T) {
	n := buildLeaf(pair{"ananas", "jaune"}, pair{"banane", ""}, pair{"cerise", "rouge"})
	assertPairs(t, n, pair{"ananas", "jaune"}, pair{"banane", ""}, pair{"cerise", "rouge"})
}

func TestInternalPointers(t *testing.T) {
	n := make(node, headerSize+2*pairCost([]byte("a"), nil))
	n.setHeader(nodeInternal, 2)
	appendKV(n, 0, 7, []byte("a"), nil)
	appendKV(n, 1, 1<<40, []byte("m"), nil)

	if n.kind() != nodeInternal {
		t.Fatalf("kind = %d ; attendu %d", n.kind(), nodeInternal)
	}
	if n.ptr(0) != 7 || n.ptr(1) != 1<<40 {
		t.Fatalf("pointeurs = %d, %d ; attendu 7, %d", n.ptr(0), n.ptr(1), uint64(1<<40))
	}
}

func TestLeafInsert(t *testing.T) {
	old := buildLeaf(pair{"b", "2"}, pair{"d", "4"})
	cases := []struct {
		i    uint16
		key  string
		want []pair
	}{
		{0, "a", []pair{{"a", "x"}, {"b", "2"}, {"d", "4"}}},
		{1, "c", []pair{{"b", "2"}, {"c", "x"}, {"d", "4"}}},
		{2, "e", []pair{{"b", "2"}, {"d", "4"}, {"e", "x"}}},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			assertPairs(t, leafInsert(old, c.i, []byte(c.key), []byte("x")), c.want...)
		})
	}
	// Copie sur écriture : l'original ne doit pas avoir bougé.
	assertPairs(t, old, pair{"b", "2"}, pair{"d", "4"})
}

func TestLeafUpdate(t *testing.T) {
	old := buildLeaf(pair{"a", "1"}, pair{"b", "2"}, pair{"c", "3"})
	assertPairs(t, leafUpdate(old, 1, []byte("b"), []byte("une valeur plus longue")),
		pair{"a", "1"}, pair{"b", "une valeur plus longue"}, pair{"c", "3"})
	assertPairs(t, leafUpdate(old, 2, []byte("c"), nil), pair{"a", "1"}, pair{"b", "2"}, pair{"c", ""})
	assertPairs(t, old, pair{"a", "1"}, pair{"b", "2"}, pair{"c", "3"})
}

func TestLookupLE(t *testing.T) {
	n := buildLeaf(pair{"b", ""}, pair{"d", ""}, pair{"f", ""})
	cases := []struct {
		key  string
		want int
	}{
		{"a", -1}, // plus petite que toutes
		{"b", 0},  // égale à la première
		{"c", 0},  // entre b et d
		{"d", 1},
		{"e", 1},
		{"f", 2},
		{"z", 2}, // plus grande que toutes
		{"bb", 0},
	}
	for _, c := range cases {
		if got := n.lookupLE([]byte(c.key)); got != c.want {
			t.Errorf("lookupLE(%q) = %d ; attendu %d", c.key, got, c.want)
		}
	}
}

// La recherche dichotomique doit donner le même résultat qu'un parcours linéaire.
func TestLookupLEMatchesLinearScan(t *testing.T) {
	var pairs []pair
	for i := 0; i < 200; i += 2 {
		pairs = append(pairs, pair{fmt.Sprintf("k%03d", i), ""})
	}
	n := buildLeaf(pairs...)
	for i := -1; i <= 201; i++ {
		key := []byte(fmt.Sprintf("k%03d", i))
		want := -1
		for j := range pairs {
			if pairs[j].key <= string(key) {
				want = j
			}
		}
		if got := n.lookupLE(key); got != want {
			t.Fatalf("lookupLE(%q) = %d ; attendu %d", key, got, want)
		}
	}
}

// Garantit qu'une paire de taille maximale tient seule dans une page : sinon un split ne pourrait jamais suffire.
func TestMaxPairFitsInPage(t *testing.T) {
	key := bytes.Repeat([]byte("k"), MaxKeySize)
	val := bytes.Repeat([]byte("v"), MaxValSize)
	if size := headerSize + pairCost(key, val); size > PageSize {
		t.Fatalf("une paire maximale occupe %d octets, plus qu'une page de %d", size, PageSize)
	}
}

func TestOutOfRangePanics(t *testing.T) {
	n := buildLeaf(pair{"a", "1"})
	defer func() {
		if recover() == nil {
			t.Fatal("key(1) sur un nœud d'une clé doit paniquer")
		}
	}()
	n.key(1)
}
