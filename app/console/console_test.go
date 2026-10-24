package console

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/app/console/consoletest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type closedEvent struct {
	info   Info
	reason string
}

type fixture struct {
	t      *testing.T
	m      *Manager
	clk    *clock
	dir    string
	closed chan closedEvent
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("", "cc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &fixture{
		t:      t,
		clk:    &clock{t: time.Unix(1_800_000_000, 0)},
		dir:    filepath.Join(root, "console"),
		closed: make(chan closedEvent, 8),
	}
	f.m = New(Options{
		Dir:    f.dir,
		Now:    f.clk.Now,
		Closed: func(i Info, r string) { f.closed <- closedEvent{i, r} },
	})
	f.m.o.Shell = consoletest.Shell(f.dir)
	t.Cleanup(f.m.Shutdown)
	return f
}

func (f *fixture) open(session string) Info {
	f.t.Helper()
	info, err := f.m.Open(Request{SessionHash: session, AuditID: session[:4], Client: "192.0.2.7", Term: "xterm"}, func() error { return nil })
	if err != nil {
		f.t.Fatalf("open: %v", err)
	}
	return info
}

func (f *fixture) attach(info Info) *gws.Conn {
	f.t.Helper()
	dialer := gws.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", info.Socket)
		},
		Subprotocols:     []string{WSProtocol},
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial("ws://localhost"+WSPath, http.Header{})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := conn.WriteMessage(gws.TextMessage, []byte(`{"columns":120,"rows":40}`)); err != nil {
		f.t.Fatal(err)
	}
	return conn
}

func (f *fixture) waitClosed(id string) closedEvent {
	f.t.Helper()
	ev := f.waitAnyClosed()
	if id != "" && ev.info.ID != id {
		f.t.Fatalf("closed %s, want %s", ev.info.ID, id)
	}
	return ev
}

func (f *fixture) waitAnyClosed() closedEvent {
	f.t.Helper()
	select {
	case ev := <-f.closed:
		return ev
	case <-time.After(5 * time.Second):
		f.t.Fatal("console not closed")
	}
	return closedEvent{}
}

func (f *fixture) expectGone(info Info, reason string) {
	f.t.Helper()
	if ev := f.waitClosed(info.ID); ev.reason != reason {
		f.t.Fatalf("closed for %q, want %q", ev.reason, reason)
	}
	if _, err := os.Lstat(info.Socket); !os.IsNotExist(err) {
		f.t.Fatalf("socket left behind: %v", err)
	}
}

func (f *fixture) expectShellGone(info Info, reason string) {
	f.t.Helper()
	pid := consoletest.ReadPid(f.t, info.Socket+".pid")
	child := consoletest.ReadPid(f.t, info.Socket+".child")
	f.expectGone(info, reason)
	consoletest.Eventually(f.t, "shell and its group gone", func() bool {
		return !consoletest.Alive(pid) && !consoletest.Alive(child)
	})
}

func TestOpenSocketAndCommandLine(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	conn := f.attach(info)
	defer func() { _ = conn.Close() }()
	consoletest.ReadPid(t, info.Socket+".child")

	fi, err := os.Lstat(info.Socket)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v mode %v", err, fi.Mode())
	}
	if di, err := os.Lstat(f.dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", di.Mode())
	}
	if filepath.Dir(info.Socket) != f.dir || !strings.HasSuffix(info.Socket, "/console-"+info.ID+".sock") {
		t.Fatalf("socket path %s", info.Socket)
	}

	args, _ := os.ReadFile(info.Socket + ".args")
	if strings.TrimSpace(string(args)) != f.dir {
		t.Fatalf("shell args %q, want %q", args, f.dir)
	}
	env, _ := os.ReadFile(info.Socket + ".env")
	got := strings.Split(strings.TrimSpace(string(env)), "\n")
	want := append(append([]string{}, shellEnv...), "TERM=xterm", "PWD=/root")
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("shell environment %q", env)
	}
}

func TestCapOfTwo(t *testing.T) {
	f := newFixture(t)
	a := f.open("aaaaaaaa")
	f.open("bbbbbbbb")

	took := false
	_, err := f.m.Open(Request{SessionHash: "cccccccc", Term: "xterm"}, func() error { took = true; return nil })
	if !errors.Is(err, ErrLimit) || took {
		t.Fatalf("third console: %v, grant taken %v", err, took)
	}

	f.m.kill(infoConsole(f.m, a.ID), ReasonClosed)
	f.expectGone(a, ReasonClosed)
	f.open("cccccccc")
}

