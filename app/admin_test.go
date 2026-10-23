package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models/structs"
)

const (
	adminPw    = "correct horse battery"
	adminPwNew = "another fine password"
)

// useAdminDirs points the admin store at a per-test directory, with a cheap
// hash so -race stays fast.
func useAdminDirs(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	prevData, prevRun, prevParams := adminDataDir, adminRuntimeDir, adminHashParams
	adminDataDir = filepath.Join(root, "admin")
	adminRuntimeDir = filepath.Join(root, "run")
	adminHashParams = admin.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}
	t.Cleanup(func() {
		adminDataDir, adminRuntimeDir, adminHashParams = prevData, prevRun, prevParams
	})
}

type adminClient struct {
	t *testing.T
	a *AppServer
}

func (c adminClient) do(method, path, body string, headers map[string]string) (int, v2Reply, http.Header) {
	c.t.Helper()
	tc := httpCase{method: method, path: path, body: body, headers: headers}
	if body != "" {
		tc.ctype = "application/json"
	}
	resp, b := doCase(c.t, c.a, tc)
	var r v2Reply
	if err := json.Unmarshal(b, &r); err != nil || r.Error == nil {
		c.t.Fatalf("%s %s: not an envelope: %s", method, path, b)
	}
	return resp.StatusCode, r, resp.Header
}

func (c adminClient) expect(method, path, body string, headers map[string]string, status int, code string) v2Reply {
	c.t.Helper()
	got, r, _ := c.do(method, path, body, headers)
	if got != status || r.Code != code {
		c.t.Fatalf("%s %s %s: got %d %q %s, want %d %q", method, path, body, got, r.Code, r.Msg, status, code)
	}
	return r
}

func (c adminClient) verify(pw string) string {
	c.t.Helper()
	r := c.expect("POST", "/api/v2/admin/verify", `{"password":"`+pw+`"}`, nil, 200, "")
	var d struct {
		Session   string `json:"session"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil || len(d.Session) != 64 || d.ExpiresIn != 900 {
		c.t.Fatalf("verify data %s", r.Data)
	}
	return d.Session
}

func idleStatus() structs.Status { return structs.Status{Status: "idle"} }

func newRawRequest(t *testing.T, tc httpCase) *http.Request {
	t.Helper()
	var body io.Reader
	if tc.body != "" {
		body = strings.NewReader(tc.body)
	}
	req := httptest.NewRequest(tc.method, tc.path, body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if tc.ctype != "" {
		req.Header.Set("Content-Type", tc.ctype)
	}
	return req
}

func session(s string) map[string]string { return map[string]string{headerAdminSession: s} }

func newAdminApp(t *testing.T) adminClient {
	(&stubBackend{status: idleStatus()}).install(t)
	return adminClient{t: t, a: newHandlerTestApp(t)}
}

func TestAdminFlow(t *testing.T) {
	c := newAdminApp(t)

	if r := c.expect("GET", "/api/v2/admin/status", "", nil, 200, ""); string(r.Data) != `{"admin_set":false}` {
		t.Fatalf("status %s", r.Data)
	}
	c.expect("POST", "/api/v2/admin/verify", `{"password":"`+adminPw+`"}`, nil, 409, codeAdminNotSet)

	r := c.expect("POST", "/api/v2/admin/set", `{"password":"short"}`, nil, 400, codePasswordTooShort)
	if string(r.Data) != `{"min_length":10}` {
		t.Fatalf("too short data %s", r.Data)
	}
	if r = c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, ""); string(r.Data) != `{"mode":"bootstrap"}` {
		t.Fatalf("bootstrap %s", r.Data)
	}
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`"}`, nil, 409, codeAdminAlreadySet)
	if r = c.expect("GET", "/api/v2/admin/status", "", nil, 200, ""); string(r.Data) != `{"admin_set":true}` {
		t.Fatalf("status %s", r.Data)
	}

	c.expect("POST", "/api/v2/admin/verify", `{"password":"`+adminPwNew+`"}`, nil, 403, codeBadPassword)
	s := c.verify(adminPw)

	// The session opens every session-allowed route, admin/status included.
	c.expect("GET", "/api/v2/admin/status", "", session(s), 200, "")
	c.expect("GET", "/api/v2/update/status", "", session(s), 200, "")
	c.expect("POST", "/api/v2/admin/reset", "", session(s), 403, codeNotAllowed)
	c.expect("POST", "/api/v2/admin/alerts/ack", `{"ids":[]}`, session(s), 403, codeNotAllowed)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`","recovery":true}`, session(s), 403, codeNotAllowed)

	// Change: every session dies, the new password is the only one.
	other := c.verify(adminPw)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`"}`, session(s), 409, codeApproverRequired)
	c.enrol(other, "tok-a")
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`"}`, session(s), 403, codeCodeRequired)
	ref := c.code(s, "admin_password", `{"password":"`+adminPwNew+`"}`)
	if r = c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`",`+ref.body()+`}`, session(s), 200, ""); string(r.Data) != `{"mode":"change"}` {
		t.Fatalf("change %s", r.Data)
	}
	c.expect("GET", "/api/v2/admin/status", "", session(s), 401, codeBadSession)
	c.expect("GET", "/api/v2/admin/status", "", session(other), 401, codeBadSession)
	c.expect("POST", "/api/v2/admin/verify", `{"password":"`+adminPw+`"}`, nil, 403, codeBadPassword)
	s = c.verify(adminPwNew)

	// Recovery: bearer only, whatever the state.
	if r = c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`","recovery":true}`, nil, 200, ""); string(r.Data) != `{"mode":"recovery"}` {
		t.Fatalf("recovery %s", r.Data)
	}
	c.expect("POST", "/api/v2/system/reboot", "", session(s), 401, codeBadSession)
	s = c.verify(adminPw)

	if r = c.expect("POST", "/api/v2/admin/reset", "", nil, 200, ""); string(r.Data) != `{"was_set":true}` {
		t.Fatalf("reset %s", r.Data)
	}
	c.expect("GET", "/api/v2/admin/status", "", session(s), 401, codeBadSession)
	if r = c.expect("GET", "/api/v2/admin/status", "", nil, 200, ""); string(r.Data) != `{"admin_set":false}` {
		t.Fatalf("status after reset %s", r.Data)
	}
	if r = c.expect("POST", "/api/v2/admin/reset", "", nil, 200, ""); string(r.Data) != `{"was_set":false}` {
		t.Fatalf("second reset %s", r.Data)
	}
	if r = c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`","recovery":true}`, nil, 200, ""); string(r.Data) != `{"mode":"recovery"}` {
		t.Fatalf("recovery on an unset box %s", r.Data)
	}
}

