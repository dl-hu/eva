// Command eva serves the EVA game lobby.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dlhu.dev/eva/internal/lobby"
	"dlhu.dev/eva/internal/store"
	"dlhu.dev/eva/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	prefix := flag.String("prefix", "", `URL prefix the site is served under, e.g. "/eva"`)
	dbPath := flag.String("db", "eva.db", "SQLite file holding accounts and match history")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("opening %s: %v", *dbPath, err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           web.New(lobby.NewManager(), db, *prefix),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: it would kill long-lived websockets.
	}

	// Shut down on a signal rather than being killed, so the database is
	// closed and its write-ahead log checkpointed back into the file.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second signal kills us the blunt way
		log.Print("shutting down")
		srv.Shutdown(context.Background())
	}()

	log.Printf("eva listening on %s%s/", *addr, *prefix)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Print(err)
	}
	if err := db.Close(); err != nil {
		log.Printf("closing %s: %v", *dbPath, err)
	}
}
