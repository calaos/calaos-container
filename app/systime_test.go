package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/calaos/calaos-container/app/admin"
	logger "github.com/calaos/calaos-container/log"
	"github.com/calaos/calaos-container/sysbus"
	"github.com/calaos/calaos-container/sysbus/sysbustest"
)

var fakeRTC = uint64(time.Date(2026, 10, 25, 9, 12, 1, 0, time.UTC).UnixMicro())

func useFakeTimedate(t *testing.T) *sysbustest.Bus {
	t.Helper()
	f := useFakeBus(t)
	f.SetTimedate(sysbus.TimedateState{Timezone: "UTC", NTP: true, NTPSynchronized: true, RTCTimeUSec: fakeRTC})
	f.SetTimezones("Africa/Abidjan", "Europe/Paris", "UTC")
	resetTimezonesCache()
	resetBoxTimeCache()
	t.Cleanup(func() { resetTimezonesCache(); resetBoxTimeCache() })
	return f
}

func TestSystemTimeGet(t *testing.T) {
	clk := useAdminClock(t)
	f := useFakeTimedate(t)
	f.SetTimedate(sysbus.TimedateState{Timezone: "UTC", NTP: true, RTCTimeUSec: fakeRTC})
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

	// Cached for 1 s: a poll does not reach timedated each time.
	f.SetTimedate(sysbus.TimedateState{Timezone: "UTC", NTP: true})
	c.expect("GET", "/api/v2/system/time", "", nil, 200, "")
	if n := f.Reads("Timedate"); n != 1 {
		t.Fatalf("%d reads", n)
	}
	resetBoxTimeCache()
	if r = c.expect("GET", "/api/v2/system/time", "", nil, 200, ""); !strings.Contains(string(r.Data), `"rtc":null`) {
		t.Fatalf("%s", r.Data)
	}

	r = c.expect("GET", "/api/v2/system/timezones", "", nil, 200, "")
	c.expect("GET", "/api/v2/system/timezones", "", nil, 200, "")
	if string(r.Data) != `{"timezones":["Africa/Abidjan","Europe/Paris","UTC"]}` || f.Reads("ListTimezones") != 1 {
		t.Fatalf("%s, %d reads", r.Data, f.Reads("ListTimezones"))
	}

	f.Fail("Timedate", errors.New(secretDetail))
	resetBoxTimeCache()
	if r = c.expect("GET", "/api/v2/system/time", "", nil, 500, codeInternal); strings.Contains(r.Msg, "SECRET") {
		t.Fatalf("%s", r.Msg)
	}
}

func TestSystemTimeSet(t *testing.T) {
	f := useFakeTimedate(t)
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
	if calls := f.Calls(); len(calls) != 0 {
		t.Fatalf("a refused request changed the box: %q", calls)
	}

	r := c.expect("POST", "/api/v2/system/time", `{"timezone":"Europe/Paris","ntp":false,"time":"2026-10-25T11:12:03+02:00"}`, session(s), 200, "")
	var d map[string]any
	_ = json.Unmarshal(r.Data, &d)
	if d["timezone"] != "Europe/Paris" || d["ntp"] != false || d["restart_required"] != true {
		t.Fatalf("%s", r.Data)
	}
	usec := time.Date(2026, 10, 25, 9, 12, 3, 0, time.UTC).UnixMicro()
	want := []string{"SetTimezone Europe/Paris", "SetNTP false", fmt.Sprintf("SetTime %d", usec)}
	if got := f.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
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
	f := useFakeTimedate(t)
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	f.Fail("SetNTP", errors.New("boom"))
	r := c.expect("POST", "/api/v2/system/time", `{"timezone":"Africa/Abidjan","ntp":false}`, session(s), 500, codeInternal)
	if string(r.Data) != `{"applied":["timezone"],"failed":"ntp"}` || strings.Contains(r.Msg, "boom") {
		t.Fatalf("%s %s", r.Msg, r.Data)
	}
	// The applied step stays: the GET shows the real state.
	f.Fail("SetNTP", nil)
	if r = c.expect("GET", "/api/v2/system/time", "", nil, 200, ""); !strings.Contains(string(r.Data), `"timezone":"Africa/Abidjan","ntp":true`) {
		t.Fatalf("%s", r.Data)
	}

	f.Fail("SetTimezone", errors.New("boom"))
	r = c.expect("POST", "/api/v2/system/time", `{"timezone":"Europe/Paris"}`, nil, 500, codeInternal)
	if string(r.Data) != `{"applied":[],"failed":"timezone"}` {
		t.Fatalf("%s", r.Data)
	}

	audit := readAudit(t)
	if !strings.Contains(audit, `result=internal session=`+admin.SessionAuditID(s)+` tz=Africa/Abidjan POST "/api/v2/system/time"`) || strings.Contains(audit, "ntp=") {
		t.Fatalf("audit:\n%s", audit)
	}
}

// A failed step stops the sequence: the time is never set on a clock whose
// NTP daemon could not be stopped, and neither follows a failed timezone.
func TestSystemTimeSetStopsAtFirstError(t *testing.T) {
	const body = `{"timezone":"Europe/Paris","ntp":false,"time":"2026-10-25T09:12:03Z"}`
	for _, tc := range []struct {
		fail, data string
		want       []string
	}{
		{"SetTimezone", `{"applied":[],"failed":"timezone"}`, []string{"SetTimezone Europe/Paris"}},
		{"SetNTP", `{"applied":["timezone"],"failed":"ntp"}`, []string{"SetTimezone Europe/Paris", "SetNTP false"}},
	} {
		t.Run(tc.fail, func(t *testing.T) {
			f := useFakeTimedate(t)
			c := newAdminApp(t)
			c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
			s := c.verify(adminPw)

			f.Fail(tc.fail, errors.New(secretDetail))
			r := c.expect("POST", "/api/v2/system/time", body, session(s), 500, codeInternal)
			if string(r.Data) != tc.data || strings.Contains(r.Msg, "SECRET") {
				t.Fatalf("%s %s", r.Msg, r.Data)
			}
			if got := f.Calls(); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("calls %q", got)
			}
		})
	}
}

// The webapp polls these: a line each would flood audit.log. Changes and
// refused sessions are still recorded.
func TestPolledReadsNotAudited(t *testing.T) {
	var logs bytes.Buffer
	logger.Logger.SetOutput(&logs)
	t.Cleanup(func() { logger.Logger.SetOutput(os.Stdout) })
	useFakeTimedate(t)
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