func TestAdminSetCheckOrder(t *testing.T) {
	c := newAdminApp(t)
	long := strings.Repeat("x", admin.MaxPasswordBytes+1)

	// Body first, then session, then state, then length.
	c.expect("POST", "/api/v2/admin/set", `{"password":`, session("dead"), 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/set", `{}`, session("dead"), 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/set", `{"password":42}`, nil, 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`","recovery":"yes"}`, nil, 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+long+`"}`, session("dead"), 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/set", `{"password":"short"}`, session("dead"), 401, codeBadSession)
	c.expect("POST", "/api/v2/admin/set", `{"password":"short","recovery":true}`, session("dead"), 403, codeNotAllowed)
	c.expect("POST", "/api/v2/admin/set", `{"password":"short","recovery":false}`, session(""), 401, codeBadSession)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	c.expect("POST", "/api/v2/admin/set", `{"password":"short"}`, nil, 409, codeAdminAlreadySet)

	c.expect("POST", "/api/v2/admin/verify", `{"password":`, nil, 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/verify", `{"pass":"x"}`, nil, 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/verify", `{"password":"`+long+`"}`, nil, 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/verify", `{"password":"`+adminPw+`"}`, session("dead"), 401, codeBadSession)

	c.expect("POST", "/api/v2/admin/alerts/ack", `{}`, nil, 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/alerts/ack", `{"ids":"x"}`, nil, 400, codeInvalidRequest)
	c.expect("GET", "/api/v2/admin/verify", "", nil, 405, codeMethodNotAllowed)
	c.expect("GET", "/api/v2/admin/push/abc", "", nil, 404, codeNotFound)
}

// A present but empty header is a session, never the bearer-only path.
func TestAdminEmptySessionHeader(t *testing.T) {
	c := newAdminApp(t)
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"GET", "/api/v2/admin/status", "", 401, codeBadSession},
		{"POST", "/api/v2/admin/verify", `{"password":"` + adminPw + `"}`, 401, codeBadSession},
		{"POST", "/api/v2/admin/set", `{"password":"` + adminPw + `"}`, 401, codeBadSession},
		{"POST", "/api/v2/admin/reset", "", 403, codeNotAllowed},
		{"POST", "/api/v2/admin/alerts/ack", `{"ids":[]}`, 403, codeNotAllowed},
	} {
		r := httpCase{method: tc.method, path: tc.path, body: tc.body, ctype: "application/json"}
		req := newRawRequest(t, r)
		req.Header[headerAdminSession] = []string{""}
		resp, err := c.a.appFiber.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		var env v2Reply
		_ = json.NewDecoder(resp.Body).Decode(&env)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status || env.Code != tc.code {
			t.Fatalf("%s %s with an empty session: %d %q", tc.method, tc.path, resp.StatusCode, env.Code)
		}
	}
	if set, _ := c.a.admin.Status(); set {
		t.Fatal("an empty session header bootstrapped the admin")
	}
}

func TestAdminRateLimited(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	for i := 0; i < 5; i++ {
		c.expect("POST", "/api/v2/admin/verify", `{"password":"`+adminPwNew+`"}`, nil, 403, codeBadPassword)
	}
	status, r, h := c.do("POST", "/api/v2/admin/verify", `{"password":"`+adminPw+`"}`, nil)
	var d struct {
		RetryAfter int `json:"retry_after"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if status != 429 || r.Code != codeRateLimited || d.RetryAfter < 1 || d.RetryAfter > 60 || h.Get("Retry-After") != itoa(d.RetryAfter) {
		t.Fatalf("over budget: %d %q data %s Retry-After %q", status, r.Code, r.Data, h.Get("Retry-After"))
	}
	// Set is not budgeted.
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`","recovery":true}`, nil, 200, "")
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestAdminSessionsSurviveDaemonRestart(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)
	c.enrol(c.verify(adminPw), "tok-a")

	b, _ := os.ReadFile(filepath.Join(adminRuntimeDir, "sessions.json"))
	if strings.Contains(string(b), s) {
		t.Fatal("the session itself is stored")
	}

	restarted, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	c2 := adminClient{t: t, a: restarted}
	c2.expect("GET", "/api/v2/update/status", "", session(s), 200, "")
	// Approvers persist; the code is requested from the new daemon.
	ref := c2.code(s, "admin_password", `{"password":"`+adminPwNew+`"}`)
	c2.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`",`+ref.body()+`}`, session(s), 200, "")
	c.expect("GET", "/api/v2/admin/status", "", session(s), 401, codeBadSession)
}

func readAudit(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(adminDataDir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAdminAudit(t *testing.T) {
	c := newAdminApp(t)
	ip := func(v string) map[string]string { return map[string]string{headerClientIP: v} }

	c.expect("GET", "/api/v2/admin/status", "", ip("10.0.0.9"), 200, "")
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, ip("192.168.1.5"), 200, "")
	c.expect("POST", "/api/v2/admin/verify", `{"password":"`+adminPwNew+`"}`, ip("fe80:0::1"), 403, codeBadPassword)
	s := c.verify(adminPw)
	hdr := map[string]string{headerAdminSession: s, headerClientIP: "not an ip\nforged"}
	c.expect("GET", "/api/v2/update/status", "", hdr, 200, "")
	c.expect("GET", "/api/v2/admin/status", "", hdr, 200, "")
	c.expect("GET", "/api/v2/network/list", "", hdr, 403, codeNotAllowed)
	c.expect("GET", "/api/v2/update/status", "", nil, 200, "")
	resp, _ := doCase(t, c.a, httpCase{method: "POST", path: "/api/v2/admin/reset", auth: "Bearer nope", headers: ip("10.1.1.1")})
	if resp.StatusCode != 401 {
		t.Fatalf("bad bearer: %d", resp.StatusCode)
	}

	sid := admin.SessionAuditID(s)
	want := []string{
		`client=192.168.1.5 result=ok session=- POST "/api/v2/admin/set"`,
		`client=fe80::1 result=bad_password session=- POST "/api/v2/admin/verify"`,
		`client=local result=ok session=` + sid + ` POST "/api/v2/admin/verify"`,
		`client=invalid result=ok session=` + sid + ` GET "/api/v2/update/status"`,
		`client=invalid result=ok session=` + sid + ` GET "/api/v2/admin/status"`,
		`client=invalid result=not_allowed session=` + sid + ` GET "/api/v2/network/list"`,
	}
	lines := strings.Split(strings.TrimSuffix(readAudit(t), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("audit log:\n%s", strings.Join(lines, "\n"))
	}
	for i, l := range lines {
		ts, rest, _ := strings.Cut(l, " ")
		if _, err := time.Parse(time.RFC3339, ts); err != nil || rest != want[i] {
			t.Fatalf("line %d: %q, want %q", i, l, want[i])
		}
	}
	all := readAudit(t)
	for _, secret := range []string{s, adminPw, adminPwNew, testToken} {
		if strings.Contains(all, secret) {
			t.Fatalf("audit log leaks %q", secret)
		}
	}
}

func TestAdminAuditLegacyUntouched(t *testing.T) {
	c := newAdminApp(t)
	resp, _ := doCase(t, c.a, httpCase{method: "GET", path: "/api/update/status", headers: session("dead")})
	if resp.StatusCode != 200 {
		t.Fatalf("legacy route with a session header: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(adminDataDir, "audit.log")); !os.IsNotExist(err) {
		t.Fatalf("legacy request audited: %v", err)
	}
}

func dialEvents(t *testing.T, addr, path string) *gws.Conn {
	t.Helper()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+testToken)
	conn, _, err := gws.DefaultDialer.Dial("ws://"+addr+path, h)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return conn
}

type alertFrame struct {
	Type string
	Data admin.AlertData
}

func readAlert(t *testing.T, conn *gws.Conn) alertFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var raw struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := conn.ReadJSON(&raw); err != nil {
		t.Fatalf("read: %v", err)
	}
	var f alertFrame
	f.Type = raw.Type
	if strings.HasPrefix(raw.Type, "admin_") {
		if string(raw.Data) != `{"id":"`+jsonField(raw.Data, "id")+`","ts":"`+jsonField(raw.Data, "ts")+`"}` {
			t.Fatalf("alert data carries more than id and ts: %s", raw.Data)
		}
		_ = json.Unmarshal(raw.Data, &f.Data)
	}
	return f
}

func jsonField(b []byte, k string) string {
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	return m[k]
}

func TestAdminAlertsReplayAndAck(t *testing.T) {
	(&stubBackend{status: idleStatus()}).install(t)
	a, addr, cleanup := newTestServer(t)
	defer cleanup()
	c := adminClient{t: t, a: a}

	// Raised while nobody listens.
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")

	for _, path := range []string{"/api/v2/events", "/api/events"} {
		conn := dialEvents(t, addr, path)
		if f := readAlert(t, conn); f.Type != events.EventUpgradeProgress {
			t.Fatalf("%s: first frame %q", path, f.Type)
		}
		if f := readAlert(t, conn); f.Type != admin.EventAdminSet || len(f.Data.ID) != 16 {
			t.Fatalf("%s: replay %+v", path, f)
		}
		_ = conn.Close()
	}

	conn := dialEvents(t, addr, "/api/v2/events")
	defer func() { _ = conn.Close() }()
	readAlert(t, conn)
	setAlert := readAlert(t, conn)

	// Live, and persisted for the next subscriber.
	c.expect("POST", "/api/v2/admin/reset", "", nil, 200, "")
	resetAlert := readAlert(t, conn)
	if resetAlert.Type != admin.EventAdminReset || resetAlert.Data.ID == setAlert.Data.ID {
		t.Fatalf("live %+v", resetAlert)
	}

	r := c.expect("POST", "/api/v2/admin/alerts/ack", `{"ids":["`+setAlert.Data.ID+`","nope"]}`, nil, 200, "")
	if string(r.Data) != `{"acked":1}` {
		t.Fatalf("ack %s", r.Data)
	}

	next := dialEvents(t, addr, "/api/v2/events")
	defer func() { _ = next.Close() }()
	readAlert(t, next)
	if f := readAlert(t, next); f.Type != admin.EventAdminReset || f.Data.ID != resetAlert.Data.ID {
		t.Fatalf("after ack %+v", f)
	}
	c.expect("POST", "/api/v2/admin/alerts/ack", `{"ids":["`+resetAlert.Data.ID+`"]}`, nil, 200, "")

	last := dialEvents(t, addr, "/api/v2/events")
	defer func() { _ = last.Close() }()
	readAlert(t, last)
	a.hub.Publish("marker", nil)
	if f := readAlert(t, last); f.Type != "marker" {
		t.Fatalf("acked alert replayed: %+v", f)
	}
}

// Concurrent requests through the router, for -race.
func TestAdminConcurrentRequests(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var tc httpCase
			switch i % 4 {
			case 0:
				tc = httpCase{method: "POST", path: "/api/v2/admin/verify", ctype: "application/json", body: `{"password":"` + adminPw + `"}`}
			case 1:
				tc = httpCase{method: "GET", path: "/api/v2/admin/status", headers: session(s)}
			case 2:
				tc = httpCase{method: "POST", path: "/api/v2/admin/alerts/ack", ctype: "application/json", body: `{"ids":["x"]}`}
			default:
				tc = httpCase{method: "GET", path: "/api/v2/admin/status"}
			}
			resp, body := doCase(t, c.a, tc)
			if resp.StatusCode != 200 && resp.StatusCode != 429 {
				t.Errorf("%s %s: %d %s", tc.method, tc.path, resp.StatusCode, body)
			}
		}(i)
	}
	wg.Wait()
}

