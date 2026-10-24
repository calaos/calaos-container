// Package consoletest is a fake ttyd for the tests: the test binary runs it
// when started through a symlink named FakeName. Only _test files import it.
package consoletest

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const FakeName = "fake-ttyd"

// Modes, written to <socket dir>/fake-mode before a console opens.
const (
	ModeNoSocket   = "nosocket"   // never listens
	ModeIgnoreTerm = "ignoreterm" // needs SIGKILL
)

// MaybeRun turns the process into the fake when it was started as one. Call
// it first in TestMain.
func MaybeRun() {
	if filepath.Base(os.Args[0]) != FakeName {
		return
	}
	// Not os.Exit: under -race it sleeps a second before exiting.
	syscall.Exit(fake(os.Args[1:]))
}

// Install links the running test binary as the fake and returns its path.
func Install(t testing.TB) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), FakeName)
	if err := os.Symlink(self, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func fake(args []string) int {
	if len(args) == 1 && args[0] == "--help" {
		_, _ = os.Stdout.WriteString("    -W, --writable\n    -P, --ping-interval\n")
		return 0
	}
	if len(args) == 2 && args[0] == "--child" {
		_ = os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600) //nolint:gosec // test directory
		time.Sleep(time.Hour)
		return 0
	}
	var sock string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-i" {
			sock = args[i+1]
		}
	}
	if sock == "" {
		return 2
	}
	_ = os.WriteFile(sock+".args", []byte(strings.Join(args, "\n")), 0o600)        //nolint:gosec // test directory
	_ = os.WriteFile(sock+".env", []byte(strings.Join(os.Environ(), "\n")), 0o600) //nolint:gosec // test directory
	_ = os.WriteFile(sock+".pid", []byte(strconv.Itoa(os.Getpid())), 0o600)        //nolint:gosec // test directory
	mode, _ := os.ReadFile(filepath.Join(filepath.Dir(sock), "fake-mode"))         //nolint:gosec // test directory

	if strings.TrimSpace(string(mode)) == ModeIgnoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	// A grandchild in ttyd's process group, as the group kill must reach.
	child := exec.Command(os.Args[0], "--child", sock+".child") //nolint:gosec // test binary
	if err := child.Start(); err != nil {
		return 3
	}
	if strings.TrimSpace(string(mode)) == ModeNoSocket {
		time.Sleep(time.Hour)
		return 0
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		return 4
	}
	// --once: one client, exit when it leaves (without removing the socket,
	// as ttyd does).
	conn, err := l.Accept()
	if err != nil {
		return 5
	}
	buf := make([]byte, 512)
	for {
		if _, err := conn.Read(buf); err != nil {
			return 0
		}
	}
}

// Alive reports whether pid runs (a zombie does not).
func Alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}

// ReadPid waits for a pid file the fake writes.
func ReadPid(t testing.TB, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(path) //nolint:gosec // test directory
		if err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Eventually polls cond for up to 5 s.
func Eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
