package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const (
	// pingInterval keeps idle connections alive through proxies.
	pingInterval = 30 * time.Second
	writeTimeout = 5 * time.Second
	// readLimit caps an inbound message. Players send turns, not payloads.
	readLimit = 256
)

// ErrRoomClosed is returned when a player arrives after the room has shut down.
var ErrRoomClosed = errors.New("room closed")

// Serve upgrades req to a websocket, joins the room as p, and blocks until the
// player disconnects. A clean disconnect returns a nil error.
func (r *Room) Serve(w http.ResponseWriter, req *http.Request, p Player) error {
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return err // Accept has already replied
	}
	conn.SetReadLimit(readLimit)

	c := &client{id: p.ID, name: p.Name, send: make(chan []byte, sendBuffer), snake: -1}
	if !r.add(c) {
		conn.Close(websocket.StatusGoingAway, "room closed")
		return ErrRoomClosed
	}
	defer r.remove(c)

	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	go c.writeLoop(ctx, conn)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			switch websocket.CloseStatus(err) {
			case websocket.StatusNormalClosure, websocket.StatusGoingAway:
				return nil
			}
			// A closed tab or a dropped network ends a connection without
			// ceremony. That is ordinary, not something to log per player.
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		var msg clientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // ignore anything we cannot read
		}
		r.send(input{c: c, msg: msg})
	}
}

// writeLoop pumps the player's outbound queue and keeps the connection alive.
func (c *client) writeLoop(ctx context.Context, conn *websocket.Conn) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		var err error
		select {
		case msg, open := <-c.send:
			if !open {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err = conn.Write(wctx, websocket.MessageText, msg)
			cancel()
		case <-ping.C:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err = conn.Ping(wctx)
			cancel()
		case <-ctx.Done():
			conn.CloseNow()
			return
		}
		if err != nil {
			conn.CloseNow()
			return
		}
	}
}
