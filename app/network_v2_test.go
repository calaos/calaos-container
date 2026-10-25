package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/models/structs"
)

const (
	netCodeParams = `{"intf":"eth0","dhcp":"false","ipv4":"192.168.1.10/24","gateway":"192.168.1.1","dns_servers":"192.168.1.1","search_domains":"lan"}`
	netFields     = `"intf":"eth0","dhcp":false,"ipv4":"192.168.1.10/24","gateway":"192.168.1.1","dns_servers":["192.168.1.1"],"search_domains":["lan"]`
	netUnit       = "[Match]\nName=eth0\n\n[Network]\nDHCP=no\nAddress=192.168.1.10/24\nGateway=192.168.1.1\nDNS=192.168.1.1\nDomains=lan\n"
	sharedUnit    = "[Match]\nName=en*\n\n[Network]\nDHCP=yes\n"
)

type fakeNetworkctl struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (f *fakeNetworkctl) run(args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	if f.fail[cmd] {
		return errors.New("networkctl " + cmd + " failed")
	}
	return nil
}

func (f *fakeNetworkctl) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

type fakeTimers struct {
	mu   sync.Mutex
	list []*fakeTimer
}

func (ft *fakeTimers) last(t *testing.T) *fakeTimer {
	t.Helper()
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.list) == 0 {
		t.Fatal("no timer armed")
	}
	return ft.list[len(ft.list)-1]
}

type netEnv struct {
	t        *testing.T
	c        adminClient
	clk      *testClock
	s        string
	etc, run string
	nc       *fakeNetworkctl
	timers   *fakeTimers
	sub      *events.Subscriber

	bootMu sync.Mutex
	boot   time.Duration
}

// advanceBoot moves CLOCK_BOOTTIME only: the wall clock is e.clk.
func (e *netEnv) advanceBoot(d time.Duration) {
	e.bootMu.Lock()
	e.boot += d
	e.bootMu.Unlock()
}

// newNetEnv: an admin with an approver, networkd's directories in a temp
// dir, a fake networkctl and fake timers. The host network is never touched.
func newNetEnv(t *testing.T) *netEnv {
	c, clk, s, _ := adminWithApprover(t)
	e := &netEnv{t: t, c: c, clk: clk, s: s, nc: &fakeNetworkctl{fail: map[string]bool{}}, timers: &fakeTimers{}}
	root := t.TempDir()
	e.etc, e.run = filepath.Join(root, "etc"), filepath.Join(root, "run")
	if err := os.MkdirAll(e.etc, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.etc, "config.network"), []byte(sharedUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	prevEtc, prevRun, prevCtl := models.NetworkEtcDir, models.NetworkRunDir, models.Networkctl
	prevAfter, prevUpgrade, prevBoot := netAfterFunc, upgradeRunning, netBootClock
	e.boot = time.Hour
	netBootClock = func() (time.Duration, error) {
		e.bootMu.Lock()
		defer e.bootMu.Unlock()
		return e.boot, nil
	}
	models.NetworkEtcDir, models.NetworkRunDir, models.Networkctl = e.etc, e.run, e.nc.run
	netAfterFunc = func(d time.Duration, f func()) func() bool {
		ft := &fakeTimer{d: d, f: f}
		e.timers.mu.Lock()
		e.timers.list = append(e.timers.list, ft)
		e.timers.mu.Unlock()
		return func() bool { ft.stopped = true; return true }
	}
	upgradeRunning = func() bool { return false }
	t.Cleanup(func() {
		models.NetworkEtcDir, models.NetworkRunDir, models.Networkctl = prevEtc, prevRun, prevCtl
		netAfterFunc, upgradeRunning, netBootClock = prevAfter, prevUpgrade, prevBoot
	})

	// Restored by the stubBackend cleanup.
	getNetInterfaces = func() ([]*structs.NetInterface, error) {
		return []*structs.NetInterface{
			{Name: "lo", IPv4: "127.0.0.1/8", IsLoopback: true},
			{Name: "eth0", IPv4: "192.168.1.20/24", DHCP: true, State: "routable"},
		}, nil
	}
	interfaceExists = func(string) (bool, error) { return true, nil }
	configureNetInterface = models.ConfigureNetInterface

	e.useHub(c.a)
	return e
}

func (e *netEnv) useHub(a *AppServer) {
	a.hub = events.NewHub()
	e.sub = a.hub.Subscribe()
}

// event skips other events until one of type typ.
func (e *netEnv) event(typ string) map[string]any {
	e.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-e.sub.Ch:
			if ev.Type != typ {
				continue
			}
			b, _ := json.Marshal(ev.Data)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			return m
		case <-deadline:
			e.t.Fatalf("no %s event", typ)
		}
	}
}

