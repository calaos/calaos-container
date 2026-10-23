package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/calaos/calaos-container/apt"
	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/models/structs"
)

// The messages are part of the legacy API: old kiosks match "in progress"
// and "package not found" in the error body.
var (
	ErrUpgradeInProgress = errors.New("upgrade already in progress")
	ErrPackageNotFound   = errors.New("package not found")
)

// ErrPackageHeld refuses a single-package upgrade of a package on hold.
var ErrPackageHeld = errors.New("package held")

// upgrading is true while a job holds upgradeLock, from StartJob to Complete,
// so RebootGuard can tell an upgrade from a version check.
var upgrading atomic.Bool

// rebootLockWait bounds how long RebootGuard waits for a version check to
// release upgradeLock.
var rebootLockWait = 30 * time.Second

// RebootGuard runs fn, a reboot or a rollback, unless an upgrade holds
// upgradeLock. On success the lock stays held: the system is going down and
// no upgrade may start meanwhile. Behind a version check it waits for the
// lock; a queued waiter makes TryAcquire fail, so no upgrade slips in. A
// holder that does not release in time is treated as an upgrade that has not
// raised its flag yet.
func RebootGuard(fn func() error) error {
	if !upgradeLock.TryAcquire(1) {
		if upgrading.Load() {
			return ErrUpgradeInProgress
		}
		ctx, cancel := context.WithTimeout(context.Background(), rebootLockWait)
		defer cancel()
		if err := upgradeLock.Acquire(ctx, 1); err != nil {
			return ErrUpgradeInProgress
		}
	}
	if err := fn(); err != nil {
		upgradeLock.Release(1)
		return err
	}
	return nil
}

// parseUpdateTime reads and validates general.update_time from the config. It
// is called from Init() so that a bad value aborts startup with a clear error
// instead of killing the process from inside a goroutine.
func parseUpdateTime() (time.Duration, error) {
	raw := config.Config.String("general.update_time")

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid general.update_time %q: %w", raw, err)
	}
	if d <= 0 {
		// time.NewTicker panics on a non-positive duration.
		return 0, fmt.Errorf("invalid general.update_time %q: must be a positive duration", raw)
	}

	return d, nil
}

// checkForUpdatesLoop runs the periodic update check every updateTime until
// quit is closed. Both the interval and the quit channel are passed by Init()
// rather than read from package state, so the goroutine never races with a
// concurrent Init()/Shutdown().
func checkForUpdatesLoop(updateTime time.Duration, quit <-chan interface{}) {
	defer wgDone.Done()

	ticker := time.NewTicker(updateTime)
	defer ticker.Stop()

	logging.Debugf("Periodic update check started (every %s)", updateTime)

	for {
		select {
		case <-quit:
			logging.Debugln("Exit checkForUpdates goroutine")
			return
		case <-ticker.C:
			runPeriodicCheck()
		}
	}
}

// runPeriodicCheck performs a single periodic check. It is a separate function
// so that nothing deferred inside a tick accumulates for the lifetime of the
// loop. A failing check is logged and the loop keeps ticking.
func runPeriodicCheck() {
	logging.Debugln("Periodic update check firing")

	if err := checkForUpdates(); err != nil {
		logging.Warnf("Periodic update check failed: %v", err)
	}
}

// CheckUpdates manually check for updates online
func CheckUpdates() error {
	return checkForUpdates()
}

// checkForUpdates refreshes the available versions under upgradeLock. A job,
// which already holds upgradeLock, calls refreshNewVersions() directly.
func checkForUpdates() error {
	if !upgradeLock.TryAcquire(1) {
		logging.Debugln("checkForUpdates(): Upgrade already in progress")
		return ErrUpgradeInProgress
	}
	defer upgradeLock.Release(1)

	return refreshNewVersions()
}

// GetNewVersions returns a copy of the currently known available versions. The
// copy is what callers get so that they can read/marshal it without holding a
// lock while a check rebuilds the shared map.
func GetNewVersions() structs.ImageMap {
	newVersionsMu.RLock()
	defer newVersionsMu.RUnlock()

	m := make(structs.ImageMap, len(newVersions))
	for k, v := range newVersions {
		m[k] = v
	}

	return m
}

