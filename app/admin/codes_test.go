package admin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var digitsRe = regexp.MustCompile(`\b(\d{6})\b`)

// digitsOf reads the code the way calaos_server does: through the payload.
func digitsOf(t *testing.T, m *Manager, codeID string) string {
	t.Helper()
	_, msg, err := m.PushPayload(codeID)
	if err != nil {
		t.Fatalf("PushPayload(%s): %v", codeID, err)
	}
	d := digitsRe.FindStringSubmatch(msg)
	if d == nil {
		t.Fatalf("no code in %q", msg)
	}
	return d[1]
}

func wrong(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func requestCode(t *testing.T, e *env, m *Manager, s, action string, p Params) CodeRef {
	t.Helper()
	info, err := m.RequestCode(s, action, p, "")
	if err != nil {
		t.Fatalf("RequestCode(%s): %v", action, err)
	}
	return CodeRef{ID: info.CodeID, Code: digitsOf(t, m, info.CodeID)}
}

// enrol adds an approver, using an approver_enroll code when one exists.
func enrol(t *testing.T, e *env, m *Manager, s, token string) ApproverInfo {
	t.Helper()
	var ref CodeRef
	if m.HasApprover() {
		ref = requestCode(t, e, m, s, ActionApproverEnroll, nil)
		e.clk.Advance(codeInterval)
	}
	info, err := m.Enroll(s, token, "Phone "+token, ref)
	if err != nil {
		t.Fatalf("Enroll(%s): %v", token, err)
	}
	a, err := m.Confirm(s, CodeRef{ID: info.CodeID, Code: digitsOf(t, m, info.CodeID)})
	if err != nil {
		t.Fatalf("Confirm(%s): %v", token, err)
	}
	if a.ID != info.ApproverID {
		t.Fatalf("confirmed %s, enrolled %s", a.ID, info.ApproverID)
	}
	return a
}

func adminWithSession(t *testing.T) (*env, *Manager, string) {
	t.Helper()
	e := newEnv(t)
	m := e.manager()
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	return e, m, mustVerify(t, m, goodPassword)
}

func pushCodes(p *published) []PushCodeData {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []PushCodeData
	for _, e := range p.events {
		if d, ok := e.data.(PushCodeData); ok && e.typ == EventPushCode {
			out = append(out, d)
		}
	}
	return out
}

func TestEnrolment(t *testing.T) {
	e, m, s := adminWithSession(t)

	if _, err := m.Enroll("dead", "tok-a", "Pixel", CodeRef{}); !errors.Is(err, ErrBadSession) {
		t.Fatalf("enrol without a session: %v", err)
	}
	if _, err := m.RequestCode(s, ActionConsoleOpen, nil, ""); !errors.Is(err, ErrApproverRequired) {
		t.Fatalf("code without an approver: %v", err)
	}

	info, err := m.Enroll(s, "tok-a", "Pixel 8", CodeRef{})
	if err != nil || !ValidID(info.CodeID) || !ValidID(info.ApproverID) || info.ExpiresIn != 60 {
		t.Fatalf("enrol: %+v %v", info, err)
	}
	if m.HasApprover() {
		t.Fatal("approver stored before its code was confirmed")
	}

	// The event names the code; the payload holds the token, for that
	// one phone only.
	pc := pushCodes(e.pub)
	if len(pc) != 1 || pc[0] != (PushCodeData{ApproverID: info.ApproverID, CodeID: info.CodeID}) {
		t.Fatalf("push_code events %+v", pc)
	}
	ev, _ := json.Marshal(pc[0])
	if string(ev) != `{"approver_id":"`+info.ApproverID+`","code_id":"`+info.CodeID+`"}` {
		t.Fatalf("push_code data %s", ev)
	}
	tok, msg, err := m.PushPayload(info.CodeID)
	if err != nil || tok != "tok-a" || !strings.Contains(msg, "enroll this phone") {
		t.Fatalf("payload %q %q %v", tok, msg, err)
	}
	code := digitsRe.FindString(msg)
	if strings.Contains(string(ev), code) {
		t.Fatal("the event carries the code")
	}

	if _, err := m.Confirm(s, CodeRef{ID: info.CodeID, Code: wrong(code)}); !isBadCode(err, 4) {
		t.Fatalf("confirm with a wrong code: %v", err)
	}
	a, err := m.Confirm(s, CodeRef{ID: info.CodeID, Code: code})
	if err != nil || a.ID != info.ApproverID || a.DeviceName != "Pixel 8" || len(a.TokenID) != 16 || a.CreatedAt != "2026-10-23T10:00:00Z" {
		t.Fatalf("confirm: %+v %v", a, err)
	}
	if _, err := m.Confirm(s, CodeRef{ID: info.CodeID, Code: code}); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("second confirm: %v", err)
	}

	list, err := m.Approvers(s)
	if err != nil || len(list) != 1 || list[0] != a {
		t.Fatalf("list %+v %v", list, err)
	}
	if b, _ := json.Marshal(list); strings.Contains(string(b), "tok-a") {
		t.Fatalf("the list carries the token: %s", b)
	}
	if _, err := m.Approvers("dead"); !errors.Is(err, ErrBadSession) {
		t.Fatalf("list without a session: %v", err)
	}

	fi, err := os.Stat(filepath.Join(e.data, approversFileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("approvers file %v %v", fi, err)
	}
	if got, _ := e.manager().Approvers(s); len(got) != 1 || got[0] != a {
		t.Fatalf("approvers not persisted: %+v", got)
	}
}

func isBadCode(err error, left int) bool {
	var bc *BadCodeError
	return errors.As(err, &bc) && bc.AttemptsLeft == left
}

func TestSecondApproverNeedsACode(t *testing.T) {
	e, m, s := adminWithSession(t)
	first := enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)

	if _, err := m.Enroll(s, "tok-a", "again", CodeRef{}); !errors.Is(err, ErrApproverExists) {
		t.Fatalf("enrol the same token: %v", err)
	}
	if _, err := m.Enroll(s, "tok-b", "Spare", CodeRef{}); !errors.Is(err, ErrCodeRequired) {
		t.Fatalf("second approver without a code: %v", err)
	}
	ref := requestCode(t, e, m, s, ActionApproverEnroll, nil)
	if pc := pushCodes(e.pub); pc[len(pc)-1].ApproverID != first.ID {
		t.Fatalf("approval code sent to %s", pc[len(pc)-1].ApproverID)
	}
	e.clk.Advance(codeInterval)
	info, err := m.Enroll(s, "tok-b", "Spare", ref)
	if err != nil {
		t.Fatalf("enrol with a code: %v", err)
	}
	if tok, _, _ := m.PushPayload(info.CodeID); tok != "tok-b" {
		t.Fatalf("enrolment code sent to %q", tok)
	}

	// Done by hand: the payload of info was fetched above.
	m.mu.Lock()
	code := m.codes.byID[info.CodeID].digits
	m.mu.Unlock()
	if _, err := m.Confirm(s, CodeRef{ID: info.CodeID, Code: code}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	e.clk.Advance(codeInterval)
	if _, err := m.RequestCode(s, ActionApproverEnroll, nil, ""); !errors.Is(err, ErrApproverLimit) {
		t.Fatalf("code for a third approver: %v", err)
	}
	if _, err := m.Enroll(s, "tok-c", "Third", CodeRef{}); !errors.Is(err, ErrApproverLimit) {
		t.Fatalf("third approver: %v", err)
	}
}

func TestCodeBinding(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	other := mustVerify(t, m, goodPassword)

	step := func() { e.clk.Advance(codeInterval) }
	step()
	ref := requestCode(t, e, m, s, ActionAdminPassword, Params{"password": otherPassword})
	if !ValidCode(ref.Code) {
		t.Fatalf("code %q", ref.Code)
	}

	// Another session, another action or other params never pass.
	if err := m.Set(ModeChange, otherPassword, other, ref); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("code of another session: %v", err)
	}
	if err := m.Set(ModeChange, "a third password", s, ref); !isBadCode(err, 4) {
		t.Fatalf("code bound to other params: %v", err)
	}
	if err := m.RemoveApprover(s, m.approvers.list[0].ID, ref); !isBadCode(err, 3) {
		t.Fatalf("code bound to another action: %v", err)
	}
	if err := m.Set(ModeChange, otherPassword, s, ref); err != nil {
		t.Fatalf("change: %v", err)
	}

	// Single use, and expiry at 60 s, neither counted as a failure.
	s = mustVerify(t, m, otherPassword)
	step()
	ref = requestCode(t, e, m, s, ActionAdminPassword, Params{"password": goodPassword})
	e.clk.Advance(CodeTTL)
	if err := m.Set(ModeChange, goodPassword, s, ref); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("expired code: %v", err)
	}
	ref = requestCode(t, e, m, s, ActionAdminPassword, Params{"password": goodPassword})
	e.clk.Advance(CodeTTL - time.Second)
	if err := m.Set(ModeChange, goodPassword, s, ref); err != nil {
		t.Fatalf("code at 59 s: %v", err)
	}
	s = mustVerify(t, m, goodPassword)
	step()
	ref = requestCode(t, e, m, s, ActionConsoleOpen, nil)
	if _, _, err := m.VerifyCode(s, ref); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, _, err := m.VerifyCode(s, ref); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("verify twice: %v", err)
	}
}