func (e *netEnv) noEvent(typ string) {
	e.t.Helper()
	for {
		select {
		case ev := <-e.sub.Ch:
			if ev.Type == typ {
				e.t.Fatalf("unexpected %s event: %+v", typ, ev.Data)
			}
		default:
			return
		}
	}
}

func (e *netEnv) files(dir string) []string {
	e.t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for _, d := range entries {
		names = append(names, d.Name())
	}
	sort.Strings(names)
	return names
}

func (e *netEnv) read(path string) string {
	e.t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test temp file
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// trialOnly: the trial is the only file written, config.network untouched.
func (e *netEnv) trialOnly() {
	e.t.Helper()
	if got := e.files(e.run); !reflect.DeepEqual(got, []string{"00-calaos-trial-eth0.network"}) {
		e.t.Fatalf("/run holds %v", got)
	}
	if got := e.read(filepath.Join(e.run, "00-calaos-trial-eth0.network")); got != netUnit {
		e.t.Fatalf("trial:\n%s", got)
	}
	e.etcOnly()
}

// etcOnly: /etc holds the image's file only, unchanged.
func (e *netEnv) etcOnly(extra ...string) {
	e.t.Helper()
	want := append([]string{}, extra...)
	want = append(want, "config.network")
	sort.Strings(want)
	if got := e.files(e.etc); !reflect.DeepEqual(got, want) {
		e.t.Fatalf("/etc holds %v, want %v", got, want)
	}
	if got := e.read(filepath.Join(e.etc, "config.network")); got != sharedUnit {
		e.t.Fatalf("config.network changed:\n%s", got)
	}
}

func (e *netEnv) noTrial() {
	e.t.Helper()
	if got := e.files(e.run); len(got) != 0 {
		e.t.Fatalf("/run holds %v", got)
	}
}

func (e *netEnv) noRecord() {
	e.t.Helper()
	if _, err := os.Stat(filepath.Join(adminRuntimeDir, netPendingFile)); !errors.Is(err, os.ErrNotExist) {
		e.t.Fatalf("pending record still there: %v", err)
	}
}

type applied struct {
	ChangeID      string `json:"change_id"`
	Intf          string `json:"intf"`
	AppliedAt     string `json:"applied_at"`
	ConfirmWithin int    `json:"confirm_within"`
}

func (e *netEnv) apply(c adminClient, s string, hdr map[string]string) (applied, codeRef) {
	e.t.Helper()
	e.clk.Advance(10 * time.Second)
	ref := c.code(s, admin.ActionNetworkConfig, netCodeParams)
	h := session(s)
	for k, v := range hdr {
		h[k] = v
	}
	r := c.expect("POST", "/api/v2/network/apply", `{`+netFields+`,`+ref.body()+`}`, h, 200, "")
	var p applied
	if err := json.Unmarshal(r.Data, &p); err != nil || !admin.ValidID(p.ChangeID) || p.Intf != "eth0" ||
		p.AppliedAt != e.clk.Now().UTC().Format(time.RFC3339) || p.ConfirmWithin != 120 {
		e.t.Fatalf("apply answer %s", r.Data)
	}
	return p, ref
}

func changeBody(id string) string { return `{"change_id":"` + id + `"}` }

func TestNetworkApplyConfirm(t *testing.T) {
	e := newNetEnv(t)
	p, ref := e.apply(e.c, e.s, map[string]string{headerClientIP: "192.168.1.5"})
	e.trialOnly()
	if got := e.nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}
	ev := e.event(evNetworkPending)
	if ev["change_id"] != p.ChangeID || ev["intf"] != "eth0" || ev["applied_at"] != p.AppliedAt || ev["confirm_within"] != float64(120) {
		t.Fatalf("network_pending %v", ev)
	}
	tm := e.timers.last(t)
	if tm.d != netConfirmWindow {
		t.Fatalf("timer %v", tm.d)
	}

	e.clk.Advance(33 * time.Second)
	r := e.c.expect("GET", "/api/v2/network/state", "", session(e.s), 200, "")
	want := `{"interfaces":[{"ipv4":"127.0.0.1/8","name":"lo","dhcp":false,"is_loopback":true},` +
		`{"ipv4":"192.168.1.20/24","name":"eth0","state":"routable","dhcp":true}],` +
		`"pending":{"change_id":"` + p.ChangeID + `","intf":"eth0","applied_at":"` + p.AppliedAt + `","expires_in":87}}`
	var got, exp any
	_ = json.Unmarshal(r.Data, &got)
	_ = json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("state %s", r.Data)
	}

	// The admin reloaded on the new address and unlocked again.
	other := e.c.verify(adminPw)
	r = e.c.expect("POST", "/api/v2/network/confirm", changeBody(p.ChangeID), session(other), 200, "")
	if string(r.Data) != `{"change_id":"`+p.ChangeID+`","intf":"eth0","state":"confirmed"}` {
		t.Fatalf("confirm %s", r.Data)
	}
	e.noTrial()
	e.etcOnly("calaos_eth0.network")
	if got := e.read(filepath.Join(e.etc, "calaos_eth0.network")); got != netUnit {
		t.Fatalf("confirmed unit:\n%s", got)
	}
	if got := e.nc.take(); !reflect.DeepEqual(got, []string{"reload"}) {
		t.Fatalf("networkctl %v", got)
	}
	ev = e.event(evNetworkApplied)
	if len(ev) != 2 || ev["change_id"] != p.ChangeID || ev["intf"] != "eth0" {
		t.Fatalf("network_applied %v", ev)
	}
	if !tm.stopped {
		t.Fatal("timer still armed")
	}
	e.noRecord()

	e.c.expect("POST", "/api/v2/network/confirm", changeBody(p.ChangeID), session(e.s), 400, codeInvalidRequest)
	e.c.expect("POST", "/api/v2/network/revert", changeBody(p.ChangeID), nil, 400, codeInvalidRequest)
	r = e.c.expect("GET", "/api/v2/network/state", "", nil, 200, "")
	if !strings.HasSuffix(string(r.Data), `"pending":null}`) {
		t.Fatalf("state %s", r.Data)
	}
	for _, al := range e.c.a.admin.PendingAlerts() {
		if al.Type == admin.EventNetworkRolledBack {
			t.Fatalf("rollback alert after a confirmation: %+v", al)
		}
	}

	audit := readAudit(t)
	sid := admin.SessionAuditID(e.s)
	for _, line := range []string{
		`client=192.168.1.5 result=ok session=` + sid + ` intf=eth0 change_id=` + p.ChangeID + ` code_id=` + ref.id + ` POST "/api/v2/network/apply"`,
		`result=ok session=` + admin.SessionAuditID(other) + ` change_id=` + p.ChangeID + ` POST "/api/v2/network/confirm"`,
		`client=local result=invalid_request session=- change_id=` + p.ChangeID + ` POST "/api/v2/network/revert"`,
	} {
		if !strings.Contains(audit, line) {
			t.Fatalf("audit lacks %q:\n%s", line, audit)
		}
	}
	if strings.Contains(audit, ref.code) {
		t.Fatal("the code is in the audit log")
	}
}

