package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// ErrDaemonTooOld is returned by EventsWS when the daemon serves no event
// bus (an older daemon replies with a plain HTTP error instead of performing
// the WebSocket upgrade handshake).
var ErrDaemonTooOld = errors.New("daemon too old: /api/events not supported")

// Event mirrors the generic event bus message shape published by the
// daemon on /api/events: {"type":"upgrade_progress","data":{...Status...}}.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// EventsWS connects to the daemon's event bus over WebSocket and returns the
// established connection. The caller is responsible for closing it.
//
// It uses /api/v2/events, and /api/events when the daemon in memory predates
// /api/v2. ErrDaemonTooOld means neither is served.
func (capi *CalaosApi) EventsWS(token string) (*websocket.Conn, error) {
	conn, err := capi.dialEvents(token, apiV2+"/events")
	if errors.Is(err, ErrDaemonTooOld) {
		conn, err = capi.dialEvents(token, "/api/events")
	}
	return conn, err
}

// dialEvents maps a plain HTTP answer to the handshake (404/405/426) to
// ErrDaemonTooOld.
func (capi *CalaosApi) dialEvents(token, path string) (*websocket.Conn, error) {
	u := url.URL{
		Scheme: "ws",
		Host:   capi.host,
		Path:   path,
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
