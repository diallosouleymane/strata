package repl

import (
	"fmt"
	"strings"

	"github.com/diallosouleymane/strata/btree"
)

// Execute est séparé de la boucle stdin pour pouvoir le tester sans terminal.
//
// Commandes (insensibles à la casse) :
//
//	SET <clé> <valeur>     -> "OK"
//	GET <clé>              -> la valeur, ou "(nil)"
//	DEL <clé>              -> "1" si supprimée, "0" sinon
//	SCAN [début [fin]]     -> les paires de [début, fin) triées, une par ligne, ou "(empty)"
func Execute(db *btree.DB, line string) string {
	args := strings.Fields(line)
	if len(args) == 0 {
		return ""
	}
	cmd := strings.ToUpper(args[0])

	switch cmd {
	case "SET":
		if len(args) != 3 {
			return wrongArgs(cmd)
		}
		if err := db.Set([]byte(args[1]), []byte(args[2])); err != nil {
			return "ERR " + err.Error()
		}
		return "OK"

	case "GET":
		if len(args) != 2 {
			return wrongArgs(cmd)
		}
		v, ok, err := db.Get([]byte(args[1]))
		if err != nil {
			return "ERR " + err.Error()
		}
		if !ok {
			return "(nil)"
		}
		return string(v)

	case "DEL":
		if len(args) != 2 {
			return wrongArgs(cmd)
		}
		deleted, err := db.Delete([]byte(args[1]))
		if err != nil {
			return "ERR " + err.Error()
		}
		if deleted {
			return "1"
		}
		return "0"

	case "SCAN":
		if len(args) > 3 {
			return wrongArgs(cmd)
		}
		var start, end []byte
		if len(args) > 1 {
			start = []byte(args[1])
		}
		if len(args) > 2 {
			end = []byte(args[2])
		}
		var lines []string
		err := db.Scan(start, end, func(k, v []byte) bool {
			lines = append(lines, string(k)+" = "+string(v))
			return true
		})
		if err != nil {
			return "ERR " + err.Error()
		}
		if len(lines) == 0 {
			return "(empty)"
		}
		return strings.Join(lines, "\n")
	}

	return fmt.Sprintf("ERR unknown command '%s'", cmd)
}

func wrongArgs(cmd string) string {
	return fmt.Sprintf("ERR wrong number of arguments for '%s'", cmd)
}
