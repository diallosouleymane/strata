package repl

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/diallosouleymane/strata/btree"
	"github.com/diallosouleymane/strata/sql"
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
	var out string
	var err error
	if sqlCommands[firstWord(cmd)] {
		out, err = s.sql(line)
	} else {
		out, err = s.run(cmd, args)
	}
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

var sqlCommands = map[string]bool{"CREATE": true, "INSERT": true, "SELECT": true, "UPDATE": true, "DELETE": true}

// firstWord coupe au premier caractère qui n'est pas une lettre, pour reconnaître SELECT dans « SELECT*FROM t ».
func firstWord(s string) string {
	if i := strings.IndexFunc(s, func(r rune) bool { return r < 'A' || r > 'Z' }); i >= 0 {
		return s[:i]
	}
	return s
}

// sql exécute une instruction SQL. Dans un BEGIN, elle rejoint la transaction ouverte. Sinon, un SELECT
// lit directement la dernière version validée, sans bloquer personne, et une écriture devient sa propre
// transaction.
func (s *Session) sql(line string) (string, error) {
	stmt, err := sql.Parse(line)
	if err != nil {
		return "", err
	}
	var res *sql.Result
	_, isSelect := stmt.(*sql.Select)
	switch {
	case s.tx != nil:
		res, err = sql.Exec(s.tx, stmt)
	case isSelect:
		res, err = sql.Exec(s.db, stmt)
	default:
		tx, berr := s.db.Begin()
		if berr != nil {
			return "", berr
		}
		if res, err = sql.Exec(tx, stmt); err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}
	if err != nil {
		return "", err
	}
	return formatResult(res), nil
}

// formatResult dessine un tableau à la manière de psql, les nombres alignés à droite.
func formatResult(res *sql.Result) string {
	if res.Columns == nil {
		return res.Message
	}
	widths := make([]int, len(res.Columns))
	for i, c := range res.Columns {
		widths[i] = utf8.RuneCountInString(c)
	}
	for _, row := range res.Rows {
		for i, v := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(v.String()))
		}
	}
	pad := func(s string, w int, right bool) string {
		gap := strings.Repeat(" ", w-utf8.RuneCountInString(s))
		if right {
			return gap + s
		}
		return s + gap
	}
	var b strings.Builder
	var sep []string
	for i, c := range res.Columns {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(pad(c, widths[i], false))
		sep = append(sep, strings.Repeat("-", widths[i]))
	}
	b.WriteString("\n" + strings.Join(sep, "-+-"))
	for _, row := range res.Rows {
		b.WriteString("\n")
		for i, v := range row {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(pad(v.String(), widths[i], v.Type == sql.TypeInt))
		}
	}
	plural := "s"
	if len(res.Rows) == 1 {
		plural = ""
	}
	fmt.Fprintf(&b, "\n(%d row%s, %s)", len(res.Rows), plural, res.Plan)
	return b.String()
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
