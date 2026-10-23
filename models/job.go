package models

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/calaos/calaos-container/apt"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models/structs"
)

// Upgrade job kinds and states (docs/API.md, Upgrade jobs).
const (
	JobKindUpgradeAll = "upgrade_all"
	JobKindUpgrade    = "upgrade"

	JobRunning = "running"
	JobDone    = "done"
	JobError   = "error"
)

const (
	defaultJobFile    = "/var/log/calaos-container/last_job.json"
	defaultBootIDFile = "/proc/sys/kernel/random/boot_id"
	procStatFile      = "/proc/stat"

	jobErrInterrupted = "interrupted"
	jobErrFailed      = "upgrade failed"

	jobDirMode = 0o700

	maxJobFileSize = 64 * 1024
)

// Job is an upgrade run, as clients see it.
type Job struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Package        string `json:"package,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	RebootRequired bool   `json:"reboot_required"`
	DaemonRestart  bool   `json:"daemon_restart"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at,omitempty"`
}

// jobRecord is the content of the job file. The boot id tells whether the
// reboot a job asked for has happened since.
type jobRecord struct {
	Job
	BootID string `json:"boot_id"`
}

// InProgressError refuses a run while another holds the upgrade lock. JobID is
// empty when the holder is a version check or a reboot, not a job.
type InProgressError struct {
	JobID string
}

func (e *InProgressError) Error() string { return ErrUpgradeInProgress.Error() }

func (e *InProgressError) Is(target error) bool { return target == ErrUpgradeInProgress }

// AptBackend is the part of libapt the update subsystem drives.
type AptBackend interface {
	GetCachePackages() ([]*apt.Pkg, error)
	UpgradeAll(cb func(apt.Progress)) error
	InstallPackage(name string, cb func(apt.Progress)) error
}

type libApt struct{}

func (libApt) GetCachePackages() ([]*apt.Pkg, error)  { return apt.GetCachePackages() }
func (libApt) UpgradeAll(cb func(apt.Progress)) error { return apt.UpgradeAll(cb) }
func (libApt) InstallPackage(n string, cb func(apt.Progress)) error {
	return apt.InstallPackage(n, cb)
}

// UpgradeHooks are the system dependencies of upgrade jobs. Tests replace
// them with SetUpgradeHooks; empty fields keep the defaults.
type UpgradeHooks struct {
	Apt         AptBackend
	JobFile     string
	BootIDFile  string
	ExeReplaced func() bool
}

var (
	hooksMu sync.RWMutex
	hooks   = defaultHooks()

	// jobMu guards lastJob, the running job or else the last finished one.
	jobMu   sync.Mutex
	lastJob *jobRecord

	// bootID is read once per daemon: the boot id cannot change under it.
	bootIDOnce sync.Once
	bootID     string

	// jobsWG counts the runs between StartJob and Complete.
	jobsWG sync.WaitGroup

	// restartCh is closed once a self-upgrade has completed; that run keeps
	// upgradeLock (lockKept) until the process exits.
	restartMu   sync.Mutex
	restartCh   = make(chan struct{})
	restartDone bool
	lockKept    bool
)

func defaultHooks() UpgradeHooks {
	return UpgradeHooks{
		Apt:         libApt{},
		JobFile:     defaultJobFile,
		BootIDFile:  defaultBootIDFile,
		ExeReplaced: exeReplaced,
	}
}

