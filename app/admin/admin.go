// Package admin keeps the admin password, the admin sessions, the approvers,
// the push codes, the persisted admin alerts and the audit log (docs/API.md,
// admin routes).
package admin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

	EventAdminSet        = "admin_set"
	EventAdminReset      = "admin_reset"
	EventApproverSet     = "approver_set"
	EventApproverRemoved = "approver_removed"
	EventConsoleOpened   = "console_opened"

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
	// SessionsEnded is told of every invalidation, with the session hash or
	// "" for all sessions. It runs under the manager lock: no call back in.
	SessionsEnded func(sessionHash, reason string)
}

type Manager struct {
	hashPath string
	params   HashParams
	publish  func(string, any)
	ended    func(string, string)
	now      func() time.Time
	initErr  error

	limiter *limiter
	audit   auditLog
	refused refusedThrottle

	// mu serializes every change of the hash file, the sessions, the
	// approvers, the codes and the alerts. argon2id never runs under it.
	mu        sync.Mutex
	sessions  sessionStore
	alerts    alertStore
	approvers approverStore
	codes     codeStore
	hashCache hashCache
}

type hashCache struct {
	valid bool
	ino   uint64
	size  int64
	mtime time.Time
	enc   string
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
	if o.SessionsEnded == nil {
		o.SessionsEnded = func(string, string) {}
	}

	m := &Manager{
		hashPath:  filepath.Join(o.DataDir, hashFileName),
		params:    o.Hash,
		publish:   o.Publish,
		ended:     o.SessionsEnded,
		now:       o.Now,
		limiter:   newLimiter(),
		audit:     auditLog{path: filepath.Join(o.DataDir, auditFileName)},
		sessions:  sessionStore{path: filepath.Join(o.RuntimeDir, sessionFileName)},
		alerts:    alertStore{path: filepath.Join(o.DataDir, alertsFileName)},
		approvers: approverStore{path: filepath.Join(o.DataDir, approversFileName)},
		codes:     newCodeStore(),
	}

	for _, d := range []string{o.DataDir, o.RuntimeDir} {
		if err := prepareDir(d); err != nil {
			m.initErr = fmt.Errorf("admin directory %s: %w", d, err)
			logging.Errorln(m.initErr)
			return m
		}
	}

	// An unreadable hash leaves the session file alone: liveSession refuses
	// those sessions until the hash reads again.
	if enc, found, err := m.readHash(); err != nil && found {
		logging.Errorf("reading the admin password hash: %v", err)
	} else if err := m.sessions.load(m.now(), fingerprintOf(enc)); err != nil {
		logging.Warnf("dropping stored admin sessions: %v", err)
		m.sessions.list = nil
		_ = m.sessions.invalidateAll()
	}
	if err := m.alerts.load(); err != nil {
		// Kept aside: the next alert would otherwise overwrite the
		// unacknowledged ones.
		logging.Errorf("reading the admin alerts, moved to %s.corrupt: %v", m.alerts.path, err)
		m.alerts.list = nil
		if rerr := os.Rename(m.alerts.path, m.alerts.path+".corrupt"); rerr != nil {
			logging.Errorf("moving the admin alerts aside: %v", rerr)
		}
	}
	// An unreadable file is a lost second factor, not an absent one: the
	// protected actions stay refused until admin reset removes it.
	if err := m.approvers.load(); err != nil {
		logging.Errorf("reading the approvers, code-protected actions disabled until admin reset: %v", err)
		m.approvers.list = nil
		m.approvers.err = err
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
// Every session check calls it: the content is cached while the inode, size
// and mtime stay the same (every write publishes a new inode). Needs m.mu.
func (m *Manager) readHash() (string, bool, error) {
	fi, err := os.Lstat(m.hashPath)
	if os.IsNotExist(err) {
		m.hashCache = hashCache{}
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	c := m.hashCache
	if c.valid && st != nil && c.ino == st.Ino && c.size == fi.Size() && c.mtime.Equal(fi.ModTime()) {
		return c.enc, true, nil
	}

	b, found, err := readSmallFile(m.hashPath, 4096)
	m.hashCache = hashCache{}
	if err != nil || !found {
		return "", found, err
	}
	enc := strings.TrimSpace(string(b))
	if st != nil {
		m.hashCache = hashCache{valid: true, ino: st.Ino, size: fi.Size(), mtime: fi.ModTime(), enc: enc}
	}
	return enc, true, nil
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

// Set runs admin/set. session and code are only used in ModeChange. The
// checks follow docs/API.md: session, state, length, approver, the limiter,
// then the code.
func (m *Manager) Set(mode Mode, password, session string, code CodeRef) error {
	if m.initErr != nil {
		return m.initErr
	}
	if err := m.checkSetState(mode, session); err != nil {
		return err
	}
	if err := checkLength(password); err != nil {
		return err
	}
	if mode == ModeChange {
		if err := m.requireApprover(); err != nil {
			return err
		}
		if code.ID == "" {
			return ErrCodeRequired
		}
	}

	if !m.limiter.tryAcquire() {
		return &RateLimitedError{RetryAfter: 1}
	}
	defer m.limiter.release()

	if mode == ModeChange {
		s, err := m.lockSession(session)
		if err != nil {
			return err
		}
		// Checked now, taken only with the write: a failure below keeps it.
		_, err = m.useCodeLocked(s, code, ActionAdminPassword, Params{"password": password}, useCheck)
		m.mu.Unlock()
		if err != nil {
			return err
		}
	}

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
		s := m.liveSession(session)
		if s == nil {
			return ErrBadSession
		}
		c := m.codes.byID[code.ID]
		if c == nil || c.session != s.Hash || c.dead(m.now()) {
			return ErrCodeExpired
		}
		if err := os.Rename(tmp, m.hashPath); err != nil {
			return err
		}
		delete(m.codes.byID, code.ID)
		if err := syncDir(filepath.Dir(m.hashPath)); err != nil {
			logging.Warnf("syncing the admin directory: %v", err)
		}
	default:
		if err := os.Rename(tmp, m.hashPath); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(m.hashPath)); err != nil {
			logging.Warnf("syncing the admin directory: %v", err)
		}
	}

	m.invalidateSessionsLocked(EventAdminSet)
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
		// No hash: liveSession above already answered bad_session.
	case ModeRecovery:
	default:
		return fmt.Errorf("unknown admin/set mode %q", mode)
	}
	return nil
}

// Reset removes the password, the approvers and every session. wasSet is
// false when there was no password, and then no alert is raised.
func (m *Manager) Reset() (wasSet bool, err error) {
	if m.initErr != nil {
		return false, m.initErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Even a failed reset ends every session: none may outlive a half-wiped
	// approver list.
	defer m.invalidateSessionsLocked(EventAdminReset)

	if err := m.approvers.wipe(); err != nil {
		return false, err
	}

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
	left, err := m.pushCodeFailedLocked(s)
	return left == 0, err
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

func (m *Manager) invalidateSessionsLocked(reason string) {
	m.ended("", reason)
	m.codes.clear()
	if err := m.sessions.invalidateAll(); err != nil {
		logging.Errorf("invalidating the admin sessions: %v", err)
	}
}

// RaiseConsoleOpened tells every phone that a root console was opened.
func (m *Manager) RaiseConsoleOpened(sessionAuditID string) {
	if m.initErr != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raiseAlertLocked(Alert{Type: EventConsoleOpened, SessionAuditID: sessionAuditID})
}

// raiseLocked never fails the request: the password change has already
// happened, so a lost alert is only logged.
func (m *Manager) raiseLocked(typ string) { m.raiseAlertLocked(Alert{Type: typ}) }

func (m *Manager) raiseAlertLocked(al Alert) {
	typ := al.Type
	al, err := m.alerts.raise(al, m.now())
	if err != nil {
		logging.Errorf("persisting the %s alert: %v", typ, err)
	}
	if al.ID != "" {
		m.publish(al.Type, al.Data())
	}
}
