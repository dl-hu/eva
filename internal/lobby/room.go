// Package lobby manages game rooms and the players connected to them.
package lobby

import (
	"cmp"
	"crypto/rand"
	"log/slog"
	"runtime/debug"
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

// Player is who a connection belongs to.
type Player struct {
	// SessionID identifies the browser session, guest or not. It is stable
	// across reconnects, so a host who refreshes the page is still the host,
	// and it dies with the session. It is not the session cookie, and nothing
	// sends it to a client.
	SessionID string

	// Name is what the room shows everyone. A guest picks it, so on its own it
	// proves nothing: a guest may well type the name of somebody's account.
	Name string

	// UserID is the account that proved it owns Name, and zero for a guest.
	// Being the only field that outlives the session, it is what a match is
	// filed under — and being proof rather than a claim, it is what allows Name
	// to be shown as a link to that history. See account.
	UserID int64
}

// Placing is where one player finished a match: place 1 is the last one
// standing. Players knocked out on the same tick share a place.
type Placing struct {
	Place  int
	UserID int64 // zero for a guest
	Name   string
}

// Manager owns every live room and hands them out by code.
type Manager struct {
	// Record, if set, is handed each finished match's scoreboard. It runs on a
	// goroutine of its own, which Close waits for.
	Record func(code string, places []Placing)

	idleTimeout time.Duration

	// closing tells every room to shut down, and wg counts what Close waits
	// for: room goroutines, player connections, and Record calls.
	closing chan struct{}
	wg      sync.WaitGroup

	mu    sync.Mutex
	rooms map[string]*Room
}

// NewManager returns a Manager with no rooms.
func NewManager() *Manager {
	return &Manager{idleTimeout: defaultIdleTimeout, rooms: make(map[string]*Room), closing: make(chan struct{})}
}

// Create starts a room under a fresh code, hosted by the session named by hostID.
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
	m.wg.Go(r.run)
	slog.Info("room created", "room", code)
	return r
}

// Close shuts every room, which disconnects its players, and waits until they
// are gone and every finished match has been handed to Record. Call it once no
// new requests can arrive, such as after http.Server.Shutdown.
func (m *Manager) Close() {
	close(m.closing)
	m.wg.Wait()
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
	hostID string // the session that may start games here

	mgr   *Manager
	join  chan *client
	leave chan *client
	input chan input
	done  chan struct{}

	clients map[*client]bool
	seats   int // seats handed out, so each connection has its own number
	state   *game.State
	playing []*client // snake number to the player driving it
	ticker  *time.Ticker
	// knockouts groups snake numbers by the tick they went out on, oldest
	// first. Reversed, it is the finishing order behind the scoreboard.
	knockouts [][]int
}

// client is one connected player as the room sees it.
type client struct {
	sessionID string
	name      string
	userID    int64
	send      chan []byte
	seat      int // its place in the roster, for as long as it is connected
	snake     int // its snake in the running game, -1 when not playing
}

// account is the name c's match history is filed under, and empty for a guest.
// It is c.name only once the session has proved the account is theirs, which is
// what keeps a guest who types somebody else's name from being shown as them.
func (c *client) account() string {
	if c.userID == 0 {
		return ""
	}
	return c.name
}

// roster is how c appears in the lobby list.
func (c *client) roster() rosterPlayer {
	return rosterPlayer{Seat: c.seat, Name: c.name, User: c.account()}
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
		// A bug in one room closes that room, not every room on the server.
		if v := recover(); v != nil {
			slog.Error("room crashed", "room", r.Code, "panic", v, "stack", string(debug.Stack()))
		}
		close(r.done)
		r.mgr.remove(r.Code)
		if r.ticker != nil {
			r.ticker.Stop()
		}
		for c := range r.clients {
			close(c.send)
		}
		slog.Info("room closed", "room", r.Code)
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
			c.seat = r.seats
			r.seats++
			r.unicast(c, encode(youMsg{Type: "you", Host: c.sessionID == r.hostID, Snake: -1, Seat: c.seat}))
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
		case <-r.mgr.closing:
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
	if c.sessionID != r.hostID || r.state != nil || len(r.clients) == 0 {
		return
	}
	w, h = clampSize(w), clampSize(h)

	r.playing = r.playing[:0]
	r.knockouts = nil
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
		r.unicast(c, encode(youMsg{Type: "you", Host: c.sessionID == r.hostID, Snake: i, Seat: c.seat}))
	}
	r.broadcast(encode(msg))
	r.ticker = time.NewTicker(tickRate)
	slog.Info("game started", "room", r.Code, "width", w, "height", h, "players", len(r.playing))
}

// step advances the game one tick and tells everyone what moved.
func (r *Room) step() {
	moved, died := r.state.Step()
	if len(died) > 0 {
		r.knockouts = append(r.knockouts, died)
	}
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
	places := r.scoreboard()
	msg := overMsg{Type: "over", Winner: -1, Places: places}
	for i, sn := range r.state.Snakes {
		if sn.Alive {
			msg.Winner, msg.Name = i, r.playing[i].name
		}
	}
	if record := r.mgr.Record; record != nil {
		code, placings := r.Code, toPlacings(places)
		r.mgr.wg.Go(func() { record(code, placings) })
	}
	r.ticker.Stop()
	r.ticker = nil
	r.state = nil
	r.knockouts = nil
	for _, c := range r.playing {
		c.snake = -1
	}
	r.broadcast(encode(msg))
	r.broadcastRoster()
}

// scoreboard ranks everyone who played, best first. The last snakes standing
// take 1st; the rest place in reverse order of being knocked out, and anyone
// who went out on the same tick shares a place.
func (r *Room) scoreboard() []placeMsg {
	// Survivors first, then each knockout tick from latest to earliest.
	var survivors []int
	for i, sn := range r.state.Snakes {
		if sn.Alive {
			survivors = append(survivors, i)
		}
	}
	groups := make([][]int, 0, len(r.knockouts)+1)
	if len(survivors) > 0 {
		groups = append(groups, survivors)
	}
	for i := len(r.knockouts) - 1; i >= 0; i-- {
		groups = append(groups, r.knockouts[i])
	}

	out := make([]placeMsg, 0, len(r.playing))
	place := 1
	for _, g := range groups {
		for _, snake := range g {
			if snake < 0 || snake >= len(r.playing) {
				continue // a snake nobody is driving; should not happen
			}
			c := r.playing[snake]
			out = append(out, placeMsg{Place: place, Snake: snake,
				Name: c.name, User: c.account(), userID: c.userID})
		}
		place += len(g) // ties all took this place; the next one skips past them
	}
	return out
}

// toPlacings converts a scoreboard to what the match recorder stores.
func toPlacings(places []placeMsg) []Placing {
	out := make([]Placing, len(places))
	for i, p := range places {
		out[i] = Placing{Place: p.Place, UserID: p.userID, Name: p.Name}
	}
	return out
}

// quit takes a departing player out of the game they were in.
func (r *Room) quit(c *client) {
	if r.state == nil || c.snake < 0 {
		return
	}
	r.state.Kill(c.snake)
	// Walking out places you exactly as if you had crashed just now.
	r.knockouts = append(r.knockouts, []int{c.snake})
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
	players := make([]rosterPlayer, 0, len(r.clients))
	for c := range r.clients {
		players = append(players, c.roster())
	}
	// Map order is random; list by name, and by seat between namesakes, so the
	// roster does not reshuffle itself under the reader on every update.
	slices.SortFunc(players, func(a, b rosterPlayer) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Seat, b.Seat))
	})
	r.broadcast(encode(rosterMsg{Type: "players", Players: players}))
}
