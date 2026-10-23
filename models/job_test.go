package models

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calaos/calaos-container/apt"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models/structs"
)

// fakeApt plays an apt transaction: every package of pkgs is announced, then
// completed, unless it is listed in fail. A run blocks on gate when set.
type fakeApt struct {
	mu       sync.Mutex
	cache    []*apt.Pkg
	cacheErr error
	pkgs     []string
	fail     map[string]bool
	err      error
	gate     chan struct{}
	started  chan struct{}
	installs []string
	removed  string // also removed by the transaction
}

func (f *fakeApt) GetCachePackages() ([]*apt.Pkg, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cache, f.cacheErr
}

func (f *fakeApt) UpgradeAll(cb func(apt.Progress)) error { return f.transaction(cb) }

func (f *fakeApt) InstallPackage(name string, cb func(apt.Progress)) error {
	f.mu.Lock()
	f.installs = append(f.installs, name)
	f.mu.Unlock()
	return f.transaction(cb)
}

func (f *fakeApt) transaction(cb func(apt.Progress)) error {
	if f.removed != "" {
		cb(apt.Progress{Stage: "pending", Pkg: f.removed, PkgPercent: -1, Msg: "remove"})
	}
	for _, p := range f.pkgs {
		cb(apt.Progress{Stage: "pending", Pkg: p, PkgPercent: -1})
	}
	f.mu.Lock()
	if f.started != nil {
		close(f.started)
		f.started = nil
	}
	f.mu.Unlock()
	if f.gate != nil {
		<-f.gate
	}
	for _, p := range f.pkgs {
		stage := "done"
		if f.fail[p] {
			stage = "error"
		}
		cb(apt.Progress{Stage: stage, Pkg: p, PkgPercent: 100})
	}
	return f.err
}

// countingExecutor answers every command with success and counts the apt
// index refreshes.
type countingExecutor struct{ aptUpdates atomic.Int32 }

func (e *countingExecutor) Run(_ context.Context, name string, args ...string) (string, error) {
	if name == "apt-get" && len(args) > 0 && args[0] == "update" {
		e.aptUpdates.Add(1)
	}
	return "", nil
}

func (e *countingExecutor) StartLogged(LoggedCommand) (Process, error) {
	return nil, errors.New("not implemented")
}

type jobEnv struct {
	apt     *fakeApt
	exec    *countingExecutor
	jobFile string
	bootID  string // path of the fake boot id file
	selfNew atomic.Bool
}

func newJobEnv(t *testing.T, f *fakeApt) *jobEnv {
	t.Helper()
	dir := t.TempDir()
	env := &jobEnv{
		apt:     f,
		exec:    &countingExecutor{},
		jobFile: filepath.Join(dir, "log", "last_job.json"),
		bootID:  filepath.Join(dir, "boot_id"),
	}
	env.setBootID(t, "boot-1")
	t.Cleanup(SetExecutor(env.exec))
	env.hook(t)
	t.Cleanup(func() {
		resetStatus()
		setNewVersions(structs.ImageMap{})
	})
	return env
}

// hook (re)installs the hooks, as a daemon start does.
func (e *jobEnv) hook(t *testing.T) {
	t.Cleanup(SetUpgradeHooks(UpgradeHooks{
		Apt:         e.apt,
		JobFile:     e.jobFile,
		BootIDFile:  e.bootID,
		ExeReplaced: e.selfNew.Load,
	}))
}

func (e *jobEnv) setBootID(t *testing.T, id string) {
	t.Helper()
	if err := os.WriteFile(e.bootID, []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *jobEnv) record(t *testing.T) jobRecord {
	t.Helper()
	b, err := os.ReadFile(e.jobFile)
	if err != nil {
		t.Fatal(err)
	}
	var r jobRecord
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func nextJobEvent(t *testing.T, sub *events.Subscriber) Job {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sub.Ch:
			if ev.Type == events.EventUpgradeJob {
				return ev.Data.(Job)
			}
		case <-timeout:
			t.Fatal("no upgrade_job event")
		}
	}
}

