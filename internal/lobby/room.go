// Package lobby manages game rooms and the players connected to them.
package lobby

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"slices"
	"sync"
	"time"
)

const (
	// defaultIdleTimeout is how long an empty room lives before it closes itself.
	defaultIdleTimeout = time.Minute
	// sendBuffer is how far a player may fall behind before being dropped.
	sendBuffer = 16
)

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

// Create starts a room under a fresh code.
func (m *Manager) Create() *Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	var code string
	for code = newCode(); m.rooms[code] != nil; code = newCode() {
	}
	r := &Room{
		Code:  code,
		mgr:   m,
		join:  make(chan *client),
		leave: make(chan *client),
		done:  make(chan struct{}),
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

// Room is one game lobby. Its player set is owned by run; everything else
// talks to it over channels.
type Room struct {
	Code string

	mgr   *Manager
	join  chan *client
	leave chan *client
	done  chan struct{}
}

// client is one connected player as the room sees it.
type client struct {
	name string
	send chan []byte
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

// run is the room's single owner goroutine.
func (r *Room) run() {
	clients := make(map[*client]bool)
	defer func() {
		close(r.done)
		r.mgr.remove(r.Code)
		for c := range clients {
			close(c.send)
		}
		log.Printf("room %s closed", r.Code)
	}()

	idle := time.NewTimer(r.mgr.idleTimeout)
	defer idle.Stop()
	for {
		select {
		case c := <-r.join:
			clients[c] = true
		case c := <-r.leave:
			if !clients[c] {
				continue // already dropped for being slow
			}
			delete(clients, c)
			close(c.send)
		case <-idle.C:
			return
		}
		// ponytail: a full roster per join is O(n) per event, fine for a lobby
		// of tens. Batch into the game tick once rooms hold thousands.
		broadcast(clients, roster(clients))
		if len(clients) == 0 {
			idle.Reset(r.mgr.idleTimeout)
		} else {
			idle.Stop()
		}
	}
}

// broadcast sends msg to every client, dropping any that cannot keep up.
func broadcast(clients map[*client]bool, msg []byte) {
	for c := range clients {
		select {
		case c.send <- msg:
		default:
			delete(clients, c)
			close(c.send)
		}
	}
}

// roster is the players message describing who is currently in the room.
func roster(clients map[*client]bool) []byte {
	names := make([]string, 0, len(clients))
	for c := range clients {
		names = append(names, c.name)
	}
	slices.Sort(names)
	msg, err := json.Marshal(struct {
		Type    string   `json:"type"`
		Players []string `json:"players"`
	}{"players", names})
	if err != nil {
		return []byte(`{"type":"players","players":[]}`)
	}
	return msg
}
