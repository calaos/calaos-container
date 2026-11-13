package models

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/calaos/calaos-container/models/structs"
)

// containerPackage is the deb that ships this daemon: its version is the
// daemon's, there is no version baked into the binary.
const containerPackage = "calaos-container"

// calaosOSPackage is the meta-package whose dpkg version is the Calaos OS
// release. It is not matched by the calaos-* inventory glob.
const calaosOSPackage = "calaos"

// calendarVersion is the Calaos OS release scheme: YYYY.MM, YYYY.MM.N, with an
// optional ~dev.N pre-release suffix.
var calendarVersion = regexp.MustCompile(`^\d{4}\.\d{2}(\.\d+)?(~dev\.\d+)?$`)

// CalaosOSVersion returns the version reported by dpkg for the calaos package,
// or "" when it is not a calendar version. It never falls back to Debian's.
func CalaosOSVersion(dpkgVersion string) string {
	v := strings.TrimSpace(dpkgVersion)
	if !calendarVersion.MatchString(v) {
		return ""
	}
	return v
}

// VersionSources are the files the version report reads; tests point them at
// temporary files.
type VersionSources struct {
	DebianVersion string
	KernelRelease string
}

var DefaultVersionSources = VersionSources{
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
func BuildSystemVersions(src VersionSources, calaosOS string, installed structs.ImageMap) SystemVersions {
	v := SystemVersions{
		CalaosOS:  CalaosOSVersion(calaosOS),
		Debian:    readTrimmed(src.DebianVersion),
		Kernel:    readTrimmed(src.KernelRelease),
		Container: installed[containerPackage].Version,
		Packages:  make([]PackageVersion, 0, len(installed)),
	}
	for name, img := range installed {
		if name == calaosOSPackage {
			continue
		}
		v.Packages = append(v.Packages, PackageVersion{Name: name, Version: img.Version})
	}
	sort.Slice(v.Packages, func(i, j int) bool { return v.Packages[i].Name < v.Packages[j].Name })
	return v
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // fixed system paths, overridable only by tests
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
