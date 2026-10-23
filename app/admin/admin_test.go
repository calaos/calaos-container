package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var testParams = HashParams{MemoryKiB: 64, Time: 1, Threads: 1}

const (
	goodPassword  = "correct horse battery"
	otherPassword = "another fine password"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type published struct {
	mu     sync.Mutex
	events []struct {
		typ  string
		data any
	}
}

func (p *published) publish(typ string, data any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, struct {
		typ  string
		data any
	}{typ, data})
}

func (p *published) types() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.events {
		out = append(out, e.typ)
	}
	return out
}

type env struct {
	data, run string
	clk       *clock
	pub       *published
}

func newEnv(t *testing.T) *env {
	root := t.TempDir()
	return &env{data: filepath.Join(root, "admin"), run: filepath.Join(root, "run"), clk: newClock(), pub: &published{}}
}

func (e *env) manager() *Manager {
	return New(Options{DataDir: e.data, RuntimeDir: e.run, Hash: testParams, Publish: e.pub.publish, Now: e.clk.Now})
}

func mustSet(t *testing.T, m *Manager, mode Mode, pw, session string) {
	t.Helper()
	if err := m.Set(mode, pw, session); err != nil {
		t.Fatalf("Set(%s): %v", mode, err)
	}
}

func mustVerify(t *testing.T, m *Manager, pw string) string {
	t.Helper()
	s, exp, err := m.Verify(pw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !wellFormedSession(s) || exp != 900 {
		t.Fatalf("session %q, expires_in %d", s, exp)
	}
	return s
}

func TestDefaultHashParamsInPlanRange(t *testing.T) {
	p := DefaultHashParams
	if p.MemoryKiB < 32*1024 || p.MemoryKiB > 64*1024 || p.Time < 2 || p.Time > 3 || p.Threads != 1 {
		t.Fatalf("default argon2id parameters %+v outside m=32..64MiB, t=2..3, p=1", p)
	}
}

func TestHashAndVerify(t *testing.T) {
	enc, err := hashPassword(testParams, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("not a PHC argon2id string: %s", enc)
	}
	if strings.Contains(enc, goodPassword) {
		t.Fatal("hash contains the password")
	}
	if ok, err := verifyPassword(enc, goodPassword); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	for _, pw := range []string{otherPassword, goodPassword + " ", "", strings.ToUpper(goodPassword)} {
		if ok, err := verifyPassword(enc, pw); ok || err != nil {
			t.Fatalf("wrong password %q: %v %v", pw, ok, err)
		}
	}

	enc2, _ := hashPassword(testParams, goodPassword)
	if enc2 == enc {
		t.Fatal("two hashes of one password share a salt")
	}

	// Stored parameters win over the current defaults.
	strong, _ := hashPassword(HashParams{MemoryKiB: 128, Time: 2, Threads: 1}, goodPassword)
	if ok, _ := verifyPassword(strong, goodPassword); !ok {
		t.Fatal("hash with other parameters not verified")
	}

	parts := strings.Split(enc, "$")
	for name, bad := range map[string]string{
		"empty":       "",
		"argon2i":     strings.Replace(enc, "argon2id", "argon2i", 1),
		"version":     strings.Replace(enc, "v=19", "v=16", 1),
		"huge memory": strings.Replace(enc, "m=64,", "m=4194304,", 1),
		"no time":     strings.Replace(enc, "t=1,", "t=0,", 1),
		"threads":     strings.Replace(enc, "p=1$", "p=9$", 1),
		"param order": strings.Replace(enc, "m=64,t=1", "t=1,m=64", 1),
		"short key":   strings.Join(append(parts[:5:5], "AAAA"), "$"),
		"bad salt":    strings.Join([]string{"", parts[1], parts[2], parts[3], "!!", parts[5]}, "$"),
		"extra field": enc + "$x",
	} {
		if ok, err := verifyPassword(bad, goodPassword); ok || !errors.Is(err, errBadHash) {
			t.Errorf("%s: %v %v", name, ok, err)
		}
	}
}

func TestDirectoriesAndFiles(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	mustVerify(t, m, goodPassword)

	for _, d := range []string{e.data, e.run} {
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi.Mode(), err)
		}
	}
	for _, f := range []string{filepath.Join(e.data, hashFileName), filepath.Join(e.data, alertsFileName), filepath.Join(e.run, sessionFileName)} {
		fi, err := os.Lstat(f)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, fi.Mode(), err)
		}
	}
	left, _ := filepath.Glob(filepath.Join(e.data, ".*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}

	// An existing directory is tightened, stale temp files are removed.
	if err := os.Chmod(e.data, 0o755); err != nil { //nolint:gosec // the loose mode under test
		t.Fatal(err)
	}
	stale := filepath.Join(e.data, "."+hashFileName+tmpInfix+"123")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.manager()
	if fi, _ := os.Lstat(e.data); fi.Mode().Perm() != 0o700 {
		t.Fatalf("data dir left %v", fi.Mode())
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatal("stale temp file kept")
	}
}

func TestSymlinkedDirectoryRefused(t *testing.T) {
	e := newEnv(t)
	target := t.TempDir()
	if err := os.Symlink(target, e.data); err != nil {
		t.Fatal(err)
	}
	m := e.manager()
	if _, err := m.Status(); err == nil {
		t.Fatal("Status works on a symlinked data directory")
	}
	if err := m.Set(ModeBootstrap, goodPassword, ""); err == nil || errors.Is(err, ErrAlreadySet) {
		t.Fatalf("Set on a symlinked data directory: %v", err)
	}
	if m.Valid(strings.Repeat("a", 64)) {
		t.Fatal("Valid on a broken manager")
	}
	if ents, _ := os.ReadDir(target); len(ents) != 0 {
		t.Fatalf("wrote through the symlink: %v", ents)
	}
}

func TestSetModes(t *testing.T) {
	e := newEnv(t)
	m := e.manager()

	if set, _ := m.Status(); set {
		t.Fatal("admin set on a fresh box")
	}
	if _, _, err := m.Verify(goodPassword); !errors.Is(err, ErrNotSet) {
		t.Fatalf("verify before bootstrap: %v", err)
	}
	if err := m.Set(ModeChange, otherPassword, strings.Repeat("a", 64)); !errors.Is(err, ErrBadSession) {
		t.Fatalf("change before bootstrap: %v", err)
	}
	if err := m.Set(ModeBootstrap, "short", ""); !errors.Is(err, ErrTooShort) {
		t.Fatalf("short bootstrap: %v", err)
	}

	mustSet(t, m, ModeBootstrap, goodPassword, "")
	if set, _ := m.Status(); !set {
		t.Fatal("not set after bootstrap")
	}
	// State is checked before length.
	if err := m.Set(ModeBootstrap, "short", ""); !errors.Is(err, ErrAlreadySet) {
		t.Fatalf("second bootstrap: %v", err)
	}
	if err := m.Set(ModeBootstrap, otherPassword, ""); !errors.Is(err, ErrAlreadySet) {
		t.Fatalf("second bootstrap: %v", err)
	}

	s := mustVerify(t, m, goodPassword)
	if err := m.Set(ModeChange, otherPassword, "nope"); !errors.Is(err, ErrBadSession) {
		t.Fatalf("change with a bad session: %v", err)
	}
	// Session is checked before length.
	if err := m.Set(ModeChange, "short", "nope"); !errors.Is(err, ErrBadSession) {
		t.Fatalf("short change with a bad session: %v", err)
	}
	if err := m.Set(ModeChange, "short", s); !errors.Is(err, ErrTooShort) {
		t.Fatalf("short change: %v", err)
	}
	mustSet(t, m, ModeChange, otherPassword, s)
	if m.Valid(s) {
		t.Fatal("change kept the session")
	}
	if b, _ := os.ReadFile(filepath.Join(e.run, sessionFileName)); strings.Contains(string(b), sessionHash(s)) {
		t.Fatal("change left the session on disk")
	}
	if _, _, err := m.Verify(goodPassword); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("old password after change: %v", err)
	}
	s = mustVerify(t, m, otherPassword)

	mustSet(t, m, ModeRecovery, goodPassword, "")
	if m.Valid(s) {
		t.Fatal("recovery kept the session")
	}
	mustVerify(t, m, goodPassword)

	if was, err := m.Reset(); !was || err != nil {
		t.Fatalf("reset: %v %v", was, err)
	}
	mustSet(t, m, ModeRecovery, goodPassword, "")
	if set, _ := m.Status(); !set {
		t.Fatal("recovery on an unset box did not set the password")
	}

	if got := e.pub.types(); strings.Join(got, ",") != "admin_set,admin_set,admin_set,admin_reset,admin_set" {
		t.Fatalf("published %v", got)
	}
}

