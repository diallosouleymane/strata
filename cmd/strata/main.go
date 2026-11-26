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

	in := bufio.NewScanner(os.Stdin)
	fmt.Print("strata> ")
	for in.Scan() {
		if out := repl.Execute(db, in.Text()); out != "" {
			fmt.Println(out)
		}
		fmt.Print("strata> ")
	}
	if err := in.Err(); err != nil {
		log.Printf("lecture de l'entrée : %v", err)
	}
}