// SetUpgradeHooks swaps the hooks and forgets the in-memory job; restore puts
// both back and frees the lock a self-upgrade keeps.
func SetUpgradeHooks(h UpgradeHooks) (restore func()) {
	d := defaultHooks()
	if h.Apt == nil {
		h.Apt = d.Apt
	}
	if h.JobFile == "" {
		h.JobFile = d.JobFile
	}
	if h.BootIDFile == "" {
		h.BootIDFile = d.BootIDFile
	}
	if h.ExeReplaced == nil {
		h.ExeReplaced = d.ExeReplaced
	}

	hooksMu.Lock()
	prev := hooks
	hooks = h
	hooksMu.Unlock()
	bootIDOnce = sync.Once{}
	jobMu.Lock()
	prevJob := lastJob
	lastJob = nil
	jobMu.Unlock()
	restartMu.Lock()
	prevCh, prevDone := restartCh, restartDone
	restartCh, restartDone = make(chan struct{}), false
	restartMu.Unlock()

	return func() {
		hooksMu.Lock()
		hooks = prev
		hooksMu.Unlock()
		bootIDOnce = sync.Once{}
		jobMu.Lock()
		lastJob = prevJob
		jobMu.Unlock()
		restartMu.Lock()
		if lockKept {
			lockKept = false
			upgrading.Store(false)
			upgradeLock.Release(1)
		}
		restartCh, restartDone = prevCh, prevDone
		restartMu.Unlock()
	}
}

func currentHooks() UpgradeHooks {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	return hooks
}

// RestartRequested is closed once a job has installed a new calaos-container
// and published its end: the daemon must exit so systemd starts the new
// binary.
func RestartRequested() <-chan struct{} {
	restartMu.Lock()
	defer restartMu.Unlock()
	return restartCh
}

// onRestart lets tests check what is already published when the exit is
// requested.
var onRestart = requestRestart

func requestRestart() {
	restartMu.Lock()
	defer restartMu.Unlock()
	lockKept = true
	if !restartDone {
		restartDone = true
		close(restartCh)
	}
}

// exeReplaced reports whether dpkg has replaced the binary this process runs.
func exeReplaced() bool {
	p, err := os.Readlink("/proc/self/exe")
	return err == nil && strings.HasSuffix(p, " (deleted)")
}

// currentBootID identifies this boot. Without boot_id it falls back on the
// boot time of /proc/stat, which changes at every boot too; without either,
// "" keeps every recorded reboot_required as it is.
func currentBootID() string {
	bootIDOnce.Do(func() {
		path := currentHooks().BootIDFile
		b, err := os.ReadFile(path) //nolint:gosec // fixed system path
		if id := strings.TrimSpace(string(b)); err == nil && id != "" {
			bootID = id
			return
		}
		if bt := bootTime(); bt != "" {
			logging.Warnf("reading %s: %v; using the boot time instead", path, err)
			bootID = "btime:" + bt
			return
		}
		logging.Warnf("reading %s: %v; reboot_required will not reset after a reboot", path, err)
		bootID = ""
	})
	return bootID
}

