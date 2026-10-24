package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/app/console"
	"github.com/calaos/calaos-container/app/console/consoletest"
)

func realConsoleApp(t *testing.T) (adminClient, *testClock, string) {
	t.Helper()
	t.Setenv("CALAOS_TEST_LEAK", "leaked")
	c, clk, s, _ := adminWithApprover(t)
	t.Cleanup(c.a.consoles.Shutdown)
	return c, clk, s
}

func consoleDialer(socket, protocol string) gws.Dialer {
	return gws.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		Subprotocols:     []string{protocol},
		HandshakeTimeout: 5 * time.Second,
	}
}

func dialConsole(t *testing.T, d consoleOpened) *gws.Conn {
	t.Helper()
	dialer := consoleDialer(d.Socket, d.WSProtocol)
	conn, resp, err := dialer.Dial("ws://localhost"+d.WSPath, http.Header{})
	if err != nil {
		t.Fatalf("dial %s: %v (%v)", d.Socket, err, resp)
	}
	if conn.Subprotocol() != console.WSProtocol {
		t.Fatalf("subprotocol %q", conn.Subprotocol())
	}
	if err := conn.WriteMessage(gws.TextMessage, []byte(`{"columns":120,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, regexp.MustCompile(`# `))
	return conn
}

func writeConsoleJSON(t *testing.T, conn *gws.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(gws.TextMessage, data); err != nil {
		t.Fatal(err)
	}
}

func send(t *testing.T, conn *gws.Conn, line string) {
	t.Helper()
	writeConsoleJSON(t, conn, map[string]string{"type": "input", "data": line + "\r"})
}

// readUntil returns the terminal output up to the first match of re.
func readUntil(t *testing.T, conn *gws.Conn, re *regexp.Regexp) []string {
	t.Helper()
	var out strings.Builder
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		kind, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading the terminal: %v; got %q", err, out.String())
		}
		if kind != gws.BinaryMessage {
			t.Fatalf("terminal output used text frame %q", msg)
		}
		out.Write(msg)
		if m := re.FindStringSubmatch(out.String()); m != nil {
			return m
		}
	}
}

func bridgeGoroutines(t *testing.T) int {
	t.Helper()
	var profile bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&profile, 1); err != nil {
		t.Fatal(err)
	}
	return bytes.Count(profile.Bytes(), []byte("console.readPTY(")) +
		bytes.Count(profile.Bytes(), []byte("console.(*Manager).readInput("))
}

