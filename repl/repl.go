package repl

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/diallosouleymane/strata/btree"
)

// Session porte l'état d'un utilisateur : sa transaction ouverte, s'il en a une.
// Chaque client a la sienne ; une Session ne doit pas être partagée entre goroutines.
type Session struct {
	db *btree.DB
	tx *btree.Tx
}

func NewSession(db *btree.DB) *Session { return &Session{db: db} }

func (s *Session) InTx() bool { return s.tx != nil }

// Close annule la transaction laissée ouverte, par exemple quand un client se déconnecte.
func (s *Session) Close() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}

// Execute est séparé de la boucle stdin pour pouvoir le tester sans terminal.
//
// Commandes (insensibles à la casse) :
//
//	SET <clé> <valeur>     -> "OK"
//	GET <clé>              -> la valeur, ou "(nil)"
//	DEL <clé>              -> "1" si supprimée, "0" sinon
//	SCAN [début [fin]]     -> les paires de [début, fin) triées, une par ligne, ou "(empty)"
//	BEGIN, COMMIT, ROLLBACK
func (s *Session) Execute(line string) string {
	args := strings.Fields(line)
	if len(args) == 0 {
		return ""
	}
	cmd := strings.ToUpper(args[0])
	out, err := s.run(cmd, args)
	if err != nil {
		// Une transaction morte (erreur d'entrée-sortie en plein milieu) ne doit pas rester attachée à la session.
		if errors.Is(err, btree.ErrTxDone) {
			s.tx = nil
		}
		return "ERR " + err.Error()
	}
	return out
}

func (s *Session) run(cmd string, args []string) (string, error) {
	need := map[string]int{"SET": 3, "GET": 2, "DEL": 2, "BEGIN": 1, "COMMIT": 1, "ROLLBACK": 1}
	if n, ok := need[cmd]; ok && len(args) != n || cmd == "SCAN" && len(args) > 3 {
		return fmt.Sprintf("ERR wrong number of arguments for '%s'", cmd), nil
	}

	switch cmd {
	case "SET":
		if s.tx != nil {
			return "OK", s.tx.Set([]byte(args[1]), []byte(args[2]))
		}
		return "OK", s.db.Set([]byte(args[1]), []byte(args[2]))

	case "GET":
		var v []byte
		var ok bool
		var err error
		if s.tx != nil {
			v, ok, err = s.tx.Get([]byte(args[1]))
		} else {
			v, ok, err = s.db.Get([]byte(args[1]))
		}
		if err != nil || !ok {
			return "(nil)", err
		}
		return show(v), nil

	case "DEL":
		var deleted bool
		var err error
		if s.tx != nil {
			deleted, err = s.tx.Delete([]byte(args[1]))
		} else {
			deleted, err = s.db.Delete([]byte(args[1]))
		}
		if deleted {
			return "1", err
		}
		return "0", err

	case "SCAN":
		var start, end []byte
		if len(args) > 1 {
			start = []byte(args[1])
		}
		if len(args) > 2 {
			end = []byte(args[2])
		}
		var lines []string
		collect := func(k, v []byte) bool {
			lines = append(lines, show(k)+" = "+show(v))
			return true
		}
		var err error
		if s.tx != nil {
			err = s.tx.Scan(start, end, collect)
		} else {
			err = s.db.Scan(start, end, collect)
		}
		if len(lines) == 0 {
			return "(empty)", err
		}
		return strings.Join(lines, "\n"), err

	case "BEGIN":
		if s.tx != nil {
			return "", errors.New("already in a transaction")
		}
		tx, err := s.db.Begin()
		if err != nil {
			return "", err
		}
		s.tx = tx
		return "OK", nil

	case "COMMIT", "ROLLBACK":
		if s.tx == nil {
			return "", errors.New("no transaction in progress")
		}
		tx := s.tx
		s.tx = nil
		if cmd == "COMMIT" {
			return "OK", tx.Commit()
		}
		return "OK", tx.Rollback()
	}

	return fmt.Sprintf("ERR unknown command '%s'", cmd), nil
}

// show affiche les octets tels quels s'ils sont lisibles, sinon entre guillemets avec des échappements :
// les clés internes des tables SQL contiennent des octets de contrôle.
func show(b []byte) string {
	if !utf8.Valid(b) {
		return strconv.Quote(string(b))
	}
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			return strconv.Quote(string(b))
		}
	}
	return string(b)
}
