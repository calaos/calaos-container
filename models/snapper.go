package models

import (
	"strconv"
	"strings"
)

func createSnapperPreSnapshot(description string) (num int) {
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
	out, err := RunCommand("snapper", "create", "-d", description, "-p", "-c", "number", "-t", "post", "--pre-number="+strconv.Itoa(snap_num))
	if err != nil {
		logging.Errorln("Error creating snapper post snapshot:", out)
	}

	out, err = RunCommand("snapper", "cleanup", "number")
	if err != nil {
		logging.Errorln("Error cleaning up snapper snapshots:", out)
	}
}
