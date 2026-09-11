// Command eva serves the EVA game lobby.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"dlhu.dev/eva/internal/lobby"
	"dlhu.dev/eva/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	prefix := flag.String("prefix", "", `URL prefix the site is served under, e.g. "/eva"`)
	flag.Parse()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           web.New(lobby.NewManager(), *prefix),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: it would kill long-lived websockets.
	}
	log.Printf("eva listening on %s%s/", *addr, *prefix)
	log.Fatal(srv.ListenAndServe())
}
