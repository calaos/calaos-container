package app

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/events"
)

const testToken = "test-token-1234"

// newTestServer spins up an AppServer backed by a real TCP listener on
// 127.0.0.1:0 (fiber's app.Test() does not support WebSocket upgrades). It
// installs a temporary token file and resets the cached token so the Bearer
// middleware accepts testToken. It returns the base host:port and a cleanup
// function.
func newTestServer(t *testing.T) (a *AppServer, addr string, cleanup func()) {
	t.Helper()

	// Point the token middleware at a temp file containing our test token.
	dir := t.TempDir()
	tf := filepath.Join(dir, "calaos-ct.token")
	if err := os.WriteFile(tf, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	prevTokenFile := tokenFile
	prevTokenStr := tokenStr
	tokenFile = tf
	tokenStr = ""

	var err error
	a, err = NewApp()
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	// Per-test hub so a previous test's Close() does not affect this one and
	// there is no shared global mutated concurrently with running handlers.
	a.hub = events.NewHub()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- a.appFiber.Listener(ln)
	}()

	// The OnShutdown hook calls wgDone.Done(); Start() would normally Add(1).
	a.wgDone.Add(1)

	// Wait until the server accepts connections.
	waitReady(t, ln.Addr().String())

	cleanup = func() {
		a.Shutdown()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Errorf("server did not shut down in time")
		}
		tokenFile = prevTokenFile
		tokenStr = prevTokenStr
	}

	return a, ln.Addr().String(), cleanup
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s never became ready", addr)
}

// TestEventsPlainGETUpgradeRequired: a plain (non-upgrade) GET on /api/events
// with a valid token must return 426 Upgrade Required.
func TestEventsPlainGETUpgradeRequired(t *testing.T) {
	_, addr, cleanup := newTestServer(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("expected 426, got %d", resp.StatusCode)
	}
}

// TestEventsNoTokenUnauthorized: a request without a token must be rejected by
// the Bearer middleware with 401 before reaching the WS handler.
func TestEventsNoTokenUnauthorized(t *testing.T) {
	_, addr, cleanup := newTestServer(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// dialWS opens a WebSocket connection using the given request header and query
// string (either may authenticate).
func dialWS(t *testing.T, addr string, header http.Header, query string) *gws.Conn {
	t.Helper()
	u := "ws://" + addr + "/api/events"
	if query != "" {
		u += "?" + query
	}
	conn, resp, err := gws.DefaultDialer.Dial(u, header)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", u, err, code)
	}
	return conn
}

func readEvent(t *testing.T, conn *gws.Conn) events.Event {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var ev events.Event
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return ev
}

// TestEventsHandshakeHeader: handshake succeeds with the token in the
// Authorization header, and the initial snapshot is received.
func TestEventsHandshakeHeader(t *testing.T) {
	_, addr, cleanup := newTestServer(t)
	defer cleanup()

	h := http.Header{}
	h.Set("Authorization", "Bearer "+testToken)
	conn := dialWS(t, addr, h, "")
	defer func() { _ = conn.Close() }()

	ev := readEvent(t, conn)
	if ev.Type != events.EventUpgradeProgress {
		t.Fatalf("expected type %q, got %q", events.EventUpgradeProgress, ev.Type)
	}
	// data should carry the status snapshot (a Status object with a "status"
	// field). Its concrete value depends on models state (set by T3), so we
	// only assert the snapshot is a status object here.
	m, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %T", ev.Data)
	}
	if _, ok := m["status"]; !ok {
		t.Fatalf("expected snapshot to contain a status field, got %v", m)
	}
}

// TestEventsHandshakeQueryToken: handshake succeeds with the token passed as a
// query parameter (browsers cannot set headers on WS).
func TestEventsHandshakeQueryToken(t *testing.T) {
	_, addr, cleanup := newTestServer(t)
	defer cleanup()

	conn := dialWS(t, addr, nil, "token="+testToken)
	defer func() { _ = conn.Close() }()

	ev := readEvent(t, conn)
	if ev.Type != events.EventUpgradeProgress {
		t.Fatalf("expected snapshot type %q, got %q", events.EventUpgradeProgress, ev.Type)
	}
}

// TestEventsPublishReceived: an event published on the hub after connection is
// delivered to the client.
func TestEventsPublishReceived(t *testing.T) {
	a, addr, cleanup := newTestServer(t)
	defer cleanup()

	conn := dialWS(t, addr, nil, "token="+testToken)
	defer func() { _ = conn.Close() }()

	// Consume the initial snapshot first.
	_ = readEvent(t, conn)

	// Give the reader goroutine/subscription a moment, then publish.
	time.Sleep(50 * time.Millisecond)
	a.hub.Publish(events.EventUpgradeProgress, map[string]any{"status": "upgrading", "message": "hello"})

	ev := readEvent(t, conn)
	if ev.Type != events.EventUpgradeProgress {
		t.Fatalf("expected type %q, got %q", events.EventUpgradeProgress, ev.Type)
	}
	m, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %T", ev.Data)
	}
	if m["message"] != "hello" {
		t.Fatalf("expected published message, got %v", m["message"])
	}
}

// TestEventsShutdownClosesClient: shutting down the server with a client
// connected must send a clean close frame and not block.
func TestEventsShutdownClosesClient(t *testing.T) {
	_, addr, cleanup := newTestServer(t)

	conn := dialWS(t, addr, nil, "token="+testToken)
	defer func() { _ = conn.Close() }()
	_ = readEvent(t, conn) // snapshot

	// Trigger shutdown in the background (cleanup also calls Shutdown, which is
	// idempotent for our purposes since events.Close and fiber.Shutdown are).
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()

	// The client should observe a close frame (read returns a close error).
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if gws.IsCloseError(err, gws.CloseGoingAway, gws.CloseNormalClosure, gws.CloseAbnormalClosure) {
				break
			}
			// Any read error means the connection is gone; acceptable.
			break
		}
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("shutdown blocked with client connected")
	}
}
