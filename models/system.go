package models

import (
	"fmt"
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

// GetSystemInfo reports current system information. Its contract is
// all-or-nothing: on success it returns a fully-populated *SystemInfo and a
// nil error; on any failure it returns a nil *SystemInfo and a non-nil
// error. Callers must never see a partially-populated struct.
func GetSystemInfo() (*SystemInfo, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("GetSystemInfo: %w", err)
	}

	uptime, err := getUptime()
	if err != nil {
		return nil, fmt.Errorf("GetSystemInfo: %w", err)
	}

	cpuUsagePercent, err := getCPUUsagePercent()
	if err != nil {
		return nil, fmt.Errorf("GetSystemInfo: %w", err)
	}

	memUsagePercent, err := getMemoryUsagePercent()
	if err != nil {
		return nil, fmt.Errorf("GetSystemInfo: %w", err)
	}

	return &SystemInfo{
		Hostname:           hostname,
		Uptime:             uptime,
		CPUUsagePercent:    cpuUsagePercent,
		MemoryUsagePercent: memUsagePercent,
	}, nil
}

func getUptime() (int64, error) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, err
	}
	return info.Uptime, nil
}

// getCPUUsagePercent reads /proc/stat and reports CPU usage as a
// percentage. See parseCPUUsagePercent for the calculation method.
func getCPUUsagePercent() (int, error) {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	return parseCPUUsagePercent(string(contents))
}

// parseCPUUsagePercent locates the aggregate "cpu" line in the contents of
// /proc/stat and computes usage from its fields.
//
// This is a single-sample ratio of cumulative-since-boot jiffie counters,
// not a delta between two samples taken some interval apart. It is
// therefore an average over the entire system uptime rather than an
// instantaneous reading; on a long-running host it will react slowly to
// recent load changes. A proper instantaneous reading would require taking
// two samples and diffing them, which is out of scope here.
func parseCPUUsagePercent(contents string) (int, error) {
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "cpu" {
			return cpuUsageFromFields(fields[1:])
		}
	}

	return 0, fmt.Errorf("parseCPUUsagePercent: no aggregate \"cpu\" line found in /proc/stat")
}

// cpuUsageFromFields computes CPU usage percentage from the numeric fields
// of the aggregate "cpu" line in /proc/stat (i.e. everything after the
// "cpu" label), in their documented order: user, nice, system, idle,
// iowait, irq, softirq, steal, guest, guest_nice.
//
// Per the standard /proc/stat method: total is the sum of all fields, and
// used = total - idle - iowait (idle and iowait are the only fields that
// represent the CPU doing no work). guest/guest_nice are already included
// in user/nice on Linux and must not be added again. At minimum, user,
// nice, system and idle must be present; iowait is optional (older kernels
// omit it) and is simply excluded from the "used" subtraction if absent.
func cpuUsageFromFields(fields []string) (int, error) {
	const (
		idxIdle   = 3
		idxIOWait = 4
	)

	if len(fields) <= idxIdle {
		return 0, fmt.Errorf("cpuUsageFromFields: expected at least %d fields (user, nice, system, idle), got %d", idxIdle+1, len(fields))
	}

	values := make([]int, len(fields))
	var total int
	for i, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			return 0, fmt.Errorf("cpuUsageFromFields: invalid value %q at field %d: %w", field, i, err)
		}
		values[i] = value
		total += value
	}

	idleTime := values[idxIdle]
	if len(values) > idxIOWait {
		idleTime += values[idxIOWait]
	}

	if total == 0 {
		return 0, fmt.Errorf("cpuUsageFromFields: all /proc/stat cpu fields are zero")
	}

	used := total - idleTime
	return (used * 100) / total, nil
}

// getMemoryUsagePercent reads /proc/meminfo and reports memory usage as a
// percentage. See parseMemoryUsagePercent for the calculation method.
func getMemoryUsagePercent() (int, error) {
	contents, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	return parseMemoryUsagePercent(string(contents))
}

// parseMemoryUsagePercent computes memory usage percentage from the
// contents of /proc/meminfo as (MemTotal - MemAvailable) / MemTotal.
// MemAvailable (rather than MemFree) is used because it estimates memory
// available for starting new applications without swapping, accounting for
// reclaimable caches and buffers.
func parseMemoryUsagePercent(contents string) (int, error) {
	var total, available int
	var haveTotal, haveAvailable bool

	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		switch fields[0] {
		case "MemTotal:":
			value, err := strconv.Atoi(fields[1])
			if err != nil {
				return 0, fmt.Errorf("parseMemoryUsagePercent: invalid MemTotal value %q: %w", fields[1], err)
			}
			total = value
			haveTotal = true
		case "MemAvailable:":
			value, err := strconv.Atoi(fields[1])
			if err != nil {
				return 0, fmt.Errorf("parseMemoryUsagePercent: invalid MemAvailable value %q: %w", fields[1], err)
			}
			available = value
			haveAvailable = true
		}
	}

	if !haveTotal || total == 0 {
		return 0, fmt.Errorf("parseMemoryUsagePercent: MemTotal not found (or zero) in /proc/meminfo")
	}
	if !haveAvailable {
		return 0, fmt.Errorf("parseMemoryUsagePercent: MemAvailable not found in /proc/meminfo")
	}

	return ((total - available) * 100) / total, nil
}
