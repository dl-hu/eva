package main

import (
	"math"
	"testing"
	"time"
)

// TestTurns checks the tables against the rule the server enforces rather than
// against themselves: game.State.Turn ignores a heading that reverses the one a
// snake is on, so a bot that "turns" back onto its own wall never turns at all.
// Which way is left is the bot's business — swapping the two tables is still a
// correct bot, and this test says so.
func TestTurns(t *testing.T) {
	// The server's rule, stated independently of the tables under test.
	reverse := [4]int{up: down, down: up, left: right, right: left}

	for d := range 4 {
		l, r := turnLeft[d], turnRight[d]
		if l == d || l == reverse[d] {
			t.Errorf("left of %s is %s: not a turn the server will take", dirNames[d], dirNames[l])
		}
		if r == d || r == reverse[d] {
			t.Errorf("right of %s is %s: not a turn the server will take", dirNames[d], dirNames[r])
		}
		if r != reverse[l] {
			t.Errorf("from %s: left %s and right %s are not opposites", dirNames[d], dirNames[l], dirNames[r])
		}
	}
}

// TestNextTurn checks the waits are exponential with a mean of 1/rate: the mean
// alone would pass for any distribution, and a bot that waits a fixed 1/rate is
// a fleet that turns in lockstep.
func TestNextTurn(t *testing.T) {
	const rate, n = 5.0, 100000
	mean := 1 / rate
	var total time.Duration
	over := 0
	for range n {
		d := nextTurn(rate)
		if d < 0 {
			t.Fatalf("negative wait %v", d)
		}
		total += d
		if d.Seconds() > mean {
			over++
		}
	}
	// Tolerances are ~6 standard errors at this n, so a pass is not luck.
	if got := total.Seconds() / n; math.Abs(got-mean) > 0.02*mean {
		t.Errorf("mean wait %.4fs, want %.4fs", got, mean)
	}
	// For an exponential, the fraction of waits past the mean is 1/e.
	if got := float64(over) / n; math.Abs(got-1/math.E) > 0.01 {
		t.Errorf("%.3f of waits exceeded the mean, want %.3f", got, 1/math.E)
	}
}
