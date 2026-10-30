package models

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
	rows := map[string][]string{
		"calaos-server":    {"restart"},
		"haproxy":          {"restart"},
		"calaos-container": {"restart"},
		"ssh":              {"restart"},
		"ntp":              {"restart"},
		"networkd":         {},
	}
	for _, id := range []string{"calaos-home", "mosquitto", "zigbee2mqtt", "influxdb", "grafana", "hue2mqtt", "envoy", "olad"} {
		rows[id] = []string{"restart", "start", "stop", "enable", "disable"}
	}
	if len(rows) != len(Services) {
		t.Fatalf("%d rows", len(Services))
	}
	for id, want := range rows {
		if s, ok := LookupService(id); !ok || !slices.Equal(s.Actions, want) || s.Actions == nil {
			t.Fatalf("%s: %+v, want %q", id, s, want)
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
	if st := byID["ssh"]; !reflect.DeepEqual(st, ServiceState{ID: "ssh", Unit: "ssh.service", Kind: ServiceKindService,
		Actions: []string{"restart"}, Restartable: true, BootUnit: "ssh.service", ActiveState: "not-found"}) {
		t.Fatalf("%+v", st)
	}
	// Masked stays a normal row: only the actions refuse it.
	if st := byID["grafana"]; st.ActiveState != "inactive" || st.Enabled != "masked" || st.Startup != "masked" {
		t.Fatalf("%+v", st)
	}
	for _, st := range states {
		if st.BootUnit != st.Unit {
			t.Fatalf("%+v", st)
		}
		if s, _ := LookupService(st.ID); st.Restartable != s.Allows(ActionRestart) {
			t.Fatalf("%+v", st)
		}
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
		want error
	}{
		{"loaded", loaded, nil},
		{"disabled", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "disabled"}, nil},
		{"sysv only", sysbus.UnitProperties{LoadState: "loaded"}, nil},
		{"not found", notFound, ErrUnitNotInstalled},
		{"masked", sysbus.UnitProperties{LoadState: "masked"}, ErrUnitMasked},
		{"unit file masked", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked"}, ErrUnitMasked},
		{"masked in run", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked-runtime"}, ErrUnitMasked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sysbustest.New()
			f.SetUnit("x.service", tc.p)
			err := CheckUnit(ctx, f, "x.service")
			if !errors.Is(err, tc.want) || (tc.want != nil && !errors.Is(err, ErrUnitUnavailable)) {
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
		{"service", Service{ID: "mosquitto"}, "RestartUnit x.service"},
		{"ssh", Service{ID: "ssh"}, "ReloadOrRestartUnit x.service"},
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
	s := Service{ID: "mosquitto"}

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

func TestNormalizeStartup(t *testing.T) {
	for state, want := range map[string]string{
		"enabled": "enabled", "enabled-runtime": "enabled", "linked": "enabled", "linked-runtime": "enabled", "alias": "enabled",
		"disabled": "disabled",
		"masked":   "masked", "masked-runtime": "masked",
		"static": "static", "indirect": "static", "generated": "static", "transient": "static", "bad": "static", "": "static",
	} {
		if got := NormalizeStartup(state); got != want {
			t.Fatalf("%q: %q, want %q", state, got, want)
		}
	}
}

// fakeSysvInstall replaces systemd-sysv-install and returns where it records
// its argv.
func fakeSysvInstall(t *testing.T, exit int) string {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "systemd-sysv-install")
	body := "#!/bin/sh\necho \"$@\" >> " + argv + "\necho sysv-detail >&2\nexit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { //nolint:gosec // the test must run it
		t.Fatal(err)
	}
	prev := SysvInstall
	SysvInstall = script
	t.Cleanup(func() { SysvInstall = prev })
	return argv
}

func readArgv(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test temp file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func serviceState(t *testing.T, f *sysbustest.Bus, id string) ServiceState {
	t.Helper()
	states, err := ServiceStates(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("no row %s", id)
	return ServiceState{}
}

func TestServiceStatesSysvStartup(t *testing.T) {
	generated := sysbus.UnitProperties{LoadState: "loaded", ActiveState: "active", UnitFileState: "generated"}
	for _, tc := range []struct {
		name string
		exit int
		want string
	}{{"enabled", 0, "enabled"}, {"disabled", 1, "disabled"}, {"broken", 4, "disabled"}} {
		t.Run(tc.name, func(t *testing.T) {
			argv := fakeSysvInstall(t, tc.exit)
			f := sysbustest.New()
			f.SetUnit("olad.service", generated)
			st := serviceState(t, f, "olad")
			if st.Startup != tc.want || st.Enabled != "generated" {
				t.Fatalf("%+v", st)
			}
			if got := readArgv(t, argv); got != "is-enabled olad\n" {
				t.Fatalf("argv %q", got)
			}
		})
	}

	t.Run("not installed", func(t *testing.T) {
		prev := SysvInstall
		SysvInstall = filepath.Join(t.TempDir(), "none")
		t.Cleanup(func() { SysvInstall = prev })
		f := sysbustest.New()
		f.SetUnit("olad.service", generated)
		if st := serviceState(t, f, "olad"); st.Startup != "disabled" {
			t.Fatalf("%+v", st)
		}
	})

	// The extra call belongs to the SysV row alone, and only while it is one.
	t.Run("other rows", func(t *testing.T) {
		argv := fakeSysvInstall(t, 0)
		f := sysbustest.New()
		f.SetUnit("mosquitto.service", generated)
		f.SetUnit("olad.service", loaded)
		if st := serviceState(t, f, "mosquitto"); st.Startup != "static" {
			t.Fatalf("%+v", st)
		}
		f.SetUnit("olad.service", sysbus.UnitProperties{LoadState: "masked", UnitFileState: "generated"})
		if st := serviceState(t, f, "olad"); st.Startup != "masked" {
			t.Fatalf("%+v", st)
		}
		f.SetUnit("olad.service", notFound)
		if st := serviceState(t, f, "olad"); st.Startup != "" {
			t.Fatalf("%+v", st)
		}
		if got := readArgv(t, argv); got != "" {
			t.Fatalf("argv %q", got)
		}
	})
}

func TestServiceStatesNtp(t *testing.T) {
	for _, tc := range []struct {
		name              string
		chrony, timesyncd sysbus.UnitProperties
		unit, state       string
	}{
		{"chrony only", loaded, notFound, "chrony.service", "active"},
		{"timesyncd only", notFound, loaded, "systemd-timesyncd.service", "active"},
		{"neither", notFound, notFound, "chrony.service", "not-found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sysbustest.New()
			f.SetUnit("chrony.service", tc.chrony)
			f.SetUnit("systemd-timesyncd.service", tc.timesyncd)
			st := serviceState(t, f, "ntp")
			if st.Unit != tc.unit || st.BootUnit != tc.unit || st.ActiveState != tc.state {
				t.Fatalf("%+v", st)
			}
		})
	}
}

func TestQueueAction(t *testing.T) {
	mosquitto, _ := LookupService("mosquitto")
	for _, tc := range []struct {
		verb string
		want []string
	}{
		{ActionStart, []string{"StartUnit x.service"}},
		{ActionStop, []string{"StopUnit x.service"}},
		{ActionEnable, []string{"EnableUnitFiles x.service", "Reload", "StartUnit x.service"}},
		{ActionDisable, []string{"DisableUnitFiles x.service", "Reload", "StopUnit x.service"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			f := sysbustest.New()
			job, err := QueueAction(f, mosquitto, "x.service", tc.verb, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := job.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if c := f.Calls(); !slices.Equal(c, tc.want) {
				t.Fatalf("%q", c)
			}
		})
	}

	f := sysbustest.New()
	if job, err := QueueAction(f, mosquitto, "x.service", ActionRestart, time.Second); err == nil || job != nil || len(f.Calls()) != 0 {
		t.Fatalf("restart has its own path: %v %q", err, f.Calls())
	}
}

// A failed step ends the sequence: no enable without its start, no disable
// without its stop, and no job for the caller to wait on.
func TestQueueActionStopsAtTheFirstFailure(t *testing.T) {
	mosquitto, _ := LookupService("mosquitto")
	for _, tc := range []struct {
		verb, failing string
		want          []string
	}{
		{ActionEnable, "EnableUnitFiles", []string{"EnableUnitFiles x.service"}},
		{ActionEnable, "Reload", []string{"EnableUnitFiles x.service", "Reload"}},
		{ActionEnable, "StartUnit", []string{"EnableUnitFiles x.service", "Reload", "StartUnit x.service"}},
		{ActionDisable, "DisableUnitFiles", []string{"DisableUnitFiles x.service"}},
		{ActionDisable, "Reload", []string{"DisableUnitFiles x.service", "Reload"}},
		{ActionDisable, "StopUnit", []string{"DisableUnitFiles x.service", "Reload", "StopUnit x.service"}},
	} {
		t.Run(tc.verb+" "+tc.failing, func(t *testing.T) {
			f := sysbustest.New()
			boom := errors.New("boom")
			f.Fail(tc.failing, boom)
			job, err := QueueAction(f, mosquitto, "x.service", tc.verb, time.Second)
			if !errors.Is(err, boom) || job != nil {
				t.Fatalf("job %v err %v", job, err)
			}
			if c := f.Calls(); !slices.Equal(c, tc.want) {
				t.Fatalf("%q", c)
			}
		})
	}
}

func TestQueueActionSysv(t *testing.T) {
	olad, _ := LookupService("olad")
	for verb, want := range map[string][]string{
		ActionEnable:  {"Reload", "StartUnit olad.service"},
		ActionDisable: {"Reload", "StopUnit olad.service"},
	} {
		t.Run(verb, func(t *testing.T) {
			argv := fakeSysvInstall(t, 0)
			f := sysbustest.New()
			job, err := QueueAction(f, olad, "olad.service", verb, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := job.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := readArgv(t, argv); got != verb+" olad\n" {
				t.Fatalf("argv %q", got)
			}
			if c := f.Calls(); !slices.Equal(c, want) {
				t.Fatalf("%q", c)
			}
		})
		t.Run(verb+" failed", func(t *testing.T) {
			fakeSysvInstall(t, 1)
			f := sysbustest.New()
			job, err := QueueAction(f, olad, "olad.service", verb, time.Second)
			if err == nil || job != nil || !strings.Contains(err.Error(), "sysv-detail") {
				t.Fatalf("job %v err %v", job, err)
			}
			if c := f.Calls(); len(c) != 0 {
				t.Fatalf("%q", c)
			}
		})
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
