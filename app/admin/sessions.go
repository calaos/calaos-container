package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"
)

const (
	// SessionTTL is absolute: using a session does not extend it.
	SessionTTL = 15 * time.Minute

	maxSessions = 32

	// MaxPushCodeFailures failed push codes kill the session they were
	// entered in.
	MaxPushCodeFailures = 5

	sessionBytes = 32
)

type session struct {
	Hash         string    `json:"hash"`
	Password     string    `json:"pw_fp"`
	Issued       time.Time `json:"issued"`
	Expires      time.Time `json:"expires"`
	PushFailures int       `json:"push_failures,omitempty"`
}

type sessionFile struct {
	Sessions []*session `json:"sessions"`
}

// sessionStore keeps the SHA-256 of each live session, never the session.
// Guarded by Manager.mu.
type sessionStore struct {
	path string
	list []*session
}

func sessionHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// SessionAuditID is the only form of a session that may appear in a log.
func SessionAuditID(s string) string {
	return sessionHash(s)[:8]
}

func wellFormedSession(s string) bool {
	if len(s) != 2*sessionBytes {
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

// load keeps the sessions issued for the current password. The deadline read
// from disk is wall-clock: the remaining time is capped at SessionTTL and
// turned back into a monotonic deadline, so a clock step cannot stretch it.
func (st *sessionStore) load(now time.Time, password string) error {
	b, found, err := readSmallFile(st.path, 64*1024)
	if err != nil || !found {
		return err
	}
	var f sessionFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	for _, s := range f.Sessions {
		if s == nil || password == "" || s.Password != password || len(s.Hash) != 64 {
			continue
		}
		left := s.Expires.Sub(now)
		if left > SessionTTL {
			left = SessionTTL
		}
		if left <= 0 {
			continue
		}
		s.Expires = now.Add(left)
		st.list = append(st.list, s)
	}
	if len(st.list) > maxSessions {
		st.list = st.list[len(st.list)-maxSessions:]
	}
	return st.save()
}

func (st *sessionStore) save() error {
	b, err := json.Marshal(sessionFile{Sessions: st.list})
	if err != nil {
		return err
	}
	return writeFileAtomic(st.path, b)
}

func (st *sessionStore) issue(now time.Time, password string) (string, error) {
	raw := make([]byte, sessionBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)

	prev := st.list
	st.prune(now)
	st.list = append(st.list, &session{Hash: sessionHash(tok), Password: password, Issued: now, Expires: now.Add(SessionTTL)})
	if len(st.list) > maxSessions {
		st.list = st.list[len(st.list)-maxSessions:]
	}
	if err := st.save(); err != nil {
		st.list = prev
		return "", err
	}
	return tok, nil
}

func (st *sessionStore) find(now time.Time, tok string) *session {
	if !wellFormedSession(tok) {
		return nil
	}
	h := sessionHash(tok)
	for _, s := range st.list {
		if s.Hash == h && now.Before(s.Expires) {
			return s
		}
	}
	return nil
}

func (st *sessionStore) liveHash(now time.Time, h string) bool {
	for _, s := range st.list {
		if s.Hash == h && now.Before(s.Expires) {
			return true
		}
	}
	return false
}

func (st *sessionStore) prune(now time.Time) {
	kept := st.list[:0:0]
	for _, s := range st.list {
		if now.Before(s.Expires) {
			kept = append(kept, s)
		}
	}
	st.list = kept
}

func (st *sessionStore) remove(target *session) error {
	kept := st.list[:0:0]
	for _, s := range st.list {
		if s != target {
			kept = append(kept, s)
		}
	}
	st.list = kept
	return st.save()
}

// invalidateAll empties the store. If the file cannot be rewritten it is
// removed; the password fingerprint drops stale entries at the next load
// anyway.
func (st *sessionStore) invalidateAll() error {
	st.list = nil
	if err := st.save(); err != nil {
		if rerr := os.Remove(st.path); rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
	}
	return nil
}
