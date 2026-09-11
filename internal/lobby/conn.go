package lobby

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const (
	// pingInterval keeps idle connections alive through proxies.
	pingInterval = 30 * time.Second
	writeTimeout = 5 * time.Second
)

// ErrRoomClosed is returned when a player arrives after the room has shut down.
var ErrRoomClosed = errors.New("room closed")

// Serve upgrades req to a websocket, joins the room as name, and blocks until
// the player disconnects. A clean disconnect returns a nil error.
func (r *Room) Serve(w http.ResponseWriter, req *http.Request, name string) error {
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return err // Accept has already replied
	}
	c := &client{name: name, send: make(chan []byte, sendBuffer)}
	if !r.add(c) {
		conn.Close(websocket.StatusGoingAway, "room closed")
		return ErrRoomClosed
	}
	defer r.remove(c)

	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	go c.writeLoop(ctx, conn)
	for {
		// ponytail: player input is read and discarded until game logic lands.
		if _, _, err := conn.Read(ctx); err != nil {
			switch websocket.CloseStatus(err) {
			case websocket.StatusNormalClosure, websocket.StatusGoingAway:
				return nil
			}
			return err
		}
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
