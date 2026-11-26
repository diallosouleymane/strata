package btree

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
)

// memStore panique sur toute erreur de gestion des pages, pour que les tests les attrapent.
type memStore struct {
	pages map[uint64]node
	next  uint64
}

func newMemStore() *memStore { return &memStore{pages: map[uint64]node{}, next: 1} }

func (s *memStore) get(id uint64) node {
	n, ok := s.pages[id]
	if !ok {
		panic(fmt.Sprintf("lecture de la page %d, qui n'existe pas ou a été libérée", id))
	}
	return n
}

func (s *memStore) alloc(n node) uint64 {
	if n.nbytes() > PageSize {
		panic(fmt.Sprintf("allocation d'un nœud de %d octets, plus qu'une page", n.nbytes()))
	}
	id := s.next
	s.next++
	s.pages[id] = n
	return id
}

func (s *memStore) free(id uint64) {
	if _, ok := s.pages[id]; !ok {
		panic(fmt.Sprintf("double libération de la page %d", id))
	}
	delete(s.pages, id)
}

func newMemTree() (*tree, *memStore) {
	s := newMemStore()
	return &tree{pages: s}, s
}

// checkTree vérifie tous les invariants d'un B+tree et que son contenu est exactement want.
func checkTree(t *testing.T, tr *tree, s *memStore, want map[string]string) {
	t.Helper()
	if tr.root == 0 {
		if len(want) != 0 {
			t.Fatalf("arbre vide, attendu %d clés", len(want))
		}
		return
	}
	var keys []string
	vals := map[string]string{}
	leafDepth := -1
	reachable := 0
	var walk func(id uint64, depth int, isRoot bool) []byte
	walk = func(id uint64, depth int, isRoot bool) []byte {
		reachable++
		n := s.get(id)
		if n.nbytes() > PageSize {
			t.Fatalf("page %d : %d octets", id, n.nbytes())
		}
		if !isRoot && n.nkeys() == 0 {
			t.Fatalf("page %d : nœud vide", id)
		}
		if n.kind() == nodeLeaf {
			if leafDepth == -1 {
				leafDepth = depth
			} else if depth != leafDepth {
				t.Fatalf("feuilles à des profondeurs %d et %d", leafDepth, depth)
			}
			for i := uint16(0); i < n.nkeys(); i++ {
				keys = append(keys, string(n.key(i)))
				vals[string(n.key(i))] = string(n.val(i))
			}
			return n.key(0)
		}
		for i := uint16(0); i < n.nkeys(); i++ {
			first := walk(n.ptr(i), depth+1, false)
			if !bytes.Equal(first, n.key(i)) {
				t.Fatalf("page %d : clé %d = %q mais l'enfant commence par %q", id, i, n.key(i), first)
			}
		}
		return n.key(0)
	}
	walk(tr.root, 0, true)

	if len(keys) == 0 || keys[0] != "" {
		t.Fatal("la première clé de l'arbre doit être la sentinelle vide")
	}
	keys = keys[1:]
	if !sort.StringsAreSorted(keys) {
		t.Fatal("les clés des feuilles ne sont pas triées")
	}
	if len(keys) != len(want) {
		t.Fatalf("%d clés dans l'arbre ; attendu %d", len(keys), len(want))
	}
	for k, v := range want {
		if vals[k] != v {
			t.Fatalf("clé %q : valeur %q ; attendu %q", k, vals[k], v)
		}
	}
	if reachable != len(s.pages) {
		t.Fatalf("%d pages atteignables mais %d allouées : des pages ont fuité", reachable, len(s.pages))
	}
}

func height(tr *tree, s *memStore) int {
	h := 1
	for n := s.get(tr.root); n.kind() == nodeInternal; n = s.get(n.ptr(0)) {
		h++
	}
	return h
}

func TestTreeSetGet(t *testing.T) {
	tr, s := newMemTree()
	if _, ok := tr.get([]byte("a")); ok {
		t.Fatal("Get sur un arbre vide doit échouer")
	}
	want := map[string]string{}
	for _, k := range []string{"m", "c", "x", "a", "p"} {
		if err := tr.set([]byte(k), []byte("v"+k)); err != nil {
			t.Fatal(err)
		}
		want[k] = "v" + k
	}
	tr.set([]byte("c"), []byte("nouvelle"))
	want["c"] = "nouvelle"
	checkTree(t, tr, s, want)

	if v, ok := tr.get([]byte("c")); !ok || string(v) != "nouvelle" {
		t.Fatalf("get(c) = %q, %v", v, ok)
	}
	if _, ok := tr.get([]byte("b")); ok {
		t.Fatal("get(b) doit échouer")
	}
	if _, ok := tr.get(nil); ok {
		t.Fatal("la sentinelle ne doit pas être visible")
	}
}

