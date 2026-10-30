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
	if s, _ := LookupService("ntp"); s.Unit != "chrony.service" ||
		strings.Join(UnitCandidates(s.Unit), " ") != "chrony.service systemd-timesyncd.service" {
		t.Fatalf("ntp unit %q", s.Unit)
	}
	for _, id := range []string{"chrony", "timesyncd"} {
		if _, ok := LookupService(id); ok {
			t.Fatalf("%s is a unit name, not an id", id)
		}
	}
}

func TestRestartArgs(t *testing.T) {
	if got := strings.Join(Service{"x", "x.service", ServiceKindOneshot, true}.RestartArgs(), " "); got != "start --no-block x.service" {
		t.Fatalf("oneshot %q", got)
	}
	if s, _ := LookupService("mosquitto"); strings.Join(s.RestartArgs(), " ") != "restart --no-block mosquitto.service" {
		t.Fatalf("service %q", s.RestartArgs())
	}
}

func TestParseResolvedUnit(t *testing.T) {
	if got := strings.Join(ResolveUnitArgs("chrony.service"), " "); got != "show --no-pager -p LoadState --value chrony.service systemd-timesyncd.service" {
		t.Fatalf("args %q", got)
	}
	for out, want := range map[string]string{
		"loaded\n\nnot-found\n":    "chrony.service",
		"loaded\n\nloaded\n":       "chrony.service",
		"not-found\n\nloaded\n":    "systemd-timesyncd.service",
		"not-found\n\nmasked\n":    "systemd-timesyncd.service",
		"not-found\n\nnot-found\n": "chrony.service",
		"loaded\n":                 "chrony.service",
		"":                         "chrony.service",
	} {
		if got := ParseResolvedUnit("chrony.service", out); got != want {
			t.Fatalf("%q: %q, want %q", out, got, want)
		}
	}
	if HasUnitFallbacks("ssh.service") || ParseResolvedUnit("ssh.service", "not-found\n") != "ssh.service" {
		t.Fatal("a unit without fallbacks was resolved")
	}
}

func TestParseServiceStatesFallback(t *testing.T) {
	blocks := map[string]string{
		"chrony.service":            "LoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\nActiveEnterTimestamp=\n\n",
		"systemd-timesyncd.service": "LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nActiveEnterTimestamp=@1793000472\n\n",
	}
	var b strings.Builder
	for _, s := range Services {
		for _, u := range UnitCandidates(s.Unit) {
			if blk, ok := blocks[u]; ok {
				b.WriteString(blk)
			} else {
				b.WriteString("LoadState=not-found\n\n")
			}
		}
	}
	states, err := ParseServiceStates(b.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.ID == "ntp" && (st.Unit != "systemd-timesyncd.service" || st.ActiveState != "active" || st.Enabled != "enabled") {
			t.Fatalf("%+v", st)
		}
	}
	if _, err := ParseServiceStates(strings.Repeat("LoadState=not-found\n\n", len(Services))); err == nil {
		t.Fatal("one block per row accepted, the fallbacks are missing")
	}
}

func TestParseServiceStatesFirstCandidateWins(t *testing.T) {
	var b strings.Builder
	for _, s := range Services {
		for _, u := range UnitCandidates(s.Unit) {
			b.WriteString("LoadState=loaded\nActiveState=active\nSubState=" + u + "\n\n")
		}
	}
	states, err := ParseServiceStates(b.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.ID == "ntp" && (st.Unit != "chrony.service" || st.SubState != "chrony.service") {
			t.Fatalf("%+v", st)
		}
	}
}

func TestParseServiceStatesNotFound(t *testing.T) {
	var b strings.Builder
	for _, s := range Services {
		for _, u := range UnitCandidates(s.Unit) {
			b.WriteString("Id=" + u + "\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\nActiveEnterTimestamp=\n\n")
		}
	}
	states, err := ParseServiceStates(b.String())
	if err != nil {
		t.Fatal(err)
	}
	for i, st := range states {
		if st.ID != Services[i].ID || st.Unit != Services[i].Unit || st.ActiveState != "not-found" || st.SubState != "" || st.Enabled != "" || st.Since != nil {
			t.Fatalf("%+v", st)
		}
	}
}

func TestParseServiceStatesBadTimestamp(t *testing.T) {
	var b strings.Builder
	for _, s := range Services {
		for range UnitCandidates(s.Unit) {
			b.WriteString("LoadState=loaded\nActiveEnterTimestamp=Sat 2026-10-25 07:41:12 CET\n\n")
		}
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
