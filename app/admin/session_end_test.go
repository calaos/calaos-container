package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEndClosesConsolesOfThatSessionOnly(t *testing.T) {
	e := newEnv(t)
	var ended endedCalls
	m := New(Options{DataDir: e.data, RuntimeDir: e.run, Hash: testParams, Publish: e.pub.publish, Now: e.clk.Now, SessionsEnded: ended.record})
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)
	other := mustVerify(t, m, goodPassword)
	ended.take()

	if ok, err := m.End(s); !ok || err != nil {
		t.Fatalf("End: %v %v", ok, err)
	}
	if got := ended.take(); len(got) != 1 || got[0] != SessionHash(s)+":"+ReasonLogout {
		t.Fatalf("consoles ended: %v", got)
	}
	if ok, _ := m.End(s); ok {
		t.Fatal("second End reported ended")
	}
	if got := ended.take(); len(got) != 0 {
		t.Fatalf("second End closed consoles: %v", got)
	}
	if !m.Valid(other) {
		t.Fatal("the other session died")
	}
}

// A failed write must not leave the revoked session in the file: it would
// come back at the next daemon start.
func TestEndRemovesTheFileWhenItCannotBeWritten(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s := mustVerify(t, m, goodPassword)

	// Renaming a file over a directory fails; an empty one can still be removed.
	path := filepath.Join(e.run, sessionFileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := m.End(s); err == nil {
		t.Fatal("End hid the write failure")
	}
	if m.Valid(s) {
		t.Fatal("session still valid")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("sessions file left behind: %v", err)
	}
}
