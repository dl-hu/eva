// Command bots load-tests an eva server with synthetic players. Each bot signs
// in, joins a room and drives a snake over its own websocket, exactly as the
// browser client does; it turns left or right at random, waiting an
// exponentially distributed time between turns.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// The wire protocol, from internal/lobby/proto.go. Only the fields a bot acts
// on are named here.
type serverMsg struct {
	Type    string `json:"type"`
	Snake   int    `json:"snake"` // "you"
	Players []struct {
		Name string `json:"name"`
	} `json:"players"` // "players": only its length matters to a bot
	Snakes []struct {
		Dir int `json:"dir"`
	} `json:"snakes"` // "start"
	Moves [][4]int `json:"moves"` // "tick": snake, x, y, dir
}

// Headings, in the order the protocol numbers them.
const (
	up = iota
	down
	left
	right
)

var (
	dirNames = [4]string{up: "up", down: "down", left: "left", right: "right"}
	// turnLeft and turnRight are the heading after a quarter turn. Y grows
	// downward, so left of up is left.
	turnLeft  = [4]int{up: left, left: down, down: right, right: up}
	turnRight = [4]int{up: right, right: down, down: left, left: up}
)

// Counters behind the progress line.
var stats struct {
	live  atomic.Int64
	msgs  atomic.Int64
	bytes atomic.Int64
	turns atomic.Int64
}

func main() {
	base := flag.String("url", "https://dlhu.dev/eva", "base URL of the server, including any prefix")
	n := flag.Int("n", 100, "number of bots")
	code := flag.String("code", "", "room code to join; empty means the first bot creates one")
	rate := flag.Float64("rate", 0.1, "mean turns per second per bot")
	w := flag.Int("w", 250, "board width")
	h := flag.Int("h", 250, "board height")
	warmup := flag.Duration("warmup", 15*time.Second, "how long the host waits for a full room before starting anyway")
	flag.Parse()

	*base = strings.TrimSuffix(*base, "/")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The first bot settles the room code, so the rest have somewhere to go.
	first, err := newBot(*base, *code, "bot0")
	if err != nil {
		log.Fatalf("bot0: %v", err)
	}
	first.host, first.want, first.warmup = *code == "", *n, *warmup
	log.Printf("room %s: %d bots against %s", first.code, *n, *base)

	var wg sync.WaitGroup
	run := func(b *bot) {
		defer wg.Done()
		if err := b.run(ctx, *rate, *w, *h); err != nil && ctx.Err() == nil {
			log.Printf("%s: %v", b.name, err)
		}
	}
	wg.Add(1)
	go run(first)
	for i := 1; i < *n; i++ {
		name := fmt.Sprintf("bot%d", i)
		b, err := newBot(*base, first.code, name)
		if err != nil {
			log.Printf("%s: %v", name, err)
			continue
		}
		wg.Add(1)
		go run(b)
	}

	go report(ctx)
	wg.Wait()
}

// report prints what the fleet is doing once a second.
func report(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			log.Printf("%d live, %d msg/s, %.1f MiB/s in, %d turns/s",
				stats.live.Load(), stats.msgs.Swap(0),
				float64(stats.bytes.Swap(0))/(1<<20), stats.turns.Swap(0))
		}
	}
}

// bot is one synthetic player: a session, a room, and the snake it drives.
type bot struct {
	name   string
	http   *http.Client // holds the session cookie
	wsURL  string
	code   string
	host   bool
	want   int           // bots expected in the room before the host starts
	warmup time.Duration // ...or how long the host waits for them

	// Owned by the reader, read by the driver.
	snake atomic.Int64 // -1 when not playing
	dir   atomic.Int64
	start chan struct{} // the host should start a game
}

// newBot signs a bot in and joins it to the room, creating one if code is
// empty. It does not connect: that is run's job.
func newBot(base, code, name string) (*bot, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	b := &bot{
		name:  name,
		http:  &http.Client{Jar: jar, Timeout: 10 * time.Second},
		start: make(chan struct{}, 1),
	}
	b.snake.Store(-1)

	form := url.Values{"name": {name}}
	page := "/create"
	if code != "" {
		page, form["code"] = "/join", []string{code}
	}
	resp, err := b.http.PostForm(base+page, form)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	// A successful join lands on /room/CODE; anything else bounced us back to
	// the form with the reason in the query.
	got := resp.Request.URL.Path
	i := strings.Index(got, "/room/")
	if resp.StatusCode != http.StatusOK || i < 0 {
		return nil, fmt.Errorf("joining: ended at %s", resp.Request.URL)
	}
	b.code = got[i+len("/room/"):]
	b.wsURL = strings.Replace(base, "http", "ws", 1) + "/ws/" + b.code
	return b, nil
}

// run connects the bot and plays until the context is cancelled or the
// connection drops.
func (b *bot) run(ctx context.Context, rate float64, w, h int) error {
	conn, _, err := websocket.Dial(ctx, b.wsURL, &websocket.DialOptions{HTTPClient: b.http})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	stats.live.Add(1)
	defer stats.live.Add(-1)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		b.read(ctx, conn)
	}()
	return b.drive(ctx, conn, rate, w, h)
}

// read tracks what the server says about this bot's snake. It is also where
// the host learns that the room is full, or that a game has ended.
func (b *bot) read(ctx context.Context, conn *websocket.Conn) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		stats.msgs.Add(1)
		stats.bytes.Add(int64(len(data)))

		var msg serverMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "you":
			b.snake.Store(int64(msg.Snake))
		case "start":
			if s := int(b.snake.Load()); s >= 0 && s < len(msg.Snakes) {
				b.dir.Store(int64(msg.Snakes[s].Dir))
			}
		case "tick":
			// The server's heading wins: a turn it refused never happened.
			s := b.snake.Load()
			for _, m := range msg.Moves {
				if int64(m[0]) == s {
					b.dir.Store(int64(m[3]))
					break
				}
			}
		case "players":
			if b.host && len(msg.Players) >= b.want {
				b.wake()
			}
		case "over":
			b.snake.Store(-1)
			if b.host {
				b.wake() // keep the load going
			}
		}
	}
}

// wake asks the driver to start a game, without blocking if one is already due.
func (b *bot) wake() {
	select {
	case b.start <- struct{}{}:
	default:
	}
}

// drive sends this bot's input: a turn every so often, and for the host, the
// message that begins a game.
func (b *bot) drive(ctx context.Context, conn *websocket.Conn, rate float64, w, h int) error {
	// The host starts anyway if the room never fills; the others never wait.
	var late <-chan time.Time
	if b.host {
		late = time.After(b.warmup)
	}
	timer := time.NewTimer(nextTurn(rate))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return conn.Close(websocket.StatusNormalClosure, "")
		case <-late:
			late = nil
			b.wake()
		case <-b.start:
			msg := fmt.Sprintf(`{"type":"start","w":%d,"h":%d}`, w, h)
			if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				return err
			}
		case <-timer.C:
			timer.Reset(nextTurn(rate))
			if b.snake.Load() < 0 {
				continue // sitting in the lobby
			}
			heading := b.dir.Load()
			d := turnLeft[heading]
			if rand.IntN(2) == 0 {
				d = turnRight[heading]
			}
			msg := fmt.Sprintf(`{"type":"dir","dir":%q}`, dirNames[d])
			if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				return err
			}
			stats.turns.Add(1)
		}
	}
}

// nextTurn is how long to wait before the next turn: exponential with a mean
// of 1/rate seconds, so turns arrive as a Poisson process.
func nextTurn(rate float64) time.Duration {
	return time.Duration(rand.ExpFloat64() / rate * float64(time.Second))
}