func TestPasswordLength(t *testing.T) {
	cases := []struct {
		pw   string
		want error
	}{
		{"123456789", ErrTooShort},
		{"1234567890", nil},
		{strings.Repeat("é", 9), ErrTooShort}, // 18 bytes
		{strings.Repeat("é", 10), nil},
		{strings.Repeat("x", MaxPasswordBytes), nil},
		{strings.Repeat("x", MaxPasswordBytes+1), ErrTooLong},
		{strings.Repeat("é", 513), ErrTooLong},
	}
	for _, c := range cases {
		if err := checkLength(c.pw); !errors.Is(err, c.want) {
			t.Errorf("%d bytes: %v, want %v", len(c.pw), err, c.want)
		}
	}
}

// Of concurrent bootstraps, exactly one wins, whether they share a limiter or
// come from two daemons on the same directory.
func TestConcurrentBootstrap(t *testing.T) {
	for _, shared := range []bool{true, false} {
		e := newEnv(t)
		ms := []*Manager{e.manager()}
		for i := 1; i < 16; i++ {
			if shared {
				ms = append(ms, ms[0])
			} else {
				ms = append(ms, e.manager())
			}
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(ms))
		for i, m := range ms {
			wg.Add(1)
			go func(i int, m *Manager) {
				defer wg.Done()
				errs <- m.Set(ModeBootstrap, goodPassword+strconv.Itoa(i), "")
			}(i, m)
		}
		wg.Wait()
		close(errs)
		wins := 0
		for err := range errs {
			var rl *RateLimitedError
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrAlreadySet), errors.As(err, &rl):
			default:
				t.Fatalf("shared=%v: %v", shared, err)
			}
		}
		if wins != 1 {
			t.Fatalf("shared=%v: %d bootstraps won", shared, wins)
		}
		if n := len(e.pub.types()); n != 1 {
			t.Fatalf("shared=%v: %d admin_set alerts", shared, n)
		}
	}
}

