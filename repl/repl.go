package repl

import (
	"fmt"
	"strings"

	"github.com/diallosouleymane/strata/kv"
)

// Execute est séparé de la boucle stdin pour pouvoir le tester sans terminal.
//
// Commandes (insensibles à la casse) :
//
//	SET <clé> <valeur>  -> "OK"
//	GET <clé>           -> la valeur, ou "(nil)"
//	DEL <clé>           -> "1" si supprimée, "0" sinon
//	COMPACT             -> "OK" (réécrit le journal sans les entrées périmées)
func Execute(s *kv.Store, line string) string {
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
		if err := s.Set(args[1], args[2]); err != nil {
			return "ERR " + err.Error()
		}
		return "OK"

	case "GET":
		if len(args) != 2 {
			return wrongArgs(cmd)
		}
		if v, ok := s.Get(args[1]); ok {
			return v
		}
		return "(nil)"

	case "DEL":
		if len(args) != 2 {
			return wrongArgs(cmd)
		}
		deleted, err := s.Delete(args[1])
		if err != nil {
			return "ERR " + err.Error()
		}
		if deleted {
			return "1"
		}
		return "0"

	case "COMPACT":
		if len(args) != 1 {
			return wrongArgs(cmd)
		}
		if err := s.Compact(); err != nil {
			return "ERR " + err.Error()
		}
		return "OK"
	}

	return fmt.Sprintf("ERR unknown command '%s'", cmd)
}

func wrongArgs(cmd string) string {
	return fmt.Sprintf("ERR wrong number of arguments for '%s'", cmd)
}
