// Package lobby manages game rooms and the players connected to them.
package lobby

import (
	"cmp"
	"crypto/rand"
	"log"
	"slices"
	"sync"
	"time"

	"dlhu.dev/eva/internal/game"
)

const (
	// defaultIdleTimeout is how long an empty room lives before it closes itself.
	defaultIdleTimeout = time.Minute
	// sendBuffer is how far a player may fall behind before being dropped.
	sendBuffer = 16
	// inputBuffer is how many unhandled keypresses a busy room holds.
	inputBuffer = 256
	// tickRate is how often a running game advances.
	tickRate = 40 * time.Millisecond
)

// Player is who a connection belongs to. The ID is stable across reconnects,
// so a host who refreshes the page is still the host.
type Player struct {
	ID   string
	Name string
}

// Manager owns every live room and hands them out by code.
type Manager struct {
	idleTimeout time.Duration

	mu    sync.Mutex
	rooms map[string]*Room
}

// NewManager returns a Manager with no rooms.
func NewManager() *Manager {
	return &Manager{idleTimeout: defaultIdleTimeout, rooms: make(map[string]*Room)}
}

// Create starts a room under a fresh code, hosted by the player named by hostID.
func (m *Manager) Create(hostID string) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	var code string
	for code = newCode(); m.rooms[code] != nil; code = newCode() {
	}
	r := &Room{
		Code:    code,
		hostID:  hostID,
		mgr:     m,
		join:    make(chan *client),
		leave:   make(chan *client),
		input:   make(chan input, inputBuffer),
		done:    make(chan struct{}),
		clients: make(map[*client]bool),
	}
	m.rooms[code] = r
	go r.run()
	log.Printf("room %s created", code)
	return r
}

// Get looks up a room by code.
func (m *Manager) Get(code string) (*Room, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[code]
	return r, ok
}

func (m *Manager) remove(code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rooms, code)
}

// newCode returns a short, unguessable room code.
func newCode() string {
	return rand.Text()[:4]
}

// Room is one game lobby: a set of connected players and, once the host says
// so, a game they are playing.
//
// Everything below hostID is owned by run. No other goroutine may touch those
// fields; they talk to run over the channels instead.
type Room struct {
	Code   string
	hostID string

	mgr   *Manager
	join  chan *client
	leave chan *client
	input chan input
	done  chan struct{}

	clients map[*client]bool
	state   *game.State
	playing []*client // snake number to the player driving it
	ticker  *time.Ticker
}

// client is one connected player as the room sees it.
type client struct {
	id    string
	name  string
	send  chan []byte
	snake int // its snake in the running game, -1 when not playing
}

// input is something a player sent.
type input struct {
	c   *client
	msg clientMsg
}

// add registers c with the room, reporting false if the room has closed.
func (r *Room) add(c *client) bool {
	select {
	case r.join <- c:
		return true
	case <-r.done:
		return false
	}
}

// remove unregisters c. It is safe to call after the room has closed.
func (r *Room) remove(c *client) {
	select {
	case r.leave <- c:
	case <-r.done:
	}
}

// send hands a message to the room, dropping it if the room is too busy to
// listen. A player whose turn is dropped can press again.
func (r *Room) send(in input) {
	select {
	case r.input <- in:
	default:
	}
}

// run is the room's single owner goroutine.
func (r *Room) run() {
	defer func() {
		close(r.done)
		r.mgr.remove(r.Code)
		if r.ticker != nil {
			r.ticker.Stop()
		}
		for c := range r.clients {
			close(c.send)
		}
		log.Printf("room %s closed", r.Code)
	}()

	idle := time.NewTimer(r.mgr.idleTimeout)
	defer idle.Stop()
	for {
		// A stopped game has no ticker, and a nil channel never fires.
		var tick <-chan time.Time
		if r.ticker != nil {
			tick = r.ticker.C
		}

		select {
		case c := <-r.join:
			r.clients[c] = true
			r.unicast(c, encode(youMsg{Type: "you", Host: c.id == r.hostID, Snake: -1}))
			r.broadcastRoster()
		case c := <-r.leave:
			if !r.clients[c] {
				continue // already dropped for being slow
			}
			delete(r.clients, c)
			close(c.send)
			r.quit(c)
			r.broadcastRoster()
		case in := <-r.input:
			r.handle(in)
			continue // a keypress changes nothing about who is in the room
		case <-tick:
			r.step()
			continue
		case <-idle.C:
			return
		}

		if len(r.clients) == 0 {
			idle.Reset(r.mgr.idleTimeout)
		} else {
			idle.Stop()
		}
	}
}

