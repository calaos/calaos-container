package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminLogoutEndsOnlyItsSession(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	mine := c.verify(adminPw)
	other := c.verify(adminPw)

	if r := c.expect("POST", "/api/v2/admin/logout", "", session(mine), 200, ""); string(r.Data) != `{"ended":true}` {
		t.Fatalf("logout %s", r.Data)
	}
	c.expect("GET", "/api/v2/admin/status", "", session(mine), 401, codeBadSession)
	c.expect("GET", "/api/v2/update/status", "", session(mine), 401, codeBadSession)
	c.expect("GET", "/api/v2/admin/status", "", session(other), 200, "")
	c.expect("GET", "/api/v2/admin/approvers", "", session(other), 200, "")
	c.expect("GET", "/api/v2/admin/approvers", "", session(mine), 401, codeBadSession)
	if set, _ := c.a.admin.Status(); !set {
		t.Fatal("logout removed the admin password")
	}
}

func TestAdminLogoutRepeats(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	c.expect("POST", "/api/v2/admin/logout", "", session(s), 200, "")
	for _, tok := range []string{s, strings.Repeat("a", 64), "junk", ""} {
		if r := c.expect("POST", "/api/v2/admin/logout", "", session(tok), 200, ""); string(r.Data) != `{"ended":false}` {
			t.Fatalf("repeated logout of %q: %s", tok, r.Data)
		}
	}
}

// A logout of a session that is not live must not write audit.log: any
// household client can have calaos_server relay a well-formed guess.
func TestAdminLogoutNoopIsNotAudited(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)
	c.expect("POST", "/api/v2/admin/logout", "", session(s), 200, "")
	before := readAudit(t)

	for i := 0; i < 300; i++ {
		tok := strings.Repeat("ab", 32)
		if i%2 == 1 {
			tok = s
		}
		c.expect("POST", "/api/v2/admin/logout", "", session(tok), 200, "")
	}
	if after := readAudit(t); after != before {
		t.Fatalf("audit.log grew:\n%s", strings.TrimPrefix(after, before))
	}
}

func TestAdminLogoutWriteFailureIs500(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	path := filepath.Join(adminRuntimeDir, "sessions.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if got, _, _ := c.do("POST", "/api/v2/admin/logout", "", session(s)); got != 500 {
		t.Fatalf("logout with an unwritable sessions file: %d", got)
	}
	c.expect("GET", "/api/v2/admin/status", "", session(s), 401, codeBadSession)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("sessions file left behind: %v", err)
	}
}

// Only the header names the session to end: the bearer alone has none.
func TestAdminLogoutNeedsTheSession(t *testing.T) {
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	c.expect("POST", "/api/v2/admin/logout", "", nil, 401, codeBadSession)
	c.expect("GET", "/api/v2/admin/status", "", session(s), 200, "")
	c.expect("GET", "/api/v2/admin/logout", "", session(s), 405, codeMethodNotAllowed)
	c.expect("GET", "/api/v2/admin/status", "", session(s), 200, "")
}

func TestAdminLogoutDropsPushCodes(t *testing.T) {
	c, _, s, _ := adminWithApprover(t)
	mine := c.verify(adminPw)

	r := c.expect("POST", "/api/v2/admin/codes", `{"action":"console_open","params":{}}`, session(mine), 200, "")
	var d struct {
		CodeID string `json:"code_id"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil || d.CodeID == "" {
		t.Fatalf("code %s", r.Data)
	}
	other := c.code(s, "console_open", `{}`)

	c.expect("POST", "/api/v2/admin/logout", "", session(mine), 200, "")
	c.expect("GET", "/api/v2/admin/push/"+d.CodeID, "", nil, 404, codeNotFound)

	// The code of another session is untouched.
	c.expect("POST", "/api/v2/admin/codes/verify", `{`+other.body()+`}`, session(s), 200, "")
}