func TestNetworkApplyTimeout(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, map[string]string{headerClientIP: "192.168.1.5"})
	e.nc.take()
	e.clk.Advance(netConfirmWindow)
	e.timers.last(t).f()

	e.noTrial()
	e.etcOnly()
	e.noRecord()
	if got := e.nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}
	ev := e.event(admin.EventNetworkRolledBack)
	if len(ev) != 3 || ev["intf"] != "eth0" || ev["id"] == "" || ev["ts"] == "" {
		t.Fatalf("network_rolled_back %v", ev)
	}
	// Persisted: replayed until acked.
	var found bool
	for _, al := range e.c.a.admin.PendingAlerts() {
		found = found || (al.Type == admin.EventNetworkRolledBack && al.Intf == "eth0")
	}
	if !found {
		t.Fatal("alert not persisted")
	}
	want := `client=192.168.1.5 result=ok session=` + admin.SessionAuditID(e.s) +
		` action=network_rollback intf=eth0 change_id=` + p.ChangeID + ` reason=timeout - "-"`
	if audit := readAudit(t); !strings.Contains(audit, want) {
		t.Fatalf("audit lacks %q:\n%s", want, audit)
	}
	e.c.expect("POST", "/api/v2/network/confirm", changeBody(p.ChangeID), session(e.s), 400, codeInvalidRequest)

	// A late timer of a finished change does nothing.
	e.timers.last(t).f()
	e.noEvent(admin.EventNetworkRolledBack)
}

