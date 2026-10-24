package console

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

const (
	wsPingPeriod = 30 * time.Second
	wsPongWait   = 60 * time.Second
	wsWriteWait  = 10 * time.Second
	wsReadLimit  = 64 * 1024
	ptyBuffer    = 16 * 1024
	ptyQueue     = 16
)

type initialFrame struct {
	Columns *int `json:"columns"`
	Rows    *int `json:"rows"`
}

type inputFrame struct {
	Type    string `json:"type"`
	Data    string `json:"data"`
	Columns int    `json:"columns"`
	Rows    int    `json:"rows"`
}

// serve listens synchronously, then runs the console until its only client,
// its shell, or a manager limit ends it.
func (m *Manager) serve(c *console, term string) error {
	_ = os.Remove(c.Socket)
	ln, err := net.Listen("unix", c.Socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(c.Socket, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(c.Socket)
		return err
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get(WSPath, func(ctx *fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(ctx) {
			return fiber.ErrNotFound
		}
		m.mu.Lock()
		accepted := !c.serverEnded && !c.claimed
		if accepted {
			c.claimed = true
		}
		m.mu.Unlock()
		if !accepted {
			return fiber.ErrNotFound
		}
		defer c.clientOnce.Do(func() { close(c.clientDone) })
		return ctx.Next()
	}, websocket.New(func(conn *websocket.Conn) {
		m.bridge(c, conn, term)
	}, websocket.Config{
		HandshakeTimeout: wsWriteWait,
		Subprotocols:     []string{WSProtocol},
	}))

	m.mu.Lock()
	c.listener = ln
	m.mu.Unlock()
	go m.run(c, app, ln)
	return nil
}

func (m *Manager) run(c *console, app *fiber.App, ln net.Listener) {
	if err := app.Listener(ln); err != nil {
		logging.WithError(err).Error("console listener stopped")
	}

	m.mu.Lock()
	c.serverEnded = true
	m.killLocked(c, ReasonClosed)
	claimed := c.claimed
	m.mu.Unlock()
	if claimed {
		<-c.clientDone
	}

	m.mu.Lock()
	started := c.cmd != nil
	m.mu.Unlock()
	if started {
		<-c.shellDone
	}

	m.mu.Lock()
	c.closePTY()
	_ = os.Remove(c.Socket)
	c.exited = true
	delete(m.consoles, c.ID)
	info := c.Info
	report := c.started || c.granted
	reason := c.reason
	m.mu.Unlock()

	close(c.done)
	if report {
		m.o.Closed(info, reason)
	}
}

func (m *Manager) bridge(c *console, conn *websocket.Conn, term string) {
	m.attach(c)
	netConn := conn.NetConn()
	stopClosed := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-c.stop:
			_ = netConn.Close()
		case <-stopClosed:
		}
	}()
	defer func() {
		close(stopClosed)
		<-watcherDone
		m.kill(c, ReasonClosed)
		c.closePTY()
		_ = conn.Close()
	}()

	conn.SetReadLimit(wsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(ConnectTimeout))
	kind, data, err := conn.ReadMessage()
	if err != nil || kind != websocket.TextMessage {
		return
	}
	var initial initialFrame
	if err := json.Unmarshal(data, &initial); err != nil || initial.Columns == nil || initial.Rows == nil ||
		!validSize(*initial.Columns, *initial.Rows) {
		return
	}
	if conn.Subprotocol() != WSProtocol {
		return
	}

	select {
	case <-c.stop:
		return
	default:
	}
	cmd := exec.Command(m.o.Shell[0], m.o.Shell[1:]...) //nolint:gosec // fixed daemon configuration
	cmd.Env = append(append([]string{}, shellEnv...), "TERM="+term)
	cmd.Dir = "/root"
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}

	m.mu.Lock()
	if c.reason != "" {
		m.mu.Unlock()
		return
	}
	c.cmd = cmd
	m.mu.Unlock()
	started := make(chan error, 1)
	//nolint:gosec // validSize restricts both dimensions to 1..1000.
	go m.runShell(c, cmd, &pty.Winsize{Cols: uint16(*initial.Columns), Rows: uint16(*initial.Rows)}, started)
	if err := <-started; err != nil {
		m.kill(c, ReasonExited)
		return
	}
	defer c.closePTY()

	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	out := make(chan []byte, ptyQueue)
	go readPTY(c, out)
	inputErr := make(chan error, 1)
	go m.readInput(c, conn, inputErr)

	writeOutput := func(b []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		return conn.WriteMessage(websocket.BinaryMessage, b) == nil
	}
	drainOutput := func() bool {
		if out == nil {
			return true
		}
		for b := range out {
			if !writeOutput(b) {
				return false
			}
		}
		return true
	}
	closeWS := func() {
		m.mu.Lock()
		reason := c.reason
		m.mu.Unlock()
		code := websocket.CloseGoingAway
		if reason == ReasonClosed || reason == ReasonExited {
			code = websocket.CloseNormalClosure
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, "console closed"), time.Now().Add(wsWriteWait))
	}

	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case b, ok := <-out:
			if !ok {
				out = nil
				continue
			}
			if !writeOutput(b) {
				return
			}
		case err := <-inputErr:
			if err != nil {
				return
			}
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
				return
			}
		case <-c.shellDone:
			if drainOutput() {
				closeWS()
			}
			return
		case <-c.stop:
			select {
			case <-c.shellDone:
				if !drainOutput() {
					return
				}
			default:
			}
			closeWS()
			return
		}
	}
}

