// strata ouvre une base dans un fichier et lit des commandes sur l'entrée standard.
//
//	strata [fichier]                       REPL local (strata.db par défaut)
//	strata -listen :7878 [fichier]         sert la base sur TCP
//	strata -connect localhost:7878         REPL sur un serveur distant
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/diallosouleymane/strata/btree"
	"github.com/diallosouleymane/strata/repl"
	"github.com/diallosouleymane/strata/server"
)

func main() {
	listen := flag.String("listen", "", "serve the database over TCP on this address, e.g. :7878")
	connect := flag.String("connect", "", "connect to a strata server instead of opening a file")
	flag.Parse()

	if *connect != "" {
		c, err := server.Dial(*connect)
		if err != nil {
			log.Fatalf("connexion à %s : %v", *connect, err)
		}
		defer c.Close()
		loop(c.Execute)
		return
	}

	path := "strata.db"
	if flag.NArg() > 0 {
		path = flag.Arg(0)
	}
	db, err := btree.Open(path)
	if err != nil {
		log.Fatalf("ouverture de %s : %v", path, err)
	}
	defer db.Close()

	if *listen != "" {
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			log.Fatalf("écoute sur %s : %v", *listen, err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		log.Printf("strata sert %s sur %s (Ctrl+C pour arrêter)", path, ln.Addr())
		if err := server.Serve(ctx, ln, db); err != nil {
			log.Printf("serveur : %v", err)
		}
		log.Printf("strata arrêté, connexions fermées")
		return
	}

	session := repl.NewSession(db)
	defer session.Close()
	loop(func(line string) (string, bool, error) {
		return session.Execute(line), session.InTx(), nil
	})
}

// loop est la boucle interactive, la même en local et à distance.
func loop(execute func(line string) (out string, inTx bool, err error)) {
	in := bufio.NewScanner(os.Stdin)
	inTx := false
	for {
		if inTx {
			fmt.Print("strata*> ") // l'étoile signale une transaction ouverte, comme dans psql
		} else {
			fmt.Print("strata> ")
		}
		if !in.Scan() {
			break
		}
		out, tx, err := execute(in.Text())
		if err != nil {
			log.Printf("%v", err)
			return
		}
		inTx = tx
		if out != "" {
			fmt.Println(out)
		}
	}
	if err := in.Err(); err != nil {
		log.Printf("lecture de l'entrée : %v", err)
	}
}
