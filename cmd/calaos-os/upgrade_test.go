package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/cmd/calaos-os/api"
)

const jobID = "3f9c2a7e5b1d4c80"

// fakeDaemon serves the given routes; anything else is a plain-text 404, as
// on a daemon without /api/v2. WebSocket peers are handed to the test once
// the snapshot is sent.
type fakeDaemon struct {
	conns chan *websocket.Conn
	posts chan string // "path body"
	mux   *http.ServeMux
	api   *api.CalaosApi
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{conns: make(chan *websocket.Conn, 4), posts: make(chan string, 4), mux: http.NewServeMux()}
	srv := httptest.NewServer(d.mux)
	t.Cleanup(srv.Close)
	d.api = api.NewCalaosApi(strings.TrimPrefix(srv.URL, "http://"))

	prev := []time.Duration{upgradeOverallTimeout, reconnectFirstDelay, reconnectGiveUp, legacyGrace}
	upgradeOverallTimeout, reconnectFirstDelay, reconnectGiveUp, legacyGrace = 10*time.Second, 10*time.Millisecond, 2*time.Second, 100*time.Millisecond
	t.Cleanup(func() {
		upgradeOverallTimeout, reconnectFirstDelay, reconnectGiveUp, legacyGrace = prev[0], prev[1], prev[2], prev[3]
	})
	return d
}

func (d *fakeDaemon) events(path string) {
	upgrader := websocket.Upgrader{}
	d.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upgrade_progress","data":{"status":"done","total_percent":100}}`))
		d.conns <- conn
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
}

