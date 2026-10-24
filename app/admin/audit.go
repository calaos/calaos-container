package admin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	auditMaxSize = 1 << 20
	auditMaxPath = 128
)

// AuditEntry is one audited request. Session is the audit id, never the
// session itself. Fields are key=value pairs of daemon-validated values.
type AuditEntry struct {
	Client  string
	Method  string
	Path    string
	Result  string
	Session string
	Fields  [][2]string
}

type auditLog struct {
	mu   sync.Mutex
	path string
}

// sanitize keeps a client-controlled path from forging audit lines.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r >= 0x7f {
			return '?'
		}
		return r
	}, s)
}

// The path comes from the client: truncated so a request cannot fill the log,
// quoted and last so it cannot pass for another field.
func (e AuditEntry) line(now time.Time) string {
	sid := e.Session
	if sid == "" {
		sid = "-"
	}
	path := e.Path
	if len(path) > auditMaxPath {
		path = path[:auditMaxPath]
	}
	var extra strings.Builder
	for _, f := range e.Fields {
		extra.WriteString(" " + sanitize(f[0]) + "=" + sanitize(f[1]))
	}
	return fmt.Sprintf("%s client=%s result=%s session=%s%s %s %s",
		now.UTC().Format(time.RFC3339), sanitize(e.Client), sanitize(e.Result), sanitize(sid),
		extra.String(), sanitize(e.Method), strconv.QuoteToASCII(path))
}

// write appends line to audit.log, rotated at 1 MiB with one old file kept.
func (al *auditLog) write(line string) error {
	al.mu.Lock()
	defer al.mu.Unlock()

	if fi, err := os.Lstat(al.path); err == nil && fi.Size() >= auditMaxSize {
		if err := os.Rename(al.path, al.path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(al.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // daemon-owned path
	if err != nil {
		return err
	}
	_, werr := f.WriteString(line + "\n")
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

const refusedInterval = time.Second

type refusedThrottle struct {
	mu      sync.Mutex
	last    time.Time
	dropped int
}

// AuditRefused logs a call refused for a bad bearer or session to the daemon
// log only, at most one line per second.
func (m *Manager) AuditRefused(e AuditEntry) {
	t := &m.refused
	now := m.now()
	t.mu.Lock()
	if !t.last.IsZero() && now.Sub(t.last) < refusedInterval {
		t.dropped++
		t.mu.Unlock()
		return
	}
	t.last = now
	dropped := t.dropped
	t.dropped = 0
	t.mu.Unlock()
	logging.Warnf("refused admin call: %s (%d more suppressed)", e.line(now), dropped)
}
