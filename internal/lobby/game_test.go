package lobby

import (
	"encoding/json"
	"testing"
	"time"

	"dlhu.dev/eva/internal/game"
)

// anyMsg is every field the server sends, for tests that only care about some.
type anyMsg struct {
	Type   string `json:"type"`
	Host   bool   `json:"host"`
	Snake  int    `json:"snake"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Snakes []struct {
		Name  string `json:"name"`
		X     int    `json:"x"`
		Y     int    `json:"y"`
		Dir   int    `json:"dir"`
		Alive bool   `json:"alive"`
	} `json:"snakes"`
	Moves  [][4]int `json:"moves"`
	Dead   []int    `json:"dead"`
	Winner int      `json:"winner"`
	Name   string   `json:"name"`
}

func TestOnlyTheHostStartsTheGame(t *testing.T) {
	t.Parallel()
	r := newTestManager(t, time.Minute).Create("host-id")
	host := joinAs(t, r, "host-id", "ada")
	guest := joinAs(t, r, "guest-id", "bob")

	r.send(input{c: guest, msg: clientMsg{Type: "start", W: 20, H: 20}})
	if msg, ok := lookFor(t, host, "start", 200*time.Millisecond); ok {
		t.Fatalf("a guest started a %dx%d game, want only the host to be able to", msg.W, msg.H)
	}

	r.send(input{c: host, msg: clientMsg{Type: "start", W: 20, H: 20}})
	msg := awaitMsg(t, guest, "start")
	if got, want := len(msg.Snakes), 2; got != want {
		t.Errorf("game started with %d snakes, want %d: everyone in the room plays", got, want)
	}
}

func TestHostChoosesTheBoardSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		w, h  int
		wantW int
		wantH int
	}{
		{"as asked", 120, 80, 120, 80},
		{"too small to play on", 1, 2, game.MinSize, game.MinSize},
		{"larger than allowed", 9999, 9999, game.MaxSize, game.MaxSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newTestManager(t, time.Minute).Create("host-id")
			host := joinAs(t, r, "host-id", "ada")

			r.send(input{c: host, msg: clientMsg{Type: "start", W: tt.w, H: tt.h}})
			msg := awaitMsg(t, host, "start")
			if msg.W != tt.wantW || msg.H != tt.wantH {
				t.Errorf("asked for %dx%d, got a %dx%d board, want %dx%d",
					tt.w, tt.h, msg.W, msg.H, tt.wantW, tt.wantH)
			}
			for i, sn := range msg.Snakes {
				if sn.X < 0 || sn.X >= msg.W || sn.Y < 0 || sn.Y >= msg.H {
					t.Errorf("snake %d spawned at (%d,%d), outside the board", i, sn.X, sn.Y)
				}
			}
		})
	}
}

func TestPlayersDriveTheirOwnSnake(t *testing.T) {
	t.Parallel()
	r := newTestManager(t, time.Minute).Create("host-id")
	host := joinAs(t, r, "host-id", "ada")

	// A board big enough that nobody crashes while the test watches.
	r.send(input{c: host, msg: clientMsg{Type: "start", W: 200, H: 200}})
	start := awaitMsg(t, host, "start")
	facing := game.Dir(start.Snakes[0].Dir)

	// Turn a quarter circle, whichever way is not a reversal.
	want := game.Left
	if facing == game.Left || facing == game.Right {
		want = game.Up
	}
	r.send(input{c: host, msg: clientMsg{Type: "dir", Dir: dirName(want)}})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("snake never turned to face %v", want)
		default:
		}
		tick := awaitMsg(t, host, "tick")
		if len(tick.Moves) == 0 {
			continue
		}
		if game.Dir(tick.Moves[0][3]) == want {
			return
		}
	}
}

func TestGameEndsAndTheRoomReturnsToItsLobby(t *testing.T) {
	t.Parallel()
	r := newTestManager(t, time.Minute).Create("host-id")
	host := joinAs(t, r, "host-id", "ada")

	// The smallest board there is: a lone snake runs out of room in seconds.
	r.send(input{c: host, msg: clientMsg{Type: "start", W: game.MinSize, H: game.MinSize}})
	awaitMsg(t, host, "start")

	over := awaitMsg(t, host, "over")
	if over.Winner != -1 {
		t.Errorf("winner = %d, want -1: the only player crashed", over.Winner)
	}
	// Back in the lobby, the host can start another round.
	awaitMsg(t, host, "players")
	r.send(input{c: host, msg: clientMsg{Type: "start", W: 20, H: 20}})
	awaitMsg(t, host, "start")
}

func TestLeavingMidGameEndsYourRun(t *testing.T) {
	t.Parallel()
	r := newTestManager(t, time.Minute).Create("host-id")
	host := joinAs(t, r, "host-id", "ada")
	guest := joinAs(t, r, "guest-id", "bob")

	r.send(input{c: host, msg: clientMsg{Type: "start", W: 200, H: 200}})
	start := awaitMsg(t, host, "start")
	var guestSnake int
	for i, sn := range start.Snakes {
		if sn.Name == "bob" {
			guestSnake = i
		}
	}

	r.remove(guest)
	// The last player standing wins, so the game ends with the host.
	over := awaitMsg(t, host, "over")
	if over.Winner == guestSnake {
		t.Errorf("winner = %d, want anyone but the player who left", over.Winner)
	}
	if got, want := over.Name, "ada"; got != want {
		t.Errorf("winner name = %q, want %q", got, want)
	}
}

func dirName(d game.Dir) string {
	for name, dir := range dirs {
		if dir == d {
			return name
		}
	}
	return ""
}

// awaitMsg reads c's queue until a message of the given type arrives.
func awaitMsg(t *testing.T, c *client, want string) anyMsg {
	t.Helper()
	msg, ok := lookFor(t, c, want, 5*time.Second)
	if !ok {
		t.Fatalf("%s saw no %q message", c.name, want)
	}
	return msg
}

// lookFor reads c's queue for a message of the given type, giving up after d.
func lookFor(t *testing.T, c *client, want string, d time.Duration) (anyMsg, bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case data, open := <-c.send:
			if !open {
				t.Fatalf("%s was dropped while waiting for %q", c.name, want)
			}
			var msg anyMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("unmarshaling %q: %v", data, err)
			}
			if msg.Type == want {
				return msg, true
			}
		case <-deadline:
			return anyMsg{}, false
		}
	}
}