// A confirmation that arrives past the deadline, before the timer ran, is
// too late: the change is rolled back.
func TestNetworkConfirmPastDeadline(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, nil)
	e.clk.Advance(netConfirmWindow)
	e.c.expect("POST", "/api/v2/network/confirm", changeBody(p.ChangeID), session(e.s), 400, codeInvalidRequest)
	e.noTrial()
	e.etcOnly()
	e.event(admin.EventNetworkRolledBack)
}

func TestNetworkRevert(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, nil)
	e.nc.take()
	e.c.expect("POST", "/api/v2/network/revert", `{"change_id":"0123456789abcdef"}`, session(e.s), 400, codeInvalidRequest)
	e.c.expect("POST", "/api/v2/network/revert", `{"change_id":"nothex"}`, session(e.s), 400, codeInvalidRequest)
	r := e.c.expect("POST", "/api/v2/network/revert", changeBody(p.ChangeID), session(e.s), 200, "")
	if string(r.Data) != `{"change_id":"`+p.ChangeID+`","intf":"eth0","state":"reverted"}` {
		t.Fatalf("revert %s", r.Data)
	}
	e.noTrial()
	e.etcOnly()
	e.noRecord()
	if got := e.nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}
	if !e.timers.last(t).stopped {
		t.Fatal("timer still armed")
	}
	e.noEvent(admin.EventNetworkRolledBack)
	want := `session=` + admin.SessionAuditID(e.s) + ` change_id=` + p.ChangeID + ` intf=eth0 reason=revert POST "/api/v2/network/revert"`
	if audit := readAudit(t); !strings.Contains(audit, want) || strings.Contains(audit, "network_rollback") {
		t.Fatalf("audit lacks %q:\n%s", want, audit)
	}
}

func TestNetworkRestartBeforeDeadline(t *testing.T) {
	e := newNetEnv(t)
	r := e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 200, "")
	var p applied
	_ = json.Unmarshal(r.Data, &p)
	e.clk.Advance(50 * time.Second)

	restarted, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	e.useHub(restarted)
	restarted.resumeNetChange()
	if tm := e.timers.last(t); tm.d != 70*time.Second {
		t.Fatalf("re-armed for %v", tm.d)
	}
	ev := e.event(evNetworkPending)
	if ev["change_id"] != p.ChangeID || ev["applied_at"] != p.AppliedAt || ev["confirm_within"] != float64(70) {
		t.Fatalf("network_pending %v, applied %+v", ev, p)
	}
	e.trialOnly()

	c2 := adminClient{t: t, a: restarted}
	c2.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 409, codeNetworkPending)
	c2.expect("POST", "/api/v2/network/confirm", changeBody(p.ChangeID), nil, 200, "")
	e.noTrial()
	e.etcOnly("calaos_eth0.network")
	e.noRecord()
}

