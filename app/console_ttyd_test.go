//go:build ttyd

// Run with a real ttyd, as root: CALAOS_TEST_TTYD=/usr/bin/ttyd go test -tags ttyd -run TTYD ./app/
package app

import (
	"context"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/app/console"
	"github.com/calaos/calaos-container/app/console/consoletest"
)

func realTTYDApp(t *testing.T) (adminClient, *testClock, string) {
	t.Helper()
	bin := os.Getenv("CALAOS_TEST_TTYD")
	if bin == "" {
		t.Skip("CALAOS_TEST_TTYD not set")
	}
	prev := consoleTTYD
	consoleTTYD = bin
	t.Cleanup(func() { consoleTTYD = prev })
	// Must not reach the shell.
	t.Setenv("CALAOS_TEST_LEAK", "leaked")
	c, clk, s, _ := adminWithApprover(t)
	t.Cleanup(c.a.consoles.Shutdown)
	return c, clk, s
}

func dialConsole(t *testing.T, d consoleOpened) *gws.Conn {
	t.Helper()
	dialer := gws.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", d.Socket)
		},
		Subprotocols:     []string{d.WSProtocol},
		HandshakeTimeout: 5 * time.Second,
	}
	conn, resp, err := dialer.Dial("ws://localhost"+d.WSPath, http.Header{})
	if err != nil {
		t.Fatalf("dial %s: %v (%v)", d.Socket, err, resp)
	}
	if conn.Subprotocol() != console.WSProtocol {
		t.Fatalf("subprotocol %q", conn.Subprotocol())
	}
	// ttyd starts the command on this first message.
	if err := conn.WriteMessage(gws.BinaryMessage, []byte(`{"AuthToken":"","columns":120,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func send(t *testing.T, conn *gws.Conn, line string) {
	t.Helper()
	if err := conn.WriteMessage(gws.BinaryMessage, append([]byte{'0'}, line+"\r"...)); err != nil {
		t.Fatal(err)
	}
}

// readUntil returns the terminal output up to the first match of re.
func readUntil(t *testing.T, conn *gws.Conn, re *regexp.Regexp) []string {
	t.Helper()
	var out strings.Builder
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading the terminal: %v; got %q", err, out.String())
		}
		if len(msg) > 0 && msg[0] == '0' {
			out.Write(msg[1:])
		}
		if m := re.FindStringSubmatch(out.String()); m != nil {
			return m
		}
	}
}

func TestTTYDConsoleRunsAndIsKilled(t *testing.T) {
	c, clk, s := realTTYDApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	if fi, err := os.Lstat(d.Socket); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v", err)
	}
	p, b := activity(d.ConsoleID, console.EventConnected)
	c.expect("POST", p, b, nil, 200, "")

	conn := dialConsole(t, d)
	defer func() { _ = conn.Close() }()
	send(t, conn, "echo ok")
	readUntil(t, conn, regexp.MustCompile(`[\r\n]ok\r\n`))
	send(t, conn, `echo "R=$(id -u) T=$TERM H=$HOME L=${CALAOS_TEST_LEAK:-none} P=$$ S=$(shopt -q login_shell && echo login)"`)
	m := readUntil(t, conn, regexp.MustCompile(`R=(\d+) T=(\S+) H=(\S+) L=(\S+) P=(\d+) S=(\S+)`))
	if m[1] != "0" || m[2] != "xterm-256color" || m[3] != "/root" || m[4] != "none" || m[6] != "login" {
		t.Fatalf("shell identity %q", m[0])
	}
	shell, _ := strconv.Atoi(m[5])

	// --max-clients 1 and --once: nobody joins a live console.
	dialer := gws.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", d.Socket)
	}, Subprotocols: []string{"tty"}, HandshakeTimeout: 2 * time.Second}
	if second, _, err := dialer.Dial("ws://localhost/ws", nil); err == nil {
		_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, rerr := second.ReadMessage(); rerr == nil {
			t.Fatal("a second client was served")
		}
		_ = second.Close()
	}

	clk.Advance(console.MaxLifetime)
	c.a.consoles.Sweep()
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=expired")
	consoletest.Eventually(t, "shell gone", func() bool { return !consoletest.Alive(shell) })
	if _, err := os.Lstat(d.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket after the kill: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

func TestTTYDTunnelCloseEndsTheConsole(t *testing.T) {
	c, clk, s := realTTYDApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	conn := dialConsole(t, d)
	send(t, conn, "echo P=$$")
	m := readUntil(t, conn, regexp.MustCompile(`P=(\d+)`))
	shell, _ := strconv.Atoi(m[1])
	_ = conn.Close()

	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=exited")
	consoletest.Eventually(t, "shell gone", func() bool { return !consoletest.Alive(shell) })
	if _, err := os.Lstat(d.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket after ttyd exited: %v", err)
	}
}

func TestTTYDResetKillsTheConsole(t *testing.T) {
	c, clk, s := realTTYDApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	conn := dialConsole(t, d)
	defer func() { _ = conn.Close() }()
	send(t, conn, "echo P=$$")
	m := readUntil(t, conn, regexp.MustCompile(`P=(\d+)`))
	shell, _ := strconv.Atoi(m[1])

	c.expect("POST", "/api/v2/admin/reset", "", nil, 200, "")
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=admin_reset")
	consoletest.Eventually(t, "shell gone", func() bool { return !consoletest.Alive(shell) })
}