func TestStartingConsoleCountsAgainstTheCap(t *testing.T) {
	f := newFixture(t)
	f.open("aaaaaaaa")
	release := make(chan struct{})
	entered := make(chan struct{})
	go func() {
		_, _ = f.m.Open(Request{SessionHash: "bbbbbbbb", Term: "xterm"}, func() error {
			close(entered)
			<-release
			return errors.New("refused")
		})
	}()
	<-entered
	if _, err := f.m.Open(Request{SessionHash: "cccccccc", Term: "xterm"}, func() error { return nil }); !errors.Is(err, ErrLimit) {
		t.Fatalf("open during a reservation: %v", err)
	}
	close(release)
	consoletest.Eventually(t, "reservation released", func() bool { return f.m.Count() == 1 })
}

func TestTakeFailureStartsNothing(t *testing.T) {
	f := newFixture(t)
	want := errors.New("grant refused")
	if _, err := f.m.Open(Request{SessionHash: "aaaaaaaa", Term: "xterm"}, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("open: %v", err)
	}
	if n := f.m.Count(); n != 0 {
		t.Fatalf("%d consoles", n)
	}
	if e, _ := os.ReadDir(f.dir); len(e) != 0 {
		t.Fatalf("directory not empty: %v", e)
	}
}

func TestListenFailureConsumesGrantAndAudits(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(f.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	took := false
	_, err := f.m.Open(Request{SessionHash: "aaaaaaaa", Term: "xterm"}, func() error { took = true; return nil })
	if err == nil || !took {
		t.Fatalf("open: %v, grant taken %v", err, took)
	}
	ev := f.waitClosed("")
	if ev.reason != ReasonExited || !ev.info.Opened.IsZero() {
		t.Fatalf("close event %+v", ev)
	}
}

func TestShellStartFailureAudits(t *testing.T) {
	f := newFixture(t)
	f.m.o.Shell = []string{filepath.Join(f.dir, "absent")}
	info := f.open("aaaaaaaa")
	conn := f.attach(info)
	defer func() { _ = conn.Close() }()
	f.expectGone(info, ReasonExited)
}

// expectCloseFrame reads until the close frame; the shell's own output is
// discarded.
func expectCloseFrame(t *testing.T, conn *gws.Conn, code int, reason string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var ce *gws.CloseError
		if !errors.As(err, &ce) || ce.Code != code || ce.Text != reason {
			t.Fatalf("close frame %v, want %d %q", err, code, reason)
		}
		return
	}
}

func TestAbsoluteLimit(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	conn := f.attach(info)
	defer func() { _ = conn.Close() }()
	consoletest.ReadPid(t, info.Socket+".pid")
	for i := 0; i < 4; i++ {
		f.clk.Advance(IdleTimeout - time.Minute)
		f.m.touch(infoConsole(f.m, info.ID))
		f.m.Sweep()
	}
	f.clk.Advance(4 * time.Minute)
	f.m.Sweep()
	f.expectShellGone(info, ReasonExpired)
	expectCloseFrame(t, conn, gws.CloseGoingAway, ReasonExpired)
}

func TestIdleLimitCountsInputOnly(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	f.clk.Advance(10 * time.Second)
	conn := f.attach(info)
	defer func() { _ = conn.Close() }()
	consoletest.ReadPid(t, info.Socket+".pid")
	f.clk.Advance(10 * time.Minute)
	f.m.attach(infoConsole(f.m, info.ID))
	f.clk.Advance(5*time.Minute - time.Second)
	f.m.Sweep()
	f.m.touch(infoConsole(f.m, info.ID))
	f.clk.Advance(IdleTimeout - time.Second)
	f.m.Sweep()
	if f.m.Count() != 1 {
		t.Fatal("killed before the idle limit")
	}
	f.clk.Advance(time.Second)
	f.m.Sweep()
	f.expectShellGone(info, ReasonIdle)
	expectCloseFrame(t, conn, gws.CloseGoingAway, ReasonIdle)
}