func TestNetworkRestartPastDeadline(t *testing.T) {
	e := newNetEnv(t)
	r := e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 200, "")
	var p applied
	_ = json.Unmarshal(r.Data, &p)
	e.nc.take()
	e.clk.Advance(netConfirmWindow + time.Second)

	restarted, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	e.useHub(restarted)
	n := len(e.timers.list)
	restarted.resumeNetChange()
	if len(e.timers.list) != n {
		t.Fatal("timer armed past the deadline")
	}
	e.noTrial()
	e.etcOnly()
	e.noRecord()
	if got := e.nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}
	if ev := e.event(admin.EventNetworkRolledBack); ev["intf"] != "eth0" {
		t.Fatalf("network_rolled_back %v", ev)
	}
	want := `client=local result=ok session=- action=network_rollback intf=eth0 change_id=` + p.ChangeID + ` reason=restart - "-"`
	if audit := readAudit(t); !strings.Contains(audit, want) {
		t.Fatalf("audit lacks %q:\n%s", want, audit)
	}
	// The CLI's own line, written without a session.
	if audit := readAudit(t); !strings.Contains(audit, `client=local result=ok session=- intf=eth0 change_id=`+p.ChangeID+` POST "/api/v2/network/trial"`) {
		t.Fatalf("no trial line:\n%s", audit)
	}
	adminClient{t: t, a: restarted}.expect("POST", "/api/v2/network/confirm", changeBody(p.ChangeID), nil, 400, codeInvalidRequest)
}

func TestNetworkCodeBinding(t *testing.T) {
	e := newNetEnv(t)
	e.clk.Advance(10 * time.Second)
	ref := e.c.code(e.s, admin.ActionNetworkConfig, netCodeParams)
	for _, b := range []string{
		strings.Replace(netFields, `"192.168.1.1","dns`, `"192.168.1.254","dns`, 1),
		strings.Replace(netFields, `"lan"`, `"lan","home"`, 1),
		strings.Replace(netFields, `"eth0"`, `"eth1"`, 1),
	} {
		if b == netFields {
			t.Fatal("test body unchanged")
		}
		if strings.Contains(b, "eth1") {
			getNetInterfaces = func() ([]*structs.NetInterface, error) {
				return []*structs.NetInterface{{Name: "eth0"}, {Name: "eth1"}}, nil
			}
		}
		e.c.expect("POST", "/api/v2/network/apply", `{`+b+`,`+ref.body()+`}`, session(e.s), 403, codeBadCode)
	}
	e.noTrial()
	e.etcOnly()
	if got := e.nc.take(); got != nil {
		t.Fatalf("networkctl ran: %v", got)
	}
	r := e.c.expect("POST", "/api/v2/network/apply", `{`+netFields+`,`+ref.body()+`}`, session(e.s), 200, "")
	if !strings.Contains(string(r.Data), `"confirm_within":120`) {
		t.Fatalf("apply %s", r.Data)
	}
	e.trialOnly()
}

