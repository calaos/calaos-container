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
			Name:          p.Name,
			Version:       p.VersionNew,
			CurrentVerion: p.VersionCurrent,
		}
	}

	return nil
}

func LoadFromDisk(filePath string) (structs.ImageMap, error) {
	imageMap := make(structs.ImageMap)
	return imageMap, nil
}

func upgradeDpkg(pkg string) error {
	logging.Debugln("Running: apt-get -qq install", pkg)
	out, err := RunCommand("apt-get", "-qq", "install", pkg)
	logging.Debugln(out)
	return err
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

	status := structs.Status{
		Status:        "upgrading",
		CurrentPkg:    img.Name,
		Progress:      0,
		ProgressTotal: 100,
	}
	upgradeStatus.SetStatus(status)
	defer resetStatus()

	snapperNum := createSnapperPreSnapshot("Calaos upgrade " + pkg + " " + img.Version)
	defer createSnapperPostSnapshot(snapperNum, "Calaos upgrade "+pkg+" "+img.Version)

	return upgradeDpkg(img.Name)
}

func UpgradeAll() error {
	if !upgradeLock.TryAcquire(1) {
		logging.Debugln("UpgradeAll(): Upgrade already in progress")
		return errors.New("upgrade already in progress")
	}
	defer upgradeLock.Release(1)

	//For full upgrade, first update all dkpg packages before containers.
	//This is done to upgrade first calaos-container package that includes all services units

	status := structs.Status{
		Status:        "upgrading",
		Progress:      0,
		ProgressTotal: len(NewVersions),
	}
	upgradeStatus.SetStatus(status)
	defer resetStatus()

	snapperNum := createSnapperPreSnapshot("Calaos upgrade all")
	defer createSnapperPostSnapshot(snapperNum, "Calaos upgrade all")

	//Upgrade dpkg packages
	out, err := RunCommand("apt-get", "-qq", "dist-upgrade")
	logging.Debugln(out)

	if err != nil {
		return err
	}

	status = upgradeStatus.GetStatus()
	for range NewVersions {
		status.Progress++
	}
	upgradeStatus.SetStatus(status)

	return nil
}