// The O_EXCL step itself: a hash that appeared after the state check is
// never replaced by a bootstrap.
func TestBootstrapNeverReplaces(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	tmp, err := writeTemp(m.hashPath, []byte("first\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := publishExclusive(tmp, m.hashPath); err != nil {
		t.Fatal(err)
	}
	tmp, _ = writeTemp(m.hashPath, []byte("second\n"))
	if err := publishExclusive(tmp, m.hashPath); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second publish: %v", err)
	}
	if b, _ := os.ReadFile(m.hashPath); string(b) != "first\n" {
		t.Fatalf("hash replaced: %q", b)
	}
	if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
		t.Fatal("temp file left")
	}
}

func TestLimiterCountsFailuresOnly(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")

	for i := 0; i < 20; i++ {
		mustVerify(t, m, goodPassword)
	}
	for i := 0; i < failureBudget; i++ {
		if _, _, err := m.Verify(otherPassword); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("failure %d: %v", i, err)
		}
		e.clk.Advance(10 * time.Second)
	}

	// Over budget: refused before hashing, even with the right password.
	var rl *RateLimitedError
	if _, _, err := m.Verify(goodPassword); !errors.As(err, &rl) || rl.RetryAfter != 10 {
		t.Fatalf("over budget: %v", err)
	}
	e.clk.Advance(9500 * time.Millisecond)
	if _, _, err := m.Verify(goodPassword); !errors.As(err, &rl) || rl.RetryAfter != 1 {
		t.Fatalf("over budget, 0.5 s left: %v", err)
	}
	e.clk.Advance(500 * time.Millisecond)
	mustVerify(t, m, goodPassword)

	// The oldest failure left: one more failure refills the budget.
	if _, _, err := m.Verify(otherPassword); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("failure after the window moved: %v", err)
	}
	if _, _, err := m.Verify(goodPassword); !errors.As(err, &rl) {
		t.Fatalf("budget not refilled: %v", err)
	}

	// Set is not budgeted.
	mustSet(t, m, ModeRecovery, goodPassword, "")
}

