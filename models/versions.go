package models

import (
	"bufio"
	"os"
	"sort"
	"strings"

	"github.com/calaos/calaos-container/models/structs"
)

// containerPackage is the deb that ships this daemon: its version is the
// daemon's, there is no version baked into the binary.
const containerPackage = "calaos-container"

// VersionSources are the files the version report reads; tests point them at
// temporary files.
type VersionSources struct {
	OSRelease     string
	DebianVersion string
	KernelRelease string
}

var DefaultVersionSources = VersionSources{
	OSRelease:     "/etc/os-release",
	DebianVersion: "/etc/debian_version",
	KernelRelease: "/proc/sys/kernel/osrelease",
}

type PackageVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type SystemVersions struct {
	CalaosOS  string           `json:"calaos_os"`
	Debian    string           `json:"debian"`
	Kernel    string           `json:"kernel"`
	Container string           `json:"container"`
	Packages  []PackageVersion `json:"packages"`
}

// BuildSystemVersions never fails on an unreadable source: the field is "".
func BuildSystemVersions(src VersionSources, installed structs.ImageMap) SystemVersions {
	v := SystemVersions{
		CalaosOS:  osReleaseVersion(src.OSRelease),
		Debian:    readTrimmed(src.DebianVersion),
		Kernel:    readTrimmed(src.KernelRelease),
		Container: installed[containerPackage].Version,
		Packages:  make([]PackageVersion, 0, len(installed)),
	}
	for name, img := range installed {
		v.Packages = append(v.Packages, PackageVersion{Name: name, Version: img.Version})
	}
	sort.Slice(v.Packages, func(i, j int) bool { return v.Packages[i].Name < v.Packages[j].Name })
	return v
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// osReleaseVersion prefers VERSION_ID and falls back to VERSION.
func osReleaseVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var versionID, version string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"'`)
		switch k {
		case "VERSION_ID":
			versionID = val
		case "VERSION":
			version = val
		}
	}
	if versionID != "" {
		return versionID
	}
	return version
}
