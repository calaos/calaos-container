// Package admin keeps the admin password, the admin sessions, the persisted
// admin alerts and the audit log (docs/API.md, admin routes).
package admin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sirupsen/logrus"

	logger "github.com/calaos/calaos-container/log"
)

const (
	DefaultDataDir    = "/mnt/calaos/admin"
	DefaultRuntimeDir = "/run/calaos-container"

	MinPasswordLength = 10   // Unicode code points
	MaxPasswordBytes  = 1024 // UTF-8 bytes

	EventAdminSet   = "admin_set"
	EventAdminReset = "admin_reset"

	hashFileName    = "password.hash"
	alertsFileName  = "alerts.json"
	auditFileName   = "audit.log"
	sessionFileName = "sessions.json"
)

// Mode is the admin/set mode.
type Mode string

const (
	ModeBootstrap Mode = "bootstrap"
	ModeChange    Mode = "change"
	ModeRecovery  Mode = "recovery"
)

var (
	ErrNotSet      = errors.New("admin password not set")
	ErrAlreadySet  = errors.New("admin password already set")
	ErrBadPassword = errors.New("wrong admin password")
	ErrBadSession  = errors.New("admin session invalid")
	ErrTooShort    = errors.New("admin password too short")
	ErrTooLong     = errors.New("admin password too long")
)

// RateLimitedError is returned when the limiter refuses an operation.
type RateLimitedError struct{ RetryAfter int }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited, retry after %ds", e.RetryAfter)
}

type Options struct {
	DataDir    string // hash, alerts and audit log: persistent, not a subvolume
	RuntimeDir string // sessions: survives a daemon restart, not a reboot
	Hash       HashParams
	Publish    func(evType string, data any)
	Now        func() time.Time
}

type Manager struct {
	hashPath string
	params   HashParams
	publish  func(string, any)
	now      func() time.Time
	initErr  error

	limiter *limiter
	audit   auditLog

	// mu serializes every change of the hash file, the sessions and the
	// alerts. argon2id never runs under it.
	mu       sync.Mutex
	sessions sessionStore
	alerts   alertStore
}

var logging *logrus.Entry

func init() {
	logging = logger.NewLogger("admin")
}

// New never fails: a broken data or runtime directory must not take the
// legacy API down, so every admin operation reports it instead.
func New(o Options) *Manager {
	if o.DataDir == "" {
		o.DataDir = DefaultDataDir
	}
	if o.RuntimeDir == "" {
		o.RuntimeDir = DefaultRuntimeDir
	}
	if o.Hash == (HashParams{}) {
		o.Hash = DefaultHashParams
	}
	if o.Publish == nil {
		o.Publish = func(string, any) {}
	}
	if o.Now == nil {
		o.Now = time.Now
	}

	m := &Manager{
		hashPath: filepath.Join(o.DataDir, hashFileName),
		params:   o.Hash,
		publish:  o.Publish,
		now:      o.Now,
		limiter:  newLimiter(),
		audit:    auditLog{path: filepath.Join(o.DataDir, auditFileName)},
		sessions: sessionStore{path: filepath.Join(o.RuntimeDir, sessionFileName)},
		alerts:   alertStore{path: filepath.Join(o.DataDir, alertsFileName)},
	}

	for _, d := range []string{o.DataDir, o.RuntimeDir} {
		if err := prepareDir(d); err != nil {
			m.initErr = fmt.Errorf("admin directory %s: %w", d, err)
			logging.Errorln(m.initErr)
			return m
		}
	}

	enc, _, err := m.readHash()
	if err != nil {
		logging.Errorf("reading the admin password hash: %v", err)
	}
	if err := m.sessions.load(m.now(), fingerprintOf(enc)); err != nil {
		logging.Warnf("dropping stored admin sessions: %v", err)
		m.sessions.list = nil
		_ = m.sessions.invalidateAll()
	}
	if err := m.alerts.load(); err != nil {
		logging.Errorf("reading the admin alerts: %v", err)
	}
	return m
}

func fingerprintOf(enc string) string {
	if enc == "" {
		return ""
	}
	return fingerprint(enc)
}

// readHash reports whether a hash file exists; an existing but unreadable or
// empty file still counts as set, so it can only be replaced by recovery.
func (m *Manager) readHash() (string, bool, error) {
	b, found, err := readSmallFile(m.hashPath, 4096)
	if err != nil {
		return "", found, err
	}
	return strings.TrimSpace(string(b)), found, nil
}

func checkLength(password string) error {
	if len(password) > MaxPasswordBytes {
		return ErrTooLong
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrTooShort
	}
	return nil
}

