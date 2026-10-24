package console

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/console/consoletest"
)

func TestMain(m *testing.M) {
	consoletest.MaybeRun()
	os.Exit(m.Run())
}

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
	// Short: a Unix socket path is limited to 108 bytes.
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
		TTYD:   consoletest.Install(t),
		Now:    f.clk.Now,
		Closed: func(i Info, r string) { f.closed <- closedEvent{i, r} },
	})
	t.Cleanup(f.m.Shutdown)
	return f
}

func (f *fixture) open(session string) Info {
	f.t.Helper()
	info, err := f.m.Open(Request{SessionHash: session, AuditID: session[:4], Client: "192.0.2.7", Term: "xterm"}, func() error { return nil })
	if err != nil {
		f.t.Fatalf("open: %v", err)
	}
	// The fake's group is complete once its grandchild has started.
	consoletest.ReadPid(f.t, info.Socket+".child")
	return info
}

func (f *fixture) waitClosed(id string) closedEvent {
	f.t.Helper()
	select {
	case ev := <-f.closed:
		if ev.info.ID != id {
			f.t.Fatalf("closed %s, want %s", ev.info.ID, id)
		}
		return ev
	case <-time.After(5 * time.Second):
		f.t.Fatalf("console %s not closed", id)
	}
	return closedEvent{}
}

func (f *fixture) expectGone(info Info, reason string) {
	f.t.Helper()
	pid := consoletest.ReadPid(f.t, info.Socket+".pid")
	child := consoletest.ReadPid(f.t, info.Socket+".child")
	if ev := f.waitClosed(info.ID); ev.reason != reason {
		f.t.Fatalf("closed for %q, want %q", ev.reason, reason)
	}
	consoletest.Eventually(f.t, "ttyd and its group gone", func() bool {
		return !consoletest.Alive(pid) && !consoletest.Alive(child)
	})
	if _, err := os.Lstat(info.Socket); !os.IsNotExist(err) {
		f.t.Fatalf("socket left behind: %v", err)
	}
	if _, err := f.m.Activity(info.ID, EventInput); !errors.Is(err, ErrNotFound) {
		f.t.Fatalf("activity on a closed console: %v", err)
	}
}

func TestOpenSocketAndCommandLine(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")

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
	got := strings.Split(string(args), "\n")
	want := []string{"-i", info.Socket, "--once", "--max-clients", "1", "--terminal-type", "xterm",
		"--cwd", "/root", "--debug", "3", "--writable", "--ping-interval", "30", "/bin/bash", "--login"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ttyd args\n got %q\nwant %q", got, want)
	}

	env, _ := os.ReadFile(info.Socket + ".env")
	if strings.Join(strings.Split(string(env), "\n"), " ") != strings.Join(shellEnv, " ") {
		t.Fatalf("ttyd environment %q", env)
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

	if _, err := f.m.Activity(a.ID, EventClosed); err != nil {
		t.Fatal(err)
	}
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

func TestMissingTTYDLeavesTheGrant(t *testing.T) {
	f := newFixture(t)
	f.m.o.TTYD = filepath.Join(f.dir, "absent")
	took := false
	if _, err := f.m.Open(Request{SessionHash: "aaaaaaaa", Term: "xterm"}, func() error { took = true; return nil }); !errors.Is(err, ErrUnavailable) || took {
		t.Fatalf("open: %v, grant taken %v", err, took)
	}
}

func TestAbsoluteLimit(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	if _, err := f.m.Activity(info.ID, EventConnected); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		f.clk.Advance(IdleTimeout - time.Minute)
		if _, err := f.m.Activity(info.ID, EventInput); err != nil {
			t.Fatalf("input after %d steps: %v", i, err)
		}
		f.m.Sweep()
	}
	st, err := f.m.Activity(info.ID, EventInput)
	if err != nil || st.ExpiresIn != 240 {
		t.Fatalf("status %+v %v", st, err)
	}
	f.clk.Advance(4 * time.Minute)
	f.m.Sweep()
	f.expectGone(info, ReasonExpired)
}

func TestIdleLimitCountsInputOnly(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	f.clk.Advance(10 * time.Second)
	if _, err := f.m.Activity(info.ID, EventConnected); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(10 * time.Minute)
	// A repeated connected report (a reconnecting tunnel, a ping) does not
	// rearm the idle limit.
	if _, err := f.m.Activity(info.ID, EventConnected); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(5*time.Minute - time.Second)
	f.m.Sweep()
	st, err := f.m.Activity(info.ID, EventInput)
	if err != nil || st.IdleIn != int(IdleTimeout/time.Second) {
		t.Fatalf("after input: %+v %v", st, err)
	}
	f.clk.Advance(IdleTimeout - time.Second)
	f.m.Sweep()
	if f.m.Count() != 1 {
		t.Fatal("killed before the idle limit")
	}
	f.clk.Advance(time.Second)
	f.m.Sweep()
	f.expectGone(info, ReasonIdle)
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
	if _, err := f.m.Activity(b.ID, EventConnected); err != nil {
		t.Fatalf("other session's console: %v", err)
	}

	f.m.SessionsEnded("", "admin_reset")
	f.expectGone(b, "admin_reset")
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
	consoletest.Eventually(t, "console gone", func() bool { return f.m.Count() == 0 })
	select {
	case ev := <-f.closed:
		t.Fatalf("a console that never opened reported closed: %+v", ev)
	default:
	}
}

func TestTunnelDisconnectEndsTheConsole(t *testing.T) {
	f := newFixture(t)
	info := f.open("aaaaaaaa")
	conn, err := net.Dial("unix", info.Socket)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	f.expectGone(info, ReasonExited)
}

func TestKillFallsBackToSigkill(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "fake-mode"), []byte(consoletest.ModeIgnoreTerm), 0o600); err != nil {
		t.Fatal(err)
	}
	info := f.open("aaaaaaaa")
	start := time.Now()
	f.m.SessionsEnded("", "admin_reset")
	f.expectGone(info, "admin_reset")
	if d := time.Since(start); d < killGrace {
		t.Fatalf("gone after %v, before the grace: SIGTERM was not ignored", d)
	}
}

func TestNoSocketFailsTheOpen(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "fake-mode"), []byte(consoletest.ModeNoSocket), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Open(Request{SessionHash: "aaaaaaaa", Term: "xterm"}, func() error { return nil }); err == nil {
		t.Fatal("open without a socket succeeded")
	}
	consoletest.Eventually(t, "console gone", func() bool { return f.m.Count() == 0 })
}

func TestShutdownKillsEverything(t *testing.T) {
	f := newFixture(t)
	a := f.open("aaaaaaaa")
	f.m.Shutdown()
	f.expectGone(a, ReasonShutdown)
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

	New(Options{Dir: f.dir, TTYD: f.m.o.TTYD})
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

func TestActivityRejectsUnknown(t *testing.T) {
	f := newFixture(t)
	if _, err := f.m.Activity("0123456789abcdef", EventInput); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown console: %v", err)
	}
	info := f.open("aaaaaaaa")
	if _, err := f.m.Activity(info.ID, "ping"); err == nil {
		t.Fatal("unknown event accepted")
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