// getNewVersion looks up a single package in the available versions. held
// reports a package left out of them because it is on hold.
func getNewVersion(pkg string) (img structs.Image, found, held bool) {
	newVersionsMu.RLock()
	defer newVersionsMu.RUnlock()

	img, found = newVersions[pkg]

	return img, found, heldPkgs[pkg]
}

// setNewVersions atomically replaces the shared available versions and the
// set of held packages.
func setNewVersions(m structs.ImageMap, held map[string]bool) {
	newVersionsMu.Lock()
	defer newVersionsMu.Unlock()

	newVersions = m
	heldPkgs = held
}

// refreshNewVersions runs `apt-get update` and rebuilds the available versions
// from the apt cache. It does NOT touch upgradeLock: callers that already hold
// it (an in-progress upgrade refreshing the list after success) call this
// directly, while checkForUpdates() acquires the lock first.
//
// The new map is built into a local variable and swapped in at the end, so
// newVersionsMu is never held across the apt-get run or the cgo cache call, and
// readers never observe a half-rebuilt map.
//
// A failing `apt-get update` is reported to the caller (so GET /update/check
// surfaces it) but the map is still rebuilt from the local apt cache: the cache
// remains the best available truth, it simply may be stale with regard to the
// remote indexes. A failing cache read, on the other hand, leaves the known
// versions untouched and is reported as an error.
func refreshNewVersions() error {
	logging.Infoln("Checking for updates")
	logging.Infoln("Checking dpkg updates")

	//Update apt cache without interaction
	out, aptErr := RunCommand("apt-get", "update", "-qq")
	logging.Debugln("apt-get update output:", out)
	if aptErr != nil {
		logging.Errorf("apt-get update failed: %v", aptErr)
	}

	versions := structs.ImageMap{}
	held := map[string]bool{}

	// A failing cache read leaves us without any trustworthy view of the
	// available updates. Report it (so GET /update/check answers an error
	// instead of "already up to date") and keep the previously known map: a
	// transient apt/dpkg lock must not erase pending updates.
	pkgs, cacheErr := currentHooks().Apt.GetCachePackages()
	if cacheErr != nil {
		logging.Errorf("Failed to read the apt cache: %v", cacheErr)

		err := fmt.Errorf("failed to read the apt cache: %w", cacheErr)
		if aptErr != nil {
			err = errors.Join(fmt.Errorf("apt-get update failed: %w", aptErr), err)
		}

		return err
	}

	for _, p := range pkgs {
		// An upgrade-all keeps a held package back, so it is no pending update.
		if p.Held {
			logging.Infof("%s: %s  -->  %s (held)\n", p.Name, p.VersionCurrent, p.VersionNew)
			held[p.Name] = true
			continue
		}
		logging.Infof("%s: %s  -->  %s\n", p.Name, p.VersionCurrent, p.VersionNew)

		versions[p.Name] = structs.Image{
			Name:           p.Name,
			Version:        p.VersionNew,
			CurrentVersion: p.VersionCurrent,
		}
	}

	setNewVersions(versions, held)

	if aptErr != nil {
		return fmt.Errorf("apt-get update failed: %w", aptErr)
	}

	return nil
}

type MultiError struct {
	Errors []error
}

func (m *MultiError) Error() string {
	var errs []string
	for _, err := range m.Errors {
		errs = append(errs, err.Error())
	}
	return strings.Join(errs, ", ")
}

// initUpgradeStatus resets the shared upgrade Status to a fresh "upgrading"
// state. The Packages list starts empty and is filled from the "pending"
// events emitted by the C++ layer (the exact transaction, dependencies
// included).
func initUpgradeStatus() {
	removedMu.Lock()
	removedPkgs = map[string]bool{}
	removedMu.Unlock()
	upgradeStatus.SetStatus(structs.Status{
		Status:        "upgrading",
		TotalPercent:  0,
		Progress:      0,
		ProgressTotal: 0,
	})
}

// pkgStepStates maps an apt Progress.Stage to the per-package state stored in
// Status.Packages.
func pkgStateForStage(stage string) string {
	switch stage {
	case "pending":
		return structs.PkgStatePending
	case "download":
		return structs.PkgStateDownloading
	case "unpack":
		return structs.PkgStateUnpacking
	case "configure", "install":
		return structs.PkgStateInstalling
	case "done":
		return structs.PkgStateDone
	case "error":
		return structs.PkgStateFailed
	default:
		return ""
	}
}

