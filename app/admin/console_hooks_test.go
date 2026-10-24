package admin

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

type endedCalls struct {
	mu    sync.Mutex
	calls []string
}

func (c *endedCalls) record(hash, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, hash+":"+reason)
}

func (c *endedCalls) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.calls
	c.calls = nil
	return out
}

func TestSessionsEndedHook(t *testing.T) {
	e := newEnv(t)
	var ended endedCalls
	m := New(Options{DataDir: e.data, RuntimeDir: e.run, Hash: testParams, Publish: e.pub.publish, Now: e.clk.Now, SessionsEnded: ended.record})

	mustSet(t, m, ModeBootstrap, goodPassword, "")
	if got := ended.take(); len(got) != 1 || got[0] != ":admin_set" {
		t.Fatalf("bootstrap: %v", got)
	}
	s := mustVerify(t, m, goodPassword)
	other := mustVerify(t, m, goodPassword)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)
	ref := requestCode(t, e, m, s, ActionConsoleOpen, nil)
	for i := 0; i < MaxPushCodeFailures; i++ {
		_, _, _ = m.VerifyCode(s, CodeRef{ID: ref.ID, Code: wrong(ref.Code)})
	}
	if got := ended.take(); len(got) != 1 || got[0] != SessionHash(s)+":bad_code" {
		t.Fatalf("five bad codes: %v", got)
	}
	if !m.Valid(other) {
		t.Fatal("the other session died")
	}

	mustSet(t, m, ModeRecovery, goodPassword, "")
	if got := ended.take(); len(got) != 1 || got[0] != ":admin_set" {
		t.Fatalf("recovery: %v", got)
	}
	if _, err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := ended.take(); len(got) != 1 || got[0] != ":admin_reset" {
		t.Fatalf("reset: %v", got)
	}
}

func TestConsoleOpenedAlert(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	m.RaiseConsoleOpened("1a2b3c4d")

	al := m.PendingAlerts()
	if len(al) != 1 || al[0].Type != EventConsoleOpened || al[0].SessionAuditID != "1a2b3c4d" || al[0].TS == "" {
		t.Fatalf("alerts %+v", al)
	}
	if got := e.pub.types(); len(got) != 1 || got[0] != EventConsoleOpened {
		t.Fatalf("published %v", got)
	}
	b, _ := json.Marshal(al[0].Data())
	if !strings.Contains(string(b), `"session_audit_id":"1a2b3c4d"`) || strings.Contains(string(b), "device_name") {
		t.Fatalf("alert data %s", b)
	}
	// Persisted: a restarted daemon replays it.
	if al := e.manager().PendingAlerts(); len(al) != 1 || al[0].SessionAuditID != "1a2b3c4d" {
		t.Fatalf("after restart %+v", al)
	}
}
