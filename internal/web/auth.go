package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"

	"dlhu.dev/eva/internal/store"
)

// historyLimit is how many past matches a profile page shows.
const historyLimit = 25

// handleAuthForm renders the log-in or sign-up form, named by its path.
func (s *server) handleAuthForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "auth.html", map[string]any{
		"Prefix": s.prefix,
		"Action": path.Base(r.URL.Path),
		"Error":  r.URL.Query().Get("error"),
	})
}

func (s *server) handleSignUp(w http.ResponseWriter, r *http.Request) {
	u, err := s.db.SignUp(r.FormValue("name"), r.FormValue("password"))
	if err != nil {
		s.authFailed(w, r, "/signup", err)
		return
	}
	s.signIn(w, r, u)
}

func (s *server) handleLogIn(w http.ResponseWriter, r *http.Request) {
	u, err := s.db.LogIn(r.FormValue("name"), r.FormValue("password"))
	if err != nil {
		s.authFailed(w, r, "/login", err)
		return
	}
	s.signIn(w, r, u)
}

// signIn attaches the account to the caller's session and sends them to their
// own history, which is where the name they just proved they own shows up.
func (s *server) signIn(w http.ResponseWriter, r *http.Request, u store.User) {
	p, _ := s.players.get(r)
	p.UserID, p.Name = u.ID, u.Name
	s.players.set(w, r, s.prefix, p)
	s.redirect(w, r, "/u/"+url.PathEscape(u.Name), nil)
}

// handleLogOut drops the account from the session, leaving the player a guest
// under the same display name rather than signing them out of the room.
func (s *server) handleLogOut(w http.ResponseWriter, r *http.Request) {
	p, _ := s.players.get(r)
	p.UserID = 0
	s.players.set(w, r, s.prefix, p)
	s.redirect(w, r, "/", nil)
}

// authFailed sends the player back to the form. Only the store's own errors
// are worth showing them; anything else is ours to log.
func (s *server) authFailed(w http.ResponseWriter, r *http.Request, form string, err error) {
	msg := "something went wrong, try again"
	for _, known := range []error{store.ErrNameTaken, store.ErrNoSuchUser,
		store.ErrWrongPass, store.ErrBadName, store.ErrShortPass} {
		if errors.Is(err, known) {
			msg = known.Error()
		}
	}
	if msg == "something went wrong, try again" {
		slog.Error("auth", "form", form, "err", err)
	}
	s.redirect(w, r, form, url.Values{"error": {msg}})
}

// handleHistory shows one account's past matches. It is public: the whole
// point is that a name on a scoreboard links here.
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	u, err := s.db.UserByName(r.PathValue("name"))
	if err != nil {
		if errors.Is(err, store.ErrNoSuchUser) {
			http.NotFound(w, r)
			return
		}
		slog.Error("history lookup", "name", r.PathValue("name"), "err", err)
		http.Error(w, "cannot read that history", http.StatusInternalServerError)
		return
	}
	matches, err := s.db.History(u.ID, historyLimit)
	if err != nil {
		slog.Error("history", "user", u.ID, "err", err)
		http.Error(w, "cannot read that history", http.StatusInternalServerError)
		return
	}
	me, _ := s.players.get(r)
	s.render(w, "history.html", map[string]any{
		"Prefix":  s.prefix,
		"Profile": u, // whose history this is
		"Matches": matches,
		"User":    account(me), // who is reading it, for the nav
		"Me":      me.UserID,   // so the reader's own name shows up blue here too
	})
}