// A second argon2id operation is refused at once, never queued.
func TestLimiterOneAtATime(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")

	if !m.limiter.tryAcquire() {
		t.Fatal("slot busy")
	}
	var rl *RateLimitedError
	start := time.Now()
	if _, _, err := m.Verify(goodPassword); !errors.As(err, &rl) || rl.RetryAfter != 1 {
		t.Fatalf("concurrent verify: %v", err)
	}
	if err := m.Set(ModeRecovery, otherPassword, ""); !errors.As(err, &rl) || rl.RetryAfter != 1 {
		t.Fatalf("concurrent set: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("refusal took %v", d)
	}
	m.limiter.release()

	// A refused concurrent verify is not a failure.
	for i := 0; i < failureBudget-1; i++ {
		_, _, _ = m.Verify(otherPassword)
	}
	mustVerify(t, m, goodPassword)

	// Real concurrency, with a hash slow enough to overlap.
	slow := New(Options{DataDir: e.data, RuntimeDir: e.run, Hash: HashParams{MemoryKiB: 8 * 1024, Time: 2, Threads: 1}, Now: e.clk.Now})
	mustSet(t, slow, ModeRecovery, goodPassword, "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := slow.Verify(goodPassword)
			mu.Lock()
			defer mu.Unlock()
			var rl *RateLimitedError
			switch {
			case err == nil:
				ok++
			case errors.As(err, &rl) && rl.RetryAfter == 1:
				limited++
			default:
				t.Errorf("concurrent verify: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok < 1 || ok+limited != 8 {
		t.Fatalf("%d ok, %d limited", ok, limited)
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)

	if !m.Valid(s) {
		t.Fatal("fresh session invalid")
	}
	for _, bad := range []string{"", strings.ToUpper(s), s[:63], s + "0", strings.Repeat("0", 64)} {
		if m.Valid(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
	e.clk.Advance(SessionTTL - time.Second)
	if !m.Valid(s) {
		t.Fatal("session expired early")
	}
	// Absolute: using it did not extend it.
	e.clk.Advance(time.Second)
	if m.Valid(s) {
		t.Fatal("session outlived 15 minutes")
	}
}

func TestSessionsSurviveRestart(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)

	b, err := os.ReadFile(filepath.Join(e.run, sessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), s) || !strings.Contains(string(b), sessionHash(s)) {
		t.Fatalf("session file must hold the SHA-256 only: %s", b)
	}

	e.clk.Advance(5 * time.Minute)
	m2 := e.manager()
	if !m2.Valid(s) {
		t.Fatal("session lost across a restart")
	}
	e.clk.Advance(10 * time.Minute)
	if m2.Valid(s) {
		t.Fatal("restart extended the session")
	}

	// Reboot: the runtime directory is gone.
	s = mustVerify(t, m2, goodPassword)
	if err := os.RemoveAll(e.run); err != nil {
		t.Fatal(err)
	}
	if e.manager().Valid(s) {
		t.Fatal("session survived a reboot")
	}
}

// A session issued for another password never comes back, even when the
// invalidation was not written.
func TestSessionBoundToPassword(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)
	stored, _ := os.ReadFile(filepath.Join(e.run, sessionFileName))

	mustSet(t, m, ModeRecovery, otherPassword, "")
	if err := os.WriteFile(filepath.Join(e.run, sessionFileName), stored, 0o600); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	if e.manager().Valid(s) {
		t.Fatal("stale session revived after a password change")
	}

	if _, err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.run, sessionFileName), stored, 0o600); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	if e.manager().Valid(s) {
		t.Fatal("stale session revived after a reset")
	}
}

func TestSessionClockStepBack(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)

	e.clk.Advance(-time.Hour)
	m2 := e.manager()
	e.clk.Advance(SessionTTL - time.Second)
	if !m2.Valid(s) {
		t.Fatal("session lost")
	}
	e.clk.Advance(time.Second)
	if m2.Valid(s) {
		t.Fatal("a clock step backwards lengthened the session past 15 minutes")
	}
}

