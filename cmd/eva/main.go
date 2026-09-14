// Command eva serves the EVA game lobby.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"dlhu.dev/eva/internal/lobby"
	"dlhu.dev/eva/internal/store"
	"dlhu.dev/eva/internal/web"
)

// shutdownTimeout bounds how long in-flight page requests get to finish.
const shutdownTimeout = 10 * time.Second

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	prefix := flag.String("prefix", "", `URL prefix the site is served under, e.g. "/eva"`)
	dbPath := flag.String("db", "eva.db", "SQLite file holding accounts and match history")
	flag.Parse()

	// The log package routes through this too, so net/http's own complaints
	// come out in the same format.
	slog.SetDefault(newLogger(os.Stderr, toJournal()))

	db, err := store.Open(*dbPath)
	if err != nil {
		slog.Error("opening database", "path", *dbPath, "err", err)
		os.Exit(1)
	}

	rooms := lobby.NewManager()
	srv := &http.Server{
		Addr:              *addr,
		Handler:           web.New(rooms, db, *prefix),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: it would kill long-lived websockets.
	}

	// Shut down on a signal rather than being killed, in order: stop taking
	// requests, then close the rooms so players get a clean disconnect and
	// finished matches are written, then close the database so its write-ahead
	// log is checkpointed back into the file.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		stop() // a second signal kills us the blunt way
		slog.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			slog.Error("shutting down http", "err", err)
		}
	}()

	slog.Info("eva listening", "addr", *addr, "prefix", *prefix)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serving", "err", err)
		db.Close()
		os.Exit(1)
	}
	<-drained // ListenAndServe returns as Shutdown starts, not when it is done
	rooms.Close()
	if err := db.Close(); err != nil {
		slog.Error("closing database", "path", *dbPath, "err", err)
	}
	slog.Info("stopped")
}

// newLogger returns a logger writing to w. For journald, which timestamps each
// entry itself, lines carry no time of their own and lead with a priority.
func newLogger(w io.Writer, journal bool) *slog.Logger {
	if !journal {
		return slog.New(slog.NewTextHandler(w, nil))
	}
	opts := &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{}
			}
			return a
		},
	}
	var h journalHandler
	mu := new(sync.Mutex)
	for i, prio := range []string{"<3>", "<4>", "<6>", "<7>"} {
		h[i] = slog.NewTextHandler(&prefixWriter{mu: mu, w: w, prefix: prio}, opts)
	}
	return slog.New(h)
}

// toJournal reports whether stderr goes to journald. systemd names that stream
// in JOURNAL_STREAM as device:inode, but child processes inherit the variable,
// so it counts only while stderr is still the stream it names.
func toJournal() bool {
	want := os.Getenv("JOURNAL_STREAM")
	if want == "" {
		return false
	}
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && fmt.Sprintf("%d:%d", st.Dev, st.Ino) == want
}

// journalHandler logs each record through the handler for its level's syslog
// priority: error, warning, info, debug. journald strips the leading <N> each of
// those writes and stores it as the entry's priority, so journalctl -p filters
// on it. The priority comes from the record's level, never from the formatted
// line, so changing the format cannot change it.
type journalHandler [4]slog.Handler

func (h journalHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h[0].Enabled(ctx, l) // all four share one set of options
}

func (h journalHandler) Handle(ctx context.Context, r slog.Record) error {
	i := 2 // info
	switch {
	case r.Level >= slog.LevelError:
		i = 0
	case r.Level >= slog.LevelWarn:
		i = 1
	case r.Level < slog.LevelInfo:
		i = 3
	}
	return h[i].Handle(ctx, r)
}

func (h journalHandler) WithAttrs(as []slog.Attr) slog.Handler {
	for i := range h {
		h[i] = h[i].WithAttrs(as)
	}
	return h
}

func (h journalHandler) WithGroup(name string) slog.Handler {
	for i := range h {
		h[i] = h[i].WithGroup(name)
	}
	return h
}

// prefixWriter writes each line after a fixed prefix, in one write, under a
// lock shared by every prefixWriter on the same stream so their lines cannot
// interleave.
type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
}

func (pw *prefixWriter) Write(p []byte) (int, error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	if _, err := pw.w.Write(append([]byte(pw.prefix), p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}