// handle acts on something a player sent.
func (r *Room) handle(in input) {
	switch in.msg.Type {
	case "dir":
		if r.state == nil {
			return
		}
		if d, ok := dirs[in.msg.Dir]; ok {
			r.state.Turn(in.c.snake, d)
		}
	case "start":
		r.start(in.c, in.msg.W, in.msg.H)
	}
}

// start begins a game at the host's request, on a board of the size they
// asked for. Only the host may start one, and only when none is running.
func (r *Room) start(c *client, w, h int) {
	if c.id != r.hostID || r.state != nil || len(r.clients) == 0 {
		return
	}
	w, h = clampSize(w), clampSize(h)

	r.playing = r.playing[:0]
	for c := range r.clients {
		r.playing = append(r.playing, c)
	}
	// Map order is random; seat players by name so snake numbers are stable.
	slices.SortFunc(r.playing, func(a, b *client) int { return cmp.Compare(a.name, b.name) })

	r.state = game.New(w, h, len(r.playing))
	msg := startMsg{Type: "start", W: w, H: h, Snakes: make([]startSnake, len(r.playing))}
	for i, c := range r.playing {
		c.snake = i
		sn := r.state.Snakes[i]
		msg.Snakes[i] = startSnake{Name: c.name, X: sn.Head.X, Y: sn.Head.Y, Dir: int(sn.Dir), Alive: sn.Alive}
		r.unicast(c, encode(youMsg{Type: "you", Host: c.id == r.hostID, Snake: i}))
	}
	r.broadcast(encode(msg))
	r.ticker = time.NewTicker(tickRate)
	log.Printf("room %s started a %dx%d game with %d players", r.Code, w, h, len(r.playing))
}

// step advances the game one tick and tells everyone what moved.
func (r *Room) step() {
	moved, died := r.state.Step()
	msg := tickMsg{Type: "tick", Moves: make([][4]int, len(moved)), Dead: died}
	for i, m := range moved {
		msg.Moves[i] = [4]int{m.Snake, m.Head.X, m.Head.Y, int(m.Dir)}
	}
	r.broadcast(encode(msg))

	// A solo game runs until the player crashes; otherwise the last one left wins.
	if alive := r.state.Alive(); alive == 0 || (len(r.playing) > 1 && alive <= 1) {
		r.over()
	}
}

// over ends the game and puts the room back in its lobby.
func (r *Room) over() {
	msg := overMsg{Type: "over", Winner: -1}
	for i, sn := range r.state.Snakes {
		if sn.Alive {
			msg.Winner, msg.Name = i, r.playing[i].name
		}
	}
	r.ticker.Stop()
	r.ticker = nil
	r.state = nil
	for _, c := range r.playing {
		c.snake = -1
	}
	// ponytail: the match record gets written here, on its own goroutine with
	// a context detached from the room, once there is a database to write to.
	r.broadcast(encode(msg))
	r.broadcastRoster()
}

// quit takes a departing player out of the game they were in.
func (r *Room) quit(c *client) {
	if r.state == nil || c.snake < 0 {
		return
	}
	r.state.Kill(c.snake)
	r.broadcast(encode(tickMsg{Type: "tick", Moves: [][4]int{}, Dead: []int{c.snake}}))
	c.snake = -1
	if alive := r.state.Alive(); alive == 0 || (len(r.playing) > 1 && alive <= 1) {
		r.over()
	}
}

// clampSize holds a host-chosen board dimension to something playable.
func clampSize(n int) int {
	return min(max(n, game.MinSize), game.MaxSize)
}

// unicast sends a message to one player, dropping them if they cannot keep up.
func (r *Room) unicast(c *client, msg []byte) {
	if msg == nil {
		return
	}
	select {
	case c.send <- msg:
	default:
		delete(r.clients, c)
		close(c.send)
	}
}

// broadcast sends msg to every player, dropping any that cannot keep up.
func (r *Room) broadcast(msg []byte) {
	if msg == nil {
		return
	}
	for c := range r.clients {
		select {
		case c.send <- msg:
		default:
			delete(r.clients, c)
			close(c.send)
		}
	}
}

// broadcastRoster tells everyone who is in the room.
//
// ponytail: a full roster per join is O(n) per event, fine for a lobby of
// tens. Fold it into the tick once rooms hold thousands.
func (r *Room) broadcastRoster() {
	names := make([]string, 0, len(r.clients))
	for c := range r.clients {
		names = append(names, c.name)
	}
	slices.Sort(names)
	r.broadcast(encode(rosterMsg{Type: "players", Players: names}))
}
