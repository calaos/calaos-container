package models

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/calaos/calaos-container/models/structs"
)

// GetInstalledCalaosPackages lists the currently installed calaos-* packages
// on the system, using dpkg-query directly (not the apt cache), so it
// reflects what is actually unpacked/configured regardless of what apt
// currently knows about upstream versions.
func GetInstalledCalaosPackages() (structs.ImageMap, error) {
	cmd := exec.Command("dpkg-query", "-W",
		"-f=${Package}\t${Version}\t${db:Status-Status}\n", "calaos-*")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// dpkg-query exits with status 1 and no stdout when the pattern
		// matches no installed package at all. That is not an error case
		// for us: it just means there is nothing installed yet.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && stdout.Len() == 0 {
			return structs.ImageMap{}, nil
		}

		e := fmt.Errorf("GetInstalledCalaosPackages error: %s, stderr: %s", err, stderr.String())
		logging.Error(e)
		return nil, e
	}

	return parseDpkgQueryOutput(stdout.String()), nil
}

// parseDpkgQueryOutput parses the output of:
//
//	dpkg-query -W -f='${Package}\t${Version}\t${db:Status-Status}\n' 'calaos-*'
//
// (shell syntax shown for readability; the actual exec.Command call passes the
// arguments without any quote characters since no shell is involved)
//
// into an ImageMap keyed by package name. Only lines whose status is exactly
// "installed" are kept (e.g. "config-files" left over from a purge is
// skipped); malformed lines (wrong field count) are skipped silently.
func parseDpkgQueryOutput(out string) structs.ImageMap {
	imageMap := make(structs.ImageMap)

	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}

		name, version, status := fields[0], fields[1], fields[2]
		if status != "installed" {
			continue
		}

		imageMap[name] = structs.Image{
			Name:    name,
			Version: version,
		}
	}

	return imageMap
}
