package admin

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const auditMaxSize = 1 << 20

// AuditEntry is one audited request. Session is the audit id, never the
// session itself.
type AuditEntry struct {
	Client  string
	Method  string
	Path    string
	Result  string
	Session string
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

func (e AuditEntry) line(now time.Time) string {
	sid := e.Session
	if sid == "" {
		sid = "-"
	}
	return fmt.Sprintf("%s client=%s %s %s result=%s session=%s",
		now.UTC().Format(time.RFC3339), sanitize(e.Client), sanitize(e.Method), sanitize(e.Path),
		sanitize(e.Result), sanitize(sid))
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