// Anything on the host can call the daemon without the bearer: those calls
// must not rotate the real entries out of audit.log.
func TestAdminAuditFlood(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")

	long := "/api/v2/admin/" + strings.Repeat("a", 3000)
	for i := 0; i < 10000; i++ {
		tc := httpCase{method: "GET", path: long, auth: "-", headers: session("x")}
		if i%2 == 1 {
			tc = httpCase{method: "POST", path: "/api/v2/admin/verify", auth: "Bearer nope", body: `{"password":"x"}`}
		}
		if resp, _ := doCase(t, c.a, tc); resp.StatusCode != 401 {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}

	log := readAudit(t)
	if !strings.Contains(log, `result=ok session=- POST "/api/v2/admin/set"`) || strings.Contains(log, codeBadBearer) {
		t.Fatalf("audit log after the flood:\n%.500s", log)
	}
	if _, err := os.Stat(filepath.Join(adminDataDir, "audit.log.1")); !os.IsNotExist(err) {
		t.Fatalf("audit log rotated by unauthenticated calls: %v", err)
	}
}

// calaos_server relays a household client's well-formed session guess on
// update_status: those refusals must not rotate audit.log either.
func TestAdminAuditBadSessionFlood(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")

	forged := session(strings.Repeat("ab", 32))
	for i := 0; i < 10000; i++ {
		tc := httpCase{method: "GET", path: "/api/v2/update/status", headers: forged}
		if resp, body := doCase(t, c.a, tc); resp.StatusCode != 401 || !strings.Contains(string(body), codeBadSession) {
			t.Fatalf("request %d: %d %s", i, resp.StatusCode, body)
		}
	}

	log := readAudit(t)
	if !strings.Contains(log, `result=ok session=- POST "/api/v2/admin/set"`) || strings.Contains(log, codeBadSession) {
		t.Fatalf("audit log after the flood:\n%.500s", log)
	}
	if _, err := os.Stat(filepath.Join(adminDataDir, "audit.log.1")); !os.IsNotExist(err) {
		t.Fatalf("audit log rotated by forged sessions: %v", err)
	}
}

// A live alert lost by the hub reaches the connected peer at the next ping.
func TestAdminAlertRedelivered(t *testing.T) {
	prevPing, prevPublish := wsPingPeriod, publishAdmin
	wsPingPeriod = 100 * time.Millisecond
	publishAdmin = func(*events.Hub, string, any) {}
	defer func() { wsPingPeriod, publishAdmin = prevPing, prevPublish }()

	(&stubBackend{status: idleStatus()}).install(t)
	a, addr, cleanup := newTestServer(t)
	defer cleanup()
	c := adminClient{t: t, a: a}

	conn := dialEvents(t, addr, "/api/v2/events")
	defer func() { _ = conn.Close() }()
	readAlert(t, conn)

	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	f := readAlert(t, conn)
	if f.Type != admin.EventAdminSet {
		t.Fatalf("got %+v", f)
	}

	// Sent once per connection, not at every ping.
	_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	var extra map[string]any
	if err := conn.ReadJSON(&extra); err == nil {
		t.Fatalf("alert sent again: %v", extra)
	}
}