func TestNetworkApplyChecks(t *testing.T) {
	e := newNetEnv(t)
	body := `{` + netFields + `,"code_id":"0123456789abcdef","code":"123456"}`
	// Body first, then the session.
	e.c.expect("POST", "/api/v2/network/apply", `{"intf":"eth0","dhcp":false,"ipv4":"192.168.1.10"}`, nil, 400, codeInvalidRequest)
	e.c.expect("POST", "/api/v2/network/apply", body, nil, 401, codeBadSession)
	e.c.expect("POST", "/api/v2/network/apply", body, session(strings.Repeat("a", 64)), 401, codeBadSession)
	for _, b := range []string{
		`{`,
		`{"dhcp":true}`,
		`{"intf":"eth0"}`,
		`{"intf":"lo","dhcp":true}`,
		`{"intf":"eth9","dhcp":true}`,
		`{"intf":"eth0","dhcp":true,"ipv4":"192.168.1.10/24"}`,
		`{"intf":"eth0","dhcp":false,"ipv4":"192.168.1.010/24"}`,
		`{"intf":"eth0","dhcp":false,"ipv4":"fd00::1/64"}`,
		`{"intf":"eth0","dhcp":false,"ipv4":"192.168.1.10/24","dns_servers":"1.1.1.1"}`,
		`{"intf":"eth0","dhcp":true,"code_id":"x","code":"123456"}`,
		`{"intf":"eth0","dhcp":true,"code":"123456"}`,
	} {
		e.c.expect("POST", "/api/v2/network/apply", b, session(e.s), 400, codeInvalidRequest)
	}
	e.c.expect("POST", "/api/v2/network/apply", `{`+netFields+`}`, session(e.s), 403, codeCodeRequired)
	e.c.expect("POST", "/api/v2/network/apply", body, session(e.s), 410, codeCodeExpired)

	upgradeRunning = func() bool { return true }
	e.c.expect("POST", "/api/v2/network/apply", body, session(e.s), 409, codeUpgradeInProgress)
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 409, codeUpgradeInProgress)
	upgradeRunning = func() bool { return false }

	// The trial is bearer only and takes no code.
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, session(e.s), 403, codeNotAllowed)

	// A failed reconfigure reverts at once.
	e.nc.fail["reconfigure eth0"] = true
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 500, codeInternal)
	e.noTrial()
	e.noRecord()
	if !e.timers.empty() {
		t.Fatal("timer armed for a failed change")
	}
	delete(e.nc.fail, "reconfigure eth0")
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 200, "")
	e.trialOnly()
}

func (ft *fakeTimers) empty() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.list) == 0
}

func TestNetworkPendingRefusals(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, nil)
	e.clk.Advance(20 * time.Second)

	// A second apply: the code is not used and stays valid.
	ref := e.c.code(e.s, admin.ActionNetworkConfig, netCodeParams)
	r := e.c.expect("POST", "/api/v2/network/apply", `{`+netFields+`,`+ref.body()+`}`, session(e.s), 409, codeNetworkPending)
	if string(r.Data) != `{"change_id":"`+p.ChangeID+`","expires_in":100}` {
		t.Fatalf("network_pending data %s", r.Data)
	}
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 409, codeNetworkPending)
	e.c.expect("POST", "/api/v2/network/eth0", `{"dhcp":true}`, nil, 409, codeNetworkPending)

	// The legacy route stays frozen: it writes /etc under the trial.
	resp, body := doCase(t, e.c.a, httpCase{method: "POST", path: "/api/network/eth0", ctype: "application/json", body: `{"dhcp":true}`})
	if resp.StatusCode != 200 || string(body) != `{"error":false,"msg":"ok"}` {
		t.Fatalf("legacy %d %s", resp.StatusCode, body)
	}
	if got := e.read(filepath.Join(e.etc, "calaos_eth0.network")); got != "[Match]\nName=eth0\n\n[Network]\nDHCP=yes\n" {
		t.Fatalf("legacy unit:\n%s", got)
	}
	if got := e.read(filepath.Join(e.run, "00-calaos-trial-eth0.network")); got != netUnit {
		t.Fatal("legacy write touched the trial")
	}

	e.c.expect("POST", "/api/v2/network/revert", changeBody(p.ChangeID), session(e.s), 200, "")
	e.c.expect("POST", "/api/v2/network/apply", `{`+netFields+`,`+ref.body()+`}`, session(e.s), 200, "")
}