func TestConsoleRunsAndIsKilled(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	if fi, err := os.Lstat(d.Socket); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v", err)
	}

	conn := dialConsole(t, d)
	defer func() { _ = conn.Close() }()
	send(t, conn, "echo o''k")
	readUntil(t, conn, regexp.MustCompile(`ok\r\n`))
	send(t, conn, `echo "R=$(id -u) T=$TERM H=$HOME L=${CALAOS_TEST_LEAK:-none} P=$$ S=$(shopt -q login_shell && echo login) Z=$(stty size)"`)
	m := readUntil(t, conn, regexp.MustCompile(`R=(\d+) T=(\S+) H=(\S+) L=(\S+) P=(\d+) S=(\S+) Z=(\d+) (\d+)`))
	if m[1] != "0" || m[2] != "xterm-256color" || m[3] != "/root" || m[4] != "none" || m[6] != "login" || m[7] != "40" || m[8] != "120" {
		t.Fatalf("shell identity %q", m[0])
	}
	shell, _ := strconv.Atoi(m[5])

	secondDialer := consoleDialer(d.Socket, d.WSProtocol)
	second, _, err := secondDialer.Dial("ws://localhost/ws", nil)
	if err == nil {
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

func TestConsoleExpiryHangsUpIgnoringShell(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	conn := dialConsole(t, d)
	defer func() { _ = conn.Close() }()
	send(t, conn, `trap '' HUP TERM; echo P=$$`)
	m := readUntil(t, conn, regexp.MustCompile(`P=(\d+)`))
	shell, _ := strconv.Atoi(m[1])

	clk.Advance(console.MaxLifetime)
	c.a.consoles.Sweep()
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=expired")
	consoletest.Eventually(t, "signal-ignoring shell gone", func() bool { return !consoletest.Alive(shell) })
	consoletest.Eventually(t, "bridge goroutines gone", func() bool { return bridgeGoroutines(t) == 0 })
}

func TestConsoleResizeAndBinaryInput(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	conn := dialConsole(t, d)
	defer func() { _ = conn.Close() }()

	writeConsoleJSON(t, conn, map[string]any{"type": "resize", "columns": 80, "rows": 25})
	send(t, conn, "stty size")
	readUntil(t, conn, regexp.MustCompile(`25 80\r\n`))
	for _, size := range []map[string]any{
		{"type": "resize", "columns": 0, "rows": 20},
		{"type": "resize", "columns": 2000, "rows": 20},
		{"type": "resize", "columns": -1, "rows": 20},
		{"type": "resize", "columns": 2147483647, "rows": 20},
	} {
		writeConsoleJSON(t, conn, size)
	}
	_ = conn.WriteMessage(gws.TextMessage, []byte(`not json`))
	writeConsoleJSON(t, conn, map[string]string{"type": "unknown"})
	if err := conn.WriteMessage(gws.BinaryMessage, []byte("stty size\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, regexp.MustCompile(`25 80\r\n`))

	send(t, conn, `printf '\342\202'; sleep 0.1; printf '\254'; echo`)
	readUntil(t, conn, regexp.MustCompile(`€\r\n`))
}

func TestConsoleTunnelCloseEndsTheConsole(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	conn := dialConsole(t, d)
	send(t, conn, "echo P=$$")
	m := readUntil(t, conn, regexp.MustCompile(`P=(\d+)`))
	shell, _ := strconv.Atoi(m[1])
	_ = conn.Close()

	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=closed")
	consoletest.Eventually(t, "shell gone", func() bool { return !consoletest.Alive(shell) })
	if _, err := os.Lstat(d.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket after client close: %v", err)
	}
}

func TestConsoleShellExit(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	conn := dialConsole(t, d)
	defer func() { _ = conn.Close() }()
	send(t, conn, "echo last-output; exit")
	readUntil(t, conn, regexp.MustCompile(`last-output\r\n`))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var closeErr error
	for {
		if _, _, closeErr = conn.ReadMessage(); closeErr != nil {
			break
		}
	}
	if err, ok := closeErr.(*gws.CloseError); !ok || err.Code != gws.CloseNormalClosure {
		t.Fatalf("shell exit close: %v", closeErr)
	}
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=exited")
}

func TestConsoleResetKillsTheConsole(t *testing.T) {
	c, clk, s := realConsoleApp(t)
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

func TestConsoleInputBeforeInitIsRejected(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	dialer := consoleDialer(d.Socket, d.WSProtocol)
	conn, _, err := dialer.Dial("ws://localhost/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	writeConsoleJSON(t, conn, map[string]string{"type": "input", "data": "id\r"})
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("input before initialization was accepted")
	}
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=closed")
}

func TestConsoleSlowClientTimesOut(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	conn := dialConsole(t, d)
	defer func() { _ = conn.Close() }()
	send(t, conn, "echo P=$$")
	m := readUntil(t, conn, regexp.MustCompile(`P=(\d+)`))
	shell, _ := strconv.Atoi(m[1])
	send(t, conn, "yes")

	want := "action=console_close console=" + d.ConsoleID + " reason=closed"
	deadline := time.Now().Add(20 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(adminDataDir, "audit.log"))
		if strings.Contains(string(b), want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow client was not disconnected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	consoletest.Eventually(t, "slow client's shell gone", func() bool { return !consoletest.Alive(shell) })
}

func TestConsoleServesOnlyWebSocketPath(t *testing.T) {
	c, clk, s := realConsoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", d.Socket)
	}}}
	for _, path := range []string{"/", "/ws", "/token"} {
		resp, err := client.Get(fmt.Sprintf("http://localhost%s", path))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: %s", path, resp.Status)
		}
	}
}
