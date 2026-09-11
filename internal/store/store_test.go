package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSignUpThenLogIn(t *testing.T) {
	t.Parallel()
	db := open(t)

	u, err := db.SignUp("ada", "correct horse")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	back, err := db.LogIn("ada", "correct horse")
	if err != nil {
		t.Fatalf("LogIn: %v", err)
	}
	if back.ID != u.ID {
		t.Errorf("logged in as %d, want the account just made (%d)", back.ID, u.ID)
	}
	if _, err := db.LogIn("ada", "correct horse "); !errors.Is(err, ErrWrongPass) {
		t.Errorf("LogIn with a trailing space = %v, want %v", err, ErrWrongPass)
	}
	if _, err := db.LogIn("eve", "whatever!"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("LogIn as a stranger = %v, want %v", err, ErrNoSuchUser)
	}
}

func TestSignUpRejectsWhatItShould(t *testing.T) {
	t.Parallel()
	db := open(t)
	if _, err := db.SignUp("ada", "correct horse"); err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	// Names are compared without case, so "ADA" is the same account.
	if _, err := db.SignUp("ADA", "another one"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("SignUp as ADA = %v, want %v", err, ErrNameTaken)
	}
	if _, err := db.SignUp("bob", "short"); !errors.Is(err, ErrShortPass) {
		t.Errorf("SignUp with a short password = %v, want %v", err, ErrShortPass)
	}
	if _, err := db.SignUp("  ", "long enough"); !errors.Is(err, ErrBadName) {
		t.Errorf("SignUp with a blank name = %v, want %v", err, ErrBadName)
	}
}

func TestHistoryKeepsFinishingOrderAndGuests(t *testing.T) {
	t.Parallel()
	db := open(t)
	ada, err := db.SignUp("ada", "correct horse")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	bob, err := db.SignUp("bob", "battery staple")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}

	// bob wins; ada and a guest went out together, so they share second.
	places := []Placing{
		{Place: 1, UserID: bob.ID, Name: "bob"},
		{Place: 2, UserID: ada.ID, Name: "ada"},
		{Place: 2, Name: "anon"},
	}
	if err := db.RecordMatch("AB12", places); err != nil {
		t.Fatalf("RecordMatch: %v", err)
	}

	got, err := db.History(ada.ID, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("History returned %d matches, want 1", len(got))
	}
	m := got[0]
	if m.Code != "AB12" || m.Place != 2 || m.Of != 3 {
		t.Errorf("match = %s, placed %d of %d, want AB12, 2 of 3", m.Code, m.Place, m.Of)
	}
	if len(m.Places) != 3 {
		t.Fatalf("scoreboard has %d rows, want 3", len(m.Places))
	}
	if m.Places[0].Name != "bob" || m.Places[0].UserID != bob.ID {
		t.Errorf("first place = %+v, want bob's account", m.Places[0])
	}
	// The guest is kept by name with no account, which is what greys them out.
	guest := m.Places[2]
	if guest.Name != "anon" || guest.UserID != 0 {
		t.Errorf("guest row = %+v, want \"anon\" with no account", guest)
	}
	// A match nobody else played does not show up in a stranger's history.
	if other, err := db.History(bob.ID, 10); err != nil || len(other) != 1 {
		t.Errorf("bob's history = %d matches (%v), want 1", len(other), err)
	}
}
