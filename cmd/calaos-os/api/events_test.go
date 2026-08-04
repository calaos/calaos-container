package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/models/structs"
)

// TestEventDecoding checks that the generic Event envelope and the nested
// Status payload decode correctly from the JSON shape published by the
// daemon on /api/events: {"type":"upgrade_progress","data":{...Status...}}.
func TestEventDecoding(t *testing.T) {
	raw := `{
		"type": "upgrade_progress",
		"data": {
			"status": "upgrading",
			"current_pkg": "calaos-base",
			"progress": 1,
			"progress_total": 3,
			"total_percent": 42,
			"current_step": "unpack",
			"current_percent": 55,
			"pkg_index": 1,
			"pkg_count": 3,
			"message": "unpacking calaos-base"
		}
	}`

	var ev Event
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("unmarshal Event: %v", err)
	}

	if ev.Type != "upgrade_progress" {
		t.Fatalf("unexpected event type: %q", ev.Type)
	}

	var st structs.Status
	if err := json.Unmarshal(ev.Data, &st); err != nil {
		t.Fatalf("unmarshal Status: %v", err)
	}

	if st.Status != "upgrading" {
		t.Errorf("Status = %q, want upgrading", st.Status)
	}
	if st.CurrentPkg != "calaos-base" {
		t.Errorf("CurrentPkg = %q, want calaos-base", st.CurrentPkg)
	}
	if st.TotalPercent != 42 {
		t.Errorf("TotalPercent = %d, want 42", st.TotalPercent)
	}
	if st.CurrentStep != "unpack" {
		t.Errorf("CurrentStep = %q, want unpack", st.CurrentStep)
	}
	if st.CurrentPercent != 55 {
		t.Errorf("CurrentPercent = %d, want 55", st.CurrentPercent)
	}
	if st.PkgIndex != 1 || st.PkgCount != 3 {
		t.Errorf("PkgIndex/PkgCount = %d/%d, want 1/3", st.PkgIndex, st.PkgCount)
	}
}

// TestEventsWSDaemonTooOld verifies that when the handshake fails with a
// plain HTTP response (as an older daemon without /api/events would produce
// via a 404), EventsWS surfaces ErrDaemonTooOld.
func TestEventsWSDaemonTooOld(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")

	capi := NewCalaosApi(host)

	conn, err := capi.EventsWS("some-token")
	if conn != nil {
		_ = conn.Close()
	}

	if !errors.Is(err, ErrDaemonTooOld) {
		t.Fatalf("EventsWS error = %v, want ErrDaemonTooOld", err)
	}
}

// TestEventsWSHandshakeOK verifies that EventsWS performs a proper WebSocket
// upgrade against a server implementing GET /api/events with gorilla's
// Upgrader, and that the Authorization header carrying the token is
// forwarded correctly.
func TestEventsWSHandshakeOK(t *testing.T) {
	var gotAuth string

	upgrader := websocket.Upgrader{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("server upgrade failed: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		// Keep the connection open briefly so the client can complete its
		// side of the handshake before the server closes it.
		_, _, _ = conn.ReadMessage()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")

	capi := NewCalaosApi(host)

	conn, err := capi.EventsWS("my-secret-token")
	if err != nil {
		t.Fatalf("EventsWS failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer my-secret-token")
	}
}
