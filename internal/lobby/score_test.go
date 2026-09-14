package lobby

import (
	"slices"
	"testing"
	"time"

	"dlhu.dev/eva/internal/game"
)

// scoreRoom returns a room seated with the named players, ready for
// scoreboard to be called against a board of its own.
func scoreRoom(names ...string) *Room {
	r := &Room{Code: "TEST", state: game.New(20, 20, len(names))}
	for i, n := range names {
		r.playing = append(r.playing, &client{name: n, snake: i})
	}
	return r
}

// TestFinishedGameIsRecorded closes the loop the scoreboard tests open: a game
// played through the room's own goroutine reaches the recorder.
func TestFinishedGameIsRecorded(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, time.Minute)
	got := make(chan []Placing, 1)
	m.Record = func(code string, places []Placing) {
		select {
		case got <- places:
		default:
		}
	}
	r := m.Create("host-id")
	// Signed in before joining: once the room has the client, run owns it.
	host := &client{sessionID: "host-id", name: "ada", userID: 7, send: make(chan []byte, sendBuffer), snake: -1}
	if !r.add(host) {
		t.Fatal("joining the room = false, want true")
	}

	// The smallest board: a lone snake drives into the edge within a second.
	r.send(input{c: host, msg: clientMsg{Type: "start", W: game.MinSize, H: game.MinSize}})
	select {
	case places := <-got:
		want := []Placing{{Place: 1, UserID: 7, Name: "ada"}}
		if !slices.Equal(places, want) {
			t.Errorf("recorded %+v, want %+v", places, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a finished game never reached the recorder")
	}
}

func TestScoreboardRanksByWhoLastedLongest(t *testing.T) {
	t.Parallel()
	r := scoreRoom("ada", "bob", "cyd")
	// bob goes out first, then cyd; ada is still driving when it ends.
	r.knockouts = [][]int{{1}, {2}}
	for _, i := range []int{1, 2} {
		r.state.Kill(i)
	}

	got := r.scoreboard()
	want := []placeMsg{
		{Place: 1, Snake: 0, Name: "ada"},
		{Place: 2, Snake: 2, Name: "cyd"},
		{Place: 3, Snake: 1, Name: "bob"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("scoreboard = %+v, want %+v", got, want)
	}
}

func TestScoreboardSharesAPlaceOnATie(t *testing.T) {
	t.Parallel()
	r := scoreRoom("ada", "bob", "cyd")
	// ada and bob take each other out head-on; cyd survives alone.
	r.knockouts = [][]int{{0, 1}}
	r.state.Kill(0)
	r.state.Kill(1)

	got := r.scoreboard()
	want := []placeMsg{
		{Place: 1, Snake: 2, Name: "cyd"},
		{Place: 2, Snake: 0, Name: "ada"},
		{Place: 2, Snake: 1, Name: "bob"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("scoreboard = %+v, want %+v", got, want)
	}
}

func TestScoreboardWithNoSurvivors(t *testing.T) {
	t.Parallel()
	r := scoreRoom("ada", "bob")
	r.knockouts = [][]int{{0}, {1}}
	r.state.Kill(0)
	r.state.Kill(1)

	got := r.scoreboard()
	want := []placeMsg{
		{Place: 1, Snake: 1, Name: "bob"},
		{Place: 2, Snake: 0, Name: "ada"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("scoreboard = %+v, want %+v", got, want)
	}
}

// A signed-in player carries the account name that makes their row a link;
// a guest carries none, which is what renders them grey and inert.
func TestScoreboardMarksAccountHolders(t *testing.T) {
	t.Parallel()
	r := scoreRoom("ada", "bob")
	r.playing[0].userID = 7
	r.knockouts = [][]int{{1}}
	r.state.Kill(1)

	got := r.scoreboard()
	if got[0].User != "ada" || got[0].userID != 7 {
		t.Errorf("signed-in row = %+v, want user \"ada\" and id 7", got[0])
	}
	if got[1].User != "" || got[1].userID != 0 {
		t.Errorf("guest row = %+v, want no account", got[1])
	}
	places := toPlacings(got)
	if places[0].UserID != 7 || places[1].UserID != 0 {
		t.Errorf("recorded placings = %+v, want ada as user 7 and bob as a guest", places)
	}
}
