package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

/*
{
    "images": [
        {
            "name": "calaos_home",
            "image": "ghcr.io/calaos/calaos_home:4.2.6",
            "version": "4.2.6"
        },
        {
            "name": "calaos_base",
            "image": "ghcr.io/calaos/calaos_base:4.8.1",
            "version": "4.8.1"
        }
    ]
}
*/

func checkForUpdates() error {
	if !upgradeLock.TryAcquire(1) {
		logging.Debugln("checkForUpdates(): Upgrade already in progress")
		return errors.New("upgrade already in progress")
	}
	defer upgradeLock.Release(1)

	logging.Infoln("Checking for updates")

	logging.Infoln("Checking container images")
	localImageMap, err := LoadFromDisk(config.Config.String("general.version_file"))
	if err != nil {
		logging.Errorln("Error loading local JSON:", err)
		return err
	}

	urlImageMap, err := downloadFromURL(config.Config.String("general.url_releases"))
	if err != nil {
		logging.Errorln("Error downloading JSON from URL:", err)
		return err
	}

	NewVersions = compareCtVersions(localImageMap, urlImageMap)

	logging.Info("New Versions:")
	for name, newVersion := range NewVersions {
		v, found := localImageMap[name]
		localVersion := "N/A"
		if found {
			localVersion = v.Version
		}
		logging.Infof("%s: %s  -->  %s\n", name, localVersion, newVersion.Version)
	}

	logging.Infoln("Checking dpkg updates")

	//Update apt cache without interaction
	out, _ := RunCommand("apt-get", "update", "-qq")
	logging.Debugln("apt-get update output:", out)

	pkgs := apt.GetCachePackages()
	for _, p := range pkgs {
		logging.Infof("%s: %s  -->  %s\n", p.Name, p.VersionCurrent, p.VersionNew)

		NewVersions["dpkg/"+p.Name] = structs.Image{
			Name:          p.Name,
			Source:        "dpkg",
			Version:       p.VersionNew,
			CurrentVerion: p.VersionCurrent,
		}
	}

	return nil
}

func LoadFromDisk(filePath string) (structs.ImageMap, error) {
	_, err := os.Stat(filePath)
	if err != nil {
		// File does not exist, return an empty ImageMap without error
		return make(structs.ImageMap), nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var imageList structs.ImageList
	if err := json.Unmarshal(data, &imageList); err != nil {
		return nil, err
	}

	imageMap := make(structs.ImageMap)
	for _, img := range imageList.Images {
		imageMap[img.Name] = img
	}

	return imageMap, nil
}

func downloadFromURL(url string) (structs.ImageMap, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var imageList structs.ImageList
	if err := json.Unmarshal(data, &imageList); err != nil {
		return nil, err
	}

	imageMap := make(structs.ImageMap)
	for _, img := range imageList.Images {
		imageMap[img.Name] = img
	}

	return imageMap, nil
}

func compareCtVersions(localMap, urlMap structs.ImageMap) structs.ImageMap {
	newVersions := make(structs.ImageMap)

	for name, urlImage := range urlMap {
		localImage, found := localMap[name]
		if !found || localImage.Version != urlImage.Version {
			img := urlImage
			img.CurrentVerion = localImage.Version
			newVersions["docker/"+name] = img
		}
	}

	return newVersions
}

func upgradeDpkg(pkg string) error {
	logging.Debugln("Running: apt-get -qq install", pkg)
	out, err := RunCommand("apt-get", "-qq", "install", pkg)
	logging.Debugln(out)
	return err
}

func upgradeDocker(pkg string) error {
	logging.Debugln("Running: podman pull", pkg)
	err := Pull(pkg)
	if err != nil {
		logging.Errorln("Error pulling image:", err)
	}

	//Stop container
	logging.Debugln("Stopping container", pkg)
	err = StopUnit(pkg)
	if err != nil {
		logging.Errorln("Error stopping container:", err)
	}

	//Start container again
	logging.Debugln("Starting container", pkg)
	err = StartUnit(pkg)
	if err != nil {
		logging.Errorln("Error starting container:", err)
	}

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

	if img.Source == "dpkg" {
		return upgradeDpkg(img.Name)
	} else { //docker image
		return upgradeDocker(img.Name)
	}
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

	var multiErr MultiError

	status = upgradeStatus.GetStatus()
	for _, img := range NewVersions {
		if img.Source == "dpkg" {
			status.Progress++
		}
	}
	upgradeStatus.SetStatus(status)

	//Upgrade all docker images
	for name, img := range NewVersions {
		if img.Source == "docker" {

			status = upgradeStatus.GetStatus()
			status.CurrentPkg = name
			status.Progress++
			upgradeStatus.SetStatus(status)

			err = upgradeDocker(name)
			if err != nil {
				multiErr.Errors = append(multiErr.Errors, fmt.Errorf("error upgrading %s: %v", name, err))
			}
		}
	}

	if len(multiErr.Errors) > 0 {
		return &multiErr
	}

	return nil
}