func bootTime() string {
	b, err := os.ReadFile(procStatFile)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func newJobID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on Linux; an id only has to be unique.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// CurrentJob returns the running job, else the last finished one, else nil.
// reboot_required turns false once the box has rebooted since the job.
func CurrentJob() *Job {
	jobMu.Lock()
	rec := lastJob
	jobMu.Unlock()
	if rec == nil {
		return nil
	}
	j := rec.Job
	if j.RebootRequired && rec.BootID != "" {
		if cur := currentBootID(); cur != "" && cur != rec.BootID {
			j.RebootRequired = false
		}
	}
	return &j
}

// RunningJobID returns the id of the running job, or "".
func RunningJobID() string {
	jobMu.Lock()
	defer jobMu.Unlock()
	if lastJob != nil && lastJob.Status == JobRunning {
		return lastJob.ID
	}
	return ""
}

func setLastJob(rec jobRecord) {
	jobMu.Lock()
	lastJob = &rec
	jobMu.Unlock()
}

// saveJob replaces the job file atomically. A failure is logged only: the run
// goes on, and the in-memory job still answers until the next restart.
func saveJob(rec jobRecord) {
	path := currentHooks().JobFile
	if err := writeJobFile(path, rec); err != nil {
		logging.Errorf("saving the upgrade job to %s: %v", path, err)
	}
}

func writeJobFile(path string, rec jobRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := prepareJobDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	d, err := os.Open(dir) //nolint:gosec // daemon-owned directory
	if err != nil {
		return err
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

// loadLastJob reads the job file at startup. A job still running there was
// cut short (crash, OOM kill, power loss): it becomes an interrupted error.
func loadLastJob() {
	path := currentHooks().JobFile
	data, err := readJobFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		logging.Errorf("reading the upgrade job from %s: %v", path, err)
		return
	}

	var rec jobRecord
	err = json.Unmarshal(data, &rec)
	if err == nil && !validJobStatus(rec.Status) {
		err = fmt.Errorf("unknown status %q", rec.Status)
	}
	if err != nil || rec.ID == "" {
		logging.Errorf("ignoring an invalid upgrade job file %s: %v", path, err)
		return
	}
	if rec.Status == JobRunning {
		logging.Warnf("upgrade job %s was interrupted", rec.ID)
		rec.Status = JobError
		rec.Error = jobErrInterrupted
		rec.FinishedAt = nowRFC3339()
		saveJob(rec)
	}
	setLastJob(rec)
}

func validJobStatus(s string) bool {
	return s == JobRunning || s == JobDone || s == JobError
}

// prepareJobDir makes the job directory a real 0700 directory of ours: the
// record decides what admins are told (reboot_required, interrupted runs).
func prepareJobDir(dir string) error {
	if err := os.MkdirAll(dir, jobDirMode); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is owned by uid %d", dir, st.Uid)
	}
	if fi.Mode().Perm() != jobDirMode {
		if err := os.Chmod(dir, jobDirMode); err != nil { //nolint:gosec // a directory needs x
			return err
		}
	}
	// A crash between writing and renaming leaves a temp file behind.
	stale, _ := filepath.Glob(filepath.Join(dir, ".*.tmp*"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	return nil
}

func readJobFile(path string) ([]byte, error) {
	if fi, err := os.Lstat(filepath.Dir(path)); err == nil && !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", filepath.Dir(path))
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // daemon-owned path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxJobFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxJobFileSize {
		return nil, fmt.Errorf("larger than %d bytes", maxJobFileSize)
	}
	return b, nil
}

// JobRun is a job holding the upgrade lock. Run it, then Complete it once the
// client that started it has its answer.
type JobRun struct {
	rec     jobRecord
	img     structs.Image
	runOnce sync.Once
	runErr  error
	done    sync.Once

	pendingReboot bool
}

// StartJob takes the upgrade lock and records a running job. Every upgrade
// goes through it, whatever the route, so one job exists per run.
func StartJob(kind, pkg string) (*JobRun, error) {
	if kind != JobKindUpgradeAll && kind != JobKindUpgrade {
		return nil, fmt.Errorf("unknown job kind %q", kind)
	}
	if !upgradeLock.TryAcquire(1) {
		logging.Debugln("StartJob(): upgrade already in progress")
		return nil, &InProgressError{JobID: RunningJobID()}
	}
	upgrading.Store(true)

	r := &JobRun{}
	// A kernel installed by an earlier job on this boot still waits for its
	// reboot: a later job must not hide it.
	if prev := CurrentJob(); prev != nil && prev.RebootRequired {
		r.pendingReboot = true
	}
	if kind == JobKindUpgrade {
		img, found, held := getNewVersion(pkg)
		if !found {
			upgrading.Store(false)
			upgradeLock.Release(1)
			if held {
				logging.WithField("pkg", pkg).Errorln("Package held")
				return nil, ErrPackageHeld
			}
			logging.WithField("pkg", pkg).Errorln("Package not found")
			return nil, ErrPackageNotFound
		}
		r.img = img
	} else {
		pkg = ""
	}

	r.rec = jobRecord{
		Job: Job{
			ID:        newJobID(),
			Kind:      kind,
			Package:   pkg,
			Status:    JobRunning,
			StartedAt: nowRFC3339(),
		},
		BootID: currentBootID(),
	}
	setLastJob(r.rec)
	saveJob(r.rec)
	jobsWG.Add(1)

	initUpgradeStatus()
	events.Publish(events.EventUpgradeJob, r.rec.Job)

	return r, nil
}

// Job returns the job as it was created.
func (r *JobRun) Job() Job {
	return r.rec.Job
}

// Run drives apt and records the outcome, persisted, before it returns the
// apt error. The upgrade lock stays held until Complete.
func (r *JobRun) Run() error {
	r.runOnce.Do(func() { r.runErr = r.run() })
	return r.runErr
}

func (r *JobRun) run() error {
	h := currentHooks()

	desc := "Calaos upgrade all"
	if r.rec.Kind == JobKindUpgrade {
		desc = "Calaos upgrade " + r.rec.Package + " " + r.img.Version
	}
	snapperNum := createSnapperPreSnapshot(desc)

	var err error
	if r.rec.Kind == JobKindUpgrade {
		err = h.Apt.InstallPackage(r.img.Name, applyAptProgress)
	} else {
		err = h.Apt.UpgradeAll(applyAptProgress)
	}

	// The terminal progress goes out before anything slow: old clients stop
	// on it.
	finishUpgrade(err)

	createSnapperPostSnapshot(snapperNum, desc)

	if err == nil {
		if rerr := refreshNewVersions(); rerr != nil {
			logging.Warnf("Failed to refresh versions after upgrade: %v", rerr)
		}
	}

	st := upgradeStatus.GetStatus()
	installed := installedPackages(st)

	rec := r.rec
	rec.Status = JobDone
	if err != nil {
		logging.Errorf("upgrade job %s failed: %v", rec.ID, err)
		rec.Status = JobError
		rec.Error = jobErrorSummary(st)
	}
	rec.RebootRequired = r.pendingReboot || needsReboot(installed)
	// The replaced binary is what matters, even when dpkg did not get to
	// configure the package: the restart's dpkg --configure -a finishes it.
	rec.DaemonRestart = h.ExeReplaced()
	rec.FinishedAt = nowRFC3339()

	r.rec = rec
	setLastJob(rec)
	saveJob(rec)

	return err
}

// Complete publishes the terminal job. It releases the upgrade lock, unless
// the run replaced this daemon: the lock then stays held and the daemon is
// asked to exit, so no new run starts on the old binary.
func (r *JobRun) Complete() {
	r.done.Do(func() {
		job := r.rec.Job
		if !job.DaemonRestart {
			resetStatus()
			upgrading.Store(false)
			upgradeLock.Release(1)
		}
		events.Publish(events.EventUpgradeJob, job)
		jobsWG.Done()
		if job.DaemonRestart {
			logging.Infof("upgrade job %s installed a new calaos-container", job.ID)
			onRestart()
		}
	})
}

// installedPackages returns the packages of the transaction that apt
// installed or upgraded, removals excluded.
func installedPackages(st structs.Status) map[string]bool {
	m := map[string]bool{}
	for _, p := range st.Packages {
		if p.State == structs.PkgStateDone && !isRemoved(p.Name) {
			m[p.Name] = true
		}
	}
	return m
}

// rebootPrefixes and rebootNames are what only takes effect after a reboot:
// kernels, firmware and microcode, Raspberry Pi ones included.
var (
	rebootPrefixes = []string{"linux-image", "linux-firmware"}
	rebootNames    = map[string]bool{
		"firmware-linux": true, "firmware-linux-nonfree": true,
		"intel-microcode": true, "amd64-microcode": true,
		"raspberrypi-kernel": true, "raspberrypi-bootloader": true, "raspi-firmware": true,
	}
)

func needsReboot(installed map[string]bool) bool {
	for name := range installed {
		if rebootNames[name] {
			return true
		}
		for _, p := range rebootPrefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
	}
	return false
}

// jobErrorSummary names the failed packages, never apt or dpkg output: the
// job is shown to remote clients, the journal has the details.
func jobErrorSummary(st structs.Status) string {
	var failed []string
	for _, p := range st.Packages {
		if p.State == structs.PkgStateFailed {
			failed = append(failed, p.Name)
		}
	}
	if len(failed) == 0 {
		return jobErrFailed
	}
	return jobErrFailed + ": " + strings.Join(failed, ", ")
}
