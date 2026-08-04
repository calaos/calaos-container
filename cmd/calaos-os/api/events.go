package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// ErrDaemonTooOld is returned by EventsWS when the daemon does not expose
// the /api/events WebSocket endpoint (an older daemon replies with a plain
// HTTP error instead of performing the WebSocket upgrade handshake).
var ErrDaemonTooOld = errors.New("daemon too old: /api/events not supported")

// Event mirrors the generic event bus message shape published by the
// daemon on /api/events: {"type":"upgrade_progress","data":{...Status...}}.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// EventsWS connects to the daemon's generic event bus over WebSocket
// (GET /api/events) and returns the established connection. The caller is
// responsible for closing the connection once done.
//
// If the daemon is too old to support /api/events, the HTTP upgrade
// handshake fails with a plain HTTP response (404/405/426): in that case
// ErrDaemonTooOld is returned.
func (capi *CalaosApi) EventsWS(token string) (*websocket.Conn, error) {
	u := url.URL{
		Scheme: "ws",
		Host:   capi.host,
		Path:   "/api/events",
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)

	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUpgradeRequired:
				return nil, ErrDaemonTooOld
			}
		}
		return nil, err
	}

	return conn, nil
}