func TestOnePendingCodePerSession(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)

	first := requestCode(t, e, m, s, ActionConsoleOpen, nil)
	e.clk.Advance(codeInterval)
	second := requestCode(t, e, m, s, ActionConsoleOpen, nil)
	if _, _, err := m.VerifyCode(s, first); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("replaced code: %v", err)
	}
	if _, _, err := m.VerifyCode(s, second); err != nil {
		t.Fatalf("new code: %v", err)
	}
}

func TestCodeGenerationLimits(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a") // 1 code

	var rl *RateLimitedError
	if _, err := m.RequestCode(s, ActionConsoleOpen, nil, ""); !errors.As(err, &rl) || rl.RetryAfter != 10 {
		t.Fatalf("code within 10 s: %v", err)
	}
	// Per session: another session is not held by the interval.
	other := mustVerify(t, m, goodPassword)
	if _, err := m.RequestCode(other, ActionConsoleOpen, nil, ""); err != nil {
		t.Fatalf("code of another session: %v", err)
	}
	e.clk.Advance(4 * time.Second)
	if _, err := m.RequestCode(s, ActionConsoleOpen, nil, ""); !errors.As(err, &rl) || rl.RetryAfter != 6 {
		t.Fatalf("code at 4 s: %v", err)
	}

	for i := 3; i <= codeBudget; i++ {
		e.clk.Advance(codeInterval)
		if _, err := m.RequestCode(s, ActionConsoleOpen, nil, ""); err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
	}
	e.clk.Advance(codeInterval)
	if _, err := m.RequestCode(other, ActionConsoleOpen, nil, ""); !errors.As(err, &rl) || rl.RetryAfter < 3000 {
		t.Fatalf("code %d in the hour: %v", codeBudget+1, err)
	}
	// The budget is the daemon's: a reset of every session keeps it.
	mustSet(t, m, ModeRecovery, goodPassword, "")
	s = mustVerify(t, m, goodPassword)
	if _, err := m.Enroll(s, "tok-b", "Spare", CodeRef{}); !errors.As(err, &rl) {
		t.Fatalf("enrolment code over the budget: %v", err)
	}
	e.clk.Advance(codeWindow)
	s = mustVerify(t, m, goodPassword)
	if _, err := m.RequestCode(s, ActionConsoleOpen, nil, ""); err != nil {
		t.Fatalf("code after the hour: %v", err)
	}
}