func TestJobLifecycle(t *testing.T) {
	started := make(chan struct{})
	env := newJobEnv(t, &fakeApt{pkgs: []string{"calaos-server"}, gate: make(chan struct{}), started: started})
	sub := events.Subscribe()
	defer events.Unsubscribe(sub)

	run, err := StartJob(JobKindUpgradeAll, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	created := run.Job()
	if len(created.ID) != 16 || created.Kind != JobKindUpgradeAll || created.Package != "" || created.Status != JobRunning || created.FinishedAt != "" {
		t.Fatalf("created job %+v", created)
	}
	if _, err := time.Parse(time.RFC3339, created.StartedAt); err != nil || !strings.HasSuffix(created.StartedAt, "Z") {
		t.Fatalf("started_at %q", created.StartedAt)
	}
	if ev := nextJobEvent(t, sub); ev != created {
		t.Fatalf("creation event %+v", ev)
	}
	if r := env.record(t); r.Status != JobRunning || r.ID != created.ID || r.BootID != "boot-1" {
		t.Fatalf("persisted while running: %+v", r)
	}
	if fi, err := os.Stat(env.jobFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("job file mode: %v %v", fi, err)
	}
	if st, _ := UpdateStatus(); st.Status != "upgrading" {
		t.Fatalf("progress %q at creation", st.Status)
	}

	done := make(chan error, 1)
	go func() { done <- run.Run() }()
	<-started

	// One run at a time: a second one is refused with the running id.
	_, err = StartJob(JobKindUpgradeAll, "")
	var ip *InProgressError
	if !errors.As(err, &ip) || ip.JobID != created.ID || err.Error() != "upgrade already in progress" {
		t.Fatalf("concurrent StartJob: %v", err)
	}
	if err := checkForUpdates(); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("check during a job: %v", err)
	}
	if err := RebootGuard(func() error { return nil }); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("reboot during a job: %v", err)
	}
	if RunningJobID() != created.ID {
		t.Fatal("RunningJobID")
	}

	close(env.apt.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r := env.record(t); r.Status != JobDone || r.FinishedAt == "" || r.RebootRequired || r.DaemonRestart {
		t.Fatalf("persisted at the end: %+v", r)
	}

	run.Complete()
	run.Complete()
	end := nextJobEvent(t, sub)
	if end.ID != created.ID || end.Status != JobDone {
		t.Fatalf("terminal event %+v", end)
	}
	if st, _ := UpdateStatus(); st.Status != "idle" {
		t.Fatalf("progress %q after the job", st.Status)
	}
	if j := CurrentJob(); j == nil || *j != end {
		t.Fatalf("CurrentJob %+v", j)
	}

	again, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = again.Run()
	again.Complete()
}

func TestJobPackage(t *testing.T) {
	env := newJobEnv(t, &fakeApt{
		cache: []*apt.Pkg{{Name: "calaos-server", VersionCurrent: "1", VersionNew: "2"}},
		pkgs:  []string{"calaos-server"},
	})
	if err := CheckUpdates(); err != nil {
		t.Fatal(err)
	}

	// No pending update: no job, and the lock is free again.
	if _, err := StartJob(JobKindUpgrade, "calaos-home"); !errors.Is(err, ErrPackageNotFound) || err.Error() != "package not found" {
		t.Fatalf("absent package: %v", err)
	}
	if CurrentJob() != nil {
		t.Fatal("a job was recorded for an absent package")
	}
	if _, err := os.Stat(env.jobFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("job file written for an absent package: %v", err)
	}

	run, err := StartJob(JobKindUpgrade, "calaos-server")
	if err != nil {
		t.Fatal(err)
	}
	if j := run.Job(); j.Kind != JobKindUpgrade || j.Package != "calaos-server" {
		t.Fatalf("job %+v", j)
	}
	if err := run.Run(); err != nil {
		t.Fatal(err)
	}
	run.Complete()
	if len(env.apt.installs) != 1 || env.apt.installs[0] != "calaos-server" {
		t.Fatalf("installed %v", env.apt.installs)
	}
	// The versions are refreshed under the job's lock.
	if env.exec.aptUpdates.Load() != 2 {
		t.Fatalf("apt-get update ran %d times, want check + refresh", env.exec.aptUpdates.Load())
	}
}

