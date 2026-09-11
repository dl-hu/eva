package game

import (
	"slices"
	"testing"
)

// board returns an empty board big enough to manoeuvre on.
func board(w, h int) *State {
	return &State{W: w, H: h, cells: make([]uint16, w*h)}
}

// place puts a snake at p facing d and returns its index.
func place(s *State, p Point, d Dir) int {
	s.Snakes = append(s.Snakes, Snake{Head: p, Dir: d, next: d, Alive: true})
	i := len(s.Snakes) - 1
	s.fill(p, i)
	return i
}

func TestSnakeDiesOnItsOwnWall(t *testing.T) {
	t.Parallel()
	s := board(20, 20)
	i := place(s, Point{10, 10}, Right)

	// Around a square, back onto the cell it started from.
	for _, d := range []Dir{Right, Down, Down, Left, Left, Up, Up} {
		s.Turn(i, d)
		if _, died := s.Step(); len(died) != 0 {
			t.Fatalf("died at %v with a clear path ahead", s.Snakes[i].Head)
		}
	}
	s.Turn(i, Right)
	_, died := s.Step()
	if !slices.Contains(died, i) {
		t.Errorf("died = %v, want the snake that drove into its own wall", died)
	}
	if s.Snakes[i].Alive {
		t.Error("snake is alive after hitting its own wall")
	}
}

func TestSnakeDiesOnAnotherSnakesWall(t *testing.T) {
	t.Parallel()
	s := board(20, 20)
	layer := place(s, Point{10, 5}, Down)
	for range 5 { // lay a vertical wall
		s.Step()
	}
	crosser := place(s, Point{8, 8}, Right)

	var died []int
	for range 2 {
		_, d := s.Step()
		died = append(died, d...)
	}
	if !slices.Contains(died, crosser) {
		t.Errorf("died = %v, want the snake that crossed the wall", died)
	}
	if !s.Snakes[layer].Alive {
		t.Error("the snake that laid the wall died too, want it unharmed")
	}
}

func TestSnakeDiesLeavingTheBoard(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		start Point
		dir   Dir
	}{
		{"off the top", Point{5, 0}, Up},
		{"off the bottom", Point{5, 9}, Down},
		{"off the left", Point{0, 5}, Left},
		{"off the right", Point{9, 5}, Right},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := board(10, 10)
			i := place(s, tt.start, tt.dir)
			moved, died := s.Step()
			if !slices.Contains(died, i) {
				t.Errorf("died = %v, want the snake that drove off the board", died)
			}
			if len(moved) != 0 {
				t.Errorf("moved = %v, want no move off the board", moved)
			}
		})
	}
}

func TestHeadOnCollisionKillsBoth(t *testing.T) {
	t.Parallel()
	s := board(20, 20)
	left := place(s, Point{9, 10}, Right)
	right := place(s, Point{11, 10}, Left)

	moved, died := s.Step() // both want cell {10,10}
	slices.Sort(died)
	if want := []int{left, right}; !slices.Equal(died, want) {
		t.Errorf("died = %v, want both snakes %v", died, want)
	}
	if len(moved) != 0 {
		t.Errorf("moved = %v, want neither snake to take the cell", moved)
	}
}

func TestWallsOutliveTheirSnake(t *testing.T) {
	t.Parallel()
	s := board(20, 20)
	ghost := place(s, Point{10, 5}, Down)
	for range 5 {
		s.Step()
	}
	s.Kill(ghost)

	crosser := place(s, Point{8, 8}, Right)
	var died []int
	for range 2 {
		_, d := s.Step()
		died = append(died, d...)
	}
	if !slices.Contains(died, crosser) {
		t.Errorf("died = %v, want the snake that hit a dead player's wall", died)
	}
}

func TestTurnRefusesToReverse(t *testing.T) {
	t.Parallel()
	s := board(20, 20)
	i := place(s, Point{10, 10}, Right)

	s.Turn(i, Left) // straight back into the wall just laid
	moved, died := s.Step()
	if len(died) != 0 {
		t.Fatalf("died = %v, want the reversal ignored", died)
	}
	if got, want := moved[0].Head, (Point{11, 10}); got != want {
		t.Errorf("head = %v, want %v: the snake kept going right", got, want)
	}
}

func TestTurnTakesEffectNextStep(t *testing.T) {
	t.Parallel()
	s := board(20, 20)
	i := place(s, Point{10, 10}, Right)

	s.Turn(i, Up)
	s.Turn(i, Left) // a second turn within the tick cannot double back
	moved, _ := s.Step()
	if got, want := moved[0].Head, (Point{10, 9}); got != want {
		t.Errorf("head = %v, want %v: the last legal turn applies", got, want)
	}
}

func TestStepReportsWhatItFilled(t *testing.T) {
	t.Parallel()
	s := board(20, 20)
	a := place(s, Point{5, 5}, Right)
	b := place(s, Point{15, 15}, Up)

	moved, died := s.Step()
	if len(died) != 0 {
		t.Fatalf("died = %v, want an empty board to be safe", died)
	}
	want := []Move{
		{Snake: a, Head: Point{6, 5}, Dir: Right},
		{Snake: b, Head: Point{15, 14}, Dir: Up},
	}
	if !slices.Equal(moved, want) {
		t.Errorf("moved = %v, want %v", moved, want)
	}
	// What a step reports is what the board now refuses.
	for _, m := range moved {
		if s.clear(m.Head) {
			t.Errorf("cell %v is still free, want it walled off", m.Head)
		}
	}
}

func TestNewSpreadsSnakesOut(t *testing.T) {
	t.Parallel()
	s := New(30, 30, 20)

	heads := make(map[Point]bool)
	for i, sn := range s.Snakes {
		if !sn.Alive {
			t.Errorf("snake %d did not fit on a board with room for it", i)
			continue
		}
		if heads[sn.Head] {
			t.Errorf("snake %d shares cell %v with another", i, sn.Head)
		}
		heads[sn.Head] = true
	}
	for head := range heads {
		for d := range 4 {
			if next := head.step(Dir(d)); heads[next] {
				t.Errorf("snakes at %v and %v spawned nose to nose", head, next)
			}
		}
	}
	if got, want := s.Alive(), 20; got != want {
		t.Errorf("Alive() = %d, want %d", got, want)
	}
}
