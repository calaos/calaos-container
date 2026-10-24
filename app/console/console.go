// Package console runs the root web consoles: one ttyd per console, listening
// on a Unix socket that calaos_server tunnels the browser WebSocket to. The
// daemon only gates and bounds them; ttyd is the terminal (docs/API.md,
// Console).
package console

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	logger "github.com/calaos/calaos-container/log"
)

const (
	DefaultTTYD = "/usr/bin/ttyd"

	MaxConsoles    = 2
	MaxLifetime    = time.Hour
	IdleTimeout    = 15 * time.Minute
	ConnectTimeout = 30 * time.Second

	// WSPath and WSProtocol are what ttyd 1.7 serves on its socket.
	WSPath     = "/ws"
	WSProtocol = "tty"

	EventConnected = "connected"
	EventInput     = "input"
	EventClosed    = "closed"

	// Close reasons, as written to the audit log.
	ReasonClosed       = "closed"
	ReasonExited       = "exited"
	ReasonExpired      = "expired"
	ReasonIdle         = "idle"
	ReasonNotConnected = "not_connected"
	ReasonShutdown     = "shutdown"

	pingInterval = "30"
	startTimeout = 5 * time.Second
	killGrace    = 2 * time.Second
	sockPrefix   = "console-"
	sockSuffix   = ".sock" // ttyd only takes -i as a socket path with this suffix
)

// Terms is the TERM whitelist; the first one is the default.
var Terms = []string{"xterm-256color", "xterm", "vt100"}

// The shell gets this environment and nothing of the daemon's; ttyd adds TERM.
var shellEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"HOME=/root",
	"USER=root",
	"LOGNAME=root",
	"SHELL=/bin/bash",
	"LANG=C.UTF-8",
}

var (
	ErrLimit       = errors.New("too many consoles")
	ErrUnavailable = errors.New("ttyd is not installed")
	ErrNotFound    = errors.New("unknown console")
	// ErrEnded: the session was invalidated while its console started.
	ErrEnded = errors.New("session ended")
)

var logging *logrus.Entry

func init() {
	logging = logger.NewLogger("console")
}

func ValidTerm(t string) bool {
	for _, v := range Terms {
		if t == v {
			return true
		}
	}
	return false
}

type Options struct {
	Dir   string   // socket directory, bind-mounted into calaos_server
	TTYD  string   // ttyd binary
	Shell []string // the command ttyd runs
	Now   func() time.Time
	// Closed is called once per console that started, after it is gone.
	Closed func(info Info, reason string)
}

// Request opens a console for a session; the caller has validated Term.
type Request struct {
	SessionHash string
	AuditID     string
	Client      string
	Term        string
}

// Info describes a console. It never carries the session itself.
type Info struct {
	ID      string
	Socket  string
	AuditID string
	Client  string
	Opened  time.Time
}

type console struct {
	Info
	session   string // hash
	pgid      int
	started   bool
	exited    bool
	attached  time.Time
	lastInput time.Time
	reason    string
	done      chan struct{}
}

type Manager struct {
	o       Options
	initErr error

	mu       sync.Mutex
	consoles map[string]*console
}

// New prepares the socket directory and removes the sockets of a previous
// run: their ttyd died with it (Pdeathsig). It never fails: a broken
// directory only makes Open fail.
func New(o Options) *Manager {
	if o.TTYD == "" {
		o.TTYD = DefaultTTYD
	}
	if len(o.Shell) == 0 {
		o.Shell = []string{"/bin/bash", "--login"}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Closed == nil {
		o.Closed = func(Info, string) {}
	}
	m := &Manager{o: o, consoles: map[string]*console{}}
	if err := m.init(); err != nil {
		m.initErr = fmt.Errorf("console directory %s: %w", o.Dir, err)
		logging.Errorln(m.initErr)
	}
	return m
}

func (m *Manager) init() error {
	if err := prepareDir(m.o.Dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.o.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), sockPrefix) && strings.HasSuffix(e.Name(), sockSuffix) {
			_ = os.Remove(filepath.Join(m.o.Dir, e.Name()))
		}
	}
	return nil
}

