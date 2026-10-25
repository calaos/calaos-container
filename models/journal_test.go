package models

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func src(t *testing.T, ids ...string) []LogSource {
	t.Helper()
	var out []LogSource
	for _, id := range ids {
		s, ok := LookupLogSource(id)
		if !ok {
			t.Fatalf("no source %q", id)
		}
		out = append(out, s)
	}
	return out
}

func TestLogSourcesUseServiceUnits(t *testing.T) {
	for _, s := range LogSources {
		if s.Unit == "" {
			continue
		}
		svc, ok := LookupService(s.ID)
		if !ok || svc.Unit != s.Unit {
			t.Errorf("source %s: unit %q, services table %q", s.ID, s.Unit, svc.Unit)
		}
	}
	if n := len(LogSources); n != 17 {
		t.Fatalf("%d sources", n)
	}
}

func TestJournalArgs(t *testing.T) {
	since := time.Date(2026, 10, 25, 8, 0, 0, 123000000, time.FixedZone("x", 3600))
	until := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	p := 4
	got := JournalArgs(JournalQuery{
		Sources:  src(t, "calaos-server", "ssh", "kernel"),
		Priority: &p,
		Since:    &since,
		Until:    &until,
		Cursor:   "-s=abc;i=1",
	})
	want := []string{"-o", "json", "--no-pager", "-q",
		"--output-fields=MESSAGE,PRIORITY,_SYSTEMD_UNIT,SYSLOG_IDENTIFIER,CONTAINER_NAME,_PID,_TRANSPORT,__CURSOR,__REALTIME_TIMESTAMP",
		"-r", "--after-cursor=-s=abc;i=1",
		"--since=2026-10-25 07:00:00.123000 UTC", "--until=2026-10-25 09:00:00.000000 UTC",
		"--priority=4",
		"_SYSTEMD_UNIT=calaos-server.service", "+", "CONTAINER_NAME=calaos-server", "+",
		"_SYSTEMD_UNIT=ssh.service", "+", "_TRANSPORT=kernel"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}

	got = JournalArgs(JournalQuery{Sources: src(t, "mosquitto", "system"), Forward: true, Cursor: "c"})
	want = []string{"-o", "json", "--no-pager", "-q", want[4], "--after-cursor=c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system args\n got %q\nwant %q", got, want)
	}
}

func TestParseJournalLine(t *testing.T) {
	r, err := ParseJournalLine([]byte(`{"__CURSOR":"s=1","__REALTIME_TIMESTAMP":"1793000472123456","PRIORITY":"3","_SYSTEMD_UNIT":"calaos-server.service","CONTAINER_NAME":"calaos-server","_PID":"812","MESSAGE":"login cn_pass=abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Cursor != "s=1" || r.Priority != 3 || r.PID == nil || *r.PID != 812 || r.Message != "login cn_pass=***" ||
		!r.Realtime.Equal(time.UnixMicro(1793000472123456)) || r.Unit != "calaos-server.service" || r.Kernel {
		t.Fatalf("%+v", r)
	}

	r, err = ParseJournalLine([]byte(`{"__CURSOR":"s=2","_TRANSPORT":"kernel","MESSAGE":[104,105,255,10]}`))
	if err != nil || !r.Kernel || r.Priority != 6 || r.PID != nil || r.Message != "hi�\n" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = ParseJournalLine([]byte(`{"__CURSOR":"s=3","MESSAGE":null}`))
	if err != nil || !r.TooLong || r.Message != "" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = ParseJournalLine([]byte(`{"__CURSOR":"s=4","MESSAGE":["token=a","b"]}`))
	if err != nil || r.Message != "token=***" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = ParseJournalLine([]byte(`{"MESSAGE":"x"}`)); err == nil {
		t.Fatal("entry without cursor accepted")
	}
	if _, err = ParseJournalLine([]byte(`{"__CURSOR":"s=5","MESS`)); err == nil {
		t.Fatal("partial line accepted")
	}
}

func TestAttribute(t *testing.T) {
	q := JournalQuery{Sources: src(t, "system", "mosquitto", "kernel")}
	for _, tc := range []struct {
		rec  JournalRecord
		want string
	}{
		{JournalRecord{Unit: "mosquitto.service"}, "mosquitto"},
		{JournalRecord{Unit: "libpod-x.scope", Container: "mosquitto"}, "mosquitto"},
		{JournalRecord{Kernel: true}, "kernel"},
		{JournalRecord{Unit: "cron.service"}, "system"},
	} {
		if got := q.Attribute(tc.rec); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.rec, got, tc.want)
		}
	}
}

func TestCutMessage(t *testing.T) {
	if s, cut := CutMessage("short"); s != "short" || cut {
		t.Fatal(s)
	}
	long := strings.Repeat("a", LogMessageMax-1) + "é" + "tail"
	s, cut := CutMessage(long)
	if !cut || len(s) != LogMessageMax-1 {
		t.Fatalf("cut %v len %d", cut, len(s))
	}
}

func TestParseTimedateShow(t *testing.T) {
	st, err := ParseTimedateShow("Timezone=UTC\nLocalRTC=no\nCanNTP=yes\nNTP=yes\nNTPSynchronized=no\nTimeUSec=Sun 2026-10-25 09:12:03 UTC\nRTCTimeUSec=Sun 2026-10-25 09:12:01 UTC\n")
	if err != nil || st.Timezone != "UTC" || !st.NTP || st.Synchronized || st.RTC == nil || *st.RTC != "2026-10-25T09:12:01Z" {
		t.Fatalf("%+v %v", st, err)
	}
	st, err = ParseTimedateShow("Timezone=Europe/Paris\nNTP=no\nNTPSynchronized=yes\nRTCTimeUSec=\n")
	if err != nil || st.NTP || !st.Synchronized || st.RTC != nil {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := time.LoadLocation("Europe/Paris"); err == nil {
		st, _ = ParseTimedateShow("Timezone=Europe/Paris\nRTCTimeUSec=Sun 2026-10-25 10:12:03 CET\n")
		if st.RTC == nil || *st.RTC != "2026-10-25T09:12:03Z" {
			t.Fatalf("paris rtc %v", st.RTC)
		}
	}
	if _, err := ParseTimedateShow("garbage"); err == nil {
		t.Fatal("no Timezone accepted")
	}
}