// TestJobAptError: the apt error reaches the legacy caller only; the job and
// the progress get a summary without apt output.
func TestJobAptError(t *testing.T) {
	env := newJobEnv(t, &fakeApt{
		pkgs: []string{"calaos-server", "calaos-home"},
		fail: map[string]bool{"calaos-home": true},
		err:  errors.New("E: Sub-process /usr/bin/dpkg returned an error code (1)"),
	})
	sub := events.Subscribe()
	defer events.Unsubscribe(sub)

	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Run(); err == nil || !strings.Contains(err.Error(), "dpkg") {
		t.Fatalf("Run: %v", err)
	}
	if st, _ := UpdateStatus(); st.Status != "error" || st.Error != "upgrade failed: calaos-home" {
		t.Fatalf("progress %+v", st)
	}
	run.Complete()
	nextJobEvent(t, sub)
	end := nextJobEvent(t, sub)
	if end.Status != JobError || end.Error != "upgrade failed: calaos-home" {
		t.Fatalf("terminal job %+v", end)
	}
	if r := env.record(t); r.Error != end.Error {
		t.Fatalf("persisted %+v", r)
	}
	if env.exec.aptUpdates.Load() != 0 {
		t.Fatal("versions refreshed after a failed run")
	}
}

// TestJobSurvivesRestart: the job is read back at start; reboot_required
// holds until the boot id changes.
func TestJobSurvivesRestart(t *testing.T) {
	env := newJobEnv(t, &fakeApt{pkgs: []string{"linux-image-amd64", "calaos-server"}})

	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Run()
	run.Complete()
	want := run.Job()
	if !want.RebootRequired {
		t.Fatal("a kernel upgrade does not require a reboot")
	}

	env.hook(t) // daemon restart, same boot
	loadLastJob()
	if j := CurrentJob(); j == nil || *j != want {
		t.Fatalf("after restart: %+v, want %+v", j, want)
	}

	env.setBootID(t, "boot-2")
	env.hook(t)
	loadLastJob()
	j := CurrentJob()
	if j == nil || j.RebootRequired || j.ID != want.ID {
		t.Fatalf("after reboot: %+v", j)
	}
	if !env.record(t).RebootRequired {
		t.Fatal("the record itself must keep what the job installed")
	}
}

