package models

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/calaos/calaos-container/apt"
	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/models/structs"
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

	NewVersions = compareVersions(localImageMap, urlImageMap)

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

		NewVersions[p.Name] = structs.Image{
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

func compareVersions(localMap, urlMap structs.ImageMap) structs.ImageMap {
	newVersions := make(structs.ImageMap)

	for name, urlImage := range urlMap {
		localImage, found := localMap[name]
		if !found || localImage.Version != urlImage.Version {
			img := urlImage
			img.CurrentVerion = localImage.Version
			newVersions[name] = img
		}
	}

	return newVersions
}

func Upgrade(pkg string) error {
	_, err := RunCommand("apt-get", "install", "-y", pkg)
	return err
}

func UpgradeAll() error {
	_, err := RunCommand("apt-get", "upgrade", "-y")
	return err
}

func UpdateStatus() (st *structs.Status, err error) {
	st = &structs.Status{}
	return st, nil
}
