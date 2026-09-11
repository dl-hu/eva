package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"dlhu.dev/eva/internal/lobby"
)

// signUp registers an account through the form a player would use.
func signUp(t *testing.T, s *site, c *http.Client, name, pass string) {
	t.Helper()
	resp := postForm(t, c, s.base+"/signup", url.Values{"name": {name}, "password": {pass}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signing up as %s = %d, want %d", name, resp.StatusCode, http.StatusOK)
	}
}

// TestSignUpLandsOnYourOwnHistory walks the flow: register, get sent to your
// page, log out, and find the page still readable by anyone.
func TestSignUpLandsOnYourOwnHistory(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "/eva")
	ada := s.browser(t)
	signUp(t, s, ada, "ada", "correct horse")

	page := bodyOf(t, get(t, ada, s.base+"/u/ada"))
	if !strings.Contains(page, "no matches yet") {
		t.Errorf("a fresh account's history does not say it is empty:\n%s", page)
	}
	if !strings.Contains(page, "/eva/logout") {
		t.Errorf("a signed-in player is not offered a way out:\n%s", page)
	}

	// Signing out leaves the history in place and public.
	if resp := postForm(t, ada, s.base+"/logout", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("logging out = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	page = bodyOf(t, get(t, ada, s.base+"/u/ada"))
	if !strings.Contains(page, "/eva/login") {
		t.Errorf("a signed-out visitor is not offered a way in:\n%s", page)
	}
}

func TestSignUpRejectionComesBackToTheForm(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "")
	ada := s.browser(t)
	signUp(t, s, ada, "ada", "correct horse")

	// A second account under the same name, from a browser of its own.
	eve := s.browser(t)
	page := bodyOf(t, postForm(t, eve, s.base+"/signup",
		url.Values{"name": {"ada"}, "password": {"another one"}}))
	if !strings.Contains(page, "taken") {
		t.Errorf("signing up as a taken name does not say so:\n%s", page)
	}
	page = bodyOf(t, postForm(t, eve, s.base+"/login",
		url.Values{"name": {"ada"}, "password": {"wrong wrong"}}))
	if !strings.Contains(page, "wrong password") {
		t.Errorf("a bad password does not say so:\n%s", page)
	}
}

// TestHistoryColoursNamesByWhoTheyAre is the point of the whole feature: the
// viewer blue, other account holders white and linked, guests grey and inert.
func TestHistoryColoursNamesByWhoTheyAre(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "")
	ada := s.browser(t)
	signUp(t, s, ada, "ada", "correct horse")
	bob, err := s.db.SignUp("bob", "battery staple")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	adaUser, err := s.db.UserByName("ada")
	if err != nil {
		t.Fatalf("UserByName: %v", err)
	}

	// Record a match the way a finished room does.
	s.rooms.Record("AB12", []lobby.Placing{
		{Place: 1, UserID: adaUser.ID, Name: "ada"},
		{Place: 2, UserID: bob.ID, Name: "bob"},
		{Place: 3, Name: "anon"},
	})
	waitForMatch(t, s, adaUser.ID)

	page := bodyOf(t, get(t, ada, s.base+"/u/ada"))
	for _, want := range []string{
		`class="player me"`,         // ada, viewing her own history
		`class="player user"`,       // bob, an account holder
		`class="player guest">anon`, // the guest, with no link
		`href="/u/bob"`,             // and bob's name goes to his history
	} {
		if !strings.Contains(page, want) {
			t.Errorf("history page is missing %s:\n%s", want, page)
		}
	}
	if strings.Contains(page, `href="/u/anon"`) {
		t.Error("the guest's name links somewhere, want it inert")
	}
}

// TestGuestOnlyMatchesAreNotRecorded keeps the table to matches someone can
// actually look back at.
func TestGuestOnlyMatchesAreNotRecorded(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "")
	ada, err := s.db.SignUp("ada", "correct horse")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	s.rooms.Record("GUES", []lobby.Placing{{Place: 1, Name: "anon"}, {Place: 2, Name: "anon"}})
	s.rooms.Record("REAL", []lobby.Placing{{Place: 1, UserID: ada.ID, Name: "ada"}})
	waitForMatch(t, s, ada.ID)

	got, err := s.db.History(ada.ID, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 || got[0].Code != "REAL" {
		t.Errorf("history = %+v, want only the match ada played in", got)
	}
}

// waitForMatch blocks until the recorder's write has landed. Recording runs
// off the room's goroutine, so a test that reads straight after may race it.
func waitForMatch(t *testing.T, s *site, userID int64) {
	t.Helper()
	for range 100 {
		if got, err := s.db.History(userID, 1); err == nil && len(got) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the match was never recorded")
}
