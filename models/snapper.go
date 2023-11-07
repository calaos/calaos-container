package models

import (
	"os"
	"strconv"
	"strings"
)

func createSnapperPreSnapshot(description string) (num int) {
	//check if dir /etc/snapper/configs/root exists
	_, err := os.Stat("/etc/snapper/configs/root")
	if err != nil {
		logging.Errorln("Error checking for snapper config:", err)
		return -1 //snapper is not enabled, do nothing
	}

	out, err := RunCommand("snapper", "create", "-d", description, "-p", "-c", "number", "-t", "pre", "-p")
	if err != nil {
		logging.Errorln("Error creating snapper pre snapshot:", out)
		return -1
	}

	//Parse output
	num, err = strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		logging.Errorf("Error parsing snapper output %s: %v", out, err)
		num = -1
	}

	out, err = RunCommand("snapper", "cleanup", "number")
	if err != nil {
		logging.Errorln("Error cleaning up snapper snapshots:", out)
	}

	return num
}

func createSnapperPostSnapshot(snap_num int, description string) {
	//check if dir /etc/snapper/configs/root exists
	_, err := os.Stat("/etc/snapper/configs/root")
	if snap_num == -1 || err != nil {
		return //snapper is not enabled, do nothing
	}

	out, err := RunCommand("snapper", "create", "-d", description, "-p", "-c", "number", "-t", "post", "--pre-number="+strconv.Itoa(snap_num))
	if err != nil {
		logging.Errorln("Error creating snapper post snapshot:", out)
	}

	out, err = RunCommand("snapper", "cleanup", "number")
	if err != nil {
		logging.Errorln("Error cleaning up snapper snapshots:", out)
	}
}