func TestRateLimitKeepsTheApprovalCode(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)
	ref := requestCode(t, e, m, s, ActionApproverEnroll, nil)

	var rl *RateLimitedError
	if _, err := m.Enroll(s, "tok-b", "Spare", ref); !errors.As(err, &rl) {
		t.Fatalf("enrolment within 10 s: %v", err)
	}
	e.clk.Advance(codeInterval)
	if _, err := m.Enroll(s, "tok-b", "Spare", ref); err != nil {
		t.Fatalf("enrolment with the same code: %v", err)
	}
}

func TestFiveWrongCodesKillTheSession(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	other := mustVerify(t, m, goodPassword)

	e.clk.Advance(codeInterval)
	ref := requestCode(t, e, m, s, ActionConsoleOpen, nil)
	bad := CodeRef{ID: ref.ID, Code: wrong(ref.Code)}
	for left := MaxPushCodeFailures - 1; left >= 0; left-- {
		if _, _, err := m.VerifyCode(s, bad); !isBadCode(err, left) {
			t.Fatalf("attempt with %d left: %v", left, err)
		}
	}
	if m.Valid(s) || e.manager().Valid(s) {
		t.Fatal("session alive after 5 wrong codes")
	}
	if _, _, err := m.VerifyCode(s, ref); !errors.Is(err, ErrBadSession) {
		t.Fatalf("right code after the 5th failure: %v", err)
	}
	if !m.Valid(other) {
		t.Fatal("another session was killed")
	}
	m.mu.Lock()
	n := len(m.codes.byID)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d codes left after the session died", n)
	}
}

