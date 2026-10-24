// Package consoletest provides a fake shell and process helpers for pty tests.
package consoletest

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const ModeIgnoreHUP = "ignorehup"

func Shell(dir string) []string {
	const script = `dir=$1
sock=$(echo "$dir"/console-*.sock)
printf %s "$dir" > "$sock.args"
env > "$sock.env"
printf %s "$$" > "$sock.pid"
if [ -f "$dir/fake-mode" ]; then trap '' HUP; fi
sleep 3600 &
printf %s "$!" > "$sock.child"
wait
`
	return []string{"/bin/sh", "-c", script, "console-test", dir}
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
