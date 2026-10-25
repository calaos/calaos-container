package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	logger "github.com/calaos/calaos-container/log"
)

// The fake keeps the zone and the NTP switch in files, as timedated would.
const fakeTimedatectl = `#!/bin/sh
D="$FAKE_TD_DIR"
echo "$*" >> "$D/calls"
if [ "$1" = "$FAKE_TD_FAIL" ]; then echo "Failed to $1: boom" >&2; exit 1; fi
case "$1" in
show)
	printf 'Timezone=%s\nLocalRTC=no\nCanNTP=yes\nNTP=%s\nNTPSynchronized=%s\nTimeUSec=Sun 2026-10-25 09:12:03 UTC\nRTCTimeUSec=%s\n' \
		"$(cat "$D/tz")" "$(cat "$D/ntp")" "$(cat "$D/sync")" "$(cat "$D/rtc")" ;;
list-timezones) printf 'Africa/Abidjan\nEurope/Paris\nUTC\n' ;;
set-timezone) echo "$2" > "$D/tz" ;;
set-ntp) if [ "$2" = true ]; then echo yes > "$D/ntp"; else echo no > "$D/ntp"; fi ;;
set-time) ;;
*) exit 2 ;;
esac
`

type fakeTimedate struct {
	t   *testing.T
	dir string
}