func TestFailuresCountAcrossCodes(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")

	// A new code does not reset the count of the session.
	for i, left := 0, MaxPushCodeFailures-1; left >= 0; i++ {
		e.clk.Advance(codeInterval)
		ref := requestCode(t, e, m, s, ActionConsoleOpen, nil)
		for j := 0; j < 2 && left >= 0; j, left = j+1, left-1 {
			if _, _, err := m.VerifyCode(s, CodeRef{ID: ref.ID, Code: wrong(ref.Code)}); !isBadCode(err, left) {
				t.Fatalf("code %d, %d left: %v", i, left, err)
			}
		}
	}
	if m.Valid(s) {
		t.Fatal("session alive after 5 wrong codes over 3 codes")
	}
}

func TestConcurrentWrongCodes(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)
	ref := requestCode(t, e, m, s, ActionConsoleOpen, nil)

	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := m.VerifyCode(s, CodeRef{ID: ref.ID, Code: wrong(ref.Code)})
			var bc *BadCodeError
			if errors.As(err, &bc) {
				mu.Lock()
				bad++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if bad != MaxPushCodeFailures || m.Valid(s) {
		t.Fatalf("%d wrong codes counted, session valid %v", bad, m.Valid(s))
	}
}

func TestPushPayload(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)

	info, err := m.RequestCode(s, ActionConsoleOpen, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	tok, msg, err := m.PushPayload(info.CodeID)
	if err != nil || tok != "tok-a" || !strings.Contains(msg, "open the web console") || !digitsRe.MatchString(msg) {
		t.Fatalf("payload %q %q %v", tok, msg, err)
	}
	if _, _, err := m.PushPayload(info.CodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second fetch: %v", err)
	}
	if _, _, err := m.PushPayload("0123456789abcdef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code: %v", err)
	}

	e.clk.Advance(codeInterval)
	info, _ = m.RequestCode(s, ActionConsoleOpen, nil, "")
	e.clk.Advance(CodeTTL)
	if _, _, err := m.PushPayload(info.CodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired code: %v", err)
	}

	// A dead session's code is never pushed: the session ends at 900 s,
	// the code would live until 955 s.
	e.clk.Advance(SessionTTL - 80*time.Second - 5*time.Second)
	if info, err = m.RequestCode(s, ActionConsoleOpen, nil, ""); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(codeInterval)
	if _, _, err := m.PushPayload(info.CodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code of an expired session: %v", err)
	}
}

