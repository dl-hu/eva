package lobby

import (
	"encoding/json"
	"log"

	"dlhu.dev/eva/internal/game"
)

// The messages below are the contract between the server, the browser client
// in room.html, and the bot loadtester. Changing one changes all three.
//
// From a player:
//
//	{"type":"dir","dir":"up"}          turn, one of up/down/left/right
//	{"type":"start","w":50,"h":50}     host only: begin a game on a w×h board
//
// From the server:
//
//	{"type":"you","host":true,"snake":3,"seat":0} who this connection is
//	{"type":"players","players":[                 lobby roster
//	 {"seat":0,"name":"ada","user":"ada"},{"seat":1,"name":"bob"}]}
//	{"type":"start","w":50,"h":50,"snakes":[…]}   a game begins
//	{"type":"tick","moves":[[3,10,4,1]],"dead":[]} one step: snake, x, y, dir
//	{"type":"over","winner":3,"name":"ada",       last snake standing, and
//	 "places":[{"place":1,"snake":3,"name":"ada","user":"ada"}]}  the scoreboard
type clientMsg struct {
	Type string `json:"type"`
	Dir  string `json:"dir"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

// dirs maps what a player sends to a heading. Anything else is ignored.
var dirs = map[string]game.Dir{
	"up":    game.Up,
	"down":  game.Down,
	"left":  game.Left,
	"right": game.Right,
}

type youMsg struct {
	Type  string `json:"type"`
	Host  bool   `json:"host"`
	Snake int    `json:"snake"` // -1 until a game starts
	Seat  int    `json:"seat"`  // which roster entry is this connection
}

// rosterPlayer is one player in the lobby. Seat names the connection, so a
// client can pick itself out of a roster holding two players under the same
// display name.
type rosterPlayer struct {
	Seat int    `json:"seat"`
	Name string `json:"name"` // what to show, whoever they are
	// User is the account Name belongs to, and absent for a guest, so it is what
	// decides whether a client may link a name to a history. It comes from
	// client.account, never from Name.
	User string `json:"user,omitempty"`
}

type rosterMsg struct {
	Type    string         `json:"type"`
	Players []rosterPlayer `json:"players"`
}

// startSnake is a snake's opening position, indexed by snake number.
type startSnake struct {
	Name  string `json:"name"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Dir   int    `json:"dir"`
	Alive bool   `json:"alive"`
}

type startMsg struct {
	Type   string       `json:"type"`
	W      int          `json:"w"`
	H      int          `json:"h"`
	Snakes []startSnake `json:"snakes"`
}

// tickMsg carries one step. A move is [snake, x, y, dir], kept as an array
// because a busy room sends thousands of them ten times a second.
type tickMsg struct {
	Type  string   `json:"type"`
	Moves [][4]int `json:"moves"`
	Dead  []int    `json:"dead,omitempty"`
}

// placeMsg is one line of the end-of-match scoreboard. Name and User split the
// same way they do in rosterPlayer: Name is shown, User is what may be linked.
type placeMsg struct {
	Place int    `json:"place"`
	Snake int    `json:"snake"`
	Name  string `json:"name"`
	User  string `json:"user,omitempty"`

	userID int64 // not sent: the client has no use for a row id
}

type overMsg struct {
	Type   string     `json:"type"`
	Winner int        `json:"winner"` // -1 if nobody survived
	Name   string     `json:"name"`
	Places []placeMsg `json:"places"`
}

// encode marshals a message, returning nil if it cannot be sent.
func encode(msg any) []byte {
	b, err := json.Marshal(msg)
	if err != nil {
		log.Printf("encoding %T: %v", msg, err)
		return nil
	}
	return b
}
