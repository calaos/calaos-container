package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/events"
	logger "github.com/calaos/calaos-container/log"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// useAdminClock must run before NewApp.
func useAdminClock(t *testing.T) *testClock {
	clk := &testClock{t: time.Now()}
	prev := adminNow
	adminNow = clk.Now
	t.Cleanup(func() { adminNow = prev })
	return clk
}

type codeRef struct{ id, code string }

func (r codeRef) body() string { return `"code_id":"` + r.id + `","code":"` + r.code + `"` }

var sixDigits = regexp.MustCompile(`\b(\d{6})\b`)

// push fetches the payload as calaos_server does and returns the code.
func (c adminClient) push(codeID, wantToken string) string {
	c.t.Helper()
	r := c.expect("GET", "/api/v2/admin/push/"+codeID, "", nil, 200, "")
	var p struct{ Token, Message string }
	if err := json.Unmarshal(r.Data, &p); err != nil || (wantToken != "" && p.Token != wantToken) {
		c.t.Fatalf("push payload %s, want token %q", r.Data, wantToken)
	}
	m := sixDigits.FindStringSubmatch(p.Message)
	if m == nil {
		c.t.Fatalf("no code in %q", p.Message)
	}
	return m[1]
}

func (c adminClient) code(s, action, params string) codeRef {
	c.t.Helper()
	r := c.expect("POST", "/api/v2/admin/codes", `{"action":"`+action+`","params":`+params+`}`, session(s), 200, "")
	var d admin.CodeInfo
	_ = json.Unmarshal(r.Data, &d)
	return codeRef{id: d.CodeID, code: c.push(d.CodeID, "")}
}

func (c adminClient) enrol(s, token string) admin.ApproverInfo {
	c.t.Helper()
	r := c.expect("POST", "/api/v2/admin/approvers/enroll", `{"token":"`+token+`","device_name":"Phone `+token+`"}`, session(s), 200, "")
	var d admin.CodeInfo
	_ = json.Unmarshal(r.Data, &d)
	code := c.push(d.CodeID, token)
	r = c.expect("POST", "/api/v2/admin/approvers/confirm", `{"code_id":"`+d.CodeID+`","code":"`+code+`"}`, session(s), 200, "")
	var a admin.ApproverInfo
	if err := json.Unmarshal(r.Data, &a); err != nil || a.ID != d.ApproverID {
		c.t.Fatalf("confirm %s", r.Data)
	}
	return a
}

func adminWithApprover(t *testing.T) (adminClient, *testClock, string, admin.ApproverInfo) {
	clk := useAdminClock(t)
	(&stubBackend{status: idleStatus(), commands: map[string]stubResult{"systemctl reboot": {}}}).install(t)
	c := adminClient{t: t, a: newHandlerTestApp(t)}
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)
	a := c.enrol(s, "tok-a")
	clk.Advance(10 * time.Second)
	return c, clk, s, a
}

func TestApproverRoutesNeedASession(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v2/admin/approvers", ""},
		{"POST", "/api/v2/admin/approvers/enroll", `{"token":"t","device_name":"d"}`},
		{"POST", "/api/v2/admin/approvers/confirm", `{"code_id":"0123456789abcdef","code":"123456"}`},
		{"POST", "/api/v2/admin/approvers/remove", `{"approver_id":"0123456789abcdef"}`},
		{"POST", "/api/v2/admin/codes", `{"action":"console_open"}`},
		{"POST", "/api/v2/admin/codes/verify", `{"code_id":"0123456789abcdef","code":"123456"}`},
	} {
		c.expect(r.method, r.path, r.body, nil, 401, codeBadSession)
		c.expect(r.method, r.path, r.body, session(strings.Repeat("a", 64)), 401, codeBadSession)
	}
	// The push payload is calaos_server's: bearer only.
	s := c.verify(adminPw)
	c.expect("GET", "/api/v2/admin/push/0123456789abcdef", "", session(s), 403, codeNotAllowed)
	c.expect("GET", "/api/v2/admin/push/0123456789abcdef", "", nil, 404, codeNotFound)
	c.expect("GET", "/api/v2/admin/push/nothex", "", nil, 404, codeNotFound)
	resp, _ := doCase(t, c.a, httpCase{method: "GET", path: "/api/v2/admin/push/0123456789abcdef", auth: "-"})
	if resp.StatusCode != 401 {
		t.Fatalf("push payload without the bearer: %d", resp.StatusCode)
	}
}

