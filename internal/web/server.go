// Package web serves the EVA lobby pages and the room websocket endpoint.
package web

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"

	"dlhu.dev/eva/internal/lobby"
)

// maxNameLen caps a player-supplied display name, in runes.
const maxNameLen = 16

//go:embed templates
var templateFS embed.FS

var tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// server holds the dependencies shared by the handlers.
type server struct {
	rooms   *lobby.Manager
	players *sessions
	prefix  string
}

// New returns the site handler, served under prefix (for example "/eva").
func New(rooms *lobby.Manager, prefix string) http.Handler {
	s := &server{rooms: rooms, players: newSessions(), prefix: strings.TrimSuffix(prefix, "/")}
	p := s.prefix
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+p+"/{$}", s.handleHome)
	mux.HandleFunc("GET "+p+"/create", s.handleForm)
	mux.HandleFunc("POST "+p+"/create", s.handleCreate)
	mux.HandleFunc("GET "+p+"/join", s.handleForm)
	mux.HandleFunc("POST "+p+"/join", s.handleJoin)
	mux.HandleFunc("GET "+p+"/room/{code}", s.handleRoom)
	mux.HandleFunc("GET "+p+"/ws/{code}", s.handleWS)
	if p != "" {
		// Anything else under the prefix goes home; registering the subtree
		// also makes ServeMux redirect a bare /eva to /eva/.
		mux.Handle("GET "+p+"/", http.RedirectHandler(p+"/", http.StatusFound))
	}
	return mux
}

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, "index.html", map[string]string{"Prefix": s.prefix})
}

// handleForm renders the create or join form, named by the path it is served
// at. The name comes from the session, so it is already filled in for anyone
// who has played before.
func (s *server) handleForm(w http.ResponseWriter, r *http.Request) {
	p, _ := s.players.get(r)
	s.render(w, "form.html", map[string]string{
		"Prefix": s.prefix,
		"Action": path.Base(r.URL.Path),
		"Name":   p.Name,
		"Code":   r.URL.Query().Get("code"),
		"Error":  r.URL.Query().Get("error"),
	})
}

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	host := s.identify(w, r)
	room := s.rooms.Create(host.ID)
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
// session, leaving any account they are signed in to alone.
func (s *server) identify(w http.ResponseWriter, r *http.Request) player {
	p, _ := s.players.get(r)
	p.Name = cleanName(r.FormValue("name"))
	return s.players.set(w, r, s.prefix, p)
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
	s.render(w, "room.html", map[string]string{
		"Prefix": s.prefix,
		"Code":   code,
		"Name":   p.Name,
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
	if err := room.Serve(w, r, lobby.Player{ID: p.ID, Name: p.Name}); err != nil {
		log.Printf("room %s: %v", code, err)
	}
}

func (s *server) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, page, data); err != nil {
		log.Printf("render %s: %v", page, err)
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
