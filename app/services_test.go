package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/models"
)

const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$FAKE_SYSTEMCTL_LOG"
if [ "$1" = show ]; then cat "$FAKE_SYSTEMCTL_SHOW"; fi
exit "${FAKE_SYSTEMCTL_RC:-0}"
`

type fakeSystemd struct {
	log, show string
}

// useFakeSystemctl puts a recording systemctl first on PATH, so the real
// executor runs.
func useFakeSystemctl(t *testing.T) *fakeSystemd {
	t.Helper()
	dir := t.TempDir()
	// #nosec G306 -- the fake must be executable.
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(fakeSystemctl), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystemd{log: filepath.Join(dir, "calls"), show: filepath.Join(dir, "show")}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SYSTEMCTL_LOG", f.log)
	t.Setenv("FAKE_SYSTEMCTL_SHOW", f.show)
	resetServicesCache()
	t.Cleanup(resetServicesCache)
	return f
}

func (f *fakeSystemd) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
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

func showBlock(lines ...string) string { return strings.Join(lines, "\n") + "\n\n" }

func TestServicesList(t *testing.T) {
	f := useFakeSystemctl(t)
	var out strings.Builder
	for i, s := range models.Services {
		for _, u := range models.UnitCandidates(s.Unit) {
			switch {
			case i == 0:
				out.WriteString(showBlock("Id="+u, "LoadState=loaded", "ActiveState=active", "SubState=running",
					"UnitFileState=enabled", "ActiveEnterTimestamp=@1793000472"))
			case i == 1:
				out.WriteString(showBlock("Id="+u, "LoadState=loaded", "ActiveState=failed", "SubState=failed",
					"UnitFileState=", "ActiveEnterTimestamp="))
			case s.ID == "envoy":
				out.WriteString(showBlock("Id="+u, "LoadState=loaded", "ActiveState=inactive", "SubState=dead",
					"UnitFileState=disabled", "ActiveEnterTimestamp=@0"))
			case u == "systemd-timesyncd.service":
				out.WriteString(showBlock("Id="+u, "LoadState=loaded", "ActiveState=active", "SubState=running",
					"UnitFileState=enabled", "ActiveEnterTimestamp=@1793000472"))
			default:
				out.WriteString(showBlock("Id="+u, "LoadState=not-found", "ActiveState=inactive", "SubState=dead",
					"UnitFileState=", "ActiveEnterTimestamp="))
			}
		}
	}
	if err := os.WriteFile(f.show, []byte(out.String()), 0o600); err != nil {
		t.Fatal(err)
	}
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

	// Cached: a second read within 1 s does not run systemctl again.
	v2Do2(t, a, httpCase{method: "GET", path: "/api/v2/system/services"})
	calls := f.calls(t)
	want := "show --no-pager --timestamp=unix -p Id,LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp " +
		"calaos-server.service calaos-home.service mosquitto.service zigbee2mqtt.service influxdb.service grafana.service " +
		"hue2mqtt.service envoy.service olad.service chrony.service systemd-timesyncd.service calaos-container.service " +
		"haproxy.service systemd-networkd.service ssh.service"
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls %q", calls)
	}
}

func TestServicesListFailure(t *testing.T) {
	for name, show := range map[string]string{
		"short output": showBlock("Id=calaos-server.service", "LoadState=loaded"),
		"bad line":     secretDetail,
	} {
		t.Run(name, func(t *testing.T) {
			f := useFakeSystemctl(t)
			if err := os.WriteFile(f.show, []byte(show), 0o600); err != nil {
				t.Fatal(err)
			}
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)
			status, r, body := v2Do2(t, a, httpCase{method: "GET", path: "/api/v2/system/services"})
			if status != 500 || r.Code != codeInternal || strings.Contains(body, "SECRET") {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
}

// TestServiceRestartAnswersFirst: the answer is out before systemctl runs.
func TestServiceRestartAnswersFirst(t *testing.T) {
	resolve := "show --no-pager -p LoadState --value chrony.service systemd-timesyncd.service"
	cases := []struct {
		id, show string
		want     []string
	}{
		{"calaos-server", "", []string{"restart --no-block calaos-server.service"}},
		{"ntp", "loaded\n\nnot-found\n", []string{resolve, "restart --no-block chrony.service"}},
		{"ntp", "not-found\n\nloaded\n", []string{resolve, "restart --no-block systemd-timesyncd.service"}},
	}
	for _, tc := range cases {
		id := tc.id
		t.Run(id, func(t *testing.T) {
			f := useFakeSystemctl(t)
			if err := os.WriteFile(f.show, []byte(tc.show), 0o600); err != nil {
				t.Fatal(err)
			}
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
			if c := f.calls(t); c != nil {
				t.Fatalf("systemctl ran before the answer: %q", c)
			}
			waitRestart(t, done)
			if time.Since(start) < restartDelay || answered >= restartDelay {
				t.Fatalf("answered after %v, restarted before %v", answered, restartDelay)
			}
			if c := f.calls(t); strings.Join(c, ";") != strings.Join(tc.want, ";") {
				t.Fatalf("calls %q, want %q", c, tc.want)
			}
		})
	}
}

// TestServiceRestartReleasesLock: a failed or successful restart gives the
// upgrade lock back.
func TestServiceRestartReleasesLock(t *testing.T) {
	for _, rc := range []string{"0", "1"} {
		f := useFakeSystemctl(t)
		t.Setenv("FAKE_SYSTEMCTL_RC", rc)
		done := trackGuard(t)
		(&stubBackend{}).install(t)
		a := newHandlerTestApp(t)

		if status, _, body := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
			ctype: "application/json", body: `{"unit":"mosquitto"}`}); status != 200 {
			t.Fatalf("rc %s: %d %s", rc, status, body)
		}
		waitRestart(t, done)
		release, err := models.ServiceGuard()
		if err != nil {
			t.Fatalf("rc %s: lock kept: %v", rc, err)
		}
		release()
		if len(f.calls(t)) != 1 {
			t.Fatalf("rc %s: calls %q", rc, f.calls(t))
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
	f := useFakeSystemctl(t)
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
	if c := f.calls(t); c != nil {
		t.Fatalf("systemctl ran: %q", c)
	}
}

func TestServiceRestartDuringUpgrade(t *testing.T) {
	f := useFakeSystemctl(t)
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
	if c := f.calls(t); c != nil {
		t.Fatalf("systemctl ran: %q", c)
	}
}

func TestServiceRestartAudit(t *testing.T) {
	useFakeSystemctl(t)
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
