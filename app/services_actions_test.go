package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/sysbus"
)

var serviceVerbs = []string{
	models.ActionRestart, models.ActionStart, models.ActionStop, models.ActionEnable, models.ActionDisable,
}

const mosquittoReset = "ResetFailedUnit mosquitto.service"

func actionPath(verb string) string { return "/api/v2/system/services/" + verb }

func postAction(t *testing.T, a *AppServer, verb, id string) (int, v2Reply, string) {
	t.Helper()
	return v2Do2(t, a, httpCase{method: "POST", path: actionPath(verb),
		ctype: "application/json", body: restartBody(t, id)})
}

func (p *guardProbe) StartUnit(ctx context.Context, unit string) (*sysbus.Job, error) {
	p.record(ctx, "StartUnit")
	return p.SystemdBus.StartUnit(ctx, unit)
}

func (p *guardProbe) StopUnit(ctx context.Context, unit string) (*sysbus.Job, error) {
	p.record(ctx, "StopUnit")
	return p.SystemdBus.StopUnit(ctx, unit)
}

func (p *guardProbe) ResetFailedUnit(ctx context.Context, unit string) error {
	p.record(ctx, "ResetFailedUnit")
	return p.SystemdBus.ResetFailedUnit(ctx, unit)
}

func (p *guardProbe) EnableUnitFiles(ctx context.Context, units []string) error {
	p.record(ctx, "EnableUnitFiles")
	return p.SystemdBus.EnableUnitFiles(ctx, units)
}

func (p *guardProbe) DisableUnitFiles(ctx context.Context, units []string) error {
	p.record(ctx, "DisableUnitFiles")
	return p.SystemdBus.DisableUnitFiles(ctx, units)
}

func (p *guardProbe) Reload(ctx context.Context) error {
	p.record(ctx, "Reload")
	return p.SystemdBus.Reload(ctx)
}

// fakeSysv replaces systemd-sysv-install and returns the file of its argv.
func fakeSysv(t *testing.T, exit int) string {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "systemd-sysv-install")
	body := "#!/bin/sh\necho \"$@\" >> " + argv + "\necho '" + secretDetail + "' >&2\nexit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { //nolint:gosec // the test must run it
		t.Fatal(err)
	}
	prev := models.SysvInstall
	models.SysvInstall = script
	t.Cleanup(func() { models.SysvInstall = prev })
	return argv
}

func sysvArgv(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test temp file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func waitLog(t *testing.T, logs *logCapture, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if strings.Contains(logs.String(), want) {
			return
		}
	}
	t.Fatalf("no %q in the log: %s", want, logs.String())
}

// Written out rather than derived from models.Services: a row that gains a
// verb must fail here.
func TestServiceActionMatrix(t *testing.T) {
	const notAllowed = "action not allowed for this service"
	const notRestartable = "unknown or non-restartable service"
	all := serviceVerbs
	restart := []string{models.ActionRestart}
	allowed := map[string][]string{
		"calaos-server": restart, "haproxy": restart, "calaos-container": restart, "ssh": restart, "ntp": restart,
		"networkd":    nil,
		"calaos-home": all, "mosquitto": all, "zigbee2mqtt": all, "influxdb": all, "grafana": all,
		"hue2mqtt": all, "envoy": all, "olad": all,
		"": nil, "nope": nil, "mosquitto.service": nil, "calaos-ddns": nil, "../mosquitto": nil, "MOSQUITTO": nil,
	}

	f := useFakeBus(t)
	(&stubBackend{}).install(t)
	guards := countGuard(t, models.ErrUpgradeInProgress)
	a := newHandlerTestApp(t)

	for id, verbs := range allowed {
		for _, verb := range serviceVerbs {
			*guards = 0
			status, r, _ := postAction(t, a, verb, id)
			if slices.Contains(verbs, verb) {
				if status != 409 || r.Code != codeUpgradeInProgress || *guards != 1 {
					t.Fatalf("%s %q: %d %q, %d guards", verb, id, status, r.Code, *guards)
				}
				continue
			}
			msg := notAllowed
			if verb == models.ActionRestart {
				msg = notRestartable
			}
			if status != 400 || r.Code != codeInvalidRequest || r.Msg != msg || *guards != 0 {
				t.Fatalf("%s %q: %d %q %q, %d guards", verb, id, status, r.Code, r.Msg, *guards)
			}
		}
	}
	if c := f.Calls(); len(c) != 0 || f.Reads("UnitProperties") != 0 {
		t.Fatalf("bus reached: %q", c)
	}
}

