package kv

import (
	"bufio"
	"os"
	"path/filepath"
)

func compactPath(path string) string {
	return path + ".compact"
}

// Compact réécrit le journal avec un seul SET par clé vivante.
//
// Le nouveau journal est écrit à côté puis renommé par-dessus l'ancien : rename est atomique,
// donc après un crash on trouve soit l'ancien journal complet, soit le nouveau, jamais un mélange.
func (s *Store) Compact() error {
	tmpPath := compactPath(s.path)
	// Ce descripteur restera celui du journal : il suit le fichier à travers le rename, pas besoin de rouvrir.
	tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if err := writeSnapshot(tmp, s.data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}

	old := s.f
	s.f = tmp
	s.records = len(s.data)
	old.Close()

	// Le rename modifie le dossier, pas le fichier : sans Sync du dossier, il peut être perdu après une coupure.
	return syncDir(filepath.Dir(s.path))
}

func writeSnapshot(f *os.File, data map[string]string) error {
	w := bufio.NewWriter(f)
	for k, v := range data {
		if _, err := w.Write(encodeRecord(record{op: opSet, key: k, value: v})); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