func TestSessionBound(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	var all []string
	for i := 0; i < maxSessions+3; i++ {
		all = append(all, mustVerify(t, m, goodPassword))
		e.clk.Advance(time.Second)
	}
	for i, s := range all {
		if want := i >= 3; m.Valid(s) != want {
			t.Fatalf("session %d valid=%v, want %v", i, !want, want)
		}
	}
	if !e.manager().Valid(all[len(all)-1]) {
		t.Fatal("newest session lost on reload")
	}
}

func TestResetAndInvalidation(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	if was, err := m.Reset(); was || err != nil {
		t.Fatalf("reset of an unset box: %v %v", was, err)
	}
	if len(e.pub.types()) != 0 || len(m.PendingAlerts()) != 0 {
		t.Fatal("reset with nothing to reset raised an alert")
	}

	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s1 := mustVerify(t, m, goodPassword)
	s2 := mustVerify(t, m, goodPassword)
	if was, err := m.Reset(); !was || err != nil {
		t.Fatalf("reset: %v %v", was, err)
	}
	if m.Valid(s1) || m.Valid(s2) || e.manager().Valid(s1) {
		t.Fatal("reset kept sessions")
	}
	if set, _ := m.Status(); set {
		t.Fatal("still set after reset")
	}
	if _, _, err := m.Verify(goodPassword); !errors.Is(err, ErrNotSet) {
		t.Fatalf("verify after reset: %v", err)
	}
	mustSet(t, m, ModeBootstrap, otherPassword, "")
}

func TestPushCodeFailures(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)
	other := mustVerify(t, m, goodPassword)

	for i := 1; i < MaxPushCodeFailures; i++ {
		if gone, err := m.PushCodeFailed(s); gone || err != nil {
			t.Fatalf("failure %d: %v %v", i, gone, err)
		}
	}
	if gone, err := m.PushCodeFailed(s); !gone || err != nil {
		t.Fatalf("failure %d: %v %v", MaxPushCodeFailures, gone, err)
	}
	if m.Valid(s) || e.manager().Valid(s) {
		t.Fatal("session alive after 5 failed push codes")
	}
	if !m.Valid(other) {
		t.Fatal("another session was killed")
	}
}