func TestVerifyAndGrant(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)

	// Only grant actions go through verify; the code survives the refusal.
	pw := requestCode(t, e, m, s, ActionAdminPassword, Params{"password": otherPassword})
	var inv *InvalidError
	if _, _, err := m.VerifyCode(s, pw); !errors.As(err, &inv) {
		t.Fatalf("verify of an admin_password code: %v", err)
	}
	if err := m.Set(ModeChange, otherPassword, s, pw); err != nil {
		t.Fatalf("change after the refused verify: %v", err)
	}

	s = mustVerify(t, m, otherPassword)
	e.clk.Advance(codeInterval)
	ref := requestCode(t, e, m, s, ActionConsoleOpen, nil)
	if err := m.TakeGrant(s, ref.ID, ActionConsoleOpen); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("grant before verify: %v", err)
	}
	action, exp, err := m.VerifyCode(s, ref)
	if err != nil || action != ActionConsoleOpen || exp != 30 {
		t.Fatalf("verify: %q %d %v", action, exp, err)
	}
	if _, _, err := m.PushPayload(ref.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("payload of a grant: %v", err)
	}
	if err := m.TakeGrant(s, ref.ID, ActionAdminPassword); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("grant for another action: %v", err)
	}
	other := mustVerify(t, m, otherPassword)
	if err := m.TakeGrant(other, ref.ID, ActionConsoleOpen); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("grant of another session: %v", err)
	}
	if err := m.TakeGrant(s, ref.ID, ActionConsoleOpen); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := m.TakeGrant(s, ref.ID, ActionConsoleOpen); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("grant twice: %v", err)
	}

	e.clk.Advance(codeInterval)
	ref = requestCode(t, e, m, s, ActionConsoleOpen, nil)
	if _, _, err := m.VerifyCode(s, ref); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(GrantTTL)
	if err := m.TakeGrant(s, ref.ID, ActionConsoleOpen); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("grant after 30 s: %v", err)
	}
}

func TestCodeRequestValidation(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)

	var inv *InvalidError
	for _, c := range []struct {
		action string
		p      Params
	}{
		{ActionNetworkConfig, nil},
		{"shell", nil},
		{"", nil},
		{"approver_confirm", nil},
		{ActionConsoleOpen, Params{"x": "y"}},
		{ActionAdminPassword, nil},
		{ActionAdminPassword, Params{"password": otherPassword, "x": "y"}},
		{ActionApproverRemove, Params{"id": "0123456789abcdef"}},
	} {
		if _, err := m.RequestCode(s, c.action, c.p, ""); !errors.As(err, &inv) {
			t.Fatalf("%q %v: %v", c.action, c.p, err)
		}
	}
	if _, err := m.RequestCode(s, ActionAdminPassword, Params{"password": "short"}, ""); !errors.Is(err, ErrTooShort) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := m.RequestCode(s, ActionApproverRemove, Params{"approver_id": "0123456789abcdef"}, ""); !errors.Is(err, ErrApproverNotFound) {
		t.Fatalf("remove an unknown approver: %v", err)
	}
	if _, err := m.RequestCode(s, ActionConsoleOpen, nil, "0123456789abcdef"); !errors.Is(err, ErrApproverNotFound) {
		t.Fatalf("code to an unknown approver: %v", err)
	}
	if _, err := m.RequestCode("dead", ActionConsoleOpen, nil, ""); !errors.Is(err, ErrBadSession) {
		t.Fatalf("code without a session: %v", err)
	}
	// None of the refusals above made a code.
	if _, err := m.RequestCode(s, ActionConsoleOpen, nil, ""); err != nil {
		t.Fatalf("valid request: %v", err)
	}
}

