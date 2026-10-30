package app

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	logger "github.com/calaos/calaos-container/log"
	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/sysbus"
	"github.com/calaos/calaos-container/sysbus/sysbustest"
)

var loadedUnit = sysbus.UnitProperties{LoadState: "loaded", ActiveState: "active", SubState: "running", UnitFileState: "enabled"}

// useFakeBus must come before stubBackend.install, which then keeps it.
func useFakeBus(t *testing.T) *sysbustest.Bus {
	t.Helper()
	f := sysbustest.New()
	prev := models.Bus
	models.Bus = f
	resetServicesCache()
	t.Cleanup(func() {
		models.Bus = prev
		resetServicesCache()
	})
	return f
}

// logCapture is safe to read while a restart still logs from its goroutine.
type logCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	logs := &logCapture{}
	logger.Logger.SetOutput(logs)
	t.Cleanup(func() { logger.Logger.SetOutput(os.Stdout) })
	return logs
}

// trackGuard wraps the real guard so a test can wait for the deferred
// restart to finish.
func trackGuard(t *testing.T) <-chan struct{} {
	t.Helper()
	done := make(chan struct{}, 16)
	prev := serviceGuard
	serviceGuard = func() (func(), error) {
		release, err := prev()
		if err != nil {
			return nil, err
		}
		return func() { release(); done <- struct{}{} }, nil
	}
	t.Cleanup(func() { serviceGuard = prev })
	return done
}

func waitRestart(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart never ran")
	}
}

