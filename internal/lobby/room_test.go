package lobby

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

// newTestManager returns a Manager whose empty rooms close after idle.
func newTestManager(t *testing.T, idle time.Duration) *Manager {
	t.Helper()
	m := NewManager()
	m.idleTimeout = idle
	return m
}

func TestRoomRosterTracksMembership(t *testing.T) {
	t.Parallel()
	r := newTestManager(t, time.Minute).Create()

	ada := join(t, r, "ada")
	bob := join(t, r, "bob")
	awaitRoster(t, ada, "ada", "bob")
	awaitRoster(t, bob, "ada", "bob")

	r.remove(bob)
	awaitRoster(t, ada, "ada")
}

func TestSlowPlayerIsDropped(t *testing.T) {
	t.Parallel()
	r := newTestManager(t, time.Minute).Create()

	slow := join(t, r, "slow") // never reads its queue
	keeping := []*client{join(t, r, "fast")}
	want := []string{"fast"}

	// Churn membership until the room gives up on the player who never reads.
	for i := range sendBuffer + 2 {
		name := fmt.Sprintf("filler%d", i)
		keeping = append(keeping, join(t, r, name))
		want = append(want, name)
		for _, c := range keeping {
			drain(c)
		}
	}
	awaitDropped(t, slow)

	// The players who kept up stay, and stop seeing the one who did not.
	join(t, r, "last")
	awaitRoster(t, keeping[0], append(want, "last")...)
}

func TestRoomClosesWhenIdle(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, 50*time.Millisecond)
	r := m.Create()
	if _, ok := m.Get(r.Code); !ok {
		t.Fatalf("Get(%q) = false, want the room just created", r.Code)
	}

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := m.Get(r.Code); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("room %s still registered, want it closed once empty", r.Code)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r.add(&client{name: "late", send: make(chan []byte, 1)}) {
		t.Error("joining a closed room = true, want false")
	}
}

func TestRoomStaysOpenWhileOccupied(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, 50*time.Millisecond)
	r := m.Create()
	ada := join(t, r, "ada")

	time.Sleep(5 * m.idleTimeout)
	if _, ok := m.Get(r.Code); !ok {
		t.Fatalf("Get(%q) = false, want an occupied room to stay open", r.Code)
	}
	awaitRoster(t, ada, "ada")
}

func TestManagerCreateAssignsUniqueCodes(t *testing.T) {
	t.Parallel()
	m := NewManager() // the default timeout keeps every room alive for the test
	seen := make(map[string]bool)
	for range 100 {
		r := m.Create()
		if seen[r.Code] {
			t.Fatalf("Create().Code = %q, want a code not already in use", r.Code)
		}
		seen[r.Code] = true
		if got, ok := m.Get(r.Code); !ok || got != r {
			t.Fatalf("Get(%q) = %v, %v, want the room just created", r.Code, got, ok)
		}
	}
}

// TestRosterWireFormat pins the bytes on the wire. The browser client and the
// bot loadtester parse this message, so a failure here means they need
// updating too — it is not a test to relax.
func TestRosterWireFormat(t *testing.T) {
	t.Parallel()
	ada, bob := &client{name: "ada"}, &client{name: "bob"}
	got := string(roster(map[*client]bool{bob: true, ada: true}))
	want := `{"type":"players","players":["ada","bob"]}`
	if got != want {
		t.Errorf("roster message = %s, want %s", got, want)
	}
}

// join adds a player to the room, failing the test if the room is gone.
func join(t *testing.T, r *Room, name string) *client {
	t.Helper()
	c := &client{name: name, send: make(chan []byte, sendBuffer)}
	if !r.add(c) {
		t.Fatalf("joining room %s as %s = false, want true", r.Code, name)
	}
	return c
}

// drain discards whatever c has queued so far.
func drain(c *client) {
	for len(c.send) > 0 {
		<-c.send
	}
}

// awaitRoster waits for c to see exactly the players in want. Intermediate
// rosters are ignored: how many updates the room sends, and when, is its own
// business — only the membership it settles on is a promise to the client.
func awaitRoster(t *testing.T, c *client, want ...string) {
	t.Helper()
	slices.Sort(want)
	deadline := time.After(time.Second)
	var last []string
	for {
		select {
		case msg, open := <-c.send:
			if !open {
				t.Fatalf("%s was dropped, want it to see roster %v", c.name, want)
			}
			if last = rosterNames(t, msg); slices.Equal(last, want) {
				return
			}
		case <-deadline:
			t.Fatalf("%s last saw roster %v, want %v", c.name, last, want)
		}
	}
}

// awaitDropped waits for the room to close c out.
func awaitDropped(t *testing.T, c *client) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case _, open := <-c.send:
			if !open {
				return
			}
		case <-deadline:
			t.Fatalf("%s still connected, want it dropped for falling behind", c.name)
		}
	}
}

// rosterNames decodes a roster message into the sorted names it lists.
func rosterNames(t *testing.T, msg []byte) []string {
	t.Helper()
	var got struct {
		Type    string   `json:"type"`
		Players []string `json:"players"`
	}
	if err := json.Unmarshal(msg, &got); err != nil {
		t.Fatalf("unmarshaling %q: %v", msg, err)
	}
	if got.Type != "players" {
		t.Fatalf("message type = %q, want %q", got.Type, "players")
	}
	names := slices.Clone(got.Players)
	slices.Sort(names)
	return names
}