func TestServiceActionBadBody(t *testing.T) {
	f := useFakeBus(t)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	for _, verb := range serviceVerbs {
		for _, b := range []string{`not json`, `{"unit":5}`, `["mosquitto"]`} {
			status, r, _ := v2Do2(t, a, httpCase{method: "POST", path: actionPath(verb),
				ctype: "application/json", body: b})
			if status != 400 || r.Code != codeInvalidRequest {
				t.Fatalf("%s %s: %d %q", verb, b, status, r.Code)
			}
		}
	}
	if c := f.Calls(); len(c) != 0 {
		t.Fatalf("bus reached: %q", c)
	}
}

func TestServiceActionUnavailable(t *testing.T) {
	const notInstalled, masked = "the unit is not installed", "the unit is masked"
	units := map[string]struct {
		p   sysbus.UnitProperties
		msg string
	}{
		"not found":      {sysbus.UnitProperties{LoadState: "not-found", ActiveState: "failed"}, notInstalled},
		"load masked":    {sysbus.UnitProperties{LoadState: "masked", ActiveState: "failed"}, masked},
		"masked":         {sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked", ActiveState: "failed"}, masked},
		"masked-runtime": {sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked-runtime", ActiveState: "failed"}, masked},
	}
	for name, u := range units {
		for _, verb := range serviceVerbs {
			for _, id := range []string{"mosquitto", "olad"} {
				t.Run(name+" "+verb+" "+id, func(t *testing.T) {
					f := useFakeBus(t)
					f.SetUnit(id+".service", u.p)
					argv := fakeSysv(t, 0)
					done := trackGuard(t)
					(&stubBackend{}).install(t)
					a := newHandlerTestApp(t)

					status, r, _ := postAction(t, a, verb, id)
					if status != 409 || r.Code != codeServiceUnavailable || r.Msg != u.msg {
						t.Fatalf("%d %q %q", status, r.Code, r.Msg)
					}
					waitRestart(t, done)
					if c := f.Calls(); len(c) != 0 {
						t.Fatalf("calls %q", c)
					}
					if got := sysvArgv(t, argv); got != "" {
						t.Fatalf("sysv argv %q", got)
					}
				})
			}
		}
	}
}

func TestServiceActionNoNtpUnit(t *testing.T) {
	f := useFakeBus(t)
	f.SetUnit("chrony.service", sysbus.UnitProperties{LoadState: "not-found"})
	f.SetUnit("systemd-timesyncd.service", sysbus.UnitProperties{LoadState: "not-found"})
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	status, r, _ := postAction(t, a, models.ActionRestart, "ntp")
	if status != 409 || r.Code != codeServiceUnavailable {
		t.Fatalf("%d %q", status, r.Code)
	}
	if c := f.Calls(); len(c) != 0 {
		t.Fatalf("calls %q", c)
	}
}

func TestServiceActionReadFailure(t *testing.T) {
	for _, verb := range serviceVerbs {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.Fail("UnitProperties", errors.New(secretDetail))
			done := trackGuard(t)
			logs := captureLogs(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			status, r, body := postAction(t, a, verb, "mosquitto")
			if status != 500 || r.Code != codeInternal || strings.Contains(body, secretDetail) {
				t.Fatalf("%d %s", status, body)
			}
			waitRestart(t, done)
			if c := f.Calls(); len(c) != 0 {
				t.Fatalf("calls %q", c)
			}
			waitLog(t, logs, secretDetail)
		})
	}
}

// The check reads the unit itself: the list may have cached it available
// less than a second ago.
func TestServiceActionIgnoresTheCache(t *testing.T) {
	for _, verb := range serviceVerbs {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)
			if status, _, body := v2Do2(t, a, httpCase{method: "GET", path: "/api/v2/system/services"}); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			f.SetUnit("mosquitto.service", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked"})
			if status, r, _ := postAction(t, a, verb, "mosquitto"); status != 409 || r.Code != codeServiceUnavailable {
				t.Fatalf("%d %q", status, r.Code)
			}
		})
	}
}