func TestTreeGrowsInHeight(t *testing.T) {
	tr, s := newMemTree()
	want := map[string]string{}
	for i := 0; i < 20000; i++ {
		k := fmt.Sprintf("cle-%06d", i)
		tr.set([]byte(k), []byte("valeur"))
		want[k] = "valeur"
	}
	checkTree(t, tr, s, want)
	if h := height(tr, s); h < 3 || h > 4 {
		t.Fatalf("hauteur %d pour 20 000 clés ; attendu 3 ou 4", h)
	}
	t.Logf("20 000 clés : hauteur %d, %d pages", height(tr, s), len(s.pages))
}

// Des paires proches de la taille maximale forcent les découpes en trois.
func TestTreeLargePairs(t *testing.T) {
	tr, s := newMemTree()
	want := map[string]string{}
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 400; i++ {
		k := fmt.Sprintf("%04d", r.IntN(1000)) + strings.Repeat("k", r.IntN(MaxKeySize-4))
		v := strings.Repeat("v", r.IntN(MaxValSize+1))
		if err := tr.set([]byte(k), []byte(v)); err != nil {
			t.Fatal(err)
		}
		want[k] = v
	}
	checkTree(t, tr, s, want)
}

func TestTreeValidation(t *testing.T) {
	tr, _ := newMemTree()
	cases := []struct {
		key, val []byte
		want     error
	}{
		{nil, []byte("v"), ErrEmptyKey},
		{bytes.Repeat([]byte("k"), MaxKeySize+1), nil, ErrKeyTooLong},
		{[]byte("k"), bytes.Repeat([]byte("v"), MaxValSize+1), ErrValTooLong},
	}
	for _, c := range cases {
		if err := tr.set(c.key, c.val); err != c.want {
			t.Errorf("set : %v ; attendu %v", err, c.want)
		}
	}
	if tr.root != 0 {
		t.Fatal("une écriture refusée ne doit rien créer")
	}
}

func TestTreeDelete(t *testing.T) {
	tr, s := newMemTree()
	if ok, _ := tr.del([]byte("a")); ok {
		t.Fatal("del sur un arbre vide doit renvoyer false")
	}
	for _, k := range []string{"a", "b", "c"} {
		tr.set([]byte(k), []byte(k))
	}
	if ok, _ := tr.del([]byte("b")); !ok {
		t.Fatal("del(b) doit renvoyer true")
	}
	if ok, _ := tr.del([]byte("b")); ok {
		t.Fatal("del(b) une seconde fois doit renvoyer false")
	}
	if ok, _ := tr.del([]byte("zz")); ok {
		t.Fatal("del d'une clé absente doit renvoyer false")
	}
	checkTree(t, tr, s, map[string]string{"a": "a", "c": "c"})
}

// Vider un grand arbre doit le ramener à une seule feuille : les fusions et la perte de niveaux marchent.
func TestTreeShrinksBackToOneLeaf(t *testing.T) {
	tr, s := newMemTree()
	r := rand.New(rand.NewPCG(3, 4))
	keys := r.Perm(20000)
	for _, k := range keys {
		tr.set([]byte(fmt.Sprintf("%06d", k)), []byte("x"))
	}
	r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	for i, k := range keys {
		if ok, _ := tr.del([]byte(fmt.Sprintf("%06d", k))); !ok {
			t.Fatalf("del(%06d) a échoué", k)
		}
		if i == len(keys)/2 {
			checkTree(t, tr, s, remaining(keys[i+1:]))
		}
	}
	checkTree(t, tr, s, map[string]string{})
	if h := height(tr, s); h != 1 || len(s.pages) != 1 {
		t.Fatalf("arbre vidé : hauteur %d, %d pages ; attendu 1 et 1", h, len(s.pages))
	}
}

func remaining(keys []int) map[string]string {
	m := map[string]string{}
	for _, k := range keys {
		m[fmt.Sprintf("%06d", k)] = "x"
	}
	return m
}

// Des opérations aléatoires comparées à une map : c'est le test qui attrape les cas auxquels on n'a pas pensé.
func TestTreeRandomOpsMatchMap(t *testing.T) {
	tr, s := newMemTree()
	want := map[string]string{}
	r := rand.New(rand.NewPCG(5, 6))
	for op := 0; op < 30000; op++ {
		k := fmt.Sprintf("k%04d", r.IntN(4000))
		if r.IntN(50) == 0 {
			k += strings.Repeat("~", r.IntN(MaxKeySize-5))
		}
		if r.IntN(3) == 0 {
			ok, _ := tr.del([]byte(k))
			_, existed := want[k]
			if ok != existed {
				t.Fatalf("op %d : del(%q) = %v ; attendu %v", op, k, ok, existed)
			}
			delete(want, k)
		} else {
			v := strings.Repeat("v", r.IntN(200))
			if r.IntN(100) == 0 {
				v = strings.Repeat("V", MaxValSize)
			}
			tr.set([]byte(k), []byte(v))
			want[k] = v
		}
		if op%3000 == 0 {
			checkTree(t, tr, s, want)
		}
	}
	checkTree(t, tr, s, want)
	for k, v := range want {
		if got, ok := tr.get([]byte(k)); !ok || string(got) != v {
			t.Fatalf("get(%q) = %d octets, %v", k, len(got), ok)
		}
	}
}