func TestRemoveApprover(t *testing.T) {
	e, m, s := adminWithSession(t)
	a := enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)
	b := enrol(t, e, m, s, "tok-b")
	e.clk.Advance(codeInterval)

	if err := m.RemoveApprover(s, a.ID, CodeRef{}); !errors.Is(err, ErrCodeRequired) {
		t.Fatalf("remove without a code: %v", err)
	}
	if err := m.RemoveApprover(s, "0123456789abcdef", CodeRef{}); !errors.Is(err, ErrApproverNotFound) {
		t.Fatalf("remove an unknown approver: %v", err)
	}
	// A code bound to b does not remove a.
	refB, err := m.RequestCode(s, ActionApproverRemove, Params{"approver_id": b.ID}, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	codeB := digitsOf(t, m, refB.CodeID)
	if err := m.RemoveApprover(s, a.ID, CodeRef{ID: refB.CodeID, Code: codeB}); !isBadCode(err, 4) {
		t.Fatalf("remove a with a code for b: %v", err)
	}

	// The removed phone's pending codes die with it.
	other := mustVerify(t, m, goodPassword)
	pending, err := m.RequestCode(other, ActionConsoleOpen, nil, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(codeInterval)
	ref := requestCode(t, e, m, s, ActionApproverRemove, Params{"approver_id": a.ID})
	if err := m.RemoveApprover(s, a.ID, ref); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, _, err := m.PushPayload(pending.CodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code to a removed approver: %v", err)
	}
	list, _ := m.Approvers(s)
	if len(list) != 1 || list[0] != b {
		t.Fatalf("after remove: %+v", list)
	}
	if got, _ := e.manager().Approvers(s); len(got) != 1 {
		t.Fatalf("remove not persisted: %+v", got)
	}

	// The last one can remove itself; then nothing is protected any more.
	e.clk.Advance(codeInterval)
	ref = requestCode(t, e, m, s, ActionApproverRemove, Params{"approver_id": b.ID})
	if err := m.RemoveApprover(s, b.ID, ref); err != nil {
		t.Fatalf("remove the last approver: %v", err)
	}
	if err := m.Set(ModeChange, otherPassword, s, CodeRef{}); !errors.Is(err, ErrApproverRequired) {
		t.Fatalf("change after the last approver left: %v", err)
	}
}

func TestResetWipesApprovers(t *testing.T) {
	e, m, s := adminWithSession(t)
	enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)
	info, err := m.RequestCode(s, ActionConsoleOpen, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	if was, err := m.Reset(); !was || err != nil {
		t.Fatalf("reset: %v %v", was, err)
	}
	if _, err := os.Lstat(filepath.Join(e.data, approversFileName)); !os.IsNotExist(err) {
		t.Fatalf("approvers file after reset: %v", err)
	}
	if _, _, err := m.PushPayload(info.CodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code after reset: %v", err)
	}
	mustSet(t, m, ModeBootstrap, goodPassword, "")
	s = mustVerify(t, m, goodPassword)
	if list, _ := m.Approvers(s); len(list) != 0 {
		t.Fatalf("approvers after reset: %+v", list)
	}
	if was, err := e.manager().Reset(); !was || err != nil {
		t.Fatalf("reset without approvers: %v %v", was, err)
	}
}

func TestRecoveryKeepsApprovers(t *testing.T) {
	e, m, s := adminWithSession(t)
	a := enrol(t, e, m, s, "tok-a")
	e.clk.Advance(codeInterval)
	info, _ := m.RequestCode(s, ActionConsoleOpen, nil, "")

	mustSet(t, m, ModeRecovery, otherPassword, "")
	if _, _, err := m.PushPayload(info.CodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code after the sessions died: %v", err)
	}
	s = mustVerify(t, m, otherPassword)
	if list, _ := m.Approvers(s); len(list) != 1 || list[0] != a {
		t.Fatalf("approvers after recovery: %+v", list)
	}
}

func TestCorruptApproversKeptAside(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.data, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.data, approversFileName)
	if err := os.WriteFile(path, []byte(`{"approvers":[{"id":"../x","token":""}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := e.manager()
	if m.HasApprover() {
		t.Fatal("corrupt approvers loaded")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("not kept aside: %v", err)
	}
}

func TestInputShapes(t *testing.T) {
	for _, tok := range []string{"a", strings.Repeat("x", MaxTokenBytes), "fcm:APA91b-_x.y"} {
		if !ValidToken(tok) {
			t.Errorf("token %q refused", tok)
		}
	}
	for _, tok := range []string{"", "a b", "a\nb", "é", strings.Repeat("x", MaxTokenBytes+1)} {
		if ValidToken(tok) {
			t.Errorf("token %q accepted", tok)
		}
	}
	for _, n := range []string{"Pixel 8", "iPhone de Raoul", strings.Repeat("é", MaxDeviceNameLen)} {
		if !ValidDeviceName(n) {
			t.Errorf("name %q refused", n)
		}
	}
	for _, n := range []string{"", "  ", "a\nb", "a\x00", "\xff", strings.Repeat("a", MaxDeviceNameLen+1)} {
		if ValidDeviceName(n) {
			t.Errorf("name %q accepted", n)
		}
	}
	for _, c := range []string{"12345", "1234567", "12345a", "١٢٣٤٥٦", " 12345"} {
		if ValidCode(c) {
			t.Errorf("code %q accepted", c)
		}
	}
	for i := 0; i < 200; i++ {
		d, err := newDigits()
		if err != nil || !ValidCode(d) {
			t.Fatalf("digits %q %v", d, err)
		}
	}
}