func TestApproverEnrolmentOverHTTP(t *testing.T) {
	(&stubBackend{status: idleStatus()}).install(t)
	clk := useAdminClock(t)
	a, addr, cleanup := newTestServer(t)
	defer cleanup()
	c := adminClient{t: t, a: a}
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	v2 := dialEvents(t, addr, "/api/v2/events")
	defer func() { _ = v2.Close() }()
	legacy := dialEvents(t, addr, "/api/events")
	defer func() { _ = legacy.Close() }()
	readAlert(t, v2) // snapshot
	readAlert(t, v2) // admin_set
	readAlert(t, legacy)
	readAlert(t, legacy)

	if r := c.expect("GET", "/api/v2/admin/approvers", "", session(s), 200, ""); string(r.Data) != `[]` {
		t.Fatalf("empty list %s", r.Data)
	}
	c.expect("POST", "/api/v2/admin/codes", `{"action":"console_open"}`, session(s), 409, codeApproverRequired)

	r := c.expect("POST", "/api/v2/admin/approvers/enroll", `{"token":"fcm:APA91b-x","device_name":"Pixel 8"}`, session(s), 200, "")
	var info admin.CodeInfo
	if err := json.Unmarshal(r.Data, &info); err != nil || info.ExpiresIn != 60 {
		t.Fatalf("enroll %s", r.Data)
	}

	// Both buses get the event, with the ids only.
	want := `{"type":"push_code","data":{"approver_id":"` + info.ApproverID + `","code_id":"` + info.CodeID + `"}}`
	for name, conn := range map[string]interface {
		SetReadDeadline(time.Time) error
		ReadMessage() (int, []byte, error)
	}{"v2": v2, "legacy": legacy} {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, frame, err := conn.ReadMessage()
		if err != nil || strings.TrimSpace(string(frame)) != want {
			t.Fatalf("%s frame %s %v, want %s", name, frame, err, want)
		}
	}

	code := c.push(info.CodeID, "fcm:APA91b-x")
	c.expect("GET", "/api/v2/admin/push/"+info.CodeID, "", nil, 404, codeNotFound)

	r = c.expect("POST", "/api/v2/admin/approvers/confirm", `{"code_id":"`+info.CodeID+`","code":"`+wrongCode(code)+`"}`, session(s), 403, codeBadCode)
	if string(r.Data) != `{"attempts_left":4}` {
		t.Fatalf("bad code data %s", r.Data)
	}
	r = c.expect("POST", "/api/v2/admin/approvers/confirm", `{"code_id":"`+info.CodeID+`","code":"`+code+`"}`, session(s), 200, "")
	var ap admin.ApproverInfo
	if err := json.Unmarshal(r.Data, &ap); err != nil || ap.ID != info.ApproverID || ap.DeviceName != "Pixel 8" {
		t.Fatalf("confirm %s", r.Data)
	}
	c.expect("POST", "/api/v2/admin/approvers/confirm", `{"code_id":"`+info.CodeID+`","code":"`+code+`"}`, session(s), 410, codeCodeExpired)

	// Every subscriber is told, by device name and token_id only.
	for name, conn := range map[string]interface {
		SetReadDeadline(time.Time) error
		ReadMessage() (int, []byte, error)
	}{"v2": v2, "legacy": legacy} {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, frame, err := conn.ReadMessage()
		var f struct {
			Type string
			Data map[string]string
		}
		if err != nil || json.Unmarshal(frame, &f) != nil || f.Type != admin.EventApproverSet ||
			f.Data["device_name"] != "Pixel 8" || f.Data["token_id"] != ap.TokenID || len(f.Data) != 4 || strings.Contains(string(frame), "APA91b") {
			t.Fatalf("%s approver_set frame %s %v", name, frame, err)
		}
	}

	r = c.expect("GET", "/api/v2/admin/approvers", "", session(s), 200, "")
	if strings.Contains(string(r.Data), "APA91b") || !strings.Contains(string(r.Data), `"token_id":"`+ap.TokenID+`"`) {
		t.Fatalf("list %s", r.Data)
	}
	b, err := os.ReadFile(filepath.Join(adminDataDir, "approvers.json"))
	if err != nil || !strings.Contains(string(b), "fcm:APA91b-x") {
		t.Fatalf("approvers.json %s %v", b, err)
	}

	// A second phone needs a code from the first one.
	clk.Advance(10 * time.Second)
	c.expect("POST", "/api/v2/admin/approvers/enroll", `{"token":"tok-b","device_name":"Spare"}`, session(s), 403, codeCodeRequired)
	c.expect("POST", "/api/v2/admin/approvers/enroll", `{"token":"fcm:APA91b-x","device_name":"Again"}`, session(s), 409, codeApproverExists)
}

