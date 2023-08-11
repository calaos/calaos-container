package models

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

type SystemInfo struct {
	Hostname           string `json:"hostname"`
	Uptime             int64  `json:"uptime"`
	CPUUsagePercent    int    `json:"cpu_usage"`
	MemoryUsagePercent int    `json:"mem_usage"`
}

func GetSystemInfo() (info *SystemInfo, err error) {
	info = &SystemInfo{}

	// Get hostname
	hostname, err := os.Hostname()
	if err == nil {
		info.Hostname = hostname
	}

	// Get uptime
	uptimeDuration, err := getUptime()
	if err == nil {
		info.Uptime = uptimeDuration
	}

	// Get CPU usage percentage
	cpuUsagePercent, err := getCPUUsagePercent()
	if err == nil {
		info.CPUUsagePercent = cpuUsagePercent
	}

	// Get memory usage percentage
	memUsagePercent, err := getMemoryUsagePercent()
	if err == nil {
		info.MemoryUsagePercent = memUsagePercent
	}

	return
}

func getUptime() (int64, error) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, err
	}
	return info.Uptime, nil
}

func getCPUUsagePercent() (int, error) {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}

	lines := strings.Split(string(contents), "\n")
	var total, used int
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "cpu" {
			for i := 1; i < len(fields); i++ {
				value, _ := strconv.Atoi(fields[i])
				total += value
				if i < 4 { // Count user, nice, system, and idle values
					used += value
				}
			}
			break
		}
	}

	if total == 0 {
		return 0, nil
	}

	cpuUsagePercent := (used * 100) / total
	return cpuUsagePercent, nil
}

func getMemoryUsagePercent() (int, error) {
	contents, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}

	var total, free int
	lines := strings.Split(string(contents), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			if fields[0] == "MemTotal:" {
				total, _ = strconv.Atoi(fields[1])
			} else if fields[0] == "MemFree:" {
				free, _ = strconv.Atoi(fields[1])
			}
		}
	}

	if total == 0 {
		return 0, nil
	}

	memUsagePercent := ((total - free) * 100) / total
	return memUsagePercent, nil
}