// Status reports whether an admin password exists.
func (m *Manager) Status() (bool, error) {
	if m.initErr != nil {
		return false, m.initErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, found, _ := m.readHash()
	return found, nil
}

// Valid implements the session check of the /api/v2 middleware.
func (m *Manager) Valid(session string) bool {
	if m.initErr != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveSession(session) != nil
}

// liveSession also checks the session against the hash on disk, so a hash
// removed or replaced behind the daemon's back ends every session.
func (m *Manager) liveSession(tok string) *session {
	s := m.sessions.find(m.now(), tok)
	if s == nil {
		return nil
	}
	enc, found, err := m.readHash()
	if !found || err != nil || fingerprint(enc) != s.Password {
		return nil
	}
	return s
}

// Verify checks the admin password and issues a session.
func (m *Manager) Verify(password string) (session string, expiresIn int, err error) {
	if m.initErr != nil {
		return "", 0, m.initErr
	}
	m.mu.Lock()
	enc, found, err := m.readHash()
	m.mu.Unlock()
	if !found {
		return "", 0, ErrNotSet
	}
	if err != nil {
		return "", 0, err
	}

	if !m.limiter.tryAcquire() {
		return "", 0, &RateLimitedError{RetryAfter: 1}
	}
	defer m.limiter.release()
	if ra := m.limiter.retryAfter(m.now()); ra > 0 {
		return "", 0, &RateLimitedError{RetryAfter: ra}
	}

	ok, err := verifyPassword(enc, password)
	if err != nil {
		return "", 0, err
	}
	if !ok {
		m.limiter.fail(m.now())
		return "", 0, ErrBadPassword
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// A reset may have run while argon2id was hashing.
	cur, found, err := m.readHash()
	switch {
	case !found:
		return "", 0, ErrNotSet
	case err != nil:
		return "", 0, err
	case cur != enc:
		return "", 0, ErrBadPassword
	}
	tok, err := m.sessions.issue(m.now(), fingerprint(enc))
	if err != nil {
		return "", 0, err
	}
	return tok, int(SessionTTL / time.Second), nil
}

// Set runs admin/set. session is only used in ModeChange. The checks follow
// docs/API.md: session, state, length, then the limiter.
func (m *Manager) Set(mode Mode, password, session string) error {
	if m.initErr != nil {
		return m.initErr
	}
	if err := m.checkSetState(mode, session); err != nil {
		return err
	}
	if err := checkLength(password); err != nil {
		return err
	}

	if !m.limiter.tryAcquire() {
		return &RateLimitedError{RetryAfter: 1}
	}
	defer m.limiter.release()

	enc, err := hashPassword(m.params, password)
	if err != nil {
		return err
	}
	tmp, err := writeTemp(m.hashPath, []byte(enc+"\n"))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()

	m.mu.Lock()
	defer m.mu.Unlock()
	switch mode {
	case ModeBootstrap:
		if err := publishExclusive(tmp, m.hashPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return ErrAlreadySet
			}
			return err
		}
	case ModeChange:
		if m.liveSession(session) == nil {
			return ErrBadSession
		}
		fallthrough
	default:
		if err := os.Rename(tmp, m.hashPath); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(m.hashPath)); err != nil {
			logging.Warnf("syncing the admin directory: %v", err)
		}
	}

	m.invalidateSessionsLocked()
	m.raiseLocked(EventAdminSet)
	return nil
}

func (m *Manager) checkSetState(mode Mode, session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mode == ModeChange && m.liveSession(session) == nil {
		return ErrBadSession
	}
	_, found, _ := m.readHash()
	switch mode {
	case ModeBootstrap:
		if found {
			return ErrAlreadySet
		}
	case ModeChange:
		if !found {
			return ErrNotSet
		}
	case ModeRecovery:
	default:
		return fmt.Errorf("unknown admin/set mode %q", mode)
	}
	return nil
}

// Reset removes the password and every session. wasSet is false when there
// was nothing to reset, and then no alert is raised.
func (m *Manager) Reset() (wasSet bool, err error) {
	if m.initErr != nil {
		return false, m.initErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := os.Lstat(m.hashPath); err == nil {
		wasSet = true
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.Remove(m.hashPath); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := syncDir(filepath.Dir(m.hashPath)); err != nil {
		logging.Warnf("syncing the admin directory: %v", err)
	}
	m.invalidateSessionsLocked()
	if wasSet {
		m.raiseLocked(EventAdminReset)
	}
	return wasSet, nil
}

// PushCodeFailed counts a failed push code against session and kills the
// session at MaxPushCodeFailures. It reports whether the session is gone.
func (m *Manager) PushCodeFailed(session string) (bool, error) {
	if m.initErr != nil {
		return false, m.initErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.liveSession(session)
	if s == nil {
		return true, nil
	}
	s.PushFailures++
	if s.PushFailures >= MaxPushCodeFailures {
		return true, m.sessions.remove(s)
	}
	return false, m.sessions.save()
}

// Ack drops the acknowledged alerts and returns how many were removed.
func (m *Manager) Ack(ids []string) (int, error) {
	if m.initErr != nil {
		return 0, m.initErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alerts.ack(ids)
}

// PendingAlerts are replayed to every new /events subscriber.
func (m *Manager) PendingAlerts() []Alert {
	if m.initErr != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alerts.pending()
}

// Audit writes e to audit.log and to the daemon log (journald).
func (m *Manager) Audit(e AuditEntry) {
	line := e.line(m.now())
	logging.Infoln("audit:", line)
	if m.initErr != nil {
		return
	}
	if err := m.audit.write(line); err != nil {
		logging.Warnf("writing the admin audit log: %v", err)
	}
}

func (m *Manager) invalidateSessionsLocked() {
	if err := m.sessions.invalidateAll(); err != nil {
		logging.Errorf("invalidating the admin sessions: %v", err)
	}
}

// raiseLocked never fails the request: the password change has already
// happened, so a lost alert is only logged.
func (m *Manager) raiseLocked(typ string) {
	al, err := m.alerts.raise(typ, m.now())
	if err != nil {
		logging.Errorf("persisting the %s alert: %v", typ, err)
	}
	if al.ID != "" {
		m.publish(al.Type, al.Data())
	}
}
