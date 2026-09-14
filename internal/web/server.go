// Package web serves the EVA lobby pages and the room websocket endpoint.
package web

import (
	"cmp"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"dlhu.dev/eva/internal/lobby"
	"dlhu.dev/eva/internal/store"
)

// maxNameLen caps a player-supplied display name, in runes.
const maxNameLen = 16

//go:embed templates
var templateFS embed.FS

var tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// server holds the dependencies shared by the handlers.
type server struct {
	rooms   *lobby.Manager
	db      *store.DB
	players *sessions
	prefix  string
}

// New returns the site handler, served under prefix (for example "/eva"). It
// also points rooms at db, so finished matches are recorded.
func New(rooms *lobby.Manager, db *store.DB, prefix string) http.Handler {
	s := &server{rooms: rooms, db: db, players: newSessions(), prefix: strings.TrimSuffix(prefix, "/")}
	rooms.Record = s.record
	p := s.prefix
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+p+"/{$}", s.handleHome)
	mux.HandleFunc("GET "+p+"/create", s.handleForm)
	mux.HandleFunc("POST "+p+"/create", s.handleCreate)
	mux.HandleFunc("GET "+p+"/join", s.handleForm)
	mux.HandleFunc("POST "+p+"/join", s.handleJoin)
	mux.HandleFunc("GET "+p+"/room/{code}", s.handleRoom)
	mux.HandleFunc("GET "+p+"/ws/{code}", s.handleWS)
	mux.HandleFunc("GET "+p+"/signup", s.handleAuthForm)
	mux.HandleFunc("POST "+p+"/signup", s.handleSignUp)
	mux.HandleFunc("GET "+p+"/login", s.handleAuthForm)
	mux.HandleFunc("POST "+p+"/login", s.handleLogIn)
	mux.HandleFunc("POST "+p+"/logout", s.handleLogOut)
	mux.HandleFunc("GET "+p+"/u/{name}", s.handleHistory)
	if p != "" {
		// Anything else under the prefix goes home; registering the subtree
		// also makes ServeMux redirect a bare /eva to /eva/.
		mux.Handle("GET "+p+"/", http.RedirectHandler(p+"/", http.StatusFound))
	}
	return logRequests(mux)
}

// logRequests logs each request once it has been served. A websocket is logged
// when it closes, so its duration is how long the player stayed.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sr, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", cmp.Or(sr.status, http.StatusOK),
			"dur", time.Since(start),
			// Caddy overwrites X-Forwarded-For, and is the only thing that can
			// reach us in production; anywhere else the header is just a claim.
			"remote", cmp.Or(r.Header.Get("X-Forwarded-For"), r.RemoteAddr),
		)
	})
}

// statusRecorder remembers the status a handler sent.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.status == 0 {
		sr.status = code
	}
	sr.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController and the websocket upgrade reach the
// connection underneath to hijack it.
func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	p, _ := s.players.get(r)
	s.render(w, "index.html", map[string]any{"Prefix": s.prefix, "User": account(p)})
}

// account returns the name p's history is filed under, empty for a guest.
func account(p player) string {
	if p.guest() {
		return ""
	}
	return p.Name
}

// handleForm renders the create or join form, named by the path it is served
// at. The name comes from the session, so it is already filled in for anyone
// who has played before.
func (s *server) handleForm(w http.ResponseWriter, r *http.Request) {
	p, _ := s.players.get(r)
	s.render(w, "form.html", map[string]any{
		"Prefix": s.prefix,
		"Action": path.Base(r.URL.Path),
		"Name":   p.Name,
		"User":   account(p),
		"Code":   r.URL.Query().Get("code"),
		"Error":  r.URL.Query().Get("error"),
	})
}

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	host := s.identify(w, r)
	room := s.rooms.Create(host.SessionID)
	s.redirect(w, r, "/room/"+room.Code, nil)
}

func (s *server) handleJoin(w http.ResponseWriter, r *http.Request) {
	s.identify(w, r)
	code := normalizeCode(r.FormValue("code"))
	if _, ok := s.rooms.Get(code); !ok {
		s.redirect(w, r, "/join", url.Values{"error": {"no room with code " + code}})
		return
	}
	s.redirect(w, r, "/room/"+code, nil)
}

// identify records the name submitted with the form against the requester's
// session, leaving any account they are signed in to alone: a signed-in player
// plays under the name their history is filed under.
func (s *server) identify(w http.ResponseWriter, r *http.Request) player {
	p, _ := s.players.get(r)
	if p.guest() {
		p.Name = cleanName(r.FormValue("name"))
	}
	return s.players.set(w, r, s.prefix, p)
}

// record stores a finished match; a match nobody signed in for is not worth
// keeping.
func (s *server) record(code string, places []lobby.Placing) {
	signedIn := false
	for _, p := range places {
		signedIn = signedIn || p.UserID != 0
	}
	if !signedIn {
		return
	}
	rows := make([]store.Placing, len(places))
	for i, p := range places {
		rows[i] = store.Placing{Place: p.Place, UserID: p.UserID, Name: p.Name}
	}
	if err := s.db.RecordMatch(code, rows); err != nil {
		slog.Error("recording match", "room", code, "err", err)
	}
}

func (s *server) handleRoom(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	if _, ok := s.rooms.Get(code); !ok {
		s.redirect(w, r, "/join", url.Values{"error": {"no room with code " + code}})
		return
	}
	// Someone opening a shared link has no session yet: ask who they are,
	// with the code they followed already filled in.
	p, ok := s.players.get(r)
	if !ok {
		s.redirect(w, r, "/join", url.Values{"code": {code}})
		return
	}
	s.render(w, "room.html", map[string]any{
		"Prefix": s.prefix,
		"Code":   code,
		"Name":   p.Name,
		"User":   account(p),
	})
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	room, ok := s.rooms.Get(code)
	if !ok {
		http.Error(w, "no such room", http.StatusNotFound)
		return
	}
	p, ok := s.players.get(r)
	if !ok {
		http.Error(w, "who are you?", http.StatusUnauthorized)
		return
	}
	if err := room.Serve(w, r, lobby.Player{SessionID: p.SessionID, Name: p.Name, UserID: p.UserID}); err != nil {
		slog.Warn("room connection", "room", code, "err", err)
	}
}

func (s *server) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, page, data); err != nil {
		slog.Error("render", "page", page, "err", err)
	}
}

func (s *server) redirect(w http.ResponseWriter, r *http.Request, path string, q url.Values) {
	to := s.prefix + path
	if len(q) > 0 {
		to += "?" + q.Encode()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func normalizeCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// cleanName trims a player-supplied name to something printable and short.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(s))
	if n := []rune(s); len(n) > maxNameLen {
		s = string(n[:maxNameLen])
	}
	if s == "" {
		return "anon"
	}
	return s
}
