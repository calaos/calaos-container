package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/calaos/calaos-container/models"
)

// The fake prints FAKE_JOURNAL_OUT as is: tests write it in the order
// journalctl would (newest first with -r).
const fakeJournalctl = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_JOURNAL_ARGS"
case " $* " in
*" --since="*" --after-cursor="*|*" --after-cursor="*" --since="*|*" --since="*" --cursor="*|*" --cursor="*" --since="*)
	echo "Please specify only one of --since=, --cursor=, and --after-cursor=." >&2
	exit 1 ;;
esac
if [ "$FAKE_JOURNAL_MODE" = hang ]; then
	sleep 30 &
	echo $! > "$FAKE_JOURNAL_PIDS"
	echo $$ >> "$FAKE_JOURNAL_PIDS"
fi
[ -f "$FAKE_JOURNAL_OUT" ] && cat "$FAKE_JOURNAL_OUT"
case "$FAKE_JOURNAL_MODE" in
hang) wait ;;
fail)
	echo "Failed to seek to cursor: Invalid argument" >&2
	exit 1 ;;
esac
exit 0
`

type fakeJournal struct {
	t               *testing.T
	args, out, pids string
}

func useFakeJournalctl(t *testing.T) *fakeJournal {
	t.Helper()
	dir := t.TempDir()
	// #nosec G306 -- the fake must be executable.
	if err := os.WriteFile(filepath.Join(dir, "journalctl"), []byte(fakeJournalctl), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeJournal{t: t, args: filepath.Join(dir, "args"), out: filepath.Join(dir, "out"), pids: filepath.Join(dir, "pids")}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_JOURNAL_ARGS", f.args)
	t.Setenv("FAKE_JOURNAL_OUT", f.out)
	t.Setenv("FAKE_JOURNAL_PIDS", f.pids)
	t.Setenv("FAKE_JOURNAL_MODE", "")
	return f
}

func (f *fakeJournal) mode(m string) { f.t.Setenv("FAKE_JOURNAL_MODE", m) }

func (f *fakeJournal) write(lines ...string) {
	f.t.Helper()
	if err := os.WriteFile(f.out, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeJournal) argv() []string {
	f.t.Helper()
	b, err := os.ReadFile(f.args)
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// assertKilled checks the fake and its background child are gone.
func (f *fakeJournal) assertKilled() {
	f.t.Helper()
	b, err := os.ReadFile(f.pids)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, p := range strings.Fields(string(b)) {
		pid, _ := strconv.Atoi(p)
		deadline := time.Now().Add(3 * time.Second)
		for alive(pid) {
			if time.Now().After(deadline) {
				f.t.Fatalf("pid %d survived the query", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// alive treats a zombie as dead: the test container may not reap orphans.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	_, rest, _ := strings.Cut(string(b), ") ")
	return !strings.HasPrefix(rest, "Z")
}

// jbase is 2026-10-26T07:41:12Z; entry i is i ms later.
const jbase = 1793000472000000

func jentry(i int, unit, msg string) string {
	b, _ := json.Marshal(map[string]any{
		"__CURSOR":             fmt.Sprintf("s=%d", i),
		"__REALTIME_TIMESTAMP": strconv.FormatInt(jbase+int64(i)*1000, 10),
		"PRIORITY":             "4",
		"_SYSTEMD_UNIT":        unit,
		"_PID":                 "812",
		"MESSAGE":              msg,
	})
	return string(b)
}

type logsData struct {
	Entries    []logEntry `json:"entries"`
	Truncated  bool       `json:"truncated"`
	Complete   bool       `json:"complete"`
	NextCursor *string    `json:"next_cursor"`
}

func logsQuery(t *testing.T, a *AppServer, body string, headers map[string]string) (int, v2Reply, logsData) {
	t.Helper()
	status, r, raw := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/logs/query", body: body, ctype: "application/json", headers: headers})
	var d logsData
	if status == 200 {
		if err := json.Unmarshal(r.Data, &d); err != nil {
			t.Fatalf("data %s", raw)
		}
	}
	return status, r, d
}

func cursors(es []logEntry) string {
	var c []string
	for _, e := range es {
		c = append(c, e.Cursor)
	}
	return strings.Join(c, ",")
}

// useLogClock puts the daemon clock an hour after the fake entries, so the
// 7-day default window holds them whatever the date of the run.
func useLogClock(t *testing.T) *testClock {
	clk := useAdminClock(t)
	clk.t = time.UnixMicro(jbase).Add(time.Hour)
	return clk
}

func useScanTime(t *testing.T, d time.Duration) {
	prev := logsScanTime
	logsScanTime = d
	t.Cleanup(func() { logsScanTime = prev })
}

func TestLogSources(t *testing.T) {
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	status, r, body := v2Do2(t, a, httpCase{method: "GET", path: "/api/v2/logs/sources"})
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	var d struct{ Sources []logSourceInfo }
	_ = json.Unmarshal(r.Data, &d)
	if len(d.Sources) != len(models.LogSources) || d.Sources[0] != (logSourceInfo{"calaos-server", "Calaos server"}) ||
		d.Sources[15] != (logSourceInfo{"system", "Whole system"}) || d.Sources[11] != (logSourceInfo{"ntp", "Time sync (NTP)"}) {
		t.Fatalf("sources %s", r.Data)
	}
}

func TestLogsQueryDefaults(t *testing.T) {
	f := useFakeJournalctl(t)
	f.write()
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	before := time.Now()
	status, _, d := logsQuery(t, a, "", nil)
	if status != 200 || !d.Complete || d.Truncated || d.NextCursor != nil || d.Entries == nil || len(d.Entries) != 0 {
		t.Fatalf("%d %+v", status, d)
	}
	argv := f.argv()
	if len(argv) != 7 || argv[5] != "-r" || !strings.HasPrefix(argv[6], "--since=") {
		t.Fatalf("argv %q", argv)
	}
	since, err := time.Parse("2006-01-02 15:04:05.000000 MST", strings.TrimPrefix(argv[6], "--since="))
	if err != nil || since.Before(before.Add(-logsDefaultSpan-time.Second)) || since.After(time.Now().Add(-logsDefaultSpan)) {
		t.Fatalf("since %q %v", argv[6], err)
	}

	until := "2026-01-10T00:00:00Z"
	logsQuery(t, a, `{"until":"`+until+`","sources":["ssh"]}`, nil)
	argv = f.argv()
	if len(argv) != 9 || argv[6] != "--since=2026-01-03 00:00:00.000000 UTC" || argv[7] != "--until=2026-01-10 00:00:00.000000 UTC" ||
		argv[8] != "_SYSTEMD_UNIT=ssh.service" {
		t.Fatalf("argv %q", argv)
	}
}

// The ntp source follows the installed client; the resolution is cached.
func TestLogsQueryNTPUnit(t *testing.T) {
	clk := useLogClock(t)
	j := useFakeJournalctl(t)
	j.write()
	f := useFakeSystemctl(t)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	for _, tc := range []struct{ show, unit string }{
		{"not-found\n\nloaded\n", "systemd-timesyncd.service"},
		{"loaded\n\nnot-found\n", "chrony.service"},
	} {
		resetServicesCache()
		if err := os.WriteFile(f.show, []byte(tc.show), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			clk.Advance(time.Second)
			if status, _, _ := logsQuery(t, a, `{"sources":["ntp","ssh"]}`, nil); status != 200 {
				t.Fatalf("%d", status)
			}
			argv := j.argv()
			if m := strings.Join(argv[len(argv)-3:], " "); m != "_SYSTEMD_UNIT="+tc.unit+" + _SYSTEMD_UNIT=ssh.service" {
				t.Fatalf("matches %q", m)
			}
		}
	}
	if c := f.calls(t); len(c) != 2 {
		t.Fatalf("calls %q", c)
	}

	// systemctl failing leaves the first candidate.
	resetServicesCache()
	t.Setenv("FAKE_SYSTEMCTL_RC", "1")
	clk.Advance(time.Second)
	logsQuery(t, a, `{"sources":["ntp"]}`, nil)
	if argv := j.argv(); argv[len(argv)-1] != "_SYSTEMD_UNIT=chrony.service" {
		t.Fatalf("argv %q", argv)
	}
}

func TestLogsQueryBackward(t *testing.T) {
	useLogClock(t)
	f := useFakeJournalctl(t)
	f.write(jentry(5, "calaos-server.service", "five"), jentry(4, "calaos-server.service", "four"),
		jentry(3, "calaos-server.service", "three"), jentry(2, "calaos-server.service", "two"))
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	status, _, d := logsQuery(t, a, `{"sources":["calaos-server"],"lines":3,"priority":"warning","cursor":"s=6"}`, nil)
	if status != 200 || cursors(d.Entries) != "s=3,s=4,s=5" || !d.Complete || d.NextCursor == nil || *d.NextCursor != "s=3" {
		t.Fatalf("%d %+v", status, d)
	}
	e := d.Entries[2]
	if e.Source != "calaos-server" || e.Unit != "calaos-server.service" || e.Priority != 4 || e.PID == nil || *e.PID != 812 ||
		e.Message != "five" || e.TS != "2026-10-26T07:41:12.005Z" || e.Cut {
		t.Fatalf("entry %+v", e)
	}
	argv := strings.Join(f.argv(), "|")
	if !strings.Contains(argv, "|-r|--after-cursor=s=6|--priority=4|") || strings.Contains(argv, "--since") {
		t.Fatalf("argv %s", argv)
	}

	// The whole output read: complete, next_cursor the oldest scanned.
	_, _, d = logsQuery(t, a, `{"sources":["calaos-server"],"priority":3}`, nil)
	if cursors(d.Entries) != "s=2,s=3,s=4,s=5" || !d.Complete || *d.NextCursor != "s=2" {
		t.Fatalf("%+v", d)
	}
}

// journalctl refuses --since with a cursor: the daemon applies it.
func TestLogsQuerySinceWithCursor(t *testing.T) {
	clk := useLogClock(t)
	f := useFakeJournalctl(t)
	var lines []string
	for i := 8; i >= 1; i-- {
		lines = append(lines, jentry(i, "ssh.service", "m"))
	}
	f.write(lines...)
	f.mode("hang")
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	// Backward: the first entry older than since ends the page.
	since := time.UnixMicro(jbase + 3500).UTC().Format(time.RFC3339Nano)
	start := time.Now()
	status, _, d := logsQuery(t, a, `{"cursor":"s=9","since":"`+since+`","lines":10}`, nil)
	if status != 200 || cursors(d.Entries) != "s=4,s=5,s=6,s=7,s=8" || !d.Complete || *d.NextCursor != "s=4" {
		t.Fatalf("%d %+v", status, d)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the scan did not stop at since")
	}
	f.assertKilled()
	if argv := strings.Join(f.argv(), "|"); strings.Contains(argv, "--since") {
		t.Fatalf("argv %s", argv)
	}

	// Backward from a cursor without since: the 7-day default, in the daemon too.
	f.mode("")
	clk.Advance(time.Second)
	if status, _, d = logsQuery(t, a, `{"cursor":"s=9","lines":3}`, nil); status != 200 || cursors(d.Entries) != "s=6,s=7,s=8" {
		t.Fatalf("%d %+v", status, d)
	}

	// Forward: older entries are skipped, until ends the page.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	f.write(lines...)
	until := time.UnixMicro(jbase + 6500).UTC().Format(time.RFC3339Nano)
	clk.Advance(time.Second)
	status, _, d = logsQuery(t, a, `{"direction":"forward","cursor":"s=0","since":"`+since+`","until":"`+until+`"}`, nil)
	if status != 200 || cursors(d.Entries) != "s=4,s=5,s=6" || !d.Complete || *d.NextCursor != "s=6" {
		t.Fatalf("%d %+v", status, d)
	}
	if argv := strings.Join(f.argv(), "|"); strings.Contains(argv, "--since") || !strings.Contains(argv, "--until=") {
		t.Fatalf("argv %s", argv)
	}
}

// Forward reads the oldest N after the cursor, then kills journalctl.
func TestLogsQueryForwardStopsAfterN(t *testing.T) {
	f := useFakeJournalctl(t)
	f.write(jentry(1, "ssh.service", "a"), jentry(2, "ssh.service", "b"), jentry(3, "ssh.service", "c"))
	f.mode("hang")
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	start := time.Now()
	status, _, d := logsQuery(t, a, `{"direction":"forward","cursor":"s=0","lines":2,"sources":["ssh"]}`, nil)
	if status != 200 || cursors(d.Entries) != "s=1,s=2" || !d.Complete || *d.NextCursor != "s=2" {
		t.Fatalf("%d %+v", status, d)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("forward query waited for the timeout")
	}
	f.assertKilled()
	argv := strings.Join(f.argv(), "|")
	if strings.Contains(argv, "|-r|") || !strings.Contains(argv, "|--after-cursor=s=0|") || strings.Contains(argv, "--since") {
		t.Fatalf("argv %s", argv)
	}
}

func TestLogsQueryTimeoutKillsGroup(t *testing.T) {
	useScanTime(t, 300*time.Millisecond)
	f := useFakeJournalctl(t)
	f.write(jentry(1, "ssh.service", "a"), jentry(2, "ssh.service", "b"))
	f.mode("hang")
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	status, _, d := logsQuery(t, a, `{"direction":"forward","since":"2026-10-25T00:00:00Z","lines":10}`, nil)
	if status != 200 || cursors(d.Entries) != "s=1,s=2" || d.Complete || *d.NextCursor != "s=2" {
		t.Fatalf("%d %+v", status, d)
	}
	f.assertKilled()
}

func TestLogsQueryScanWindow(t *testing.T) {
	// Large outputs: a slow builder must not hit the 5 s scan limit.
	useScanTime(t, time.Minute)
	f := useFakeJournalctl(t)
	lines := make([]string, logsScanEntries+10)
	for i := range lines {
		lines[i] = jentry(i+1, "ssh.service", "nothing here")
	}
	f.write(lines...)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	status, _, d := logsQuery(t, a, `{"direction":"forward","since":"2026-10-25T00:00:00Z","text":"WAGO"}`, nil)
	if status != 200 || len(d.Entries) != 0 || d.Complete || *d.NextCursor != fmt.Sprintf("s=%d", logsScanEntries) {
		t.Fatalf("%d %+v", status, len(d.Entries))
	}
}

func TestLogsQueryTextOnRedacted(t *testing.T) {
	f := useFakeJournalctl(t)
	f.write(jentry(1, "calaos-server.service", "WAGO login cn_pass=hunter2"), jentry(2, "calaos-server.service", "other"),
		jentry(3, "calaos-server.service", "wago ok"))
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	_, _, d := logsQuery(t, a, `{"direction":"forward","cursor":"s=0","text":"Wago"}`, nil)
	if cursors(d.Entries) != "s=1,s=3" || d.Entries[0].Message != "WAGO login cn_pass=***" || *d.NextCursor != "s=3" {
		t.Fatalf("%+v", d)
	}
	_, _, d = logsQuery(t, a, `{"direction":"forward","cursor":"s=0","text":"hunter2"}`, nil)
	if len(d.Entries) != 0 || !d.Complete || *d.NextCursor != "s=3" {
		t.Fatalf("the text matched the secret: %+v", d)
	}
}

func TestLogsQueryCutAndTruncated(t *testing.T) {
	// Large outputs: a slow builder must not hit the 5 s scan limit.
	useScanTime(t, time.Minute)
	clk := useLogClock(t)
	f := useFakeJournalctl(t)
	big := strings.Repeat("x", 3000)
	lines := []string{jentry(1, "ssh.service", big)}
	for i := 2; i <= 1000; i++ {
		lines = append(lines, jentry(i, "ssh.service", big[:1500]))
	}
	lines = append(lines, `{"__CURSOR":"s=x","MESSAGE":"`+strings.Repeat("y", logsLineCap)+`"}`)
	f.write(lines...)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)

	status, _, d := logsQuery(t, a, `{"direction":"forward","cursor":"s=0","lines":1000}`, nil)
	if status != 200 || !d.Truncated || len(d.Entries) >= 1000 || len(d.Entries) < 500 {
		t.Fatalf("%d truncated=%v n=%d", status, d.Truncated, len(d.Entries))
	}
	if e := d.Entries[0]; !e.Cut || len(e.Message) != models.LogMessageMax {
		t.Fatalf("first entry cut=%v len=%d", e.Cut, len(e.Message))
	}
	if last := d.Entries[len(d.Entries)-1]; *d.NextCursor != last.Cursor || d.Entries[1].Cut {
		t.Fatalf("next %s, last kept %s", *d.NextCursor, last.Cursor)
	}

	// Backward drops the oldest: the newest are kept.
	for i, j := 0, len(lines)-2; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	f.write(lines[:1000]...)
	clk.Advance(time.Second)
	_, _, d = logsQuery(t, a, `{"lines":1000}`, nil)
	if !d.Truncated || d.Entries[len(d.Entries)-1].Cursor != "s=1000" || *d.NextCursor != d.Entries[0].Cursor {
		t.Fatalf("backward truncation: n=%d next=%s", len(d.Entries), *d.NextCursor)
	}

	// An overlong line is skipped, not buffered.
	f.write(lines[len(lines)-1], jentry(7, "ssh.service", "ok"))
	clk.Advance(time.Second)
	_, _, d = logsQuery(t, a, `{"direction":"forward","cursor":"s=0"}`, nil)
	if cursors(d.Entries) != "s=7" {
		t.Fatalf("%+v", cursors(d.Entries))
	}
}

func TestLogsQueryJournalctlFails(t *testing.T) {
	f := useFakeJournalctl(t)
	f.mode("fail")
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	status, r, _ := logsQuery(t, a, `{"cursor":"s=bogus"}`, nil)
	if status != 500 || r.Code != codeInternal || strings.Contains(r.Msg, "seek") {
		t.Fatalf("%d %+v", status, r)
	}
}

func TestLogsQueryInvalid(t *testing.T) {
	clk := useAdminClock(t)
	f := useFakeJournalctl(t)
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	for _, body := range []string{
		`{"sources":["calaos-server.service"]}`,
		`{"sources":[]}`,
		`{"sources":["ssh","ssh","ssh","ssh","ssh","ssh","ssh","ssh","ssh"]}`,
		`{"priority":"3"}`,
		`{"priority":8}`,
		`{"priority":-1}`,
		`{"priority":2.5}`,
		`{"priority":"warn"}`,
		`{"since":"2026-10-25T09:00:00Z","until":"2026-10-25T09:00:00Z"}`,
		`{"since":"2026-10-25T10:00:00Z","until":"2026-10-25T09:00:00Z"}`,
		`{"since":"yesterday"}`,
		`{"until":"2026-10-25 09:00:00"}`,
		`{"since":"0001-01-01T00:00:00Z"}`,
		`{"until":"9999-12-31T00:00:00Z"}`,
		`{"direction":"forward","since":"1969-12-31T23:59:59Z"}`,
		`{"text":"` + strings.Repeat("é", logsMaxText+1) + `"}`,
		`{"lines":0}`,
		`{"lines":1001}`,
		`{"lines":"200"}`,
		`{"cursor":"s=1 i=2"}`,
		`{"cursor":""}`,
		`{"cursor":"` + strings.Repeat("a", logsMaxCursor+1) + `"}`,
		`{"cursor":"s=é"}`,
		`{"direction":"forward"}`,
		`{"direction":"up","cursor":"s=1"}`,
		`[1]`,
	} {
		status, r, _ := logsQuery(t, a, body, nil)
		if status != 400 || r.Code != codeInvalidRequest {
			t.Errorf("%s: %d %q", body, status, r.Code)
		}
	}
	if _, err := os.Stat(f.args); !os.IsNotExist(err) {
		t.Fatal("journalctl ran on an invalid query")
	}
	for _, body := range []string{
		`{"priority":"warning","text":"` + strings.Repeat("é", logsMaxText) + `"}`,
		`{"priority":0,"lines":1000,"sources":["kernel","system"]}`,
		`{"direction":"forward","since":"2026-10-25T08:00:00.5+02:00"}`,
	} {
		clk.Advance(time.Second)
		if status, r, _ := logsQuery(t, a, body, nil); status != 200 {
			t.Errorf("%s: %d %q", body, status, r.Code)
		}
	}
}

func TestLogsQueryRateLimit(t *testing.T) {
	clk := useAdminClock(t)
	f := useFakeJournalctl(t)
	f.write()
	c := newAdminApp(t)
	c.expect("POST", "/api/v2/admin/set", `{"password":"`+adminPw+`"}`, nil, 200, "")
	s := c.verify(adminPw)

	q := func(h map[string]string) int {
		status, _, _ := logsQuery(t, c.a, "{}", h)
		return status
	}
	// Bearer-only calls share the local bucket; a session has its own.
	if q(nil) != 200 || q(map[string]string{headerClientIP: "10.0.0.1"}) != 200 {
		t.Fatal("first two refused")
	}
	status, r, _ := logsQuery(t, c.a, "{}", nil)
	if status != 429 || r.Code != codeRateLimited || string(r.Data) != `{"retry_after":1}` {
		t.Fatalf("%d %+v", status, r)
	}
	if got := []int{q(session(s)), q(session(s)), q(session(s))}; got[0] != 200 || got[1] != 200 || got[2] != 429 {
		t.Fatal("session bucket")
	}
	clk.Advance(500 * time.Millisecond)
	if q(nil) != 200 || q(nil) != 429 {
		t.Fatal("refill")
	}

	// 4 journalctl processes at most; a refusal for that costs no token.
	for i := 0; i < logsMaxProcs; i++ {
		c.a.logs.procs <- struct{}{}
	}
	clk.Advance(time.Second)
	if got := []int{q(nil), q(nil), q(nil)}; got[0] != 429 || got[1] != 429 || got[2] != 429 {
		t.Fatal("fifth process allowed")
	}
	<-c.a.logs.procs
	if got := []int{q(nil), q(nil), q(nil)}; got[0] != 200 || got[1] != 200 || got[2] != 429 {
		t.Fatalf("after the slot came back: %v", got)
	}
}

func TestLogsProcessCapConcurrent(t *testing.T) {
	useScanTime(t, 2*time.Second)
	clk := useAdminClock(t)
	f := useFakeJournalctl(t)
	f.mode("hang")
	f.write(jentry(1, "ssh.service", "a"))
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	// The clock is frozen, so the bucket keeps what it is given.
	a.logs.buckets[logsLocalBucket] = &tokenBucket{tokens: 10, last: clk.Now()}

	var wg sync.WaitGroup
	codes := make(chan int, logsMaxProcs)
	for i := 0; i < logsMaxProcs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- logsQueryDirect(a, `{"direction":"forward","since":"2026-10-25T00:00:00Z","lines":5}`)
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(a.logs.procs) < logsMaxProcs {
		if time.Now().After(deadline) {
			t.Fatal("queries never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status, r, _ := logsQuery(t, a, `{"lines":5}`, map[string]string{headerClientIP: "10.0.0.2"}); status != 429 || r.Code != codeRateLimited {
		t.Fatalf("fifth query: %d %s", status, r.Code)
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("hung query answered %d", code)
		}
	}
}

// logsQueryDirect is safe off the test goroutine: it reports instead of
// failing.
func logsQueryDirect(a *AppServer, body string) int {
	req := httptest.NewRequest("POST", "/api/v2/logs/query", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.appFiber.Test(req, -1)
	if err != nil {
		return -1
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A clock stepped back (time_set) must not starve the buckets, a step
// forward must not grow them past their size.
func TestLogsRateClockStep(t *testing.T) {
	l := newLogLimits()
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	if takes(l, now) != 2 {
		t.Fatal("bucket size")
	}
	now = now.Add(-time.Hour)
	if l.allow("k", now) {
		t.Fatal("a step back refilled the bucket")
	}
	now = now.Add(500 * time.Millisecond)
	if !l.allow("k", now) {
		t.Fatal("bucket starved after a step back")
	}
	now = now.Add(24 * time.Hour)
	if takes(l, now) != 2 {
		t.Fatal("a step forward grew the bucket")
	}
}

// takes drains key "k" at a frozen instant.
func takes(l *logLimits, now time.Time) int {
	n := 0
	for n < 10 && l.allow("k", now) {
		n++
	}
	return n
}
