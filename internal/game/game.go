// Package game implements the rules of EVA: snakes that leave a permanent
// wall wherever their head has been, on a board that only ever fills up.
package game

import (
	"math/rand/v2"
	"slices"
)

// Board sizes the host may ask for.
const (
	MinSize = 50
	MaxSize = 1000
)

// Dir is a heading on the board.
type Dir uint8

// The headings, in the order the wire protocol numbers them.
const (
	Up Dir = iota
	Down
	Left
	Right
)

// steps is the cell offset of each heading. Y grows downward, as on a canvas.
var steps = [4]Point{Up: {0, -1}, Down: {0, 1}, Left: {-1, 0}, Right: {1, 0}}

// reverses reports whether d faces back the way e came.
func (d Dir) reverses(e Dir) bool {
	return d == Up && e == Down || d == Down && e == Up ||
		d == Left && e == Right || d == Right && e == Left
}

// Point is a cell on the board.
type Point struct {
	X, Y int
}

func (p Point) step(d Dir) Point {
	return Point{p.X + steps[d].X, p.Y + steps[d].Y}
}

// Snake is one player's head. The body is not tracked: everywhere the head has
// been is wall, and wall belongs to the board.
type Snake struct {
	Head  Point
	Dir   Dir
	Alive bool

	next Dir // heading to take at the next step
}

// Move is a snake advancing into a cell it now owns forever.
type Move struct {
	Snake int
	Head  Point
	Dir   Dir
}

// State is a game in progress. Snakes are addressed by their index.
type State struct {
	W, H   int
	Snakes []Snake

	cells []uint16 // snake index + 1, or 0 for an empty cell
}

// New lays out n snakes, spread apart, on an empty w×h board.
func New(w, h, n int) *State {
	s := &State{W: w, H: h, Snakes: make([]Snake, n), cells: make([]uint16, w*h)}
	for i := range s.Snakes {
		s.Snakes[i] = s.spawn(i)
	}
	return s
}

// spawn finds room for snake i, away from the edges and from anyone already
// placed. A board too crowded to hold it leaves the snake sitting the game out.
func (s *State) spawn(i int) Snake {
	for try := 0; try < 100; try++ {
		p := Point{1 + rand.IntN(s.W-2), 1 + rand.IntN(s.H-2)}
		if !s.clear(p) {
			continue
		}
		for d := range 4 {
			if !s.clear(p.step(Dir(d))) {
				p = Point{-1, -1} // too close to another snake
				break
			}
		}
		if p.X < 0 {
			continue
		}
		s.fill(p, i)
		d := Dir(rand.IntN(4))
		return Snake{Head: p, Dir: d, next: d, Alive: true}
	}
	return Snake{}
}

// clear reports whether p is on the board and nothing has been there yet.
func (s *State) clear(p Point) bool {
	return p.X >= 0 && p.X < s.W && p.Y >= 0 && p.Y < s.H && s.cells[p.Y*s.W+p.X] == 0
}

func (s *State) fill(p Point, i int) {
	s.cells[p.Y*s.W+p.X] = uint16(i) + 1
}

// Turn points snake i in direction d from its next step onward. Turning back
// into the wall it just laid would be suicide, so that turn is ignored.
func (s *State) Turn(i int, d Dir) {
	if i < 0 || i >= len(s.Snakes) || d > Right {
		return
	}
	sn := &s.Snakes[i]
	if sn.Alive && !d.reverses(sn.Dir) {
		sn.next = d
	}
}

// Kill takes snake i out of play. The wall it laid stays.
func (s *State) Kill(i int) {
	if i >= 0 && i < len(s.Snakes) {
		s.Snakes[i].Alive = false
	}
}

// Alive counts the snakes still in play.
func (s *State) Alive() int {
	n := 0
	for _, sn := range s.Snakes {
		if sn.Alive {
			n++
		}
	}
	return n
}

// Step advances every living snake one cell, reporting the cells they filled
// and the snakes that did not survive the move.
//
// ponytail: a map per tick. If it shows up in a profile, keep a scratch grid
// of intended moves on State and reuse it.
func (s *State) Step() (moved []Move, died []int) {
	targets := make(map[Point][]int)
	for i := range s.Snakes {
		sn := &s.Snakes[i]
		if !sn.Alive {
			continue
		}
		sn.Dir = sn.next
		next := sn.Head.step(sn.Dir)
		if !s.clear(next) { // the edge of the board, or a wall someone laid
			sn.Alive = false
			died = append(died, i)
			continue
		}
		targets[next] = append(targets[next], i)
	}

	for p, ids := range targets {
		if len(ids) > 1 { // two heads, one cell: nobody comes out
			for _, i := range ids {
				s.Snakes[i].Alive = false
				died = append(died, i)
			}
			continue
		}
		i := ids[0]
		s.fill(p, i)
		s.Snakes[i].Head = p
		moved = append(moved, Move{Snake: i, Head: p, Dir: s.Snakes[i].Dir})
	}

	// Map iteration is unordered; report in snake order so clients and tests
	// see the same tick every time.
	slices.SortFunc(moved, func(a, b Move) int { return a.Snake - b.Snake })
	slices.Sort(died)
	return moved, died
}
