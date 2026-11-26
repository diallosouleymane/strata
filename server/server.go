// Package server expose une base strata sur TCP.
//
// Protocole, ligne par ligne en UTF-8 :
//
//	client  -> une commande par ligne, exactement ce qu'on taperait dans le REPL
//	serveur -> "<n> <état>" puis n lignes de réponse ; état vaut "tx" si une transaction est ouverte, sinon "idle"
//
// Le nombre de lignes en tête permet les réponses sur plusieurs lignes (SCAN, SELECT) sans caractère
// de fin réservé, et l'état permet au client d'afficher le bon prompt.
package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/diallosouleymane/strata/btree"
	"github.com/diallosouleymane/strata/repl"
)

// maxLine borne une commande : au-delà, un client pourrait faire grossir la mémoire du serveur sans limite.
const maxLine = 1 << 20

// Serve accepte des connexions jusqu'à l'annulation de ctx, puis ferme celles en cours et attend leur fin.
// Chaque connexion a sa propre goroutine et sa propre session : une transaction laissée ouverte par un
// client qui se déconnecte est annulée.
func Serve(ctx context.Context, ln net.Listener, db *btree.DB) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns = map[net.Conn]struct{}{}
	)
	stop := context.AfterFunc(ctx, func() {
		ln.Close()
		mu.Lock()
		for c := range conns {
			c.Close() // débloque la lecture en cours de chaque connexion
		}
		mu.Unlock()
	})
	defer stop()

	for {
		conn, err := ln.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		conns[conn] = struct{}{}
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle(conn, db)
			mu.Lock()
			delete(conns, conn)
			mu.Unlock()
		}()
	}
}

func handle(conn net.Conn, db *btree.DB) {
	defer conn.Close()
	session := repl.NewSession(db)
	defer session.Close()

	in := bufio.NewScanner(conn)
	in.Buffer(make([]byte, 0, 4096), maxLine)
	out := bufio.NewWriter(conn)
	for in.Scan() {
		writeResponse(out, session.Execute(in.Text()), session.InTx())
		if out.Flush() != nil {
			return
		}
	}
}

func writeResponse(w *bufio.Writer, out string, inTx bool) {
	var lines []string
	if out != "" {
		lines = strings.Split(out, "\n")
	}
	state := "idle"
	if inTx {
		state = "tx"
	}
	fmt.Fprintf(w, "%d %s\n", len(lines), state)
	for _, l := range lines {
		w.WriteString(l + "\n")
	}
}

// Client parle le protocole du serveur. Il ne doit pas être partagé entre goroutines.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func Dial(addr string) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Execute envoie une commande et renvoie la réponse, et si la session distante est dans une transaction.
func (c *Client) Execute(line string) (out string, inTx bool, err error) {
	if strings.ContainsAny(line, "\r\n") {
		return "", false, errors.New("a command must fit on one line")
	}
	c.w.WriteString(line + "\n")
	if err := c.w.Flush(); err != nil {
		return "", false, err
	}
	header, err := c.r.ReadString('\n')
	if err != nil {
		return "", false, err
	}
	count, state, ok := strings.Cut(strings.TrimSuffix(header, "\n"), " ")
	n, convErr := strconv.Atoi(count)
	if !ok || convErr != nil || n < 0 || state != "tx" && state != "idle" {
		return "", false, fmt.Errorf("unexpected response header %q", header)
	}
	lines := make([]string, n)
	for i := range lines {
		l, err := c.r.ReadString('\n')
		if err != nil {
			return "", false, err
		}
		lines[i] = strings.TrimSuffix(l, "\n")
	}
	return strings.Join(lines, "\n"), state == "tx", nil
}