func TestNetworkRoutes(t *testing.T) {
	e := newNetEnv(t)
	// apply with a session reaches apply, not the bearer-only network/:intf.
	e.c.expect("POST", "/api/v2/network/apply", `{"intf":"eth0","dhcp":true}`, session(e.s), 403, codeCodeRequired)
	e.c.expect("POST", "/api/v2/network/Apply", `{"intf":"eth0","dhcp":true}`, session(e.s), 403, codeCodeRequired)
	for _, path := range []string{"confirm", "revert"} {
		e.c.expect("POST", "/api/v2/network/"+path, `{"change_id":"0123456789abcdef"}`, session(e.s), 400, codeInvalidRequest)
		e.c.expect("POST", "/api/v2/network/"+path, `{"change_id":"0123456789abcdef"}`, session(strings.Repeat("b", 64)), 401, codeBadSession)
	}
	e.c.expect("GET", "/api/v2/network/state", "", session(strings.Repeat("b", 64)), 401, codeBadSession)
	e.c.expect("GET", "/api/v2/network/list", "", session(e.s), 403, codeNotAllowed)

	// GET-only words fall through to network/:intf, whose validator refuses them.
	for _, w := range []string{"state", "list"} {
		r := e.c.expect("POST", "/api/v2/network/"+w, `{"dhcp":true}`, nil, 400, codeInvalidRequest)
		if r.Msg != `invalid interface: "`+w+`" is not a valid interface name` {
			t.Fatalf("msg %q", r.Msg)
		}
	}
	for _, w := range []string{"apply", "confirm", "revert", "state", "list", "trial"} {
		resp, body := doCase(t, e.c.a, httpCase{method: "POST", path: "/api/network/" + w, ctype: "application/json", body: `{"dhcp":true}`})
		if resp.StatusCode != 400 || string(body) != `{"error":true,"msg":"invalid interface: \"`+w+`\" is not a valid interface name"}` {
			t.Fatalf("legacy %s: %d %s", w, resp.StatusCode, body)
		}
	}
	e.etcOnly()
	e.noTrial()
}

func (e *netEnv) hasAlert() bool {
	for _, al := range e.c.a.admin.PendingAlerts() {
		if al.Type == admin.EventNetworkRolledBack {
			return true
		}
	}
	return false
}

func (e *netEnv) hasRecord() bool {
	_, err := os.Stat(filepath.Join(adminRuntimeDir, netPendingFile))
	return err == nil
}

// blockTrial turns the trial into a non-empty directory: even root cannot
// remove it with unlink.
func (e *netEnv) blockTrial() func() {
	e.t.Helper()
	trial := filepath.Join(e.run, "00-calaos-trial-eth0.network")
	if err := os.Remove(trial); err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(trial, "x"), 0o750); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		if err := os.RemoveAll(trial); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestNetworkApplyReloadFails(t *testing.T) {
	e := newNetEnv(t)
	e.nc.fail["reload"] = true
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 500, codeInternal)
	e.noTrial()
	e.etcOnly()
	e.noRecord()
	if got := e.nc.take(); !reflect.DeepEqual(got, []string{"reload", "reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}
	if !e.timers.empty() || e.hasAlert() {
		t.Fatal("a failed apply left a timer or an alert")
	}
}

func TestNetworkRollbackNetworkctlFails(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, nil)
	e.nc.fail["reconfigure eth0"] = true
	e.clk.Advance(netConfirmWindow)
	e.timers.last(t).f()
	// The trial is gone, so the change is over even if networkd lags.
	e.noTrial()
	e.noRecord()
	if !e.hasAlert() {
		t.Fatal("no rollback alert")
	}
	want := `result=internal session=` + admin.SessionAuditID(e.s) + ` action=network_rollback intf=eth0 change_id=` + p.ChangeID + ` reason=timeout - "-"`
	if audit := readAudit(t); !strings.Contains(audit, want) {
		t.Fatalf("audit lacks %q:\n%s", want, audit)
	}
}

func TestNetworkRollbackTrialKept(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, nil)
	unblock := e.blockTrial()
	e.nc.take()
	e.clk.Advance(netConfirmWindow)
	e.timers.last(t).f()
	if !e.hasRecord() || e.hasAlert() {
		t.Fatal("a rollback that kept the trial cleared the record or raised the alert")
	}
	if got := e.nc.take(); got != nil {
		t.Fatalf("networkctl ran: %v", got)
	}
	if tm := e.timers.last(t); tm.d != netRetryDelay || tm.stopped {
		t.Fatalf("retry timer %+v", tm)
	}
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 409, codeNetworkPending)

	unblock()
	e.timers.last(t).f()
	e.noRecord()
	if !e.hasAlert() {
		t.Fatal("no alert once the trial is gone")
	}
	want := `action=network_rollback intf=eth0 change_id=` + p.ChangeID + ` reason=timeout`
	if audit := readAudit(t); strings.Count(audit, want) != 1 {
		t.Fatalf("audit has not one %q:\n%s", want, audit)
	}
}

