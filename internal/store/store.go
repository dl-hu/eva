// Package store keeps EVA's accounts and match history in a SQLite file.
package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Password hashing parameters. Raising iterations invalidates nothing: the
// count is stored per user, so old rows keep verifying at the old cost.
const (
	hashIter = 600_000
	hashLen  = 32
	saltLen  = 16
)

// Errors a caller is expected to show the player rather than log.
var (
	ErrNameTaken  = errors.New("that name is taken")
	ErrNoSuchUser = errors.New("no account with that name")
	ErrWrongPass  = errors.New("wrong password")
	ErrBadName    = errors.New("names are 1-16 printable characters")
	ErrShortPass  = errors.New("passwords are at least 8 characters")
	errUniqueName = "UNIQUE constraint failed: users.name"
)

// DB is the open database.
type DB struct{ sql *sql.DB }

// schema is applied on every open; each statement is idempotent.
const schema = `
CREATE TABLE IF NOT EXISTS users (
  id      INTEGER PRIMARY KEY,
  name    TEXT NOT NULL COLLATE NOCASE UNIQUE,
  salt    BLOB NOT NULL,
  hash    BLOB NOT NULL,
  iter    INTEGER NOT NULL,
  created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS matches (
  id     INTEGER PRIMARY KEY,
  code   TEXT NOT NULL,
  played INTEGER NOT NULL
);
-- One row per player in a match. user_id is NULL for a guest, whose name is
-- kept only so the scoreboard of that match still reads correctly.
CREATE TABLE IF NOT EXISTS placings (
  match_id INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
  place    INTEGER NOT NULL,
  user_id  INTEGER REFERENCES users(id),
  name     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS placings_by_user ON placings(user_id);
CREATE INDEX IF NOT EXISTS placings_by_match ON placings(match_id);
`

// Open opens the database at path, creating and migrating it as needed. Use
// ":memory:" for a throwaway one.
func Open(path string) (*DB, error) {
	// WAL lets the reads that render history run while a match is recorded.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating: %w", err)
	}
	return &DB{sql: db}, nil
}

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }

// User is an account.
type User struct {
	ID   int64
	Name string
}

// SignUp creates an account and returns it.
func (d *DB) SignUp(name, pass string) (User, error) {
	name = strings.TrimSpace(name)
	if n := []rune(name); len(n) == 0 || len(n) > 16 {
		return User{}, ErrBadName
	}
	if len(pass) < 8 {
		return User{}, ErrShortPass
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return User{}, err
	}
	hash, err := pbkdf2.Key(sha256.New, pass, salt, hashIter, hashLen)
	if err != nil {
		return User{}, err
	}
	res, err := d.sql.Exec(`INSERT INTO users (name, salt, hash, iter, created) VALUES (?, ?, ?, ?, ?)`,
		name, salt, hash, hashIter, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), errUniqueName) {
			return User{}, ErrNameTaken
		}
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Name: name}, nil
}

// LogIn checks a password and returns the account behind it.
func (d *DB) LogIn(name, pass string) (User, error) {
	var (
		u          User
		salt, hash []byte
		iter       int
	)
	err := d.sql.QueryRow(`SELECT id, name, salt, hash, iter FROM users WHERE name = ?`,
		strings.TrimSpace(name)).Scan(&u.ID, &u.Name, &salt, &hash, &iter)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSuchUser
	}
	if err != nil {
		return User{}, err
	}
	got, err := pbkdf2.Key(sha256.New, pass, salt, iter, len(hash))
	if err != nil {
		return User{}, err
	}
	if subtle.ConstantTimeCompare(got, hash) != 1 {
		return User{}, ErrWrongPass
	}
	return u, nil
}

// UserByName looks up an account by name.
func (d *DB) UserByName(name string) (User, error) {
	var u User
	err := d.sql.QueryRow(`SELECT id, name FROM users WHERE name = ?`, strings.TrimSpace(name)).
		Scan(&u.ID, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSuchUser
	}
	return u, err
}

// Placing is where one player finished. UserID is 0 for a guest.
type Placing struct {
	Place  int
	UserID int64
	Name   string
}

// RecordMatch stores one match's finish order. Places are 1-based; players
// knocked out in the same tick share a place.
func (d *DB) RecordMatch(code string, places []Placing) error {
	if len(places) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO matches (code, played) VALUES (?, ?)`, code, time.Now().Unix())
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, p := range places {
		var user any
		if p.UserID != 0 {
			user = p.UserID
		}
		if _, err := tx.Exec(`INSERT INTO placings (match_id, place, user_id, name) VALUES (?, ?, ?, ?)`,
			id, p.Place, user, p.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Match is one played game as the history pages show it.
type Match struct {
	Code   string
	Played time.Time
	Place  int // where the player whose history this is finished
	Of     int // how many played
	Places []Placing
}

// History returns a user's most recent matches, newest first.
func (d *DB) History(userID int64, limit int) ([]Match, error) {
	rows, err := d.sql.Query(`
		SELECT m.id, m.code, m.played, p.place,
		       (SELECT COUNT(*) FROM placings WHERE match_id = m.id)
		FROM matches m JOIN placings p ON p.match_id = m.id
		WHERE p.user_id = ?
		ORDER BY m.played DESC, m.id DESC
		LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		out []Match
		ids []int64
	)
	for rows.Next() {
		var (
			id     int64
			m      Match
			played int64
		)
		if err := rows.Scan(&id, &m.Code, &played, &m.Place, &m.Of); err != nil {
			return nil, err
		}
		m.Played = time.Unix(played, 0)
		out = append(out, m)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		if out[i].Places, err = d.placings(id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// placings returns one match's scoreboard in finishing order.
//
// ponytail: a query per match, so a history page costs limit+1 round trips.
// Fine against a local file; fold it into one IN (...) query if it ever isn't.
func (d *DB) placings(matchID int64) ([]Placing, error) {
	rows, err := d.sql.Query(`
		SELECT p.place, COALESCE(p.user_id, 0), COALESCE(u.name, p.name)
		FROM placings p LEFT JOIN users u ON u.id = p.user_id
		WHERE p.match_id = ? ORDER BY p.place, p.name`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Placing
	for rows.Next() {
		var p Placing
		if err := rows.Scan(&p.Place, &p.UserID, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
