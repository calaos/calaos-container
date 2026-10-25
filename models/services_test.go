package models

import (
	"errors"
	"strings"
	"testing"
)

func TestServicesTable(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Services {
		if seen[s.ID] || !strings.HasSuffix(s.Unit, ".service") || strings.ContainsAny(s.Unit, "@/ ") {
			t.Fatalf("bad row %+v", s)
		}
		seen[s.ID] = true
	}
	for _, id := range []string{"calaos-container", "haproxy", "networkd", "ssh"} {
		if s, ok := LookupService(id); !ok || s.Restartable {
			t.Fatalf("%s must be status only", id)
		}
	}
	if s, _ := LookupService("timesyncd"); s.Unit != "systemd-timesyncd.service" {
		t.Fatalf("timesyncd unit %q", s.Unit)
	}
	if _, ok := LookupService("chrony"); ok {
		t.Fatal("chrony is not a Calaos OS unit")
	}
}

func TestParseServiceStatesNotFound(t *testing.T) {
	var b strings.Builder
	for _, s := range Services {
		b.WriteString("Id=" + s.Unit + "\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\nActiveEnterTimestamp=\n\n")
	}
	states, err := ParseServiceStates(b.String())
	if err != nil {
		t.Fatal(err)
	}
	for i, st := range states {
		if st.ID != Services[i].ID || st.ActiveState != "not-found" || st.SubState != "" || st.Enabled != "" || st.Since != nil {
			t.Fatalf("%+v", st)
		}
	}
}

func TestParseServiceStatesBadTimestamp(t *testing.T) {
	var b strings.Builder
	for range Services {
		b.WriteString("LoadState=loaded\nActiveEnterTimestamp=Sat 2026-10-25 07:41:12 CET\n\n")
	}
	if _, err := ParseServiceStates(b.String()); err == nil {
		t.Fatal("a non-unix timestamp was accepted")
	}
}

func TestServiceGuardRefusedDuringUpgrade(t *testing.T) {
	holdLock(t, true)
	if _, err := ServiceGuard(); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("err %v", err)
	}
}

func TestServiceGuardHoldsUntilRelease(t *testing.T) {
	release, err := ServiceGuard()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartJob(JobKindUpgradeAll, ""); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("upgrade while a restart is pending: %v", err)
	}
	release()
	release()
	if !upgradeLock.TryAcquire(1) {
		t.Fatal("lock still held after release")
	}
	upgradeLock.Release(1)
}
