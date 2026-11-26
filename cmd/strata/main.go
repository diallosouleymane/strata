package main

import (
	"bufio"
	"fmt"
	"log"
	"os"

	"github.com/diallosouleymane/strata/btree"
	"github.com/diallosouleymane/strata/repl"
)

func main() {
	path := "strata.db"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	db, err := btree.Open(path)
	if err != nil {
		log.Fatalf("ouverture de %s : %v", path, err)
	}
	defer db.Close()

	session := repl.NewSession(db)
	defer session.Close()

	in := bufio.NewScanner(os.Stdin)
	prompt := func() {
		if session.InTx() {
			fmt.Print("strata*> ") // l'étoile signale une transaction ouverte, comme dans psql
		} else {
			fmt.Print("strata> ")
		}
	}
	prompt()
	for in.Scan() {
		if out := session.Execute(in.Text()); out != "" {
			fmt.Println(out)
		}
		prompt()
	}
	if err := in.Err(); err != nil {
		log.Printf("lecture de l'entrée : %v", err)
	}
}