func TestNetworkRevertTrialKept(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, nil)
	unblock := e.blockTrial()
	e.c.expect("POST", "/api/v2/network/revert", changeBody(p.ChangeID), session(e.s), 500, codeInternal)
	if !e.hasRecord() || e.timers.last(t).stopped {
		t.Fatal("a revert that kept the trial ended the change")
	}
	unblock()
	e.c.expect("POST", "/api/v2/network/revert", changeBody(p.ChangeID), session(e.s), 200, "")
	e.noRecord()
	if e.hasAlert() {
		t.Fatal("explicit revert raised the alert")
	}
}

func TestNetworkConfirmWriteFails(t *testing.T) {
	e := newNetEnv(t)
	p, _ := e.apply(e.c, e.s, nil)
	// A directory in the way makes the rename into /etc fail, even as root.
	if err := os.MkdirAll(filepath.Join(e.etc, "calaos_eth0.network", "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	e.c.expect("POST", "/api/v2/network/confirm", changeBody(p.ChangeID), session(e.s), 500, codeInternal)
	if !e.hasRecord() || e.timers.last(t).stopped {
		t.Fatal("a failed confirm ended the change")
	}
	if got := e.files(e.run); !reflect.DeepEqual(got, []string{"00-calaos-trial-eth0.network"}) {
		t.Fatalf("/run holds %v", got)
	}
	e.noEvent(evNetworkApplied)
	e.clk.Advance(netConfirmWindow)
	e.timers.last(t).f()
	e.noTrial()
	e.noRecord()
	if !e.hasAlert() {
		t.Fatal("no rollback alert")
	}
}

// A trial whose record is unreadable or a symlink has no deadline left: the
// restarted daemon reverts it.
func TestNetworkRestartWithoutValidRecord(t *testing.T) {
	for _, tc := range []struct {
		name        string
		breakRecord func(path string) error
	}{
		{"corrupt", func(path string) error { return os.WriteFile(path, []byte(`{"change_id":"zz"`), 0o600) }},
		{"symlink", func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink("/etc/hostname", path)
		}},
		{"missing", os.Remove},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newNetEnv(t)
			e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 200, "")
			e.nc.take()
			if err := tc.breakRecord(filepath.Join(adminRuntimeDir, netPendingFile)); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewApp()
			if err != nil {
				t.Fatal(err)
			}
			e.useHub(restarted)
			restarted.resumeNetChange()
			e.noRecord()
			e.noTrial()
			e.etcOnly()
			if got := e.nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
				t.Fatalf("networkctl %v", got)
			}
			if ev := e.event(admin.EventNetworkRolledBack); ev["intf"] != "eth0" {
				t.Fatalf("network_rolled_back %v", ev)
			}
			want := `client=- result=ok session=- action=network_rollback intf=eth0 change_id=- reason=restart - "-"`
			if audit := readAudit(t); !strings.Contains(audit, want) {
				t.Fatalf("audit lacks %q:\n%s", want, audit)
			}
		})
	}
}

// Stepping the box time back between restarts does not extend the window:
// the boot clock deadline wins.
func TestNetworkRestartClockSteppedBack(t *testing.T) {
	e := newNetEnv(t)
	e.c.expect("POST", "/api/v2/network/trial", `{`+netFields+`}`, nil, 200, "")
	restart := func() {
		e.clk.Advance(100 * time.Second)
		e.advanceBoot(100 * time.Second)
		e.clk.Advance(-100 * time.Second)
		restarted, err := NewApp()
		if err != nil {
			t.Fatal(err)
		}
		e.useHub(restarted)
		restarted.resumeNetChange()
	}
	restart()
	if tm := e.timers.last(t); tm.d != 20*time.Second {
		t.Fatalf("re-armed for %v", tm.d)
	}
	n := len(e.timers.list)
	restart()
	if len(e.timers.list) != n {
		t.Fatal("re-armed past the boot deadline")
	}
	e.noTrial()
	e.noRecord()
	e.event(admin.EventNetworkRolledBack)
}