// prepareDir never removes the directory: calaos_server bind-mounts it, and a
// new directory would not be seen through the mount.
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return os.Chmod(dir, 0o700) //nolint:gosec // a directory needs x
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ValidID matches a console id.
func ValidID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Open reserves a slot, runs take (which consumes the console_open grant),
// then starts ttyd. A refused slot or a missing ttyd leaves the grant alone.
func (m *Manager) Open(req Request, take func() error) (Info, error) {
	if m.initErr != nil {
		return Info{}, m.initErr
	}
	if fi, err := os.Stat(m.o.TTYD); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return Info{}, ErrUnavailable
	}
	id, err := newID()
	if err != nil {
		return Info{}, err
	}
	c := &console{
		Info: Info{
			ID:      id,
			Socket:  filepath.Join(m.o.Dir, sockPrefix+id+sockSuffix),
			AuditID: req.AuditID,
			Client:  req.Client,
		},
		session: req.SessionHash,
		done:    make(chan struct{}),
	}

	// Registered before the grant is taken, so an invalidation from then on
	// marks it and the start below gives up.
	m.mu.Lock()
	if len(m.consoles) >= MaxConsoles {
		m.mu.Unlock()
		return Info{}, ErrLimit
	}
	if _, taken := m.consoles[id]; taken {
		m.mu.Unlock()
		return Info{}, errors.New("console id collision")
	}
	m.consoles[id] = c
	m.mu.Unlock()

	release := func() {
		m.mu.Lock()
		delete(m.consoles, id)
		m.mu.Unlock()
	}
	if err := take(); err != nil {
		release()
		return Info{}, err
	}

	// On failure the slot is freed once ttyd is reaped, not before.
	if err := m.start(c, req.Term); err != nil {
		return Info{}, err
	}

	m.mu.Lock()
	if c.reason != "" {
		m.killLocked(c, c.reason)
		m.mu.Unlock()
		return Info{}, ErrEnded
	}
	if c.exited {
		m.mu.Unlock()
		return Info{}, errors.New("ttyd exited at start")
	}
	c.started = true
	c.Opened = m.o.Now()
	info := c.Info
	m.mu.Unlock()
	return info, nil
}

func (m *Manager) args(c *console, term string) []string {
	a := []string{
		"-i", c.Socket,
		"--once",
		"--max-clients", "1",
		"--terminal-type", term,
		"--cwd", "/root",
		// Errors and warnings only: nothing of the terminal is ever logged.
		"--debug", "3",
	}
	help := m.help()
	// ttyd before 1.7.4 is writable by default and does not know the flag.
	if bytes.Contains(help, []byte("--writable")) {
		a = append(a, "--writable")
	}
	// Only built against libwebsockets 4.
	if bytes.Contains(help, []byte("--ping-interval")) {
		a = append(a, "--ping-interval", pingInterval)
	}
	return append(a, m.o.Shell...)
}

func (m *Manager) help() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.o.TTYD, "--help") //nolint:gosec // daemon-configured path
	cmd.Env = shellEnv
	out, _ := cmd.CombinedOutput()
	return out
}

