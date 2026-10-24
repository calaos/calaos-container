// Package console runs root login shells in ptys behind private Unix
// WebSockets. The daemon gates, bounds and audits each console.
package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	logger "github.com/calaos/calaos-container/log"
)

const (
	MaxConsoles    = 2
	MaxLifetime    = time.Hour
	IdleTimeout    = 15 * time.Minute
	ConnectTimeout = 30 * time.Second

	WSPath     = "/ws"
	WSProtocol = "calaos-console.v1"

	// Close reasons, as written to the audit log.
	ReasonClosed       = "closed"
	ReasonExited       = "exited"
	ReasonExpired      = "expired"
	ReasonIdle         = "idle"
	ReasonNotConnected = "not_connected"
	ReasonShutdown     = "shutdown"

	killGrace  = 2 * time.Second
	sockPrefix = "console-"
	sockSuffix = ".sock"
)

// Terms is the TERM whitelist; the first one is the default.
var Terms = []string{"xterm-256color", "xterm", "vt100"}

// The shell gets this environment and nothing of the daemon's, plus TERM.
var shellEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"HOME=/root",
	"USER=root",
	"LOGNAME=root",
	"SHELL=/bin/bash",
	"LANG=C.UTF-8",
}

var (
	ErrLimit    = errors.New("too many consoles")
	ErrNotFound = errors.New("unknown console")
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
	Shell []string // the login command run in the pty
	Now   func() time.Time
	// Closed is called once per granted console, after it is gone.
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
	session     string
	granted     bool
	started     bool
	exited      bool
	attached    time.Time
	lastInput   time.Time
	reason      string
	done        chan struct{}
	stop        chan struct{}
	listener    net.Listener
	cmd         *exec.Cmd
	pty         *os.File
	pgid        int
	shellDone   chan struct{}
	clientDone  chan struct{}
	clientOnce  sync.Once
	ptyOnce     sync.Once
	serverEnded bool
	claimed     bool
	termSent    bool
	shellExited bool
}

func (c *console) closePTY() {
	c.ptyOnce.Do(func() {
		if c.pty != nil {
			_ = c.pty.Close()
		}
	})
}

type Manager struct {
	o       Options
	initErr error

	mu       sync.Mutex
	consoles map[string]*console
}

// New prepares the socket directory and removes the sockets of a previous
// run. It never replaces the directory, which calaos_server bind-mounts.
func New(o Options) *Manager {
	if len(o.Shell) == 0 {
		o.Shell = []string{"/bin/login", "-f", "root"}
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
		ch := s[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// Open reserves a slot, consumes the grant, then starts the private listener.
func (m *Manager) Open(req Request, take func() error) (Info, error) {
	if m.initErr != nil {
		return Info{}, m.initErr
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
		session:    req.SessionHash,
		done:       make(chan struct{}),
		stop:       make(chan struct{}),
		shellDone:  make(chan struct{}),
		clientDone: make(chan struct{}),
	}

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
	m.mu.Lock()
	c.granted = true
	m.mu.Unlock()

	if err := m.serve(c, req.Term); err != nil {
		m.mu.Lock()
		delete(m.consoles, id)
		c.exited = true
		info := c.Info
		m.mu.Unlock()
		close(c.done)
		m.o.Closed(info, ReasonExited)
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
		return Info{}, errors.New("console exited at start")
	}
	c.started = true
	c.Opened = m.o.Now()
	info := c.Info
	m.mu.Unlock()
	return info, nil
}

func (m *Manager) kill(c *console, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killLocked(c, reason)
}

// killLocked never blocks: SessionsEnded calls it under the admin lock.
func (m *Manager) killLocked(c *console, reason string) {
	if c.reason == "" && reason != "" {
		c.reason = reason
		close(c.stop)
	}
	if c.listener != nil {
		_ = c.listener.Close()
	}
	_ = os.Remove(c.Socket)
	if c.pgid == 0 || c.shellExited || c.termSent {
		return
	}
	c.termSent = true
	pgid := c.pgid
	_ = syscall.Kill(-pgid, syscall.SIGHUP)
	go func() {
		select {
		case <-c.done:
		case <-time.After(killGrace):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()
}

func (m *Manager) attach(c *console) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.attached.IsZero() {
		c.attached = m.o.Now()
	}
}

func (m *Manager) touch(c *console) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.o.Now()
	if c.attached.IsZero() {
		c.attached = now
	}
	c.lastInput = now
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

func (c *console) lastActive() time.Time {
	if c.lastInput.After(c.attached) {
		return c.lastInput
	}
	return c.attached
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
