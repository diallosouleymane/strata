package kv

import (
	"os"
	"path/filepath"
	"testing"
)

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	return s
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s := openAt(t, filepath.Join(t.TempDir(), "db.log"))
	t.Cleanup(func() { s.Close() })
	return s
}

func mustSet(t *testing.T, s *Store, key, value string) {
	t.Helper()
	if err := s.Set(key, value); err != nil {
		t.Fatalf("Set(%q) : %v", key, err)
	}
}

func TestSetGet(t *testing.T) {
	s := openTemp(t)
	mustSet(t, s, "name", "Issa")

	v, ok := s.Get("name")
	if !ok || v != "Issa" {
		t.Fatalf("Get(name) = %q, %v ; attendu \"Issa\", true", v, ok)
	}
}

func TestGetAbsent(t *testing.T) {
	s := openTemp(t)
	if v, ok := s.Get("absent"); ok {
		t.Fatalf("Get(absent) = %q, true ; attendu ok=false", v)
	}
}

func TestEmptyValueIsNotAbsent(t *testing.T) {
	s := openTemp(t)
	mustSet(t, s, "vide", "")
	if _, ok := s.Get("vide"); !ok {
		t.Fatal("une clé avec une valeur vide doit exister")
	}
}

func TestOverwrite(t *testing.T) {
	s := openTemp(t)
	mustSet(t, s, "k", "1")
	mustSet(t, s, "k", "2")
	if v, _ := s.Get("k"); v != "2" {
		t.Fatalf("Get(k) = %q ; attendu \"2\"", v)
	}
	if s.Len() != 1 {
		t.Fatalf("Len() = %d ; attendu 1", s.Len())
	}
}

func TestDelete(t *testing.T) {
	s := openTemp(t)
	mustSet(t, s, "k", "v")

	if ok, err := s.Delete("k"); !ok || err != nil {
		t.Fatalf("Delete(k) = %v, %v ; attendu true, nil", ok, err)
	}
	if _, ok := s.Get("k"); ok {
		t.Fatal("la clé doit avoir disparu après Delete")
	}
	if ok, _ := s.Delete("k"); ok {
		t.Fatal("Delete(k) une seconde fois doit renvoyer false")
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.log")
	s := openAt(t, path)
	mustSet(t, s, "a", "1")
	mustSet(t, s, "b", "2")
	mustSet(t, s, "a", "3")
	s.Delete("b")
	s.Close()

	s = openAt(t, path)
	defer s.Close()
	if v, _ := s.Get("a"); v != "3" {
		t.Fatalf("après réouverture, Get(a) = %q ; attendu \"3\"", v)
	}
	if _, ok := s.Get("b"); ok {
		t.Fatal("après réouverture, b doit rester supprimée")
	}
}

// Simule un crash au milieu d'une écriture : seuls les premiers octets du dernier enregistrement sont sur disque.
func TestTornWriteIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.log")
	s := openAt(t, path)
	mustSet(t, s, "a", "1")
	s.Close()

	partial := encodeRecord(record{op: opSet, key: "b", value: "2"})[:7]
	appendBytes(t, path, partial)

	s = openAt(t, path)
	if v, _ := s.Get("a"); v != "1" {
		t.Fatalf("Get(a) = %q ; attendu \"1\"", v)
	}
	if _, ok := s.Get("b"); ok {
		t.Fatal("l'enregistrement incomplet ne doit pas être appliqué")
	}
	// Le morceau incomplet a été coupé : une nouvelle écriture doit être relue correctement.
	mustSet(t, s, "c", "3")
	s.Close()

	s = openAt(t, path)
	defer s.Close()
	if v, _ := s.Get("c"); v != "3" {
		t.Fatalf("après troncature, Get(c) = %q ; attendu \"3\"", v)
	}
}

func TestCorruptedRecordIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.log")
	s := openAt(t, path)
	mustSet(t, s, "a", "1")
	mustSet(t, s, "b", "2")
	s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xFF // inverse les bits du dernier octet de la valeur "2"
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	s = openAt(t, path)
	defer s.Close()
	if v, _ := s.Get("a"); v != "1" {
		t.Fatalf("Get(a) = %q ; attendu \"1\"", v)
	}
	if _, ok := s.Get("b"); ok {
		t.Fatal("un enregistrement au checksum faux ne doit pas être appliqué")
	}
}

func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}
