package app

import (
	"time"

	"github.com/gofiber/contrib/websocket"

	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models"
)

const (
	// wsWriteWait is the time allowed to write a single message to the peer.
	wsWriteWait = 10 * time.Second
	// wsPongWait is how long we wait for the next pong from the peer before
	// considering the connection dead.
	wsPongWait = 60 * time.Second
	// wsPingPeriod is how often we send pings to the peer. Must be less than
	// wsPongWait.
	wsPingPeriod = 30 * time.Second
)

// apiEvents is the WebSocket handler for GET /api/events. It streams typed
// events from the in-process hub to the connected client. The connection has
// already been authenticated by the global Bearer middleware.
func (a *AppServer) apiEvents(c *websocket.Conn) {
	sub := a.hub.Subscribe()
	defer func() {
		a.hub.Unsubscribe(sub)
		_ = c.Close()
	}()

	// Initial snapshot so a freshly connected client immediately knows the
	// current state without waiting for the next event.
	if st, err := models.UpdateStatus(); err == nil {
		_ = c.SetWriteDeadline(time.Now().Add(wsWriteWait))
		if err := c.WriteJSON(events.Event{Type: events.EventUpgradeProgress, Data: st}); err != nil {
			return
		}
	}

	// Reader goroutine: required to process control frames (pong/close) and to
	// detect client disconnection. It never returns application messages to us.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.SetReadDeadline(time.Now().Add(wsPongWait))
		c.SetPongHandler(func(string) error {
			return c.SetReadDeadline(time.Now().Add(wsPongWait))
		})
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-sub.Ch:
			if !ok {
				// Hub closed (daemon shutting down): send a clean close frame
				// and return.
				_ = c.SetWriteDeadline(time.Now().Add(wsWriteWait))
				_ = c.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
					time.Now().Add(wsWriteWait),
				)
				return
			}
			_ = c.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.WriteJSON(ev); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
				return
			}
		case <-done:
			// Client disconnected.
			return
		}
	}
}