func TestEmptyInputDoesNotRearmIdle(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	conn := f.attach(info)
	defer func() { _ = conn.Close() }()
	consoletest.ReadPid(t, info.Socket+".pid")
	f.clk.Advance(IdleTimeout - time.Second)
	if err := conn.WriteMessage(gws.BinaryMessage, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(gws.TextMessage, []byte(`{"type":"input","data":""}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	f.clk.Advance(time.Second)
	f.m.Sweep()
	f.expectGone(info, ReasonIdle)
}

func infoConsole(m *Manager, id string) *console {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consoles[id]
}

func TestNotConnectedWithin30s(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	f.clk.Advance(ConnectTimeout - time.Second)
	f.m.Sweep()
	if f.m.Count() != 1 {
		t.Fatal("killed before the connect limit")
	}
	f.clk.Advance(time.Second)
	f.m.Sweep()
	f.expectGone(info, ReasonNotConnected)
}

func TestSessionsEnded(t *testing.T) {
	f := newFixture(t)
	a := f.open("aaaaaaaa")
	b := f.open("bbbbbbbb")

	f.m.SessionsEnded("aaaaaaaa", "bad_code")
	f.expectGone(a, "bad_code")
	if infoConsole(f.m, b.ID) == nil {
		t.Fatal("other session's console was killed")
	}

	f.m.SessionsEnded("", "admin_reset")
	f.expectGone(b, "admin_reset")
}

func TestSessionsEndedCloseFrame(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	conn := f.attach(info)
	defer func() { _ = conn.Close() }()
	consoletest.ReadPid(t, info.Socket+".child")
	f.m.SessionsEnded("aaaaaaaa", "admin_set")
	f.expectShellGone(info, "admin_set")
	expectCloseFrame(t, conn, gws.CloseGoingAway, "admin_set")
}

func TestSessionEndedWhileStarting(t *testing.T) {
	f := newFixture(t)
	_, err := f.m.Open(Request{SessionHash: "aaaaaaaa", Term: "xterm"}, func() error {
		f.m.SessionsEnded("aaaaaaaa", "admin_set")
		return nil
	})
	if !errors.Is(err, ErrEnded) {
		t.Fatalf("open: %v", err)
	}
	ev := f.waitClosed("")
	if ev.reason != "admin_set" || !ev.info.Opened.IsZero() {
		t.Fatalf("close event %+v", ev)
	}
}

func TestTunnelDisconnectEndsTheConsole(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	conn := f.attach(info)
	consoletest.ReadPid(t, info.Socket+".child")
	_ = conn.Close()
	f.expectShellGone(info, ReasonClosed)
}

func TestKillFallsBackToSigkill(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "fake-mode"), []byte(consoletest.ModeIgnoreHUP), 0o600); err != nil {
		t.Fatal(err)
	}
	info := f.open("aaaaaaaa")
	conn := f.attach(info)
	defer func() { _ = conn.Close() }()
	consoletest.ReadPid(t, info.Socket+".child")
	start := time.Now()
	f.m.SessionsEnded("", "admin_reset")
	f.expectShellGone(info, "admin_reset")
	if d := time.Since(start); d < killGrace {
		t.Fatalf("gone after %v, before the grace: SIGHUP was not ignored", d)
	}
}

func TestShutdownKillsEverything(t *testing.T) {
	f := newFixture(t)
	a := f.open("aaaaaaaa")
	conn := f.attach(a)
	defer func() { _ = conn.Close() }()
	consoletest.ReadPid(t, a.Socket+".child")
	f.m.Shutdown()
	f.expectGone(a, ReasonShutdown)
	expectCloseFrame(t, conn, gws.CloseGoingAway, ReasonShutdown)
}

func TestNewRemovesStaleSockets(t *testing.T) {
	f := newFixture(t)
	stale := filepath.Join(f.dir, "console-0123456789abcdef.sock")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	other := filepath.Join(f.dir, "keep")
	_ = os.WriteFile(other, nil, 0o600)
	_ = os.Chmod(f.dir, 0o755) //nolint:gosec // the loose mode under test

	New(Options{Dir: f.dir})
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale socket: %v", err)
	}
	if _, err := os.Lstat(other); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
	if di, _ := os.Lstat(f.dir); di.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", di.Mode())
	}
}

func TestValidTerm(t *testing.T) {
	for _, s := range []string{"xterm-256color", "xterm", "vt100"} {
		if !ValidTerm(s) {
			t.Fatalf("%s refused", s)
		}
	}
	for _, s := range []string{"", "dumb", "xterm-256color\n", "../../x"} {
		if ValidTerm(s) {
			t.Fatalf("%q accepted", s)
		}
	}
}
