package sql

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Une table n'est qu'un ensemble de clés du B+tree qui partagent un préfixe :
//
//	schéma d'une table : 0x01 nom                       -> colonnes
//	ligne              : 0x02 nom 0x00 clé primaire     -> toutes les colonnes de la ligne
//
// Le 0x00 après le nom empêche la table "a" de voir les lignes de la table "ab". Comme le B+tree trie
// les clés octet par octet, les lignes d'une table sont rangées côte à côte, dans l'ordre de leur clé
// primaire : c'est ce qui rend un WHERE sur la clé primaire rapide.
const (
	prefixSchema byte = 0x01
	prefixRow    byte = 0x02
)

func schemaKey(table string) []byte {
	return append([]byte{prefixSchema}, table...)
}

func tablePrefix(table string) []byte {
	b := append([]byte{prefixRow}, table...)
	return append(b, 0x00)
}

// prefixEnd est la première clé après toutes celles qui commencent par prefix (qui se termine par 0x00).
func prefixEnd(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	end[len(end)-1]++
	return end
}

func rowKey(t *table, pk Value) []byte {
	return append(tablePrefix(t.name), encodeKey(pk)...)
}

// encodeKey encode une valeur pour que l'ordre des octets soit l'ordre des valeurs.
//
// Un int64 en complément à deux place les négatifs après les positifs (-1 = 0xFF...). Inverser le bit
// de signe puis écrire en big-endian donne -2 < -1 < 0 < 1 octet par octet. Le texte est déjà dans
// le bon ordre, et comme il est en dernier dans la clé, il n'a pas besoin de séparateur.
func encodeKey(v Value) []byte {
	if v.Type == TypeInt {
		return binary.BigEndian.AppendUint64(nil, uint64(v.Int)^(1<<63))
	}
	return []byte(v.Text)
}

// Schéma : | nombre de colonnes (2) | pour chaque colonne : type (1) | longueur du nom (2) | nom |
func encodeSchema(t *table) []byte {
	b := binary.LittleEndian.AppendUint16(nil, uint16(len(t.cols)))
	for _, c := range t.cols {
		b = append(b, byte(c.Type))
		b = binary.LittleEndian.AppendUint16(b, uint16(len(c.Name)))
		b = append(b, c.Name...)
	}
	return b
}

var errCorrupt = errors.New("corrupt table data")

func decodeSchema(name string, b []byte) (*table, error) {
	if len(b) < 2 {
		return nil, errCorrupt
	}
	n := int(binary.LittleEndian.Uint16(b))
	b = b[2:]
	t := &table{name: name}
	for i := 0; i < n; i++ {
		if len(b) < 3 {
			return nil, errCorrupt
		}
		typ, l := Type(b[0]), int(binary.LittleEndian.Uint16(b[1:]))
		if len(b) < 3+l || typ != TypeInt && typ != TypeText {
			return nil, errCorrupt
		}
		t.cols = append(t.cols, Column{Name: string(b[3 : 3+l]), Type: typ})
		b = b[3+l:]
	}
	return t, nil
}

// Ligne : chaque colonne à la suite, dans l'ordre du schéma. INT : 8 octets ; TEXT : longueur (2) puis octets.
func encodeRow(t *table, row []Value) []byte {
	var b []byte
	for i, c := range t.cols {
		if c.Type == TypeInt {
			b = binary.LittleEndian.AppendUint64(b, uint64(row[i].Int))
		} else {
			b = binary.LittleEndian.AppendUint16(b, uint16(len(row[i].Text)))
			b = append(b, row[i].Text...)
		}
	}
	return b
}

func decodeRow(t *table, b []byte) ([]Value, error) {
	row := make([]Value, len(t.cols))
	for i, c := range t.cols {
		if c.Type == TypeInt {
			if len(b) < 8 {
				return nil, fmt.Errorf("%w: row of table %s", errCorrupt, t.name)
			}
			row[i] = IntValue(int64(binary.LittleEndian.Uint64(b)))
			b = b[8:]
			continue
		}
		if len(b) < 2 {
			return nil, fmt.Errorf("%w: row of table %s", errCorrupt, t.name)
		}
		l := int(binary.LittleEndian.Uint16(b))
		if len(b) < 2+l {
			return nil, fmt.Errorf("%w: row of table %s", errCorrupt, t.name)
		}
		row[i] = TextValue(string(b[2 : 2+l]))
		b = b[2+l:]
	}
	return row, nil
}
