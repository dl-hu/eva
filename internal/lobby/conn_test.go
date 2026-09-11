package lobby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// newTestSite serves the rooms of m over websockets at ws://…/ws/{code}.
func newTestSite(t *testing.T, m *Manager) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/{code}", func(w http.ResponseWriter, r *http.Request) {
		room, ok := m.Get(r.PathValue("code"))
		if !ok {
			http.Error(w, "no such room", http.StatusNotFound)
			return
		}
		room.Serve(w, r, r.URL.Query().Get("name"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http")
}

func TestServeDeliversRoster(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, time.Minute)
	wsURL := newTestSite(t, m)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	room := m.Create()
	ada := dial(ctx, t, wsURL, room.Code, "ada")
	bob := dial(ctx, t, wsURL, room.Code, "bob")
	awaitWireRoster(ctx, t, ada, "ada", "bob")
	awaitWireRoster(ctx, t, bob, "ada", "bob")

	bob.Close(websocket.StatusNormalClosure, "")
	awaitWireRoster(ctx, t, ada, "ada")
}

func TestServeRejectsUnknownRoom(t *testing.T) {
	t.Parallel()
	wsURL := newTestSite(t, newTestManager(t, time.Minute))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if _, _, err := websocket.Dial(ctx, wsURL+"/ws/ZZZZ", nil); err == nil {
		t.Error("dialing a room that does not exist = nil error, want a failure")
	}
}

// TestServeOutlivesTheIdleTimer guards the join that races a closing room: the
// player either gets in or is turned away, but never hangs.
func TestServeOutlivesTheIdleTimer(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, time.Millisecond)
	wsURL := newTestSite(t, m)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	for range 50 {
		room := m.Create()
		conn, _, err := websocket.Dial(ctx, wsURL+"/ws/"+room.Code+"?name=ada", nil)
		if err != nil {
			continue // the room closed first; that is a fair outcome
		}
		if _, _, err := conn.Read(ctx); err != nil {
			conn.CloseNow()
			continue // connected, then the room went away; also fair
		}
		conn.CloseNow()
	}
}

func dial(ctx context.Context, t *testing.T, wsURL, code, name string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL+"/ws/"+code+"?name="+name, nil)
	if err != nil {
		t.Fatalf("dialing as %s = %v, want no error", name, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// awaitWireRoster waits for conn to receive a roster listing exactly want.
func awaitWireRoster(ctx context.Context, t *testing.T, conn *websocket.Conn, want ...string) {
	t.Helper()
	slices.Sort(want)
	var last []string
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("last saw roster %v, want %v: %v", last, want, err)
		}
		if last = rosterNames(t, msg); slices.Equal(last, want) {
			return
		}
	}
}