// start runs ttyd and returns once its socket is up and 0600.
func (m *Manager) start(c *console, term string) error {
	_ = os.Remove(c.Socket)
	cmd := exec.Command(m.o.TTYD, m.args(c, term)...) //nolint:gosec // daemon-configured path
	cmd.Env = shellEnv
	cmd.Dir = "/"
	lw := &lineLogger{prefix: "console " + c.ID + ": ttyd: "}
	cmd.Stdout = lw
	cmd.Stderr = lw
	// Something left holding ttyd's stderr must not keep Wait, and the
	// cleanup behind it, waiting.
	cmd.WaitDelay = time.Second
	// Pdeathsig: KillMode=process leaves children alive when the daemon
	// stops, and a console whose daemon is gone has no limit left.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}

	started := make(chan error, 1)
	go m.run(c, cmd, started)
	if err := <-started; err != nil {
		return err
	}

	deadline := time.Now().Add(startTimeout)
	for {
		select {
		case <-c.done:
			return errors.New("ttyd exited at start")
		default:
		}
		fi, err := os.Lstat(c.Socket)
		if err == nil && fi.Mode()&os.ModeSocket != 0 {
			// The directory is 0700 already; the mode is a second barrier.
			if err := os.Chmod(c.Socket, 0o600); err != nil {
				m.kill(c, ReasonExited)
				return err
			}
			return nil
		}
		if time.Now().After(deadline) {
			m.kill(c, ReasonExited)
			return errors.New("ttyd socket did not appear")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// run keeps its OS thread for ttyd's lifetime: Pdeathsig fires when the
// thread that forked exits, not the process.
func (m *Manager) run(c *console, cmd *exec.Cmd, started chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		delete(m.consoles, c.ID)
		m.mu.Unlock()
		close(c.done)
		started <- err
		return
	}
	m.mu.Lock()
	c.pgid = cmd.Process.Pid
	m.mu.Unlock()
	started <- nil

	_ = cmd.Wait()

	m.mu.Lock()
	c.exited = true
	// Whatever ttyd left in its group goes with it.
	_ = syscall.Kill(-c.pgid, syscall.SIGKILL)
	reason := c.reason
	if reason == "" {
		reason = ReasonExited
	}
	wasStarted := c.started
	delete(m.consoles, c.ID)
	info := c.Info
	m.mu.Unlock()

	if fi, err := os.Lstat(c.Socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(c.Socket)
	}
	close(c.done)
	if wasStarted {
		m.o.Closed(info, reason)
	}
}

func (m *Manager) kill(c *console, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killLocked(c, reason)
}

// killLocked never blocks: SessionsEnded calls it under the admin lock.
func (m *Manager) killLocked(c *console, reason string) {
	if c.reason == "" {
		c.reason = reason
	}
	if c.pgid == 0 || c.exited {
		return
	}
	pgid := c.pgid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	go func() {
		select {
		case <-c.done:
		case <-time.After(killGrace):
			m.mu.Lock()
			if !c.exited {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			m.mu.Unlock()
		}
	}()
}

// SessionsEnded kills the consoles of an invalidated session, or every
// console when sessionHash is "". Wired to admin.Options.SessionsEnded.
func (m *Manager) SessionsEnded(sessionHash, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.consoles {
		if sessionHash == "" || c.session == sessionHash {
			m.killLocked(c, reason)
		}
	}
}

// Status is what an activity report answers.
type Status struct {
	ExpiresIn int `json:"expires_in"`
	IdleIn    int `json:"idle_in"`
}

// Activity records what the tunnel reports: its connection, input sent to
// the terminal (never pings or resizes), or its end.
func (m *Manager) Activity(id, event string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.consoles[id]
	if c == nil || !c.started || c.reason != "" {
		return Status{}, ErrNotFound
	}
	now := m.o.Now()
	switch event {
	case EventConnected:
		if c.attached.IsZero() {
			c.attached = now
		}
	case EventInput:
		if c.attached.IsZero() {
			c.attached = now
		}
		c.lastInput = now
	case EventClosed:
		m.killLocked(c, ReasonClosed)
		return Status{}, nil
	default:
		return Status{}, fmt.Errorf("unknown console event %q", event)
	}
	return Status{
		ExpiresIn: seconds(c.Opened.Add(MaxLifetime).Sub(now)),
		IdleIn:    seconds(c.lastActive().Add(IdleTimeout).Sub(now)),
	}, nil
}

func (c *console) lastActive() time.Time {
	if c.lastInput.After(c.attached) {
		return c.lastInput
	}
	return c.attached
}

func seconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// Sweep applies the time limits; Run calls it every second.
func (m *Manager) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.o.Now()
	for _, c := range m.consoles {
		if !c.started || c.reason != "" {
			continue
		}
		switch {
		case !now.Before(c.Opened.Add(MaxLifetime)):
			m.killLocked(c, ReasonExpired)
		case c.attached.IsZero():
			if !now.Before(c.Opened.Add(ConnectTimeout)) {
				m.killLocked(c, ReasonNotConnected)
			}
		case !now.Before(c.lastActive().Add(IdleTimeout)):
			m.killLocked(c, ReasonIdle)
		}
	}
}

// Run sweeps until ctx ends, then kills every console and waits for them.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.Sweep()
		case <-ctx.Done():
			m.Shutdown()
			return
		}
	}
}

// Shutdown kills every console and waits, at most past the kill grace.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	var done []chan struct{}
	for _, c := range m.consoles {
		m.killLocked(c, ReasonShutdown)
		done = append(done, c.done)
	}
	m.mu.Unlock()
	limit := time.After(killGrace + time.Second)
	for _, d := range done {
		select {
		case <-d:
		case <-limit:
			logging.Warnln("consoles still running at shutdown")
			return
		}
	}
}

// Count is the number of consoles, starting ones included.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.consoles)
}

// lineLogger forwards ttyd's own messages; at --debug 3 they never contain
// terminal data.
type lineLogger struct {
	prefix string
	mu     sync.Mutex
	buf    []byte
}

const maxLogLine = 256

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) > maxLogLine {
				l.emit(l.buf)
				l.buf = l.buf[:0]
			}
			return len(p), nil
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
}

func (l *lineLogger) emit(b []byte) {
	if len(b) > maxLogLine {
		b = b[:maxLogLine]
	}
	s := strings.TrimSpace(string(bytes.ToValidUTF8(b, []byte("?"))))
	switch {
	case s == "":
	case strings.Contains(s, "] N: "): // libwebsockets notice
		logging.Infoln(l.prefix + s)
	default:
		logging.Warnln(l.prefix + s)
	}
}

var _ io.Writer = (*lineLogger)(nil)