func TestServiceActionCalls(t *testing.T) {
	for _, tc := range []struct {
		verb, state string
		want        []string
	}{
		{models.ActionStart, "starting", []string{mosquittoReset, "StartUnit mosquitto.service"}},
		{models.ActionStop, "stopping", []string{"StopUnit mosquitto.service"}},
		{models.ActionEnable, "enabling", []string{mosquittoReset, "EnableUnitFiles mosquitto.service", "Reload", "StartUnit mosquitto.service"}},
		{models.ActionDisable, "disabling", []string{"DisableUnitFiles mosquitto.service", "Reload", "StopUnit mosquitto.service"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			status, r, body := postAction(t, a, tc.verb, "mosquitto")
			if want := `{"id":"mosquitto","state":"` + tc.state + `"}`; status != 200 || string(r.Data) != want {
				t.Fatalf("%d %s", status, body)
			}
			if c := f.Calls(); !slices.Equal(c, tc.want) {
				t.Fatalf("calls %q", c)
			}
		})
	}
}

func TestServiceActionClearsAFailedUnit(t *testing.T) {
	failed := sysbus.UnitProperties{LoadState: "loaded", ActiveState: "failed", SubState: "failed", UnitFileState: "enabled"}
	for _, tc := range []struct {
		verb string
		want []string
	}{
		{models.ActionStart, []string{mosquittoReset, "StartUnit mosquitto.service"}},
		{models.ActionStop, []string{"StopUnit mosquitto.service", mosquittoReset}},
		{models.ActionEnable, []string{mosquittoReset, "EnableUnitFiles mosquitto.service", "Reload", "StartUnit mosquitto.service"}},
		{models.ActionDisable, []string{"DisableUnitFiles mosquitto.service", "Reload", "StopUnit mosquitto.service", mosquittoReset}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", failed)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			if status, _, body := postAction(t, a, tc.verb, "mosquitto"); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			if c := f.Calls(); !slices.Equal(c, tc.want) {
				t.Fatalf("calls %q", c)
			}
		})
	}

	t.Run(models.ActionRestart, func(t *testing.T) {
		f := useFakeBus(t)
		f.SetUnit("mosquitto.service", failed)
		done := trackGuard(t)
		(&stubBackend{}).install(t)
		a := newHandlerTestApp(t)
		if status, _, body := postAction(t, a, models.ActionRestart, "mosquitto"); status != 200 {
			t.Fatalf("%d %s", status, body)
		}
		waitRestart(t, done)
		want := []string{mosquittoReset, "RestartUnit mosquitto.service"}
		if c := f.Calls(); !slices.Equal(c, want) {
			t.Fatalf("calls %q", c)
		}
	})
}

// The reset answers nothing to the client: the action it comes with has gone
// through, or is about to.
func TestServiceActionResetFailure(t *testing.T) {
	failed := sysbus.UnitProperties{LoadState: "loaded", ActiveState: "failed", UnitFileState: "enabled"}
	for _, verb := range []string{models.ActionStart, models.ActionStop, models.ActionEnable, models.ActionDisable} {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", failed)
			f.Fail("ResetFailedUnit", errors.New(secretDetail))
			logs := captureLogs(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			status, _, body := postAction(t, a, verb, "mosquitto")
			if status != 200 || strings.Contains(body, secretDetail) {
				t.Fatalf("%d %s", status, body)
			}
			waitLog(t, logs, secretDetail)
		})
	}
}