func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestCodeProtectedActions(t *testing.T) {
	c, clk, s, first := adminWithApprover(t)

	// admin_password is bound to the new password.
	ref := c.code(s, "admin_password", `{"password":"`+adminPwNew+`"}`)
	c.expect("POST", "/api/v2/admin/set", `{"password":"a third password",`+ref.body()+`}`, session(s), 403, codeBadCode)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`",`+ref.body()+`}`, session(s), 200, "")
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`",`+ref.body()+`}`, session(s), 401, codeBadSession)
	s = c.verify(adminPwNew)

	// A second approver, then the removal of the first, each with a code.
	enrolRef := c.code(s, "approver_enroll", `{}`)
	clk.Advance(10 * time.Second)
	r := c.expect("POST", "/api/v2/admin/approvers/enroll", `{"token":"tok-b","device_name":"Spare",`+enrolRef.body()+`}`, session(s), 200, "")
	var info admin.CodeInfo
	_ = json.Unmarshal(r.Data, &info)
	code := c.push(info.CodeID, "tok-b")
	c.expect("POST", "/api/v2/admin/approvers/confirm", `{"code_id":"`+info.CodeID+`","code":"`+code+`"}`, session(s), 200, "")
	clk.Advance(10 * time.Second)
	c.expect("POST", "/api/v2/admin/approvers/enroll", `{"token":"tok-c","device_name":"Third"}`, session(s), 409, codeApproverLimit)

	c.expect("POST", "/api/v2/admin/approvers/remove", `{"approver_id":"`+first.ID+`"}`, session(s), 403, codeCodeRequired)
	rm := c.code(s, "approver_remove", `{"approver_id":"`+first.ID+`"}`)
	r = c.expect("POST", "/api/v2/admin/approvers/remove", `{"approver_id":"`+first.ID+`",`+rm.body()+`}`, session(s), 200, "")
	if string(r.Data) != `{"approver_id":"`+first.ID+`"}` {
		t.Fatalf("remove %s", r.Data)
	}
	r = c.expect("GET", "/api/v2/admin/approvers", "", session(s), 200, "")
	if strings.Contains(string(r.Data), first.ID) || !strings.Contains(string(r.Data), info.ApproverID) {
		t.Fatalf("after remove %s", r.Data)
	}
	// The last approver is refused before any code is looked at.
	clk.Advance(10 * time.Second)
	last := c.code(s, "console_open", `{}`)
	c.expect("POST", "/api/v2/admin/codes", `{"action":"approver_remove","params":{"approver_id":"`+info.ApproverID+`"}}`, session(s), 409, codeApproverLast)
	c.expect("POST", "/api/v2/admin/approvers/remove", `{"approver_id":"`+info.ApproverID+`",`+last.body()+`}`, session(s), 409, codeApproverLast)

	// console_open: verified here, its ticket comes with the console.
	con := last
	r = c.expect("POST", "/api/v2/admin/codes/verify", `{`+con.body()+`}`, session(s), 200, "")
	if string(r.Data) != `{"action":"console_open","expires_in":30}` {
		t.Fatalf("verify %s", r.Data)
	}
	if err := c.a.admin.TakeGrant(s, con.id, admin.ActionConsoleOpen); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Updates and reboot never need a code.
	c.expect("POST", "/api/v2/system/reboot", "", session(s), 200, "")
}