func TestNeedsReboot(t *testing.T) {
	for name, want := range map[string]bool{
		"linux-image-6.1.0-26-amd64": true,
		"linux-image-rpi-v8":         true,
		"linux-firmware":             true,
		"intel-microcode":            true,
		"amd64-microcode":            true,
		"firmware-linux-nonfree":     true,
		"raspberrypi-kernel":         true,
		"raspberrypi-bootloader":     true,
		"raspi-firmware":             true,
		"linux-headers-6.1.0-26":     false,
		"linux-libc-dev":             false,
		"linux-kbuild-6.1":           false,
		"linux-compiler-gcc-12-x86":  false,
		"calaos-container":           false,
		"libc6":                      false,
	} {
		if got := needsReboot(map[string]bool{name: true}); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestJobInterruptedAtStart(t *testing.T) {
	env := newJobEnv(t, &fakeApt{})
	running := jobRecord{Job: Job{ID: "00112233aabbccdd", Kind: JobKindUpgradeAll, Status: JobRunning, StartedAt: "2026-10-22T09:12:03Z"}, BootID: "boot-1"}
	if err := writeJobFile(env.jobFile, running); err != nil {
		t.Fatal(err)
	}

	setUpdateTimeConfig(t, "1h")
	if err := Init(); err != nil {
		t.Fatal(err)
	}
	Shutdown()
	t.Cleanup(resetSharedStatus)

	j := CurrentJob()
	if j == nil || j.ID != running.ID || j.Status != JobError || j.Error != "interrupted" || j.FinishedAt == "" {
		t.Fatalf("job after start: %+v", j)
	}
	if r := env.record(t); r.Status != JobError || r.Error != "interrupted" {
		t.Fatalf("persisted %+v", r)
	}
	if RunningJobID() != "" {
		t.Fatal("an interrupted job still counts as running")
	}
}

func TestJobFileUnreadable(t *testing.T) {
	env := newJobEnv(t, &fakeApt{})
	if err := os.MkdirAll(filepath.Dir(env.jobFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.jobFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadLastJob()
	if CurrentJob() != nil {
		t.Fatal("a corrupt job file produced a job")
	}

	// The next run replaces it.
	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Run()
	run.Complete()
	if env.record(t).ID != run.Job().ID {
		t.Fatal("job file not replaced")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(env.jobFile), ".*tmp*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

// TestJobSelfUpgrade: a run that replaced this daemon asks for the exit only
// after its terminal event, and keeps the lock until then.
func TestJobSelfUpgrade(t *testing.T) {
	env := newJobEnv(t, &fakeApt{pkgs: []string{"calaos-container", "calaos-server"}})
	env.selfNew.Store(true)
	sub := events.Subscribe()
	defer events.Unsubscribe(sub)

	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	nextJobEvent(t, sub)
	if err := run.Run(); err != nil {
		t.Fatal(err)
	}
	if r := env.record(t); !r.DaemonRestart || r.Status != JobDone {
		t.Fatalf("persisted %+v", r)
	}
	select {
	case <-RestartRequested():
		t.Fatal("exit requested before the job completed")
	default:
	}

	var published []Job
	onRestart = func() {
		for len(sub.Ch) > 0 {
			if ev := <-sub.Ch; ev.Type == events.EventUpgradeJob {
				published = append(published, ev.Data.(Job))
			}
		}
		requestRestart()
	}
	t.Cleanup(func() { onRestart = requestRestart })
	run.Complete()
	if len(published) != 1 || !published[0].DaemonRestart || published[0].Status != JobDone {
		t.Fatalf("published when the exit was requested: %+v", published)
	}
	select {
	case <-RestartRequested():
	case <-time.After(5 * time.Second):
		t.Fatal("no exit requested after a self-upgrade")
	}

	if _, err := StartJob(JobKindUpgradeAll, ""); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("a run started on the replaced binary: %v", err)
	}
	if st, _ := UpdateStatus(); st.Status != "done" {
		t.Fatalf("progress %q: the deferred restart must not see idle before the exit", st.Status)
	}
}

// TestJobRestartFollowsTheBinary: only a replaced binary asks for the exit,
// even when the run failed before calaos-container was configured.
func TestJobRestartFollowsTheBinary(t *testing.T) {
	t.Run("package done, binary untouched", func(t *testing.T) {
		newJobEnv(t, &fakeApt{pkgs: []string{"calaos-container"}})
		run, err := StartJob(JobKindUpgradeAll, "")
		if err != nil {
			t.Fatal(err)
		}
		_ = run.Run()
		run.Complete()
		if run.Job().DaemonRestart {
			t.Fatal("restart without a replaced binary")
		}
		select {
		case <-RestartRequested():
			t.Fatal("exit requested")
		default:
		}
	})
	t.Run("binary replaced, configure failed", func(t *testing.T) {
		env := newJobEnv(t, &fakeApt{
			pkgs: []string{"calaos-container"},
			fail: map[string]bool{"calaos-container": true},
			err:  errors.New("dpkg: error processing package calaos-container (--configure)"),
		})
		env.selfNew.Store(true)
		run, err := StartJob(JobKindUpgradeAll, "")
		if err != nil {
			t.Fatal(err)
		}
		_ = run.Run()
		run.Complete()
		if j := run.Job(); !j.DaemonRestart || j.Status != JobError {
			t.Fatalf("job %+v", j)
		}
		select {
		case <-RestartRequested():
		case <-time.After(5 * time.Second):
			t.Fatal("the old binary keeps running")
		}
	})
}

// TestJobKeepsPendingReboot: a kernel installed earlier on this boot still
// needs its reboot after a job that installs no kernel.
func TestJobKeepsPendingReboot(t *testing.T) {
	f := &fakeApt{pkgs: []string{"linux-image-amd64"}}
	env := newJobEnv(t, f)
	runJob := func() Job {
		t.Helper()
		run, err := StartJob(JobKindUpgradeAll, "")
		if err != nil {
			t.Fatal(err)
		}
		_ = run.Run()
		run.Complete()
		return run.Job()
	}

	if !runJob().RebootRequired {
		t.Fatal("kernel job")
	}
	f.pkgs = []string{"calaos-server"}
	if !runJob().RebootRequired {
		t.Fatal("a later job hid the pending reboot")
	}

	env.setBootID(t, "boot-2")
	env.hook(t)
	loadLastJob()
	if runJob().RebootRequired {
		t.Fatal("reboot still required after the reboot")
	}
}

// TestJobIgnoresRemovals: an autoremoved old kernel is no reason to reboot.
func TestJobIgnoresRemovals(t *testing.T) {
	newJobEnv(t, &fakeApt{pkgs: []string{"calaos-server"}, removed: "linux-image-6.1.0-25-amd64"})
	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Run()
	run.Complete()
	if run.Job().RebootRequired {
		t.Fatal("a removed kernel requires a reboot")
	}
}

func TestBootIDFallback(t *testing.T) {
	env := newJobEnv(t, &fakeApt{})
	if err := os.Remove(env.bootID); err != nil {
		t.Fatal(err)
	}
	env.hook(t)
	id := currentBootID()
	if !strings.HasPrefix(id, "btime:") || len(id) <= len("btime:") {
		t.Fatalf("boot id %q", id)
	}
	if currentBootID() != id {
		t.Fatal("boot id not stable")
	}
}

// TestJobDirHardened: the job directory is a real 0700 directory, never a
// symlink, and the job file is never read through a symlink.
func TestJobDirHardened(t *testing.T) {
	env := newJobEnv(t, &fakeApt{})
	dir := filepath.Dir(env.jobFile)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // a loose directory the daemon must tighten
		t.Fatal(err)
	}
	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Run()
	run.Complete()
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("job dir mode: %v %v", fi.Mode(), err)
	}

	// The file replaced by a symlink: not followed.
	target := filepath.Join(t.TempDir(), "forged.json")
	if err := writeJobFile(target, jobRecord{Job: Job{ID: "ffffffffffffffff", Status: JobDone, RebootRequired: true}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(env.jobFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, env.jobFile); err != nil {
		t.Fatal(err)
	}
	env.hook(t)
	loadLastJob()
	if j := CurrentJob(); j != nil {
		t.Fatalf("job read through a symlink: %+v", j)
	}

	// The directory replaced by a symlink: no write through it.
	other := t.TempDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	if err := writeJobFile(env.jobFile, jobRecord{Job: Job{ID: "0123456789abcdef"}}); err == nil {
		t.Fatal("job written through a symlinked directory")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("files written through the symlink: %v", entries)
	}
}

// TestShutdownWaitsForJob: apt cannot be cancelled, Shutdown lets it end.
func TestShutdownWaitsForJob(t *testing.T) {
	started := make(chan struct{})
	env := newJobEnv(t, &fakeApt{pkgs: []string{"calaos-server"}, gate: make(chan struct{}), started: started})
	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = run.Run()
		run.Complete()
	}()
	<-started

	stopped := make(chan struct{})
	go func() {
		Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Shutdown returned while apt runs")
	case <-time.After(100 * time.Millisecond):
	}
	close(env.apt.gate)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after the job")
	}
}

// TestShutdownWaitIsBounded: past the bound the daemon stops anyway, short
// of the unit's SIGKILL.
func TestShutdownWaitIsBounded(t *testing.T) {
	prev := shutdownJobWait
	shutdownJobWait = 50 * time.Millisecond
	t.Cleanup(func() { shutdownJobWait = prev })
	started := make(chan struct{})
	env := newJobEnv(t, &fakeApt{pkgs: []string{"calaos-server"}, gate: make(chan struct{}), started: started})
	run, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = run.Run()
		run.Complete()
	}()
	<-started

	stopped := make(chan struct{})
	go func() {
		Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown waited past its bound")
	}
	close(env.apt.gate)
	_ = jobsDone(t)
}

func jobsDone(t *testing.T) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		jobsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		t.Fatal("job never completed")
		return false
	}
}