// post records the request, waits for release when set, then answers.
func (d *fakeDaemon) post(path string, status int, body string, release chan struct{}) {
	d.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		d.posts <- r.Method + " " + r.URL.RequestURI() + " " + string(b)
		if release != nil {
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func (d *fakeDaemon) reply(path string, status int, body string) {
	d.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func send(t *testing.T, conn *websocket.Conn, frames ...string) {
	t.Helper()
	for _, f := range frames {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(f)); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
}

func (d *fakeDaemon) run(t *testing.T, pkg string) chan error {
	t.Helper()
	out := make(chan error, 1)
	go func() { out <- runUpgrade(d.api, "tok", pkg) }()
	return out
}

func wait(t *testing.T, ch chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("runUpgrade did not return")
		return nil
	}
}

func next[T any](t *testing.T, ch chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}

const (
	accepted     = `{"error":false,"msg":"accepted","data":{"id":"` + jobID + `","kind":"upgrade_all","status":"running","reboot_required":false,"daemon_restart":false,"started_at":"2026-10-22T09:12:03Z"}}`
	progressUp   = `{"type":"upgrade_progress","data":{"status":"upgrading","current_pkg":"calaos-server","total_percent":40,"pkg_index":1,"pkg_count":1}}`
	progressDone = `{"type":"upgrade_progress","data":{"status":"done","total_percent":100}}`
	jobRunning   = `{"type":"upgrade_job","data":{"id":"` + jobID + `","status":"running"}}`
	jobDone      = `{"type":"upgrade_job","data":{"id":"` + jobID + `","status":"done","reboot_required":true}}`
	otherJobDone = `{"type":"upgrade_job","data":{"id":"0000000000000000","status":"done"}}`
)

func TestUpgradeFollowsJob(t *testing.T) {
	for _, tc := range []struct{ pkg, path, body string }{
		{"", "/api/v2/update/upgrade-all", ""},
		{"calaos-server", "/api/v2/update/upgrade", `{"package":"calaos-server"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			d := newFakeDaemon(t)
			d.events("/api/v2/events")
			d.post(tc.path, http.StatusAccepted, accepted, nil)
			d.post("/api/update/upgrade-all", 500, `{}`, nil)

			res := d.run(t, tc.pkg)
			conn := next(t, d.conns)
			if got := next(t, d.posts); got != "POST "+tc.path+" "+tc.body {
				t.Fatalf("request %q", got)
			}
			// Another job ending, and the progress reaching done, do not end ours.
			send(t, conn, jobRunning, progressUp, otherJobDone, progressDone)
			select {
			case err := <-res:
				t.Fatalf("returned before the terminal job: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			send(t, conn, jobDone)
			if err := wait(t, res); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpgradeJobError(t *testing.T) {
	d := newFakeDaemon(t)
	d.events("/api/v2/events")
	d.post("/api/v2/update/upgrade-all", http.StatusAccepted, accepted, nil)

	res := d.run(t, "")
	conn := next(t, d.conns)
	next(t, d.posts)
	send(t, conn, `{"type":"upgrade_job","data":{"id":"`+jobID+`","status":"error","error":"upgrade failed: calaos-home"}}`)
	err := wait(t, res)
	if err == nil || !strings.Contains(err.Error(), "upgrade failed: calaos-home") || exitCode(err) != ExitServer {
		t.Fatalf("err %v, code %d", err, exitCode(err))
	}
}

func TestUpgradeInProgress(t *testing.T) {
	d := newFakeDaemon(t)
	d.events("/api/v2/events")
	d.post("/api/v2/update/upgrade-all", http.StatusConflict,
		`{"error":true,"code":"upgrade_in_progress","msg":"an upgrade is already in progress","data":{"job_id":"`+jobID+`"}}`, nil)

	err := wait(t, d.run(t, ""))
	if err == nil || !strings.Contains(err.Error(), "already in progress") || !strings.Contains(err.Error(), jobID) || exitCode(err) != ExitServer {
		t.Fatalf("err %v, code %d", err, exitCode(err))
	}
}

func TestUpgradePackageNotFound(t *testing.T) {
	d := newFakeDaemon(t)
	d.events("/api/v2/events")
	d.post("/api/v2/update/upgrade", http.StatusNotFound, `{"error":true,"code":"package_not_found","msg":"no pending update for this package"}`, nil)
	d.post("/api/update/upgrade", 500, `{}`, nil)

	err := wait(t, d.run(t, "calaos-nope"))
	var ae *api.APIError
	if !errors.As(err, &ae) || ae.Code != "package_not_found" || exitCode(err) != ExitServer {
		t.Fatalf("err %v", err)
	}
	if got := next(t, d.posts); !strings.HasPrefix(got, "POST /api/v2/") {
		t.Fatalf("fell back to the legacy route: %s", got)
	}
}

// TestUpgradeSurvivesDaemonRestart: the daemon exits after upgrading itself;
// the CLI reconnects and reads the job.
func TestUpgradeSurvivesDaemonRestart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		events []string
	}{
		{"ended while away", `{"error":false,"msg":"ok","data":{"progress":{"status":"idle"},"job":{"id":"` + jobID + `","status":"done","daemon_restart":true}}}`, nil},
		{"still running", `{"error":false,"msg":"ok","data":{"progress":{"status":"upgrading"},"job":{"id":"` + jobID + `","status":"running"}}}`, []string{progressUp, jobDone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDaemon(t)
			d.events("/api/v2/events")
			d.post("/api/v2/update/upgrade-all", http.StatusAccepted, accepted, nil)
			d.reply("/api/v2/update/status", 200, tc.status)

			res := d.run(t, "")
			conn := next(t, d.conns)
			next(t, d.posts)
			send(t, conn, progressUp)
			_ = conn.Close()

			again := next(t, d.conns)
			send(t, again, tc.events...)
			if err := wait(t, res); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpgradeJobLostAfterRestart(t *testing.T) {
	d := newFakeDaemon(t)
	d.events("/api/v2/events")
	d.post("/api/v2/update/upgrade-all", http.StatusAccepted, accepted, nil)
	d.reply("/api/v2/update/status", 200, `{"error":false,"msg":"ok","data":{"progress":{"status":"idle"},"job":null}}`)

	res := d.run(t, "")
	conn := next(t, d.conns)
	next(t, d.posts)
	_ = conn.Close()
	err := wait(t, res)
	if err == nil || exitCode(err) != ExitNetwork {
		t.Fatalf("err %v, code %d", err, exitCode(err))
	}
}

func TestUpgradeTimesOut(t *testing.T) {
	d := newFakeDaemon(t)
	upgradeOverallTimeout = 200 * time.Millisecond
	d.events("/api/v2/events")
	d.post("/api/v2/update/upgrade-all", http.StatusAccepted, accepted, nil)

	res := d.run(t, "")
	next(t, d.conns)
	err := wait(t, res)
	if err == nil || !strings.Contains(err.Error(), "timed out") || exitCode(err) != ExitNetwork {
		t.Fatalf("err %v", err)
	}
}

// TestUpgradeLegacyFallback: against a daemon without jobs the CLI runs the
// synchronous legacy upgrade, as the CLI did before.
func TestUpgradeLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name, events, v2Route, pkg, legacyPath, want string
	}{
		{name: "daemon without /api/v2", events: "/api/events", pkg: "",
			legacyPath: "/api/update/upgrade-all", want: "POST /api/update/upgrade-all "},
		{name: "daemon without /api/v2, one package", events: "/api/events", pkg: "calaos-server",
			legacyPath: "/api/update/upgrade", want: "POST /api/update/upgrade?package=calaos-server "},
		{name: "/api/v2 without jobs", events: "/api/v2/events", v2Route: "/api/v2/update/upgrade-all", pkg: "",
			legacyPath: "/api/update/upgrade-all", want: "POST /api/update/upgrade-all "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDaemon(t)
			d.events(tc.events)
			if tc.v2Route != "" {
				d.reply(tc.v2Route, http.StatusNotFound, `{"error":true,"code":"not_found","msg":"not found"}`)
			}
			release := make(chan struct{})
			d.post(tc.legacyPath, 200, `{"msg":"Upgrade done"}`, release)

			res := d.run(t, tc.pkg)
			conn := next(t, d.conns)
			if got := next(t, d.posts); got != tc.want {
				t.Fatalf("request %q, want %q", got, tc.want)
			}
			send(t, conn, progressUp, progressDone)
			close(release)
			if err := wait(t, res); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpgradeLegacyRefused(t *testing.T) {
	d := newFakeDaemon(t)
	d.events("/api/events")
	d.post("/api/update/upgrade-all", 500, `{"error":true,"msg":"upgrade already in progress"}`, nil)

	err := wait(t, d.run(t, ""))
	if err == nil || err.Error() != "upgrade already in progress" || exitCode(err) != ExitServer {
		t.Fatalf("err %v", err)
	}
}

func TestUpgradeLegacyAptError(t *testing.T) {
	d := newFakeDaemon(t)
	d.events("/api/events")
	release := make(chan struct{})
	d.post("/api/update/upgrade-all", 500, `{"error":true,"msg":"E: dpkg failed"}`, release)

	res := d.run(t, "")
	conn := next(t, d.conns)
	next(t, d.posts)
	send(t, conn, `{"type":"upgrade_progress","data":{"status":"error","error":"E: dpkg failed"}}`)
	close(release)
	err := wait(t, res)
	if err == nil || err.Error() != "E: dpkg failed" || exitCode(err) != ExitServer {
		t.Fatalf("err %v", err)
	}
}

func TestUpgradeDaemonWithoutEvents(t *testing.T) {
	d := newFakeDaemon(t)
	err := wait(t, d.run(t, ""))
	if err == nil || exitCode(err) != ExitServer {
		t.Fatalf("err %v", err)
	}
}
