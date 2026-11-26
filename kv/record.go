package kv

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

// Format d'un enregistrement sur disque (entiers en little-endian) :
//
//	| crc32 (4) | op (1) | len clé (4) | len valeur (4) | clé | valeur |
//
// Le crc couvre tout ce qui suit lui-même.
const headerSize = 4 + 1 + 4 + 4

const (
	opSet byte = 1
	opDel byte = 2
)

type record struct {
	op    byte
	key   string
	value string
}

var (
	errEnd  = errors.New("fin du journal")
	errTorn = errors.New("enregistrement incomplet ou corrompu")
)

func encodeRecord(rec record) []byte {
	buf := make([]byte, headerSize+len(rec.key)+len(rec.value))
	buf[4] = rec.op
	binary.LittleEndian.PutUint32(buf[5:9], uint32(len(rec.key)))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(len(rec.value)))
	copy(buf[headerSize:], rec.key)
	copy(buf[headerSize+len(rec.key):], rec.value)
	binary.LittleEndian.PutUint32(buf[0:4], crc32.ChecksumIEEE(buf[4:]))
	return buf
}

// readRecord lit un enregistrement et renvoie sa taille en octets.
// remaining borne les longueurs lues : sans ça, une longueur corrompue pourrait demander 4 Go de RAM.
func readRecord(r io.Reader, remaining int64) (record, int64, error) {
	var h [headerSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if err == io.EOF {
			return record{}, 0, errEnd
		}
		if err == io.ErrUnexpectedEOF {
			return record{}, 0, errTorn
		}
		return record{}, 0, err
	}

	keyLen := int64(binary.LittleEndian.Uint32(h[5:9]))
	valLen := int64(binary.LittleEndian.Uint32(h[9:13]))
	size := headerSize + keyLen + valLen
	if size > remaining {
		return record{}, 0, errTorn
	}

	body := make([]byte, keyLen+valLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return record{}, 0, err
	}

	sum := crc32.Update(crc32.ChecksumIEEE(h[4:]), crc32.IEEETable, body)
	if sum != binary.LittleEndian.Uint32(h[0:4]) {
		return record{}, 0, errTorn
	}

	op := h[4]
	if op != opSet && op != opDel {
		return record{}, 0, errTorn
	}
	return record{op: op, key: string(body[:keyLen]), value: string(body[keyLen:])}, size, nil
}
