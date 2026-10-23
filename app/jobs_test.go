package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calaos/calaos-container/apt"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models"
)

// jobApt is a fake libapt: the transaction installs pkgs and waits on gate.
type jobApt struct {
	cache   []*apt.Pkg
	pkgs    []string
	err     error
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (f *jobApt) GetCachePackages() ([]*apt.Pkg, error) { return f.cache, nil }

func (f *jobApt) UpgradeAll(cb func(apt.Progress)) error { return f.run(cb) }

func (f *jobApt) InstallPackage(_ string, cb func(apt.Progress)) error { return f.run(cb) }

func (f *jobApt) run(cb func(apt.Progress)) error {
	for _, p := range f.pkgs {
		cb(apt.Progress{Stage: "pending", Pkg: p, PkgPercent: -1})
	}
	f.once.Do(func() { close(f.started) })
	<-f.gate
	for _, p := range f.pkgs {
		cb(apt.Progress{Stage: "done", Pkg: p, PkgPercent: 100})
	}
	return f.err
}

type quietExecutor struct{}

func (quietExecutor) Run(context.Context, string, ...string) (string, error) { return "", nil }

func (quietExecutor) StartLogged(models.LoggedCommand) (models.Process, error) {
	return nil, errors.New("not implemented")
}

type jobFixture struct {
	a       *AppServer
	apt     *jobApt
	jobFile string
	selfNew atomic.Bool
	sub     *events.Subscriber
}

// newJobFixture runs the handlers on the real job code, with apt, the
// commands and the job file faked.
func newJobFixture(t *testing.T, s stubBackend, pkgs ...string) *jobFixture {
	t.Helper()
	s.install(t)
	startUpgradeJob = startJob
	currentJob = models.CurrentJob
	updateStatus = models.UpdateStatus
	rebootGuard = models.RebootGuard
	checkUpdates = models.CheckUpdates
	getNewVersions = models.GetNewVersions

	f := &jobFixture{
		apt: &jobApt{
			cache:   []*apt.Pkg{{Name: "calaos-server", VersionCurrent: "1", VersionNew: "2"}},
			pkgs:    pkgs,
			gate:    make(chan struct{}),
			started: make(chan struct{}),
		},
		jobFile: filepath.Join(t.TempDir(), "last_job.json"),
	}
	bootID := filepath.Join(t.TempDir(), "boot_id")
	if err := os.WriteFile(bootID, []byte("boot-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(models.SetExecutor(quietExecutor{}))
	t.Cleanup(models.SetUpgradeHooks(models.UpgradeHooks{
		Apt: f.apt, JobFile: f.jobFile, BootIDFile: bootID, ExeReplaced: f.selfNew.Load,
	}))
	if err := models.CheckUpdates(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.apt.cache = nil
		_ = models.CheckUpdates()
	})

	f.sub = events.Subscribe()
	t.Cleanup(func() { events.Unsubscribe(f.sub) })
	f.a = newHandlerTestApp(t)
	return f
}

// waitJob returns the next upgrade_job event of job id in a terminal state.
func (f *jobFixture) waitJob(t *testing.T, id string) models.Job {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-f.sub.Ch:
			if j, ok := ev.Data.(models.Job); ok && ev.Type == events.EventUpgradeJob && j.ID == id && j.Status != models.JobRunning {
				return j
			}
		case <-timeout:
			t.Fatalf("job %s never ended", id)
		}
	}
}

type jobReply struct {
	Error bool       `json:"error"`
	Code  string     `json:"code"`
	Msg   string     `json:"msg"`
	Data  models.Job `json:"data"`
}

func (f *jobFixture) post(t *testing.T, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	tc := httpCase{method: "POST", path: path, body: body, headers: headers}
	if body != "" {
		tc.ctype = "application/json"
	}
	resp, b := doCase(t, f.a, tc)
	return resp.StatusCode, string(b)
}