func TestCodeRequestErrors(t *testing.T) {
	c, clk, s, a := adminWithApprover(t)
	for _, b := range []string{
		`{`,
		`{}`,
		`{"action":"shell"}`,
		`{"action":"network_config"}`,
		`{"action":"console_open","params":[]}`,
		`{"action":"console_open","params":{"x":1}}`,
		`{"action":"console_open","params":{"x":"y"}}`,
		`{"action":"admin_password","params":{}}`,
		`{"action":"console_open","approver_id":"ABC"}`,
	} {
		c.expect("POST", "/api/v2/admin/codes", b, session(s), 400, codeInvalidRequest)
	}
	c.expect("POST", "/api/v2/admin/codes", `{"action":"admin_password","params":{"password":"short"}}`, session(s), 400, codePasswordTooShort)
	c.expect("POST", "/api/v2/admin/codes", `{"action":"console_open","approver_id":"0123456789abcdef"}`, session(s), 404, codeApproverNotFound)
	c.expect("POST", "/api/v2/admin/codes", `{"action":"approver_remove","params":{"approver_id":"0123456789abcdef"}}`, session(s), 404, codeApproverNotFound)

	r := c.expect("POST", "/api/v2/admin/codes", `{"action":"console_open","params":null,"approver_id":"`+a.ID+`"}`, session(s), 200, "")
	var info admin.CodeInfo
	if err := json.Unmarshal(r.Data, &info); err != nil || info.ApproverID != a.ID || !admin.ValidID(info.CodeID) || info.ExpiresIn != 60 {
		t.Fatalf("code %s", r.Data)
	}
	_, r, h := c.do("POST", "/api/v2/admin/codes", `{"action":"console_open"}`, session(s))
	if r.Code != codeRateLimited || string(r.Data) != `{"retry_after":10}` || h.Get("Retry-After") != "10" {
		t.Fatalf("second code at once: %s %s %v", r.Code, r.Data, h)
	}

	for _, b := range []string{
		`{}`,
		`{"code_id":"` + info.CodeID + `"}`,
		`{"code":"123456"}`,
		`{"code_id":"` + info.CodeID + `","code":"12345"}`,
		`{"code_id":"` + info.CodeID + `","code":"abcdef"}`,
		`{"code_id":"short","code":"123456"}`,
	} {
		c.expect("POST", "/api/v2/admin/codes/verify", b, session(s), 400, codeInvalidRequest)
	}
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`","code":"123456"}`, session(s), 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/codes/verify", `{"code_id":"0123456789abcdef","code":"123456"}`, session(s), 410, codeCodeExpired)

	clk.Advance(admin.CodeTTL)
	code := "123456"
	c.expect("POST", "/api/v2/admin/codes/verify", `{"code_id":"`+info.CodeID+`","code":"`+code+`"}`, session(s), 410, codeCodeExpired)
	// None of the above counted against the session.
	clk.Advance(10 * time.Second)
	ref := c.code(s, "console_open", `{}`)
	r = c.expect("POST", "/api/v2/admin/codes/verify", `{"code_id":"`+ref.id+`","code":"`+wrongCode(ref.code)+`"}`, session(s), 403, codeBadCode)
	if string(r.Data) != `{"attempts_left":4}` {
		t.Fatalf("first failure %s", r.Data)
	}
}

func TestFiveWrongCodesOverHTTP(t *testing.T) {
	c, _, s, _ := adminWithApprover(t)
	other := c.verify(adminPw)
	ref := c.code(s, "console_open", `{}`)
	for left := 4; left >= 0; left-- {
		r := c.expect("POST", "/api/v2/admin/codes/verify", `{"code_id":"`+ref.id+`","code":"`+wrongCode(ref.code)+`"}`, session(s), 403, codeBadCode)
		if string(r.Data) != fmt.Sprintf(`{"attempts_left":%d}`, left) {
			t.Fatalf("attempts left %s, want %d", r.Data, left)
		}
	}
	c.expect("POST", "/api/v2/admin/codes/verify", `{`+ref.body()+`}`, session(s), 401, codeBadSession)
	c.expect("GET", "/api/v2/update/status", "", session(s), 401, codeBadSession)
	c.expect("GET", "/api/v2/update/status", "", session(other), 200, "")
}

func TestResetWipesApproversOverHTTP(t *testing.T) {
	c, _, _, _ := adminWithApprover(t)
	c.expect("POST", "/api/v2/admin/reset", "", nil, 200, "")
	if _, err := os.Lstat(filepath.Join(adminDataDir, "approvers.json")); !os.IsNotExist(err) {
		t.Fatalf("approvers.json after reset: %v", err)
	}
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)
	if r := c.expect("GET", "/api/v2/admin/approvers", "", session(s), 200, ""); string(r.Data) != `[]` {
		t.Fatalf("approvers after reset %s", r.Data)
	}
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`"}`, session(s), 409, codeApproverRequired)
}