// TestPeriodicCheckLoop guards against a loop that runs once: the check
// fires again and again until Shutdown.
func TestPeriodicCheckLoop(t *testing.T) {
	env := newJobEnv(t, &fakeApt{cache: []*apt.Pkg{{Name: "calaos-server", VersionCurrent: "1", VersionNew: "2"}}})
	t.Cleanup(resetSharedStatus)

	setUpdateTimeConfig(t, "5ms")
	if err := Init(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for env.exec.aptUpdates.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	Shutdown()
	n := env.exec.aptUpdates.Load()
	if n < 3 {
		t.Fatalf("%d periodic checks", n)
	}
	time.Sleep(30 * time.Millisecond)
	if env.exec.aptUpdates.Load() != n {
		t.Fatal("the loop kept running after Shutdown")
	}
	if _, ok := GetNewVersions()["calaos-server"]; !ok {
		t.Fatal("periodic check did not fill the versions")
	}
}

// TestCheckCacheErrorSurfaces: a cache that cannot be read is an error, not
// "up to date", and the known versions stay.
func TestCheckCacheErrorSurfaces(t *testing.T) {
	f := &fakeApt{cache: []*apt.Pkg{{Name: "calaos-server", VersionCurrent: "1", VersionNew: "2"}}}
	newJobEnv(t, f)
	if err := CheckUpdates(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.cacheErr = errors.New("E: Could not get lock /var/lib/dpkg/lock")
	f.mu.Unlock()

	err := CheckUpdates()
	if err == nil || !strings.Contains(err.Error(), "apt cache") {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if _, ok := GetNewVersions()["calaos-server"]; !ok {
		t.Fatal("a failed cache read erased the pending updates")
	}
}