// A healthy unit that dies while stopping must stay failed: nothing here may
// reset it.
func TestServiceActionKeepsAFailureFromTheStop(t *testing.T) {
	for _, verb := range []string{models.ActionStop, models.ActionDisable} {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			f.SetJobResult("mosquitto.service", "failed")
			logs := captureLogs(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			if status, _, body := postAction(t, a, verb, "mosquitto"); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			waitLog(t, logs, "job failed")
			if c := f.Calls(); slices.Contains(c, mosquittoReset) {
				t.Fatalf("calls %q", c)
			}
		})
	}
}

func TestServiceActionDuringUpgradeClearsNothing(t *testing.T) {
	for _, verb := range serviceVerbs {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "failed", UnitFileState: "enabled"})
			(&stubBackend{}).install(t)
			countGuard(t, models.ErrUpgradeInProgress)
			a := newHandlerTestApp(t)

			if status, r, body := postAction(t, a, verb, "mosquitto"); status != 409 || r.Code != "upgrade_in_progress" {
				t.Fatalf("%d %s", status, body)
			}
			if c := f.Calls(); len(c) != 0 {
				t.Fatalf("calls %q", c)
			}
		})
	}
}

func TestServiceActionRestartState(t *testing.T) {
	f := useFakeBus(t)
	f.SetUnit("mosquitto.service", loadedUnit)
	done := trackGuard(t)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	status, r, body := postAction(t, a, models.ActionRestart, "mosquitto")
	if status != 200 || string(r.Data) != `{"id":"mosquitto","state":"restarting"}` {
		t.Fatalf("%d %s", status, body)
	}
	waitRestart(t, done)
}

func TestServiceActionFailedStep(t *testing.T) {
	stopped := sysbus.UnitProperties{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", UnitFileState: "disabled"}
	for _, tc := range []struct {
		verb, failing string
		want          []string
	}{
		{models.ActionStart, "StartUnit", []string{mosquittoReset, "StartUnit mosquitto.service"}},
		{models.ActionStop, "StopUnit", []string{"StopUnit mosquitto.service"}},
		{models.ActionEnable, "EnableUnitFiles", []string{mosquittoReset, "EnableUnitFiles mosquitto.service"}},
		{models.ActionEnable, "Reload", []string{mosquittoReset, "EnableUnitFiles mosquitto.service", "Reload"}},
		{models.ActionEnable, "StartUnit", []string{mosquittoReset, "EnableUnitFiles mosquitto.service", "Reload", "StartUnit mosquitto.service"}},
		{models.ActionDisable, "DisableUnitFiles", []string{"DisableUnitFiles mosquitto.service"}},
		{models.ActionDisable, "Reload", []string{"DisableUnitFiles mosquitto.service", "Reload"}},
		{models.ActionDisable, "StopUnit", []string{"DisableUnitFiles mosquitto.service", "Reload", "StopUnit mosquitto.service"}},
	} {
		t.Run(tc.verb+" "+tc.failing, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			done := trackGuard(t)
			logs := captureLogs(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			get := httpCase{method: "GET", path: "/api/v2/system/services"}
			v2Do2(t, a, get)
			f.SetUnit("mosquitto.service", stopped)
			f.Fail(tc.failing, errors.New(secretDetail))

			status, r, body := postAction(t, a, tc.verb, "mosquitto")
			if status != 500 || r.Code != codeInternal || strings.Contains(body, secretDetail) {
				t.Fatalf("%d %s", status, body)
			}
			waitRestart(t, done)
			if c := f.Calls(); !slices.Equal(c, tc.want) {
				t.Fatalf("calls %q", c)
			}
			waitLog(t, logs, secretDetail)
			// A step may have gone through: the list must show it at once.
			if _, _, list := v2Do2(t, a, get); !strings.Contains(list, `"id":"mosquitto","unit":"mosquitto.service","kind":"service","actions":["restart","start","stop","enable","disable"],"restartable":true,"active_state":"inactive"`) {
				t.Fatalf("stale list %s", list)
			}
		})
	}
}

// The answer is gone when the restart fails: only the list can show it.
func TestServiceRestartFailureResetsTheCache(t *testing.T) {
	f := useFakeBus(t)
	f.SetUnit("mosquitto.service", loadedUnit)
	done := trackGuard(t)
	logs := captureLogs(t)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	get := httpCase{method: "GET", path: "/api/v2/system/services"}
	v2Do2(t, a, get)
	f.SetUnit("mosquitto.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "failed", UnitFileState: "enabled"})
	f.Fail("RestartUnit", errors.New(secretDetail))

	if status, _, body := postAction(t, a, models.ActionRestart, "mosquitto"); status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	waitRestart(t, done)
	waitLog(t, logs, secretDetail)
	if _, _, list := v2Do2(t, a, get); !strings.Contains(list, `"active_state":"failed"`) {
		t.Fatalf("stale list %s", list)
	}
}

// The job ends after the answer: its outcome goes to the log and to the list.
func TestServiceActionJobFails(t *testing.T) {
	for verb, state := range map[string]string{
		models.ActionStart: "starting", models.ActionStop: "stopping",
		models.ActionEnable: "enabling", models.ActionDisable: "disabling",
	} {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			f.SetJobResult("mosquitto.service", "failed")
			logs := captureLogs(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			if status, _, body := postAction(t, a, verb, "mosquitto"); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			waitLog(t, logs, state+" mosquitto: ")
			waitLog(t, logs, "job failed")
		})
	}
}

func TestServiceActionFreesTheGuardBeforeTheJobEnds(t *testing.T) {
	for _, verb := range []string{models.ActionStart, models.ActionStop, models.ActionEnable, models.ActionDisable} {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			f.HoldJobs()
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			if status, _, body := postAction(t, a, verb, "mosquitto"); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			release, err := models.ServiceGuard()
			if err != nil {
				t.Fatalf("guard still held: %v", err)
			}
			release()
			f.FinishJob("mosquitto.service", "done")
		})
	}
}