func TestApproverAudit(t *testing.T) {
	c, clk, s, a := adminWithApprover(t)
	ref := c.code(s, "console_open", `{}`)
	c.expect("POST", "/api/v2/admin/codes/verify", `{"code_id":"`+ref.id+`","code":"`+wrongCode(ref.code)+`"}`, session(s), 403, codeBadCode)
	c.expect("POST", "/api/v2/admin/codes/verify", `{`+ref.body()+`}`, session(s), 200, "")
	clk.Advance(10 * time.Second)
	pw := c.code(s, "admin_password", `{"password":"`+adminPwNew+`"}`)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`",`+pw.body()+`}`, session(s), 200, "")

	log := readAudit(t)
	sid := admin.SessionAuditID(s)
	for _, want := range []string{
		`result=ok session=` + sid + ` action=approver_enroll approver=` + a.ID + ` enrol_code_id=`,
		`result=ok session=` + sid + ` action=approver_confirm code_id=`,
		`result=ok session=- GET "/api/v2/admin/push/`,
		`result=ok session=` + sid + ` action=console_open approver=` + a.ID + ` code_id=` + ref.id + ` POST "/api/v2/admin/codes"`,
		`result=bad_code session=` + sid + ` code_id=` + ref.id + ` attempts_left=4 POST "/api/v2/admin/codes/verify"`,
		`result=ok session=` + sid + ` code_id=` + ref.id + ` action=console_open POST "/api/v2/admin/codes/verify"`,
		`result=ok session=` + sid + ` action=admin_password code_id=` + pw.id + ` POST "/api/v2/admin/set"`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("audit lacks %q:\n%s", want, log)
		}
	}
	for _, secret := range []string{ref.code, pw.code, "tok-a", "Phone tok-a", adminPwNew, s} {
		if strings.Contains(log, secret) {
			t.Fatalf("audit log leaks %q", secret)
		}
	}
}

func TestPushPayloadOverHTTP(t *testing.T) {
	var logs bytes.Buffer
	logger.Logger.SetOutput(&logs)
	t.Cleanup(func() { logger.Logger.SetOutput(os.Stdout) })

	c, clk, s, _ := adminWithApprover(t)
	r := c.expect("POST", "/api/v2/admin/codes", `{"action":"admin_password","params":{"password":"`+adminPwNew+`"}}`, session(s), 200, "")
	var info admin.CodeInfo
	_ = json.Unmarshal(r.Data, &info)
	clk.Advance(12 * time.Second)
	r = c.expect("GET", "/api/v2/admin/push/"+info.CodeID, "", nil, 200, "")
	var p map[string]any
	if err := json.Unmarshal(r.Data, &p); err != nil || len(p) != 5 {
		t.Fatalf("push payload %s", r.Data)
	}
	code, _ := p["code"].(string)
	if p["token"] != "tok-a" || p["action"] != "admin_password" || p["expires_in"] != float64(48) || !admin.ValidCode(code) ||
		p["message"] != "Calaos admin code "+code+": change the admin password. Valid 60 s, never share it." {
		t.Fatalf("push payload %s", r.Data)
	}
	c.expect("GET", "/api/v2/admin/push/"+info.CodeID, "", nil, 404, codeNotFound)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`","code_id":"`+info.CodeID+`","code":"`+code+`"}`, session(s), 200, "")

	for name, out := range map[string]string{"audit": readAudit(t), "log": logs.String()} {
		if strings.Contains(out, code) {
			t.Fatalf("%s leaks the code:\n%s", name, out)
		}
	}
}

// The approval path must stay clear of the legacy hub's shapes.
func TestPushCodeNeverCarriesSecrets(t *testing.T) {
	c, _, s, _ := adminWithApprover(t)
	sub := events.Default.Subscribe()
	defer events.Default.Unsubscribe(sub)
	ref := c.code(s, "console_open", `{}`)
	for {
		select {
		case ev := <-sub.Ch:
			if ev.Type != admin.EventPushCode {
				continue
			}
			b, _ := json.Marshal(ev)
			if strings.Contains(string(b), ref.code) || strings.Contains(string(b), "tok-a") || !strings.Contains(string(b), ref.id) {
				t.Fatalf("push_code %s", b)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("no push_code event")
		}
	}
}

func TestUnreadableApproversAnswerInternal(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	if err := os.WriteFile(filepath.Join(adminDataDir, "approvers.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	c = adminClient{t: t, a: restarted}
	s := c.verify(adminPw)
	c.expect("POST", "/api/v2/admin/approvers/enroll", `{"token":"tok-a","device_name":"Pixel"}`, session(s), 500, codeInternal)
	c.expect("POST", "/api/v2/admin/codes", `{"action":"console_open"}`, session(s), 500, codeInternal)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`"}`, session(s), 500, codeInternal)
	c.expect("POST", "/api/v2/admin/reset", "", nil, 200, "")
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	c.enrol(c.verify(adminPw), "tok-a")
}
