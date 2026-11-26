package main

import (
	"bufio"
	"fmt"
	"log"
	"os"

	"github.com/diallosouleymane/strata/kv"
	"github.com/diallosouleymane/strata/repl"
)

func main() {
	path := "strata.log"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	store, err := kv.Open(path)
	if err != nil {
		log.Fatalf("ouverture de %s : %v", path, err)
	}
	defer store.Close()

	in := bufio.NewScanner(os.Stdin)
	fmt.Print("strata> ")
	for in.Scan() {
		if out := repl.Execute(store, in.Text()); out != "" {
			fmt.Println(out)
		}
		fmt.Print("strata> ")
	}
	if err := in.Err(); err != nil {
		log.Printf("lecture de l'entrée : %v", err)
	}
}