// Every bus call of the five verbs runs under the guard, and none can hold
// it longer than serviceCallLimit. olad takes the SysV path of enable and
// disable.
func TestServiceActionRunsUnderTheGuard(t *testing.T) {
	for _, id := range []string{"mosquitto", "olad"} {
		for _, verb := range serviceVerbs {
			t.Run(id+" "+verb, func(t *testing.T) { testActionUnderGuard(t, id, verb) })
		}
	}
}

func testActionUnderGuard(t *testing.T, id, verb string) {
	f := useFakeBus(t)
	unit := loadedUnit
	unit.ActiveState = "failed"
	if id == "olad" {
		unit.UnitFileState = "generated"
		fakeSysv(t, 0)
	}
	f.SetUnit(id+".service", unit)
	probe := &guardProbe{SystemdBus: f}
	prev := serviceGuard
	serviceGuard = func() (func(), error) {
		release, err := prev()
		if err != nil {
			return nil, err
		}
		probe.held.Store(true)
		return func() {
			probe.held.Store(false)
			release()
		}, nil
	}
	t.Cleanup(func() { serviceGuard = prev })
	done := trackGuard(t)
	(&stubBackend{}).install(t)
	models.Bus = probe
	a := newHandlerTestApp(t)

	if status, _, body := postAction(t, a, verb, id); status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	waitRestart(t, done)
	if len(f.Calls()) == 0 {
		t.Fatal("no call")
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.unguarded) != 0 {
		t.Fatalf("outside the guard: %q", probe.unguarded)
	}
	if probe.longest > serviceCallLimit {
		t.Fatalf("a bus call may last %s under the guard", probe.longest)
	}
}

func TestServiceActionSysv(t *testing.T) {
	for verb, want := range map[string][]string{
		models.ActionEnable:  {"ResetFailedUnit olad.service", "Reload", "StartUnit olad.service"},
		models.ActionDisable: {"Reload", "StopUnit olad.service"},
	} {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("olad.service", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "generated"})
			argv := fakeSysv(t, 0)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			if status, _, body := postAction(t, a, verb, "olad"); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			if got := sysvArgv(t, argv); got != verb+" olad\n" {
				t.Fatalf("sysv argv %q", got)
			}
			if c := f.Calls(); !slices.Equal(c, want) {
				t.Fatalf("calls %q", c)
			}
		})
		t.Run(verb+" failed", func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("olad.service", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "generated"})
			fakeSysv(t, 1)
			logs := captureLogs(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			status, r, body := postAction(t, a, verb, "olad")
			if status != 500 || r.Code != codeInternal || strings.Contains(body, secretDetail) {
				t.Fatalf("%d %s", status, body)
			}
			var want []string
			if verb == models.ActionEnable {
				want = []string{"ResetFailedUnit olad.service"}
			}
			if c := f.Calls(); !slices.Equal(c, want) {
				t.Fatalf("calls %q", c)
			}
			waitLog(t, logs, secretDetail)
		})
	}
}

