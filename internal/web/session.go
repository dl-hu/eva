package web

import (
	"crypto/rand"
	"net/http"
	"sync"
	"time"
)

const (
	// cookieName holds an opaque session token. Nothing identifying travels in
	// the URL, so a room link can be shared without carrying a player with it.
	cookieName = "eva_session"
	// sessionTTL is how long the server remembers a player between visits.
	sessionTTL = 24 * time.Hour
)

// player is who the server takes a request to be coming from. UserID is zero
// for a guest; signing in fills it, and only those matches are worth recording.
// ID names the player to the game and outlives any rename, so a host who
// refreshes the page is still the host.
type player struct {
	ID     string
	Name   string
	UserID int64
	expiry time.Time
}

// guest reports whether p is playing without an account.
func (p player) guest() bool { return p.UserID == 0 }

// sessions maps opaque cookie tokens to the players holding them.
//
// ponytail: in memory, so a restart signs everyone out and expired tokens are
// only dropped when read. Move it behind the same DB as accounts and match
// history; the callers below do not change when it does.
type sessions struct {
	mu      sync.Mutex
	byToken map[string]player
}

func newSessions() *sessions {
	return &sessions{byToken: make(map[string]player)}
}

// get returns the player behind r's session cookie.
func (s *sessions) get(r *http.Request) (player, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return player{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byToken[c.Value]
	if !ok {
		return player{}, false
	}
	if time.Now().After(p.expiry) {
		delete(s.byToken, c.Value)
		return player{}, false
	}
	return p, true
}

// set stores p and hands the browser the token naming it, reusing the token
// the request arrived with so that renaming does not leak a session per visit.
// It returns the stored player, which carries the ID if one was just minted.
func (s *sessions) set(w http.ResponseWriter, r *http.Request, prefix string, p player) player {
	p.expiry = time.Now().Add(sessionTTL)
	if p.ID == "" {
		p.ID = rand.Text()
	}

	s.mu.Lock()
	token := ""
	// Only a token the server itself issued may be reused; taking one from the
	// request unchecked would let a client pin a session token of its choosing.
	if c, err := r.Cookie(cookieName); err == nil {
		if _, known := s.byToken[c.Value]; known {
			token = c.Value
		}
	}
	if token == "" {
		token = rand.Text()
	}
	s.byToken[token] = p
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     prefix + "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	return p
}
