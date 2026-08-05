package models

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/calaos/calaos-container/apt"
	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/models/structs"
	"github.com/sirupsen/logrus"
)

func checkForUpdatesLoop() {
	defer wgDone.Done()

	//Parse duration from config
	updateTime, err := time.ParseDuration(config.Config.String("general.update_time"))
	if err != nil {
		logging.Fatalf("Failed to parse update_time duration: %v", err)
		return
	}

	for {
		select {
		case <-quitCheckUpdate:
			logging.Debugln("Exit checkForUpdates goroutine")
			return
		case <-time.After(updateTime):
			if muCheck.TryLock() {
				defer muCheck.Unlock()

				checkForUpdates()
			}
			return
		}
	}
}

// CheckUpdates manually check for updates online
func CheckUpdates() error {
	muCheck.Lock()
	defer muCheck.Unlock()

	return checkForUpdates()
}

func checkForUpdates() error {
	if !upgradeLock.TryAcquire(1) {
		logging.Debugln("checkForUpdates(): Upgrade already in progress")
		return errors.New("upgrade already in progress")
	}
	defer upgradeLock.Release(1)

	return refreshNewVersions()
}

// refreshNewVersions runs `apt-get update` and rebuilds NewVersions from the
// apt cache. It does NOT touch upgradeLock: callers that already hold it (an
// in-progress upgrade refreshing the list after success) call this directly,
// while checkForUpdates() acquires the lock first.
func refreshNewVersions() error {
	logging.Infoln("Checking for updates")

	NewVersions = structs.ImageMap{}

	logging.Infoln("Checking dpkg updates")

	//Update apt cache without interaction
	out, _ := RunCommand("apt-get", "update", "-qq")
	logging.Debugln("apt-get update output:", out)

	pkgs := apt.GetCachePackages()
	for _, p := range pkgs {
		logging.Infof("%s: %s  -->  %s\n", p.Name, p.VersionCurrent, p.VersionNew)

		NewVersions[p.Name] = structs.Image{
			Name:           p.Name,
			Version:        p.VersionNew,
			CurrentVersion: p.VersionCurrent,
		}
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

// applyAptProgress is the callback passed to apt.UpgradeAll / apt.InstallPackage.
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
	st.Message = p.Msg

	// Retro-compat with the old coarse progress fields.
	st.Progress = st.PkgIndex
	st.ProgressTotal = st.PkgCount

	upgradeStatus.SetStatus(st)
}

// finishUpgrade writes the terminal status (done/error) on the shared Status.
// It MUST be called before resetStatus() so the terminal event is published
// on the hub before the return-to-idle event.
func finishUpgrade(err error) {
	st := upgradeStatus.GetStatus()

	if err != nil {
		st.Status = "error"
		st.Error = err.Error()
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

func Upgrade(pkg string) error {
	if !upgradeLock.TryAcquire(1) {
		logging.Debugln("Upgrade(): Upgrade already in progress")
		return errors.New("upgrade already in progress")
	}
	defer upgradeLock.Release(1)

	found := false
	//search for package in cache
	for name := range NewVersions {
		if name == pkg {
			//found package, upgrade it
			found = true
		}
	}

	if !found {
		logging.WithFields(logrus.Fields{
			"pkg": pkg,
		}).Errorln("Package not found")
		return fmt.Errorf("package not found")
	}

	img := NewVersions[pkg]

	initUpgradeStatus()

	snapperNum := createSnapperPreSnapshot("Calaos upgrade " + pkg + " " + img.Version)

	err := apt.InstallPackage(img.Name, applyAptProgress)

	// Publish the terminal (done/error) status BEFORE returning to idle: the
	// CLI exits on the terminal event, the reset comes after.
	finishUpgrade(err)

	createSnapperPostSnapshot(snapperNum, "Calaos upgrade "+pkg+" "+img.Version)

	if err == nil {
		// Refresh the available versions now that the upgrade succeeded. The
		// upgradeLock is still held here, so call the lock-free refresh.
		if rerr := refreshNewVersions(); rerr != nil {
			logging.Warnf("Failed to refresh versions after upgrade: %v", rerr)
		}
	}

	resetStatus()

	return err
}

func UpgradeAll() error {
	if !upgradeLock.TryAcquire(1) {
		logging.Debugln("UpgradeAll(): Upgrade already in progress")
		return errors.New("upgrade already in progress")
	}
	defer upgradeLock.Release(1)

	//For full upgrade, first update all dkpg packages before containers.
	//This is done to upgrade first calaos-container package that includes all services units

	initUpgradeStatus()

	snapperNum := createSnapperPreSnapshot("Calaos upgrade all")

	//Upgrade dpkg packages
	err := apt.UpgradeAll(applyAptProgress)

	// Publish the terminal (done/error) status BEFORE returning to idle.
	finishUpgrade(err)

	createSnapperPostSnapshot(snapperNum, "Calaos upgrade all")

	if err == nil {
		// Refresh the available versions now that the upgrade succeeded. The
		// upgradeLock is still held here, so call the lock-free refresh.
		if rerr := refreshNewVersions(); rerr != nil {
			logging.Warnf("Failed to refresh versions after upgrade: %v", rerr)
		}
	}

	resetStatus()

	return err
}