func TestServiceRestartNetworkPending(t *testing.T) {
	e := newNetEnv(t)
	e.nc.SetUnit("calaos-container.service", loadedUnit)
	e.nc.SetUnit("mosquitto.service", loadedUnit)
	p, _ := e.apply(e.c, e.s, nil)
	e.nc.Take()
	done := trackGuard(t)
	restart := actionPath(models.ActionRestart)

	r := e.c.expect("POST", restart, `{"unit":"calaos-container"}`, session(e.s), 409, codeNetworkPending)
	if !strings.Contains(string(r.Data), `"change_id":"`+p.ChangeID+`"`) {
		t.Fatalf("data %s", r.Data)
	}
	waitRestart(t, done)

	e.nc.SetUnit("calaos-container.service", sysbus.UnitProperties{LoadState: "not-found"})
	e.c.expect("POST", restart, `{"unit":"calaos-container"}`, session(e.s), 409, codeServiceUnavailable)
	waitRestart(t, done)
	if c := e.nc.Take(); len(c) != 0 {
		t.Fatalf("calls %q", c)
	}

	// Only the daemon carries the confirmation of the change.
	e.c.expect("POST", restart, `{"unit":"mosquitto"}`, session(e.s), 200, "")
	waitRestart(t, done)
	if c := e.nc.Take(); !slices.Contains(c, "RestartUnit mosquitto.service") {
		t.Fatalf("calls %q", c)
	}
}

func TestServiceActionNeedsAGoodSession(t *testing.T) {
	f := useFakeBus(t)
	f.SetUnit("mosquitto.service", loadedUnit)
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	for _, verb := range serviceVerbs {
		c.expect("POST", actionPath(verb), `{"unit":"mosquitto"}`, session(strings.Repeat("0", 64)), 401, codeBadSession)
	}
	if calls := f.Calls(); len(calls) != 0 || f.Reads("UnitProperties") != 0 {
		t.Fatalf("bus reached: %q", calls)
	}
}

func TestServiceActionAudit(t *testing.T) {
	f := useFakeBus(t)
	f.SetUnit("mosquitto.service", loadedUnit)
	f.SetUnit("grafana.service", sysbus.UnitProperties{LoadState: "loaded", UnitFileState: "masked"})
	done := trackGuard(t)
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)
	line := func(result, unit, verb string) string {
		return `result=` + result + ` session=` + admin.SessionAuditID(s) + unit + ` POST "` + actionPath(verb) + `"`
	}

	for _, verb := range serviceVerbs {
		c.expect("POST", actionPath(verb), `{"unit":"mosquitto"}`, session(s), 200, "")
		waitRestart(t, done)
		c.expect("POST", actionPath(verb), `{"unit":"grafana"}`, session(s), 409, codeServiceUnavailable)
		waitRestart(t, done)
		c.expect("POST", actionPath(verb), `{"unit":"networkd"}`, session(s), 400, codeInvalidRequest)
		c.expect("POST", actionPath(verb), `{"unit":"x y"}`, session(s), 400, codeInvalidRequest)
	}

	log := readAudit(t)
	for _, verb := range serviceVerbs {
		for _, want := range []string{
			line("ok", " unit=mosquitto", verb),
			line("service_unavailable", " unit=grafana", verb),
			line("invalid_request", "", verb),
		} {
			if !strings.Contains(log, want) {
				t.Fatalf("audit missing %q:\n%s", want, log)
			}
		}
	}
	if strings.Contains(log, "unit=networkd") || strings.Contains(log, "x y") {
		t.Fatalf("refused value in audit:\n%s", log)
	}
}
