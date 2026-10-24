package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/app/console"
	"github.com/calaos/calaos-container/app/console/consoletest"
)

// consoleApp is an admin with an approver and a test clock.
func consoleApp(t *testing.T) (adminClient, *testClock, string) {
	t.Helper()
	c, clk, s, _ := adminWithApprover(t)
	t.Cleanup(c.a.consoles.Shutdown)
	return c, clk, s
}

// grant runs code request and verify; the next code of s needs 10 s.
func (c adminClient) grant(clk *testClock, s string) string {
	c.t.Helper()
	ref := c.code(s, admin.ActionConsoleOpen, `{}`)
	c.expect("POST", "/api/v2/admin/codes/verify", `{`+ref.body()+`}`, session(s), 200, "")
	clk.Advance(10 * time.Second)
	return ref.id
}

func (c adminClient) openConsole(s, codeID string) consoleOpened {
	c.t.Helper()
	r := c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+codeID+`"}`, session(s), 200, "")
	var d consoleOpened
	if err := json.Unmarshal(r.Data, &d); err != nil {
		c.t.Fatal(err)
	}
	return d
}

func waitAudit(t *testing.T, want string) string {
	t.Helper()
	var log string
	consoletest.Eventually(t, "audit "+want, func() bool {
		b, _ := os.ReadFile(filepath.Join(adminDataDir, "audit.log"))
		log = string(b)
		return strings.Contains(log, want)
	})
	return log
}

func TestConsoleOpen(t *testing.T) {
	c, clk, s := consoleApp(t)
	id := c.grant(clk, s)
	d := c.openConsole(s, id)

	want := consoleOpened{
		ConsoleID: d.ConsoleID, Socket: filepath.Join(adminRuntimeDir, "console", "console-"+d.ConsoleID+".sock"),
		WSPath: "/ws", WSProtocol: "calaos-console.v1", ConnectWithin: 30, ExpiresIn: 3600, IdleTimeout: 900,
	}
	if d != want || !console.ValidID(d.ConsoleID) {
		t.Fatalf("open data %+v, want %+v", d, want)
	}
	if fi, err := os.Lstat(d.Socket); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v", err)
	}

	c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+id+`"}`, session(s), 410, codeCodeExpired)

	var found bool
	for _, al := range c.a.admin.PendingAlerts() {
		if al.Type == admin.EventConsoleOpened {
			found = al.SessionAuditID == admin.SessionAuditID(s) && al.TS != "" && al.ID != ""
		}
	}
	if !found {
		t.Fatalf("no console_opened alert for the session: %+v", c.a.admin.PendingAlerts())
	}

	log := waitAudit(t, "console="+d.ConsoleID)
	if !strings.Contains(log, "result=ok session="+admin.SessionAuditID(s)+" action=console_open code_id="+id+" console="+d.ConsoleID+` POST "/api/v2/admin/console"`) {
		t.Fatalf("open audit line missing:\n%s", log)
	}
}

func TestConsoleGrantIsBoundToTheSession(t *testing.T) {
	c, clk, s := consoleApp(t)
	other := c.verify(adminPw)
	id := c.grant(clk, s)

	c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+id+`"}`, session(other), 410, codeCodeExpired)
	c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+id+`"}`, nil, 401, codeBadSession)
	if n := c.a.consoles.Count(); n != 0 {
		t.Fatalf("%d consoles after refusals", n)
	}
	c.openConsole(s, id)
}

func TestConsoleOpenNeedsAVerifiedCode(t *testing.T) {
	c, _, s := consoleApp(t)
	ref := c.code(s, admin.ActionConsoleOpen, `{}`)
	c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+ref.id+`"}`, session(s), 410, codeCodeExpired)
	c.expect("POST", "/api/v2/admin/console", `{}`, session(s), 400, codeInvalidRequest)
	c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+ref.id+`","term":"dumb"}`, session(s), 400, codeInvalidRequest)
}

func TestConsoleCapLeavesTheGrant(t *testing.T) {
	c, clk, s := consoleApp(t)
	first := c.openConsole(s, c.grant(clk, s))
	c.openConsole(s, c.grant(clk, s))

	id := c.grant(clk, s)
	r := c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+id+`"}`, session(s), 409, codeConsoleLimit)
	if string(r.Data) != `{"max":2}` {
		t.Fatalf("limit data %s", r.Data)
	}

	conn := dialConsole(t, first)
	_ = conn.Close()
	consoletest.Eventually(t, "first console closed", func() bool { return c.a.consoles.Count() == 1 })
	c.openConsole(s, id)
}

func TestConsoleKilledOnReset(t *testing.T) {
	c, clk, s := consoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	c.expect("POST", "/api/v2/admin/reset", "", nil, 200, "")
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=admin_reset")
	consoletest.Eventually(t, "socket removed", func() bool {
		_, err := os.Lstat(d.Socket)
		return os.IsNotExist(err)
	})
}

func TestConsoleKilledOnAdminSet(t *testing.T) {
	c, clk, s := consoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPwNew+`","recovery":true}`, nil, 200, "")
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=admin_set")
}

func TestConsoleKilledAfterFiveBadCodes(t *testing.T) {
	c, clk, s := consoleApp(t)
	other := c.verify(adminPw)
	mine := c.openConsole(s, c.grant(clk, s))
	theirs := c.openConsole(other, c.grant(clk, other))

	ref := c.code(s, admin.ActionConsoleOpen, `{}`)
	for i := 0; i < admin.MaxPushCodeFailures; i++ {
		c.expect("POST", "/api/v2/admin/codes/verify", `{"code_id":"`+ref.id+`","code":"`+wrongCode(ref.code)+`"}`, session(s), 403, codeBadCode)
	}
	waitAudit(t, "action=console_close console="+mine.ConsoleID+" reason=bad_code")
	if _, err := os.Lstat(theirs.Socket); err != nil {
		t.Fatalf("other session's console: %v", err)
	}
}

func TestConsoleActivityRouteRemoved(t *testing.T) {
	c, clk, s := consoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	c.expect("POST", "/api/v2/admin/console/"+d.ConsoleID+"/activity", `{"event":"closed"}`, nil, 404, codeNotFound)
}

func TestConsoleTimeLimitsThroughTheApp(t *testing.T) {
	c, clk, s := consoleApp(t)
	d := c.openConsole(s, c.grant(clk, s))
	clk.Advance(console.ConnectTimeout)
	c.a.consoles.Sweep()
	waitAudit(t, "action=console_close console="+d.ConsoleID+" reason=not_connected duration=30")
}

func TestConsoleCloseAuditCarriesThePeer(t *testing.T) {
	c, clk, s := consoleApp(t)
	id := c.grant(clk, s)
	hdr := map[string]string{headerAdminSession: s, headerClientIP: "192.0.2.44"}
	c.expect("POST", "/api/v2/admin/console", `{"code_id":"`+id+`"}`, hdr, 200, "")
	c.a.consoles.SessionsEnded("", "admin_reset")
	log := waitAudit(t, "action=console_close")
	if !strings.Contains(log, "client=192.0.2.44 result=ok session="+admin.SessionAuditID(s)+" action=console_close") {
		t.Fatalf("close audit line:\n%s", log)
	}
}
