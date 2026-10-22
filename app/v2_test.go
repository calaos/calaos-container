package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gws "github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/models/structs"
)

const secretDetail = "SECRET-DETAIL /etc/shadow"

type v2Reply struct {
	Error *bool           `json:"error"`
	Code  string          `json:"code"`
	Msg   string          `json:"msg"`
	Data  json.RawMessage `json:"data"`
}

func v2Do(t *testing.T, tc httpCase) (int, v2Reply, string) {
	t.Helper()
	s := tc.backend
	s.install(t)
	a := newHandlerTestApp(t)
	resp, body := doCase(t, a, tc)

	var r v2Reply
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("%s %s: body is not JSON: %q", tc.method, tc.path, body)
	}
	if r.Error == nil {
		t.Fatalf("%s %s: envelope without error field: %s", tc.method, tc.path, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("%s %s: content type %q", tc.method, tc.path, ct)
	}
	return resp.StatusCode, r, string(body)
}

func TestV2Success(t *testing.T) {
	dir := t.TempDir()
	codeFile := filepath.Join(dir, "code")
	logFile := filepath.Join(dir, "log")
	if err := os.WriteFile(codeFile, []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logFile, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	images := structs.ImageMap{"calaos-server": {Name: "calaos-server", Version: "4.5.1", CurrentVersion: "4.5.0"}}

	cases := []struct {
		tc   httpCase
		want string
	}{
		{httpCase{method: "GET", path: "/api/v2/update/check", backend: stubBackend{newVersions: images}},
			`{"error":false,"msg":"ok","data":{"calaos-server":{"name":"calaos-server","version":"4.5.1","current_version":"4.5.0"}}}`},
		{httpCase{method: "GET", path: "/api/v2/update/available"},
			`{"error":false,"msg":"ok","data":{}}`},
		{httpCase{method: "GET", path: "/api/v2/update/installed", backend: stubBackend{installed: structs.ImageMap{}}},
			`{"error":false,"msg":"ok","data":{}}`},
		{httpCase{method: "GET", path: "/api/v2/update/status", backend: stubBackend{status: structs.Status{Status: "idle"}}},
			`{"error":false,"msg":"ok","data":{"progress":{"status":"idle","total_percent":0},"job":null}}`},
		{httpCase{method: "POST", path: "/api/v2/system/reboot", backend: stubBackend{commands: map[string]stubResult{"systemctl reboot": {}}}},
			`{"error":false,"msg":"ok","data":null}`},
		{httpCase{method: "POST", path: "/api/v2/system/restart", backend: stubBackend{commands: map[string]stubResult{"systemctl restart calaos-home": {}}}},
			`{"error":false,"msg":"ok","data":null}`},
		{httpCase{method: "GET", path: "/api/v2/system/info", backend: stubBackend{sysInfo: &models.SystemInfo{Hostname: "calaos", Uptime: 5}}},
			`{"error":false,"msg":"ok","data":{"hostname":"calaos","uptime":5,"cpu_usage":0,"mem_usage":0}}`},
		{httpCase{method: "GET", path: "/api/v2/system/fs_status", backend: stubBackend{commands: map[string]stubResult{"findmnt --json /": {out: `{"filesystems":[]}`}}}},
			`{"error":false,"msg":"ok","data":{"filesystems":[]}}`},
		{httpCase{method: "POST", path: "/api/v2/system/rollback_snapshot", backend: stubBackend{commands: map[string]stubResult{"calaos_rollback": {}, "systemctl reboot": {}}}},
			`{"error":false,"msg":"ok","data":null}`},
		{httpCase{method: "GET", path: "/api/v2/system/install/list_devices", backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {out: `{"blockdevices":[]}`}}}},
			`{"error":false,"msg":"ok","data":{"blockdevices":[]}}`},
		{httpCase{method: "GET", path: "/api/v2/system/install/status"},
			`{"error":false,"msg":"ok","data":{"status":"never_run"}}`},
		{httpCase{method: "GET", path: "/api/v2/system/install/status", backend: stubBackend{installLogFile: logFile}},
			`{"error":false,"msg":"ok","data":{"status":"running"}}`},
		{httpCase{method: "GET", path: "/api/v2/system/install/status", backend: stubBackend{installCodeFile: codeFile, installLogFile: logFile}},
			`{"error":false,"msg":"ok","data":{"exit_code":3,"log":"done\n","status":"finished"}}`},
		{httpCase{method: "GET", path: "/api/v2/network/list", backend: stubBackend{nets: []*structs.NetInterface{{Name: "eth0", IPv4: "10.0.0.2/24"}}}},
			`{"error":false,"msg":"ok","data":[{"name":"eth0","ipv4":"10.0.0.2/24","dhcp":false}]}`},
		{httpCase{method: "POST", path: "/api/v2/network/eth0", ctype: "application/json", body: `{"dhcp":true}`, backend: stubBackend{intfExists: true}},
			`{"error":false,"msg":"ok","data":null}`},
	}

	for _, c := range cases {
		t.Run(c.tc.method+" "+c.tc.path, func(t *testing.T) {
			status, _, body := v2Do(t, c.tc)
			if status != http.StatusOK || body != c.want {
				t.Fatalf("got %d %s\nwant 200 %s", status, body, c.want)
			}
		})
	}
}