func TestServicesList(t *testing.T) {
	f := useFakeBus(t)
	running := loadedUnit
	running.ActiveEnterTimestamp = 1793000472 * 1_000_000
	f.SetUnit("calaos-server.service", running)
	f.SetUnit("systemd-timesyncd.service", running)
	f.SetUnit("calaos-home.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "failed", SubState: "failed"})
	f.SetUnit("envoy.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", UnitFileState: "disabled"})
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	status, _, body := v2Do2(t, a, httpCase{method: "GET", path: "/api/v2/system/services"})
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	for _, want := range []string{
		`{"error":false,"msg":"ok","data":{"services":[{"id":"calaos-server","unit":"calaos-server.service","kind":"service","restartable":true,"active_state":"active","sub_state":"running","enabled":"enabled","since":"2026-10-26T07:41:12Z"},`,
		`{"id":"calaos-home","unit":"calaos-home.service","kind":"service","restartable":true,"active_state":"failed","sub_state":"failed","enabled":"","since":null}`,
		`{"id":"ntp","unit":"systemd-timesyncd.service","kind":"service","restartable":true,"active_state":"active","sub_state":"running","enabled":"enabled","since":"2026-10-26T07:41:12Z"}`,
		`{"id":"envoy","unit":"envoy.service","kind":"service","restartable":true,"active_state":"inactive","sub_state":"dead","enabled":"disabled","since":null}`,
		`{"id":"ssh","unit":"ssh.service","kind":"service","restartable":false,"active_state":"not-found","sub_state":"","enabled":"","since":null}]}}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s\nin %s", want, body)
		}
	}

	// Cached: a second read within 1 s does not reach the bus again.
	v2Do2(t, a, httpCase{method: "GET", path: "/api/v2/system/services"})
	if n := f.Reads("UnitProperties"); n != 1 {
		t.Fatalf("%d reads", n)
	}
}

func TestServicesListFailure(t *testing.T) {
	f := useFakeBus(t)
	f.Fail("UnitProperties", errors.New(secretDetail))
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	status, r, body := v2Do2(t, a, httpCase{method: "GET", path: "/api/v2/system/services"})
	if status != 500 || r.Code != codeInternal || strings.Contains(body, "SECRET") {
		t.Fatalf("%d %s", status, body)
	}
}

// A burst of polls within the TTL makes one read, and nothing races on it.
func TestServicesCacheConcurrent(t *testing.T) {
	f := useFakeBus(t)
	f.SetUnit("calaos-server.service", loadedUnit)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if states, err := serviceStates(); err != nil || len(states) != len(models.Services) {
				t.Errorf("%d states: %v", len(states), err)
			}
		}()
	}
	wg.Wait()
	if n := f.Reads("UnitProperties"); n != 1 {
		t.Fatalf("%d reads", n)
	}
}

// TestServiceRestartAnswersFirst: the answer is out before the bus is called.
func TestServiceRestartAnswersFirst(t *testing.T) {
	cases := []struct{ id, installed, want string }{
		{"calaos-server", "calaos-server.service", "RestartUnit calaos-server.service"},
		{"ntp", "chrony.service", "RestartUnit chrony.service"},
		{"ntp", "systemd-timesyncd.service", "RestartUnit systemd-timesyncd.service"},
	}
	for _, tc := range cases {
		id := tc.id
		t.Run(id, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit(tc.installed, loadedUnit)
			done := trackGuard(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			start := time.Now()
			status, _, body := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
				ctype: "application/json", body: `{"unit":"` + id + `"}`})
			answered := time.Since(start)
			if status != 200 || body != `{"error":false,"msg":"ok","data":{"id":"`+id+`","state":"restarting"}}` {
				t.Fatalf("%d %s", status, body)
			}
			if c := f.Calls(); len(c) != 0 || f.Reads("UnitProperties") != 0 {
				t.Fatalf("bus reached before the answer: %q", c)
			}
			waitRestart(t, done)
			if time.Since(start) < restartDelay || answered >= restartDelay {
				t.Fatalf("answered after %v, restarted before %v", answered, restartDelay)
			}
			if c := f.Calls(); len(c) != 1 || c[0] != tc.want {
				t.Fatalf("calls %q, want %q", c, tc.want)
			}
		})
	}
}

// TestServiceRestartReleasesLock: whatever becomes of the restart, the upgrade
// lock comes back.
func TestServiceRestartReleasesLock(t *testing.T) {
	for name, breakIt := range map[string]func(*sysbustest.Bus){
		"ok":          func(*sysbustest.Bus) {},
		"job failed":  func(f *sysbustest.Bus) { f.SetJobResult("mosquitto.service", "failed") },
		"not queued":  func(f *sysbustest.Bus) { f.Fail("RestartUnit", errors.New("boom")) },
		"not found":   func(f *sysbustest.Bus) { f.SetUnit("mosquitto.service", sysbus.UnitProperties{LoadState: "not-found"}) },
		"read failed": func(f *sysbustest.Bus) { f.Fail("UnitProperties", errors.New("boom")) },
	} {
		t.Run(name, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			breakIt(f)
			done := trackGuard(t)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			if status, _, body := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
				ctype: "application/json", body: `{"unit":"mosquitto"}`}); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			waitRestart(t, done)
			release, err := models.ServiceGuard()
			if err != nil {
				t.Fatalf("lock kept: %v", err)
			}
			release()
		})
	}
}

// An unavailable unit never reaches systemd, and the refusal is logged since
// the client has already had its answer.
func TestServiceRestartUnavailableUnit(t *testing.T) {
	for name, p := range map[string]sysbus.UnitProperties{
		"not found":     {LoadState: "not-found"},
		"masked":        {LoadState: "masked", ActiveState: "inactive", UnitFileState: "masked"},
		"masked in run": {LoadState: "loaded", ActiveState: "active", UnitFileState: "masked-runtime"},
	} {
		t.Run(name, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", p)
			logs := captureLogs(t)

			restartService(models.Service{ID: "mosquitto", Unit: "mosquitto.service", Kind: models.ServiceKindService}, func() {}, 0, f)

			if c := f.Calls(); len(c) != 0 {
				t.Fatalf("calls %q", c)
			}
			if !strings.Contains(logs.String(), "restarting mosquitto") {
				t.Fatalf("not logged: %s", logs.String())
			}
		})
	}
}

// A job that does not end done is reported as a failure, never as a success.
func TestServiceRestartJobResult(t *testing.T) {
	svc, _ := models.LookupService("mosquitto")
	for _, result := range []string{"done", "failed", "timeout", "canceled", "dependency", "skipped"} {
		t.Run(result, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", loadedUnit)
			f.SetJobResult("mosquitto.service", result)
			logs := captureLogs(t)

			restartService(svc, func() {}, 0, f)

			logged := strings.Contains(logs.String(), "job "+result)
			if logged != (result != sysbus.JobDone) {
				t.Fatalf("logged %v: %s", logged, logs.String())
			}
		})
	}
}

// The lock comes back as soon as systemd has the job, and jobs ending out of
// order each get their own result.
func TestServiceRestartConcurrentJobs(t *testing.T) {
	f := useFakeBus(t)
	f.HoldJobs()
	logs := captureLogs(t)
	results := map[string]string{"mosquitto": "failed", "grafana": "done", "influxdb": "timeout", "envoy": "canceled"}

	var wg sync.WaitGroup
	for id := range results {
		svc, ok := models.LookupService(id)
		if !ok {
			t.Fatal(id)
		}
		f.SetUnit(svc.Unit, loadedUnit)
		wg.Add(1)
		go func() {
			defer wg.Done()
			restartService(svc, func() {}, 0, f)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.Calls()) < len(results) {
		if time.Now().After(deadline) {
			t.Fatalf("calls %q", f.Calls())
		}
		time.Sleep(time.Millisecond)
	}

	release, err := models.ServiceGuard()
	if err != nil {
		t.Fatalf("lock held through the job: %v", err)
	}
	release()

	for id, result := range results {
		f.FinishJob(id+".service", result)
	}
	wg.Wait()
	for id, result := range results {
		line := id + ".service: job " + result
		if logged := strings.Contains(logs.String(), line); logged != (result != sysbus.JobDone) {
			t.Fatalf("%s logged %v:\n%s", line, logged, logs.String())
		}
	}
}

// The restart of the kiosk waits for its job: its result is the HTTP answer.
func TestSystemRestartJobResult(t *testing.T) {
	routes := []string{"/api/system/restart", "/api/v2/system/restart"}
	cases := []struct {
		name  string
		setup func(*sysbustest.Bus)
		want  int
		calls int
	}{
		{"done", func(*sysbustest.Bus) {}, 200, 1},
		{"failed", func(f *sysbustest.Bus) { f.SetJobResult("calaos-home.service", "failed") }, 500, 1},
		{"timeout", func(f *sysbustest.Bus) { f.SetJobResult("calaos-home.service", "timeout") }, 500, 1},
		{"not found", func(f *sysbustest.Bus) {
			f.SetUnit("calaos-home.service", sysbus.UnitProperties{LoadState: "not-found"})
		}, 500, 0},
		{"masked", func(f *sysbustest.Bus) { f.SetUnit("calaos-home.service", sysbus.UnitProperties{LoadState: "masked"}) }, 500, 0},
		{"not queued", func(f *sysbustest.Bus) { f.Fail("RestartUnit", errors.New(secretDetail)) }, 500, 1},
	}
	for _, route := range routes {
		for _, tc := range cases {
			t.Run(route+" "+tc.name, func(t *testing.T) {
				f := useFakeBus(t)
				f.SetUnit("calaos-home.service", loadedUnit)
				tc.setup(f)
				(&stubBackend{}).install(t)
				a := newHandlerTestApp(t)

				resp, body := doCase(t, a, httpCase{method: "POST", path: route})
				if resp.StatusCode != tc.want {
					t.Fatalf("%d %s", resp.StatusCode, body)
				}
				if c := f.Calls(); len(c) != tc.calls {
					t.Fatalf("calls %q", c)
				}
			})
		}
	}
}

func TestServiceRestartRefused(t *testing.T) {
	const msg = "unknown or non-restartable service"
	bodies := []string{
		`{"unit":"calaos-container"}`, `{"unit":"haproxy"}`, `{"unit":"networkd"}`, `{"unit":"ssh"}`,
		`{"unit":"calaos-server.service"}`, `{"unit":"systemd-networkd.service"}`,
		`{"unit":"getty@tty1"}`, `{"unit":"calaos-server@"}`, `{"unit":"calaos-server@x.service"}`,
		`{"unit":".."}`, `{"unit":"../calaos-server"}`, `{"unit":"calaos-server/../haproxy"}`,
		`{"unit":"CALAOS-SERVER"}`, `{"unit":" calaos-server"}`, `{"unit":"calaos-server\n"}`,
		`{"unit":"--force"}`, `{"unit":"chrony"}`, `{"unit":"chrony.service"}`, `{"unit":"timesyncd"}`, `{"unit":"sshd"}`, `{"unit":"calaos-ddns"}`, `{"unit":"*"}`, `{"unit":""}`, `{}`,
		`{"id":"calaos-server"}`,
	}
	f := useFakeBus(t)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	for _, b := range bodies {
		status, r, _ := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
			ctype: "application/json", body: b})
		if status != 400 || r.Code != codeInvalidRequest || r.Msg != msg {
			t.Fatalf("%s: %d %q %q", b, status, r.Code, r.Msg)
		}
	}
	for _, b := range []string{`not json`, `{"unit":5}`, `["calaos-server"]`} {
		status, r, _ := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
			ctype: "application/json", body: b})
		if status != 400 || r.Code != codeInvalidRequest {
			t.Fatalf("%s: %d %q", b, status, r.Code)
		}
	}
	time.Sleep(restartDelay + 200*time.Millisecond)
	if c := f.Calls(); len(c) != 0 || f.Reads("UnitProperties") != 0 {
		t.Fatalf("bus reached: %q", c)
	}
}

func TestServiceRestartDuringUpgrade(t *testing.T) {
	f := useFakeBus(t)
	(&stubBackend{}).install(t)
	prev := serviceGuard
	serviceGuard = func() (func(), error) { return nil, models.ErrUpgradeInProgress }
	t.Cleanup(func() { serviceGuard = prev })
	a := newHandlerTestApp(t)

	for _, s := range models.Services {
		if !s.Restartable {
			continue
		}
		status, r, _ := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
			ctype: "application/json", body: `{"unit":"` + s.ID + `"}`})
		if status != 409 || r.Code != codeUpgradeInProgress {
			t.Fatalf("%s: %d %q", s.ID, status, r.Code)
		}
	}
	time.Sleep(restartDelay + 200*time.Millisecond)
	if c := f.Calls(); len(c) != 0 || f.Reads("UnitProperties") != 0 {
		t.Fatalf("bus reached: %q", c)
	}
}

func TestServiceRestartAudit(t *testing.T) {
	f := useFakeBus(t)
	f.SetUnit("calaos-home.service", loadedUnit)
	done := trackGuard(t)
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	c.expect("POST", "/api/v2/system/services/restart", `{"unit":"calaos-home"}`, session(s), 200, "")
	waitRestart(t, done)
	c.expect("POST", "/api/v2/system/services/restart", `{"unit":"haproxy"}`, session(s), 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/system/services/restart", `{"unit":"../x y"}`, session(s), 400, codeInvalidRequest)

	log := readAudit(t)
	for _, want := range []string{
		`result=ok session=` + admin.SessionAuditID(s) + ` unit=calaos-home POST "/api/v2/system/services/restart"`,
		`result=invalid_request session=` + admin.SessionAuditID(s) + ` unit=haproxy POST "/api/v2/system/services/restart"`,
		`result=invalid_request session=` + admin.SessionAuditID(s) + ` POST "/api/v2/system/services/restart"`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("audit missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "x y") {
		t.Fatalf("client value in audit:\n%s", log)
	}
}