// runShell keeps the forking thread alive until Wait for Pdeathsig.
func (m *Manager) runShell(c *console, cmd *exec.Cmd, size *pty.Winsize, started chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	master, err := pty.StartWithSize(cmd, size)
	if err != nil {
		started <- err
		m.mu.Lock()
		c.shellExited = true
		m.killLocked(c, ReasonExited)
		m.mu.Unlock()
		close(c.shellDone)
		return
	}
	// A pollable master lets Close interrupt pending reads and hang up the
	// slave even when its foreground process ignores SIGHUP.
	if fd, derr := syscall.Dup(int(master.Fd())); derr == nil {
		_ = master.Close()
		_ = syscall.SetNonblock(fd, true)
		master = os.NewFile(uintptr(fd), "/dev/ptmx")
	}
	m.mu.Lock()
	c.pty = master
	c.pgid = cmd.Process.Pid
	if c.reason != "" {
		m.killLocked(c, c.reason)
	}
	m.mu.Unlock()
	started <- nil

	_ = cmd.Wait()
	c.closePTY()
	m.mu.Lock()
	m.killLocked(c, ReasonExited)
	c.shellExited = true
	m.mu.Unlock()
	close(c.shellDone)
}

func readPTY(c *console, out chan<- []byte) {
	defer close(out)
	for {
		buf := make([]byte, ptyBuffer)
		n, err := c.pty.Read(buf)
		if n > 0 {
			select {
			case out <- buf[:n]:
			default:
				select {
				case out <- buf[:n]:
				case <-c.stop:
					select {
					case <-c.shellDone:
						out <- buf[:n]
					default:
						return
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, syscall.EIO) {
				// util-linux login briefly disconnects the slave around vhangup.
				select {
				case <-c.shellDone:
					return
				case <-c.stop:
					return
				case <-time.After(20 * time.Millisecond):
					continue
				}
			}
			return
		}
	}
}

func (m *Manager) readInput(c *console, conn *websocket.Conn, failed chan<- error) {
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			failed <- err
			return
		}
		switch kind {
		case websocket.BinaryMessage:
			if len(data) > 0 {
				m.touch(c)
			}
			if _, err := c.pty.Write(data); err != nil {
				failed <- err
				return
			}
		case websocket.TextMessage:
			var frame inputFrame
			if json.Unmarshal(data, &frame) != nil {
				continue
			}
			switch frame.Type {
			case "input":
				if frame.Data != "" {
					m.touch(c)
				}
				if _, err := io.WriteString(c.pty, frame.Data); err != nil {
					failed <- err
					return
				}
			case "resize":
				if validSize(frame.Columns, frame.Rows) {
					//nolint:gosec // validSize restricts both dimensions to 1..1000.
					_ = pty.Setsize(c.pty, &pty.Winsize{Cols: uint16(frame.Columns), Rows: uint16(frame.Rows)})
				}
			}
		}
	}
}

func validSize(columns, rows int) bool {
	return columns > 0 && columns <= 1000 && rows > 0 && rows <= 1000
}