func TestV2Errors(t *testing.T) {
	leak := errors.New(secretDetail)
	upgrading := structs.Status{Status: "upgrading"}

	cases := []struct {
		name   string
		tc     httpCase
		status int
		code   string
		msg    string // checked when set
	}{
		{"check in progress", httpCase{method: "GET", path: "/api/v2/update/check", backend: stubBackend{checkErr: models.ErrUpgradeInProgress}},
			409, codeUpgradeInProgress, "an upgrade is already in progress"},
		{"check apt failure", httpCase{method: "GET", path: "/api/v2/update/check", backend: stubBackend{checkErr: leak}},
			500, codeInternal, "internal error"},
		{"installed failure", httpCase{method: "GET", path: "/api/v2/update/installed", backend: stubBackend{installedErr: leak}},
			500, codeInternal, ""},
		{"reboot in progress", httpCase{method: "POST", path: "/api/v2/system/reboot", backend: stubBackend{status: upgrading}},
			409, codeUpgradeInProgress, ""},
		{"rollback in progress", httpCase{method: "POST", path: "/api/v2/system/rollback_snapshot", backend: stubBackend{status: upgrading}},
			409, codeUpgradeInProgress, ""},
		{"reboot failure", httpCase{method: "POST", path: "/api/v2/system/reboot", backend: stubBackend{commands: map[string]stubResult{"systemctl reboot": {err: leak}}}},
			500, codeInternal, ""},
		{"restart failure", httpCase{method: "POST", path: "/api/v2/system/restart", backend: stubBackend{commands: map[string]stubResult{"systemctl restart calaos-home": {err: leak}}}},
			500, codeInternal, ""},
		{"info failure", httpCase{method: "GET", path: "/api/v2/system/info", backend: stubBackend{sysInfoErr: leak}},
			500, codeInternal, ""},
		{"fs_status bad json", httpCase{method: "GET", path: "/api/v2/system/fs_status", backend: stubBackend{commands: map[string]stubResult{"findmnt --json /": {out: secretDetail}}}},
			500, codeInternal, ""},
		{"rollback failure", httpCase{method: "POST", path: "/api/v2/system/rollback_snapshot", backend: stubBackend{commands: map[string]stubResult{"calaos_rollback": {err: leak}}}},
			500, codeInternal, ""},
		{"list_devices failure", httpCase{method: "GET", path: "/api/v2/system/install/list_devices", backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {err: leak}}}},
			500, codeInternal, ""},
		{"install bad body", httpCase{method: "POST", path: "/api/v2/system/install/start", ctype: "application/json", body: `{"device":`},
			400, codeInvalidRequest, "invalid JSON body"},
		{"install inventory failure", httpCase{method: "POST", path: "/api/v2/system/install/start", ctype: "application/json", body: `{"device":"/dev/sdb","confirm":true}`,
			backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {err: leak}}}},
			500, codeInternal, ""},
		{"install root disk", httpCase{method: "POST", path: "/api/v2/system/install/start", ctype: "application/json", body: `{"device":"/dev/sda","confirm":true}`,
			backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {out: goldenLsblkInventory}}}},
			400, codeInvalidRequest, `device "/dev/sda" hosts the root filesystem and cannot be used as an install target`},
		{"network list failure", httpCase{method: "GET", path: "/api/v2/network/list", backend: stubBackend{netsErr: leak}},
			500, codeInternal, ""},
		{"network bad name", httpCase{method: "POST", path: "/api/v2/network/-all", ctype: "application/json", body: `{"dhcp":true}`},
			400, codeInvalidRequest, "invalid interface: must not start with '-'"},
		{"network bad body", httpCase{method: "POST", path: "/api/v2/network/eth0", ctype: "application/json", body: `{"dhcp":`},
			400, codeInvalidRequest, "invalid JSON body"},
		{"network invalid config", httpCase{method: "POST", path: "/api/v2/network/eth0", ctype: "application/json", body: `{"ipv4":"999.1.1.1/24"}`},
			400, codeInvalidRequest, `invalid ipv4: "999.1.1.1/24" is not a valid IPv4 address in CIDR form`},
		{"network unknown interface", httpCase{method: "POST", path: "/api/v2/network/eth9", ctype: "application/json", body: `{"dhcp":true}`},
			400, codeInvalidRequest, `invalid interface: "eth9" does not exist`},
		{"network exists failure", httpCase{method: "POST", path: "/api/v2/network/eth0", ctype: "application/json", body: `{"dhcp":true}`, backend: stubBackend{intfExistsErr: leak}},
			500, codeInternal, ""},
		{"network configure failure", httpCase{method: "POST", path: "/api/v2/network/eth0", ctype: "application/json", body: `{"dhcp":true}`, backend: stubBackend{intfExists: true, configureErr: leak}},
			500, codeInternal, ""},

		{"unknown route", httpCase{method: "GET", path: "/api/v2/nope"}, 404, codeNotFound, "not found"},
		{"unknown route, other case", httpCase{method: "GET", path: "/API/V2/nope"}, 404, codeNotFound, ""},
		{"upgrade route not served yet", httpCase{method: "POST", path: "/api/v2/update/upgrade-all"}, 404, codeNotFound, ""},
		{"wrong method", httpCase{method: "GET", path: "/api/v2/system/reboot"}, 405, codeMethodNotAllowed, ""},
		{"plain GET on events", httpCase{method: "GET", path: "/api/v2/events"}, 426, codeUpgradeRequired, ""},

		{"no bearer", httpCase{method: "GET", path: "/api/v2/update/available", auth: "-"}, 401, codeBadBearer, "unauthorized"},
		{"wrong bearer", httpCase{method: "GET", path: "/api/v2/update/available", auth: "Bearer nope"}, 401, codeBadBearer, ""},
		{"query token refused", httpCase{method: "GET", path: "/api/v2/update/available?token=" + testToken, auth: "-"}, 401, codeBadBearer, ""},

		{"unknown session", httpCase{method: "GET", path: "/api/v2/update/status", headers: map[string]string{headerAdminSession: "00ff"}},
			401, codeBadSession, ""},
		{"session on bearer-only route", httpCase{method: "GET", path: "/api/v2/network/list", headers: map[string]string{headerAdminSession: "00ff"}},
			403, codeNotAllowed, ""},
		{"session on rollback", httpCase{method: "POST", path: "/api/v2/system/rollback_snapshot", headers: map[string]string{headerAdminSession: "00ff"}},
			403, codeNotAllowed, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, r, body := v2Do(t, c.tc)
			if status != c.status || r.Code != c.code || !*r.Error {
				t.Fatalf("got %d %s, want %d %s", status, body, c.status, c.code)
			}
			if c.msg != "" && r.Msg != c.msg {
				t.Fatalf("msg %q, want %q", r.Msg, c.msg)
			}
			if strings.Contains(body, "SECRET") {
				t.Fatalf("internal error text leaked: %s", body)
			}
		})
	}
}