func (f *jobFixture) status(t *testing.T) *models.Job {
	t.Helper()
	resp, b := doCase(t, f.a, httpCase{method: "GET", path: "/api/v2/update/status"})
	var r struct {
		Data struct {
			Job *models.Job `json:"job"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &r) != nil {
		t.Fatalf("update/status: %d %s", resp.StatusCode, b)
	}
	return r.Data.Job
}

func TestV2UpgradeJob(t *testing.T) {
	f := newJobFixture(t, stubBackend{commands: map[string]stubResult{"systemctl reboot": {err: errors.New("boom")}}}, "calaos-server")

	if j := f.status(t); j != nil {
		t.Fatalf("job before any run: %+v", j)
	}

	code, body := f.post(t, "/api/v2/update/upgrade-all", "", nil)
	var r jobReply
	if err := json.Unmarshal([]byte(body), &r); err != nil || code != http.StatusAccepted || r.Error || r.Msg != "accepted" {
		t.Fatalf("POST upgrade-all: %d %s", code, body)
	}
	job := r.Data
	if len(job.ID) != 16 || job.Kind != "upgrade_all" || job.Status != "running" || job.StartedAt == "" {
		t.Fatalf("job %+v", job)
	}
	if !strings.Contains(body, `"reboot_required":false,"daemon_restart":false`) || strings.Contains(body, "boot_id") {
		t.Fatalf("job shape: %s", body)
	}
	<-f.apt.started

	if j := f.status(t); j == nil || j.ID != job.ID || j.Status != "running" {
		t.Fatalf("status while running: %+v", j)
	}

	inProgress := fmt.Sprintf(`{"error":true,"code":"upgrade_in_progress","msg":"an upgrade is already in progress","data":{"job_id":%q}}`, job.ID)
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/api/v2/update/upgrade-all", ""},
		{"POST", "/api/v2/update/upgrade", `{"package":"calaos-server"}`},
		{"GET", "/api/v2/update/check", ""},
		{"POST", "/api/v2/system/reboot", ""},
		{"POST", "/api/v2/system/rollback_snapshot", ""},
	} {
		tc := httpCase{method: req.method, path: req.path, body: req.body}
		if req.body != "" {
			tc.ctype = "application/json"
		}
		resp, b := doCase(t, f.a, tc)
		if resp.StatusCode != http.StatusConflict || string(b) != inProgress {
			t.Fatalf("%s %s during the job: %d %s", req.method, req.path, resp.StatusCode, b)
		}
	}

	// Legacy routes keep their answers during a run.
	legacyBusy := `{"error":true,"msg":"upgrade already in progress"}`
	for _, req := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/api/update/upgrade-all", http.StatusInternalServerError},
		{"POST", "/api/update/upgrade?package=calaos-server", http.StatusInternalServerError},
		{"GET", "/api/update/check", http.StatusInternalServerError},
		{"POST", "/api/system/reboot", http.StatusConflict},
	} {
		resp, b := doCase(t, f.a, httpCase{method: req.method, path: req.path})
		if resp.StatusCode != req.status || string(b) != legacyBusy {
			t.Fatalf("legacy %s %s during the job: %d %s", req.method, req.path, resp.StatusCode, b)
		}
	}
	resp, b := doCase(t, f.a, httpCase{method: "GET", path: "/api/update/status"})
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(b), `{"status":"upgrading"`) {
		t.Fatalf("legacy status during the job: %s", b)
	}

	close(f.apt.gate)
	end := f.waitJob(t, job.ID)
	if end.Status != "done" || end.FinishedAt == "" || end.DaemonRestart {
		t.Fatalf("terminal job %+v", end)
	}
	if j := f.status(t); j == nil || *j != end {
		t.Fatalf("status after the job: %+v", j)
	}
	resp, b = doCase(t, f.a, httpCase{method: "GET", path: "/api/update/status"})
	if string(b) != `{"status":"idle","total_percent":0}` {
		t.Fatalf("legacy status after the job: %d %s", resp.StatusCode, b)
	}

	// The guard lets the reboot through again (it fails here, which frees
	// the lock for the next tests).
	if code, body := f.post(t, "/api/v2/system/reboot", "", nil); code != http.StatusInternalServerError {
		t.Fatalf("reboot after the job: %d %s", code, body)
	}
}

func TestV2UpgradePackageRequest(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-server")

	for _, body := range []string{
		``, `{"package":`, `{}`, `{"package":null}`, `{"package":""}`,
		`{"package":"-calaos"}`, `{"package":".calaos"}`, `{"package":"calaos..server"}`,
		`{"package":"../etc"}`, `{"package":"calaos/server"}`, `{"package":"calaos server"}`,
		`{"package":"calaos?x"}`, `{"package":"calaos%2e"}`,
		`{"package":"` + strings.Repeat("a", 129) + `"}`,
	} {
		code, resp := f.post(t, "/api/v2/update/upgrade", body, nil)
		if code != http.StatusBadRequest || !strings.Contains(resp, `"code":"invalid_request"`) {
			t.Fatalf("body %q: %d %s", body, code, resp)
		}
	}

	code, resp := f.post(t, "/api/v2/update/upgrade", `{"package":"`+strings.Repeat("a", 128)+`"}`, nil)
	if code != http.StatusNotFound || resp != `{"error":true,"code":"package_not_found","msg":"no pending update for this package"}` {
		t.Fatalf("absent package: %d %s", code, resp)
	}
	if f.status(t) != nil {
		t.Fatal("a job was created for an absent package")
	}

	code, resp = f.post(t, "/api/v2/update/upgrade", `{"package":"calaos-server","extra":1}`, nil)
	var r jobReply
	if code != http.StatusAccepted || json.Unmarshal([]byte(resp), &r) != nil || r.Data.Kind != "upgrade" || r.Data.Package != "calaos-server" {
		t.Fatalf("upgrade: %d %s", code, resp)
	}
	close(f.apt.gate)
	f.waitJob(t, r.Data.ID)
}

func TestHeldPackage(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-server")
	f.apt.cache = append(f.apt.cache, &apt.Pkg{Name: "linux-image-amd64", VersionCurrent: "1", VersionNew: "2", Held: true})
	if err := models.CheckUpdates(); err != nil {
		t.Fatal(err)
	}

	want := `{"calaos-server":{"name":"calaos-server","version":"2","current_version":"1"}}`
	for _, path := range []string{"/api/update/check", "/api/update/available"} {
		resp, b := doCase(t, f.a, httpCase{method: "GET", path: path})
		if resp.StatusCode != http.StatusOK || string(b) != want {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
	}
	resp, b := doCase(t, f.a, httpCase{method: "GET", path: "/api/v2/update/check"})
	if resp.StatusCode != http.StatusOK || string(b) != `{"error":false,"msg":"ok","data":`+want+`}` {
		t.Fatalf("v2 check: %d %s", resp.StatusCode, b)
	}

	code, body := f.post(t, "/api/v2/update/upgrade", `{"package":"linux-image-amd64"}`, nil)
	if code != http.StatusConflict || body != `{"error":true,"code":"package_held","msg":"the package is held (apt-mark hold)"}` {
		t.Fatalf("v2 upgrade: %d %s", code, body)
	}
	resp, b = doCase(t, f.a, httpCase{method: "POST", path: "/api/update/upgrade?package=linux-image-amd64"})
	if resp.StatusCode != http.StatusInternalServerError || string(b) != `{"error":true,"msg":"package not found"}` {
		t.Fatalf("legacy upgrade: %d %s", resp.StatusCode, b)
	}
	if f.status(t) != nil {
		t.Fatal("a job was created for a held package")
	}
}

func TestV2UpgradeSessions(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-server")
	f.a.sessions = acceptSessions{ok: "live"}

	for _, path := range []string{"/api/v2/update/upgrade-all", "/api/v2/update/upgrade"} {
		code, body := f.post(t, path, `{"package":"calaos-server"}`, map[string]string{headerAdminSession: "dead"})
		if code != http.StatusUnauthorized || !strings.Contains(body, `"code":"bad_session"`) {
			t.Fatalf("%s with a dead session: %d %s", path, code, body)
		}
	}
	if f.status(t) != nil {
		t.Fatal("a dead session started a job")
	}

	code, body := f.post(t, "/api/v2/update/upgrade-all", "", map[string]string{headerAdminSession: "live"})
	var r jobReply
	if code != http.StatusAccepted || json.Unmarshal([]byte(body), &r) != nil {
		t.Fatalf("live session: %d %s", code, body)
	}
	close(f.apt.gate)
	f.waitJob(t, r.Data.ID)
}

// TestLegacyUpgradeIsAJob: the legacy route answers once apt has ended, with
// its frozen body, and the run is recorded as a job.
func TestLegacyUpgradeIsAJob(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-server")
	close(f.apt.gate)

	resp, b := doCase(t, f.a, httpCase{method: "POST", path: "/api/update/upgrade?package=calaos-server"})
	if got := renderGolden(resp, b); string(got) != "status: 200\ncontent-type: application/json\n\n{\"msg\":\"Upgrade done\"}\n" {
		t.Fatalf("legacy answer changed:\n%s", got)
	}
	j := f.status(t)
	if j == nil || j.Kind != "upgrade" || j.Package != "calaos-server" {
		t.Fatalf("legacy run not recorded: %+v", j)
	}
	if end := f.waitJob(t, j.ID); end.Status != "done" {
		t.Fatalf("terminal job %+v", end)
	}
}

// TestLegacySelfUpgradeExitAfterAnswer: the legacy caller gets its answer on
// a connection the daemon closes, and the exit is requested.
func TestLegacySelfUpgradeExitAfterAnswer(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-container")
	f.selfNew.Store(true)
	close(f.apt.gate)

	completed := make(chan struct{})
	startUpgradeJob = func(kind, pkg string) (upgradeRun, error) {
		r, err := startJob(kind, pkg)
		if err != nil {
			return nil, err
		}
		return &watchedRun{upgradeRun: r, completed: completed}, nil
	}

	resp, b := doCase(t, f.a, httpCase{method: "POST", path: "/api/update/upgrade-all"})
	if resp.StatusCode != http.StatusOK || string(b) != `{"msg":"Upgrade done"}` || !resp.Close {
		t.Fatalf("legacy answer: %d %s, close %v", resp.StatusCode, b, resp.Close)
	}
	<-completed
	j := f.status(t)
	if j == nil || !j.DaemonRestart {
		t.Fatalf("job %+v", j)
	}
	f.waitJob(t, j.ID)
	select {
	case <-models.RestartRequested():
	case <-time.After(5 * time.Second):
		t.Fatal("no exit after a self-upgrade")
	}
}

// watchedRun reports when the job completes.
type watchedRun struct {
	upgradeRun
	completed chan struct{}
}

func (w *watchedRun) Complete() {
	w.upgradeRun.Complete()
	close(w.completed)
}

// TestV2SelfUpgradeExit: the exit is requested only once the terminal job is
// published and persisted, and the lock stays held until then.
func TestV2SelfUpgradeExit(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-container", "calaos-server")
	f.selfNew.Store(true)

	code, body := f.post(t, "/api/v2/update/upgrade-all", "", nil)
	var r jobReply
	if code != http.StatusAccepted || json.Unmarshal([]byte(body), &r) != nil {
		t.Fatalf("POST: %d %s", code, body)
	}
	close(f.apt.gate)

	select {
	case <-models.RestartRequested():
	case <-time.After(5 * time.Second):
		t.Fatal("no exit after a self-upgrade")
	}
	// Already queued when the exit was requested.
	select {
	case ev := <-drainJob(f.sub, r.Data.ID):
		if !ev.DaemonRestart || ev.Status != "done" {
			t.Fatalf("terminal job %+v", ev)
		}
	default:
		t.Fatal("exit requested before the terminal job was published")
	}
	raw, err := os.ReadFile(f.jobFile)
	if err != nil || !strings.Contains(string(raw), `"daemon_restart":true`) || !strings.Contains(string(raw), `"status":"done"`) {
		t.Fatalf("persisted job: %s %v", raw, err)
	}

	code, body = f.post(t, "/api/v2/update/upgrade-all", "", nil)
	if code != http.StatusConflict {
		t.Fatalf("an upgrade started on the replaced daemon: %d %s", code, body)
	}
	if code, _ := f.post(t, "/api/v2/system/reboot", "", nil); code != http.StatusConflict {
		t.Fatalf("reboot while the daemon exits: %d", code)
	}
}

// drainJob returns a channel holding the terminal job id if it is already
// queued on sub.
func drainJob(sub *events.Subscriber, id string) <-chan models.Job {
	out := make(chan models.Job, 1)
	for {
		select {
		case ev := <-sub.Ch:
			if j, ok := ev.Data.(models.Job); ok && j.ID == id && j.Status != models.JobRunning {
				out <- j
				return out
			}
		default:
			return out
		}
	}
}

// TestShutdownFlushesEvents: an event published just before Shutdown, such
// as the terminal job of a self-upgrade, reaches the connected client before
// the close frame.
func TestShutdownFlushesEvents(t *testing.T) {
	a, addr, cleanup := newTestServer(t)
	conn := dialWS(t, addr, nil, "token="+testToken)
	defer func() { _ = conn.Close() }()
	_ = readEvent(t, conn) // snapshot

	a.hub.Publish(events.EventUpgradeJob, models.Job{ID: "0011223344556677", Status: "done", DaemonRestart: true})
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()

	ev := readEvent(t, conn)
	if ev.Type != events.EventUpgradeJob {
		t.Fatalf("got %q before the close", ev.Type)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked")
	}
}

// TestLegacyQueue: a kiosk posts the next package of its queue as soon as
// the previous answer arrives; it must never find the run still in progress.
func TestLegacyQueue(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-server")
	close(f.apt.gate)

	for i := 0; i < 20; i++ {
		resp, b := doCase(t, f.a, httpCase{method: "POST", path: "/api/update/upgrade?package=calaos-server"})
		if resp.StatusCode != http.StatusOK || string(b) != `{"msg":"Upgrade done"}` || resp.Close {
			t.Fatalf("upgrade %d: %d %s", i, resp.StatusCode, b)
		}
	}
}

// TestLegacyPackageRule: the legacy route applies the v2 package rule, with
// the body of an unknown package, and starts no job.
func TestLegacyPackageRule(t *testing.T) {
	f := newJobFixture(t, stubBackend{}, "calaos-server")
	for _, q := range []string{"", "-y", "..%2Fetc", "calaos..server", "a%2Fb", strings.Repeat("a", 129)} {
		resp, b := doCase(t, f.a, httpCase{method: "POST", path: "/api/update/upgrade?package=" + q})
		if got := renderGolden(resp, b); string(got) != "status: 500\ncontent-type: application/json\n\n{\"error\":true,\"msg\":\"package not found\"}\n" {
			t.Fatalf("package %q:\n%s", q, got)
		}
	}
	if f.status(t) != nil {
		t.Fatal("a job was started for an invalid name")
	}
}
