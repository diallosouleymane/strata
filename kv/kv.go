package kv

import (
	"bufio"
	"errors"
	"os"
)

type Store struct {
	data map[string]string
	f    *os.File
}

// Open ouvre (ou crée) le journal et reconstruit l'état en le rejouant.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Store{data: make(map[string]string), f: f}
	if err := s.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) replay() error {
	info, err := s.f.Stat()
	if err != nil {
		return err
	}
	r := bufio.NewReader(s.f)
	var offset int64
	for {
		rec, n, err := readRecord(r, info.Size()-offset)
		if err == errEnd {
			return nil
		}
		if err == errTorn {
			// Une écriture interrompue par un crash laisse une fin de fichier incomplète :
			// on la coupe pour que les prochains ajouts repartent d'un état propre.
			return s.f.Truncate(offset)
		}
		if err != nil {
			return err
		}
		s.apply(rec)
		offset += n
	}
}

func (s *Store) apply(rec record) {
	switch rec.op {
	case opSet:
		s.data[rec.key] = rec.value
	case opDel:
		delete(s.data, rec.key)
	}
}

// write écrit sur le disque avant la mémoire : si le disque refuse, l'état en RAM reste cohérent avec le fichier.
func (s *Store) write(rec record) error {
	if _, err := s.f.Write(encodeRecord(rec)); err != nil {
		return err
	}
	// Sans Sync, les octets peuvent rester dans le cache de l'OS et disparaître en cas de coupure de courant.
	if err := s.f.Sync(); err != nil {
		return err
	}
	s.apply(rec)
	return nil
}

func (s *Store) Set(key, value string) error {
	return s.write(record{op: opSet, key: key, value: value})
}

// Get renvoie ok=false si la clé n'existe pas, pour distinguer « absente » de « valeur vide ».
func (s *Store) Get(key string) (value string, ok bool) {
	value, ok = s.data[key]
	return value, ok
}

// Delete renvoie true si la clé existait. Une clé absente n'écrit rien dans le journal.
func (s *Store) Delete(key string) (bool, error) {
	if _, ok := s.data[key]; !ok {
		return false, nil
	}
	if err := s.write(record{op: opDel, key: key}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) Len() int {
	return len(s.data)
}

func (s *Store) Close() error {
	if s.f == nil {
		return errors.New("store déjà fermé")
	}
	err := s.f.Close()
	s.f = nil
	return err
}