func TestV2NoTokenFile(t *testing.T) {
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	resetTokenCache(t, filepath.Join(t.TempDir(), "missing.token"))

	resp, body := doCase(t, a, httpCase{method: "GET", path: "/api/v2/update/available"})
	want := `{"error":true,"code":"bad_bearer","msg":"unauthorized"}`
	if resp.StatusCode != http.StatusUnauthorized || string(body) != want {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
}

type acceptSessions struct{ ok string }

func (s acceptSessions) Valid(v string) bool { return v == s.ok }

func TestV2Sessions(t *testing.T) {
	(&stubBackend{commands: map[string]stubResult{"systemctl reboot": {}}}).install(t)
	a := newHandlerTestApp(t)
	a.sessions = acceptSessions{ok: "live"}

	check := func(method, path, session string, wantStatus int, wantCode string) {
		t.Helper()
		resp, body := doCase(t, a, httpCase{method: method, path: path, headers: map[string]string{headerAdminSession: session}})
		var r v2Reply
		_ = json.Unmarshal(body, &r)
		if resp.StatusCode != wantStatus || r.Code != wantCode {
			t.Fatalf("%s %s session %q: got %d %s", method, path, session, resp.StatusCode, body)
		}
	}

	check("POST", "/api/v2/system/reboot", "live", 200, "")
	check("POST", "/api/v2/system/reboot", "dead", 401, codeBadSession)
	check("GET", "/api/v2/network/list", "live", 403, codeNotAllowed)

	// Present but empty must not fall back to the bearer-only path.
	r := httptest.NewRequest("GET", "/api/v2/update/available", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header[headerAdminSession] = []string{""}
	resp, err := a.appFiber.Test(r, -1)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty session header: got %d, want 401", resp.StatusCode)
	}
}

func TestV2EventsWebSocket(t *testing.T) {
	a, addr, cleanup := newTestServer(t)
	defer cleanup()

	h := http.Header{}
	h.Set("Authorization", "Bearer "+testToken)
	conn, _, err := gws.DefaultDialer.Dial("ws://"+addr+"/api/v2/events", h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if ev := readEvent(t, conn); ev.Type != events.EventUpgradeProgress {
		t.Fatalf("first frame %q, want the upgrade_progress snapshot", ev.Type)
	}

	a.hub.Publish("upgrade_job", map[string]any{"id": "x"})
	if ev := readEvent(t, conn); ev.Type != "upgrade_job" {
		t.Fatalf("got %q", ev.Type)
	}

	_, resp, err := gws.DefaultDialer.Dial("ws://"+addr+"/api/v2/events?token="+testToken, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token on /api/v2/events: err %v, resp %v", err, resp)
	}
}