// applyAptProgress is the progress callback of the apt transactions.
// It maps one apt.Progress event onto the shared upgrade Status and publishes
// it on the hub (through SetStatus).
//
// It is invoked synchronously from the cgo thread while dpkg is suspended, so
// it MUST never block: it only does a Get/SetStatus (the hub Publish is
// non-blocking).
// normalizePkgName strips a trailing ":arch" qualifier (e.g. "libtasn1-6:amd64"
// -> "libtasn1-6"). The C++ layer announces packages by their bare name during
// the "pending"/"download" stages but dpkg reports the arch-qualified name
// during unpack/configure. Without normalization the same package would be
// tracked under two distinct entries, doubling PkgCount.
func normalizePkgName(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[:i]
	}
	return name
}

func applyAptProgress(p apt.Progress) {
	p.Pkg = normalizePkgName(p.Pkg)
	st := upgradeStatus.GetStatus()

	// A "pending" event declares a package that is part of the transaction.
	if p.Stage == "pending" {
		if p.Msg == "remove" {
			removedMu.Lock()
			removedPkgs[p.Pkg] = true
			removedMu.Unlock()
		}
		st.Packages = append(st.Packages, structs.PkgStatus{
			Name:  p.Pkg,
			State: structs.PkgStatePending,
		})
		st.PkgCount = len(st.Packages)
	}

	// Update the per-package entry for the package this event concerns.
	if p.Pkg != "" {
		state := pkgStateForStage(p.Stage)
		idx := -1
		for i := range st.Packages {
			if st.Packages[i].Name == p.Pkg {
				idx = i
				break
			}
		}
		if idx < 0 && p.Stage != "pending" {
			// Package not previously announced via "pending" (should not
			// happen, but stay robust): register it now.
			st.Packages = append(st.Packages, structs.PkgStatus{Name: p.Pkg})
			st.PkgCount = len(st.Packages)
			idx = len(st.Packages) - 1
		}
		if idx >= 0 {
			if state != "" && p.Stage != "pending" {
				st.Packages[idx].State = state
			}
			switch p.Stage {
			case "done":
				st.Packages[idx].Percent = 100
			default:
				if p.PkgPercent >= 0 {
					st.Packages[idx].Percent = p.PkgPercent
				}
			}
			// PkgIndex is 1-based within Packages.
			st.PkgIndex = idx + 1
		}
	}

	// Current-transaction fields.
	if p.Pkg != "" {
		st.CurrentPkg = p.Pkg
	}
	st.CurrentStep = p.Stage
	if p.PkgPercent >= 0 {
		st.CurrentPercent = p.PkgPercent
	}
	st.TotalPercent = p.TotalPercent
	// apt messages carry repository URLs and archive paths: they stay in the
	// journal, the progress goes to every event subscriber.
	logging.Debugf("apt %s %s: %s", p.Stage, p.Pkg, p.Msg)

	// Retro-compat with the old coarse progress fields.
	st.Progress = st.PkgIndex
	st.ProgressTotal = st.PkgCount

	upgradeStatus.SetStatus(st)
}

// removedPkgs are the packages the current transaction removes, guarded by
// removedMu: apt reports them done like the installed ones.
var (
	removedMu   sync.Mutex
	removedPkgs = map[string]bool{}
)

func isRemoved(name string) bool {
	removedMu.Lock()
	defer removedMu.Unlock()
	return removedPkgs[name]
}

// finishUpgrade writes the terminal status (done/error) on the shared Status.
// Error gets the job's summary: the apt error itself is only logged.
// It MUST be called before resetStatus() so the terminal event is published
// on the hub before the return-to-idle event.
func finishUpgrade(err error) {
	st := upgradeStatus.GetStatus()

	if err != nil {
		st.Status = "error"
		st.Error = jobErrorSummary(st)
		upgradeStatus.SetStatus(st)
		return
	}

	st.Status = "done"
	st.TotalPercent = 100
	for i := range st.Packages {
		if st.Packages[i].State != structs.PkgStateFailed {
			st.Packages[i].State = structs.PkgStateDone
			st.Packages[i].Percent = 100
		}
	}
	st.Error = ""
	upgradeStatus.SetStatus(st)
}
