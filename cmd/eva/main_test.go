package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// TestJournalLines checks what journald is handed: a priority it can filter
// on, the level still readable, and no second timestamp.
func TestJournalLines(t *testing.T) {
	var out bytes.Buffer
	log := newLogger(&out, true)
	log.Debug("hidden") // below the default level
	log.Info("a")
	log.Warn("b")
	log.Error("c", "err", "boom")
	log.With("room", "AB12").WithGroup("g").Error("d", "k", 1)
	log.Log(context.Background(), slog.LevelError+4, "e")
	log.Info("f") // With and WithGroup above left log itself alone

	want := strings.Join([]string{
		`<6>level=INFO msg=a`,
		`<4>level=WARN msg=b`,
		`<3>level=ERROR msg=c err=boom`,
		`<3>level=ERROR msg=d room=AB12 g.k=1`,
		`<3>level=ERROR+4 msg=e`,
		`<6>level=INFO msg=f`,
	}, "\n")
	if got := strings.TrimSpace(out.String()); got != want {
		t.Errorf("journal lines:\n%s\nwant:\n%s", got, want)
	}

	out.Reset()
	newLogger(&out, false).Info("a")
	if !strings.HasPrefix(out.String(), "time=") {
		t.Errorf("terminal line = %q, want it to carry its own time", out.String())
	}
}

// TestJournalLinesDoNotInterleave logs at every priority at once: each level
// has a handler of its own, and only the shared lock keeps their lines whole.
func TestJournalLinesDoNotInterleave(t *testing.T) {
	var out bytes.Buffer
	log := newLogger(&out, true)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { log.Info("a") })
		wg.Go(func() { log.Warn("b") })
		wg.Go(func() { log.Error("c") })
	}
	wg.Wait()

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		switch line {
		case `<6>level=INFO msg=a`, `<4>level=WARN msg=b`, `<3>level=ERROR msg=c`:
		default:
			t.Fatalf("mangled line %q", line)
		}
	}
}
