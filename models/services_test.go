package models

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/calaos/calaos-container/sysbus"
	"github.com/calaos/calaos-container/sysbus/sysbustest"
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

var (
	loaded   = sysbus.UnitProperties{LoadState: "loaded", ActiveState: "active", SubState: "running", UnitFileState: "enabled"}
	notFound = sysbus.UnitProperties{LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}
)

func TestResolveUnit(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name              string
		chrony, timesyncd sysbus.UnitProperties
		want              string
	}{
		{"first installed", loaded, loaded, "chrony.service"},
		{"fallback", notFound, loaded, "systemd-timesyncd.service"},
		{"none installed", notFound, notFound, "chrony.service"},
		// A masked unit is installed: the fallback must not win over it.
		{"masked first", sysbus.UnitProperties{LoadState: "masked"}, loaded, "chrony.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sysbustest.New()
			f.SetUnit("chrony.service", tc.chrony)
			f.SetUnit("systemd-timesyncd.service", tc.timesyncd)
			got, err := ResolveUnit(ctx, f, "chrony.service")
			if err != nil || got != tc.want {
				t.Fatalf("%q, %v", got, err)
			}
		})
	}

	f := sysbustest.New()
	f.Fail("UnitProperties", errors.New("boom"))
	if got, err := ResolveUnit(ctx, f, "chrony.service"); err == nil || got != "chrony.service" {
		t.Fatalf("%q, %v", got, err)
	}
	if got, err := ResolveUnit(ctx, shortBus{f}, "chrony.service"); err == nil || got != "chrony.service" {
		t.Fatalf("short reply: %q, %v", got, err)
	}
}

// shortBus answers fewer rows than asked, which must not shift the table.
type shortBus struct{ sysbus.SystemdBus }

func (shortBus) UnitProperties(context.Context, ...string) ([]sysbus.UnitProperties, error) {
	return nil, nil
}

func TestServiceStates(t *testing.T) {
	f := sysbustest.New()
	f.SetUnit("calaos-server.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "active", SubState: "running",
		UnitFileState: "enabled", ActiveEnterTimestamp: 1793000472*1_000_000 + 123456})
	f.SetUnit("chrony.service", notFound)
	f.SetUnit("systemd-timesyncd.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", UnitFileState: "disabled"})
	f.SetUnit("grafana.service", sysbus.UnitProperties{LoadState: "masked", ActiveState: "inactive", SubState: "dead", UnitFileState: "masked"})

	states, err := ServiceStates(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != len(Services) || f.Reads("UnitProperties") != 1 {
		t.Fatalf("%d rows, %d reads", len(states), f.Reads("UnitProperties"))
	}
	byID := map[string]ServiceState{}
	for i, st := range states {
		if st.ID != Services[i].ID {
			t.Fatalf("row %d is %s", i, st.ID)
		}
		byID[st.ID] = st
	}

	if st := byID["calaos-server"]; st.Since == nil || *st.Since != "2026-10-26T07:41:12Z" || st.ActiveState != "active" ||
		st.SubState != "running" || st.Enabled != "enabled" {
		t.Fatalf("%+v", st)
	}
	if st := byID["ntp"]; st.Unit != "systemd-timesyncd.service" || st.ActiveState != "inactive" || st.Enabled != "disabled" || st.Since != nil {
		t.Fatalf("%+v", st)
	}
	if st := byID["ssh"]; st != (ServiceState{ID: "ssh", Unit: "ssh.service", Kind: ServiceKindService, ActiveState: "not-found"}) {
		t.Fatalf("%+v", st)
	}
	// Masked stays a normal row: only a restart refuses it.
	if st := byID["grafana"]; st.ActiveState != "inactive" || st.Enabled != "masked" {
		t.Fatalf("%+v", st)
	}
}

func TestServiceStatesFirstCandidateWins(t *testing.T) {
	f := sysbustest.New()
	f.SetUnit("chrony.service", loaded)
	f.SetUnit("systemd-timesyncd.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "inactive"})
	states, err := ServiceStates(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.ID == "ntp" && (st.Unit != "chrony.service" || st.ActiveState != "active") {
			t.Fatalf("%+v", st)
		}
	}
}

func TestServiceStatesFailure(t *testing.T) {
	f := sysbustest.New()
	f.Fail("UnitProperties", errors.New("boom"))
	if _, err := ServiceStates(context.Background(), f); err == nil {
		t.Fatal("no error")
	}
	if _, err := ServiceStates(context.Background(), shortBus{f}); err == nil {
		t.Fatal("no error on a short reply")
	}
}

func TestCheckUnit(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		p    sysbus.UnitProperties
		ok   bool
	}{
		{"loaded", loaded, true},
		{"disabled", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "disabled"}, true},
		{"sysv only", sysbus.UnitProperties{LoadState: "loaded"}, true},
		{"not found", notFound, false},
		{"masked", sysbus.UnitProperties{LoadState: "masked"}, false},
		{"unit file masked", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked"}, false},
		{"masked in run", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked-runtime"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sysbustest.New()
			f.SetUnit("x.service", tc.p)
			err := CheckUnit(ctx, f, "x.service")
			if tc.ok != (err == nil) || (err != nil && !errors.Is(err, ErrUnitUnavailable)) {
				t.Fatalf("%v", err)
			}
		})
	}

	f := sysbustest.New()
	boom := errors.New("boom")
	f.Fail("UnitProperties", boom)
	if err := CheckUnit(ctx, f, "x.service"); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
	if err := CheckUnit(ctx, shortBus{f}, "x.service"); err == nil {
		t.Fatal("no error on a short reply")
	}
}

func TestQueueRestart(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		s    Service
		want string
	}{
		{"service", Service{ID: "mosquitto", Kind: ServiceKindService}, "RestartUnit x.service"},
		{"oneshot", Service{ID: "backup", Kind: ServiceKindOneshot}, "StartUnit x.service"},
		{"ssh", Service{ID: "ssh", Kind: ServiceKindService}, "ReloadOrRestartUnit x.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sysbustest.New()
			f.SetUnit("x.service", loaded)
			job, err := QueueRestart(ctx, f, tc.s, "x.service")
			if err != nil {
				t.Fatal(err)
			}
			if err := job.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if c := f.Calls(); len(c) != 1 || c[0] != tc.want {
				t.Fatalf("%q", c)
			}
		})
	}
}

func TestQueueRestartRefused(t *testing.T) {
	ctx := context.Background()
	s := Service{ID: "mosquitto", Kind: ServiceKindService}

	f := sysbustest.New()
	if _, err := QueueRestart(ctx, f, s, "x.service"); !errors.Is(err, ErrUnitUnavailable) {
		t.Fatalf("not found: %v", err)
	}
	f.SetUnit("x.service", sysbus.UnitProperties{LoadState: "masked"})
	if _, err := QueueRestart(ctx, f, s, "x.service"); !errors.Is(err, ErrUnitUnavailable) {
		t.Fatalf("masked: %v", err)
	}
	f.Fail("UnitProperties", errors.New("boom"))
	if _, err := QueueRestart(ctx, f, s, "x.service"); err == nil {
		t.Fatal("no error on a failed read")
	}
	if c := f.Calls(); len(c) != 0 {
		t.Fatalf("%q", c)
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