func useFakeTimedatectl(t *testing.T) *fakeTimedate {
	t.Helper()
	bin := t.TempDir()
	// #nosec G306 -- the fake must be executable.
	if err := os.WriteFile(filepath.Join(bin, "timedatectl"), []byte(fakeTimedatectl), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeTimedate{t: t, dir: t.TempDir()}
	f.set("tz", "UTC")
	f.set("ntp", "yes")
	f.set("sync", "yes")
	f.set("rtc", "Sun 2026-10-25 09:12:01 UTC")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TD_DIR", f.dir)
	t.Setenv("FAKE_TD_FAIL", "")
	resetTimezonesCache()
	resetBoxTimeCache()
	t.Cleanup(func() { resetTimezonesCache(); resetBoxTimeCache() })
	return f
}

func (f *fakeTimedate) set(name, v string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(v+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// calls lists the commands that change something.
func (f *fakeTimedate) calls() []string {
	f.t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(l, "set-") {
			out = append(out, l)
		}
	}
	return out
}

func (f *fakeTimedate) count(cmd string) int {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
	return strings.Count(string(b), cmd+"\n")
}

func TestSystemTimeGet(t *testing.T) {
	clk := useAdminClock(t)
	f := useFakeTimedatectl(t)
	f.set("sync", "no")
	c := newAdminApp(t)

	r := c.expect("GET", "/api/v2/system/time", "", nil, 200, "")
	var bt map[string]any
	_ = json.Unmarshal(r.Data, &bt)
	want := map[string]any{"now": clk.Now().UTC().Format("2006-01-02T15:04:05Z07:00"), "timezone": "UTC", "ntp": true,
		"synchronized": false, "rtc": "2026-10-25T09:12:01Z"}
	if len(bt) != len(want) {
		t.Fatalf("%s", r.Data)
	}
	for k, v := range want {
		if bt[k] != v {
			t.Fatalf("%s: %v, want %v (%s)", k, bt[k], v, r.Data)
		}
	}

	// Cached for 1 s: a poll does not run timedatectl each time.
	f.set("rtc", "")
	c.expect("GET", "/api/v2/system/time", "", nil, 200, "")
	if n := f.count("show"); n != 1 {
		t.Fatalf("%d show calls", n)
	}
	resetBoxTimeCache()
	if r = c.expect("GET", "/api/v2/system/time", "", nil, 200, ""); !strings.Contains(string(r.Data), `"rtc":null`) {
		t.Fatalf("%s", r.Data)
	}

	r = c.expect("GET", "/api/v2/system/timezones", "", nil, 200, "")
	c.expect("GET", "/api/v2/system/timezones", "", nil, 200, "")
	if string(r.Data) != `{"timezones":["Africa/Abidjan","Europe/Paris","UTC"]}` || f.count("list-timezones") != 1 {
		t.Fatalf("%s, %d calls", r.Data, f.count("list-timezones"))
	}

	t.Setenv("FAKE_TD_FAIL", "show")
	resetBoxTimeCache()
	c.expect("GET", "/api/v2/system/time", "", nil, 500, codeInternal)
}

func TestSystemTimeSet(t *testing.T) {
	f := useFakeTimedatectl(t)
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	for body, msg := range map[string]string{
		`{}`:                                          "no field to set",
		`{"tz":"UTC"}`:                                "no field to set",
		`{"timezone":"Mars/Olympus"}`:                 "unknown timezone",
		`{"timezone":"-h"}`:                           "unknown timezone",
		`{"timezone":"../etc/passwd x"}`:              "unknown timezone",
		`{"time":"2026-10-25T09:12:03Z"}`:             "ntp is active",
		`{"ntp":true,"time":"2026-10-25T09:12:03Z"}`:  "ntp is active",
		`{"ntp":false,"time":"2019-12-31T23:00:00Z"}`: "invalid time: year 2020 to 2100",
		`{"ntp":false,"time":"2026-10-25 09:12:03"}`:  "invalid time: RFC 3339 time expected",
		`{"ntp":"no"}`:                                "invalid JSON body",
	} {
		r := c.expect("POST", "/api/v2/system/time", body, session(s), 400, codeInvalidRequest)
		if r.Msg != msg {
			t.Errorf("%s: %q, want %q", body, r.Msg, msg)
		}
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("a refused request changed the box: %q", calls)
	}

	r := c.expect("POST", "/api/v2/system/time", `{"timezone":"Europe/Paris","ntp":false,"time":"2026-10-25T11:12:03+02:00"}`, session(s), 200, "")
	var d map[string]any
	_ = json.Unmarshal(r.Data, &d)
	if d["timezone"] != "Europe/Paris" || d["ntp"] != false || d["restart_required"] != true {
		t.Fatalf("%s", r.Data)
	}
	want := []string{"set-timezone Europe/Paris", "set-ntp false", "set-time 2026-10-25 09:12:03 UTC"}
	if got := f.calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %q", got)
	}

	// Same zone: nothing to restart. NTP already off: time alone is fine.
	r = c.expect("POST", "/api/v2/system/time", `{"timezone":"Europe/Paris","time":"2030-01-01T00:00:00Z"}`, session(s), 200, "")
	if !strings.Contains(string(r.Data), `"restart_required":false`) {
		t.Fatalf("%s", r.Data)
	}

	audit := readAudit(t)
	if !strings.Contains(audit, ` tz=Europe/Paris ntp=false time=set POST "/api/v2/system/time"`) ||
		strings.Contains(audit, "2026-10-25 09:12:03") || strings.Contains(audit, "2030-01-01") {
		t.Fatalf("audit:\n%s", audit)
	}
}

func TestSystemTimeSetPartialFailure(t *testing.T) {
	useFakeTimedatectl(t)
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	t.Setenv("FAKE_TD_FAIL", "set-ntp")
	r := c.expect("POST", "/api/v2/system/time", `{"timezone":"Africa/Abidjan","ntp":false}`, session(s), 500, codeInternal)
	if string(r.Data) != `{"applied":["timezone"],"failed":"ntp"}` || strings.Contains(r.Msg, "boom") {
		t.Fatalf("%s %s", r.Msg, r.Data)
	}
	// The applied step stays: the GET shows the real state.
	t.Setenv("FAKE_TD_FAIL", "")
	if r = c.expect("GET", "/api/v2/system/time", "", nil, 200, ""); !strings.Contains(string(r.Data), `"timezone":"Africa/Abidjan","ntp":true`) {
		t.Fatalf("%s", r.Data)
	}

	t.Setenv("FAKE_TD_FAIL", "set-timezone")
	r = c.expect("POST", "/api/v2/system/time", `{"timezone":"Europe/Paris"}`, nil, 500, codeInternal)
	if string(r.Data) != `{"applied":[],"failed":"timezone"}` {
		t.Fatalf("%s", r.Data)
	}

	audit := readAudit(t)
	if !strings.Contains(audit, `result=internal session=`+admin.SessionAuditID(s)+` tz=Africa/Abidjan POST "/api/v2/system/time"`) || strings.Contains(audit, "ntp=") {
		t.Fatalf("audit:\n%s", audit)
	}
}

// The webapp polls these: a line each would flood audit.log. Changes and
// refused sessions are still recorded.
func TestPolledReadsNotAudited(t *testing.T) {
	var logs bytes.Buffer
	logger.Logger.SetOutput(&logs)
	t.Cleanup(func() { logger.Logger.SetOutput(os.Stdout) })
	useFakeTimedatectl(t)
	f := useFakeJournalctl(t)
	f.write()
	clk := useAdminClock(t)
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)
	before := readAudit(t)

	for _, r := range [][2]string{
		{"GET", "/api/v2/logs/sources"}, {"GET", "/api/v2/system/services"},
		{"GET", "/api/v2/system/time"}, {"GET", "/api/v2/system/timezones"}, {"GET", "/api/v2/network/state"},
		{"GET", "/api/v2/System/Time/"},
	} {
		c.do(r[0], r[1], "", session(s))
	}
	// Log follow: forward from a cursor.
	c.expect("POST", "/api/v2/logs/query", `{"direction":"forward","cursor":"s=1"}`, session(s), 200, "")
	if after := readAudit(t); after != before {
		t.Fatalf("polled reads audited:\n%s", strings.TrimPrefix(after, before))
	}

	c.expect("POST", "/api/v2/logs/query", "", session(strings.Repeat("0", 64)), 401, codeBadSession)
	if !strings.Contains(logs.String(), `refused admin call: `) || !strings.Contains(logs.String(), `result=bad_session session=`) {
		t.Fatalf("refused call not in the daemon log:\n%s", logs.String())
	}
	c.expect("POST", "/api/v2/system/time", `{"ntp":true}`, session(s), 200, "")
	c.expect("GET", "/api/v2/update/status", "", session(s), 200, "")
	// A search or a page of history is recorded, never the text.
	clk.Advance(time.Second)
	c.expect("POST", "/api/v2/logs/query", `{"sources":["ssh","kernel"],"text":"wago","lines":50}`, session(s), 200, "")
	c.expect("POST", "/api/v2/logs/query", `{"direction":"forward","since":"2026-10-25T00:00:00Z"}`, session(s), 200, "")
	added := strings.TrimPrefix(readAudit(t), before)
	sid := admin.SessionAuditID(s)
	for _, want := range []string{
		`ntp=true POST "/api/v2/system/time"`,
		`GET "/api/v2/update/status"`,
		`result=ok session=` + sid + ` sources=ssh+kernel lines=50 dir=backward text=yes POST "/api/v2/logs/query"`,
		`result=ok session=` + sid + ` sources=system lines=200 dir=forward text=no POST "/api/v2/logs/query"`,
	} {
		if !strings.Contains(added, want) {
			t.Fatalf("audit lacks %q:\n%s", want, added)
		}
	}
	if strings.Count(added, "\n") != 4 || strings.Contains(added, "wago") {
		t.Fatalf("audit:\n%s", added)
	}
}