func TestAlertsPersistAndAck(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	if _, err := m.Reset(); err != nil {
		t.Fatal(err)
	}

	pending := m.PendingAlerts()
	if len(pending) != 2 || pending[0].Type != EventAdminSet || pending[1].Type != EventAdminReset {
		t.Fatalf("pending %+v", pending)
	}
	for _, al := range pending {
		if len(al.ID) != 16 || strings.Trim(al.ID, "0123456789abcdef") != "" {
			t.Fatalf("id %q", al.ID)
		}
		if _, err := time.Parse(time.RFC3339, al.TS); err != nil || al.TS != "2026-10-23T10:00:00Z" {
			t.Fatalf("ts %q", al.TS)
		}
	}
	e.pub.mu.Lock()
	if d, ok := e.pub.events[0].data.(AlertData); !ok || d.ID != pending[0].ID {
		t.Fatalf("published %+v", e.pub.events[0])
	}
	e.pub.mu.Unlock()

	// The published data and the file carry no secret.
	raw, _ := os.ReadFile(filepath.Join(e.data, alertsFileName))
	b, _ := json.Marshal(pending[0].Data())
	if string(b) != `{"id":"`+pending[0].ID+`","ts":"2026-10-23T10:00:00Z"}` || strings.Contains(string(raw), goodPassword) || strings.Contains(string(raw), "argon2") {
		t.Fatalf("alert data %s, file %s", b, raw)
	}

	// Replayed after a restart until acked.
	m2 := e.manager()
	if got := m2.PendingAlerts(); len(got) != 2 || got[0].ID != pending[0].ID {
		t.Fatalf("after restart %+v", got)
	}
	if n, err := m2.Ack([]string{pending[0].ID, "unknown", pending[0].ID}); n != 1 || err != nil {
		t.Fatalf("ack: %d %v", n, err)
	}
	if n, _ := m2.Ack([]string{pending[0].ID}); n != 0 {
		t.Fatalf("second ack removed %d", n)
	}
	if got := e.manager().PendingAlerts(); len(got) != 1 || got[0].ID != pending[1].ID {
		t.Fatalf("after ack and restart %+v", got)
	}
}

func TestAlertsBound(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	first := m.PendingAlerts()[0].ID
	for i := 0; i < maxAlerts; i++ {
		mustSet(t, m, ModeRecovery, goodPassword, "")
	}
	got := e.manager().PendingAlerts()
	if len(got) != maxAlerts || got[0].ID == first {
		t.Fatalf("%d alerts, oldest kept: %v", len(got), got[0].ID == first)
	}
}

func TestAudit(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	m.Audit(AuditEntry{Client: "192.168.1.5", Method: "POST", Path: "/api/v2/admin/verify\nforged line", Result: "bad_password"})
	m.Audit(AuditEntry{Client: "local", Method: "GET", Path: "/api/v2/admin/status", Result: "ok", Session: "0123abcd"})

	b, err := os.ReadFile(filepath.Join(e.data, auditFileName))
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-10-23T10:00:00Z client=192.168.1.5 POST /api/v2/admin/verify?forged?line result=bad_password session=-\n" +
		"2026-10-23T10:00:00Z client=local GET /api/v2/admin/status result=ok session=0123abcd\n"
	if string(b) != want {
		t.Fatalf("audit log\n%s\nwant\n%s", b, want)
	}
	if fi, _ := os.Lstat(filepath.Join(e.data, auditFileName)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("audit log mode %v", fi.Mode())
	}

	// Rotated at 1 MiB, one old file kept.
	big := strings.Repeat("x", auditMaxSize)
	if err := os.WriteFile(filepath.Join(e.data, auditFileName), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Audit(AuditEntry{Client: "local", Method: "POST", Path: "/api/v2/admin/reset", Result: "ok"})
	old, _ := os.ReadFile(filepath.Join(e.data, auditFileName+".1"))
	cur, _ := os.ReadFile(filepath.Join(e.data, auditFileName))
	if string(old) != big || !strings.HasSuffix(string(cur), "admin/reset result=ok session=-\n") || len(cur) > 200 {
		t.Fatalf("rotation: old %d bytes, current %q", len(old), cur)
	}
}

func TestSessionDiesWithHashRemovedOutOfBand(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)
	if err := os.Remove(filepath.Join(e.data, hashFileName)); err != nil {
		t.Fatal(err)
	}
	if m.Valid(s) {
		t.Fatal("session valid without a password hash")
	}
	if err := m.Set(ModeChange, otherPassword, s); !errors.Is(err, ErrBadSession) {
		t.Fatalf("change without a hash: %v", err)
	}
}
