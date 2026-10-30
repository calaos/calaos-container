// Package sysbus reaches systemd and its companion daemons over D-Bus. The
// daemon only sees the SystemdBus interface, so tests run without a system bus.
package sysbus

import (
	"context"
	"fmt"
)

// JobDone is the only job result that means success.
const JobDone = "done"

// UnitProperties are the properties of org.freedesktop.systemd1.Unit the
// daemon reads. ActiveEnterTimestamp is in microseconds, 0 for never.
type UnitProperties struct {
	LoadState            string
	ActiveState          string
	SubState             string
	UnitFileState        string
	ActiveEnterTimestamp uint64
}

// TimedateState is what timedated reports. RTCTimeUSec is 0 when the
// hardware clock cannot be read.
type TimedateState struct {
	Timezone        string
	NTP             bool
	NTPSynchronized bool
	RTCTimeUSec     uint64
}

// SystemdBus is every call the daemon makes to the system. Unit names must
// come from the static tables of models. Errors are safe to show a client:
// the D-Bus detail goes to the daemon log.
type SystemdBus interface {
	// UnitProperties reads units in one go and answers in argument order. A
	// unit that is not installed is a row with LoadState "not-found", not an
	// error.
	UnitProperties(ctx context.Context, units ...string) ([]UnitProperties, error)

	// These return once systemd has queued the job; its result comes from
	// Job.Wait.
	StartUnit(ctx context.Context, unit string) (*Job, error)
	StopUnit(ctx context.Context, unit string) (*Job, error)
	RestartUnit(ctx context.Context, unit string) (*Job, error)
	ReloadOrRestartUnit(ctx context.Context, unit string) (*Job, error)

	// ResetFailedUnit leaves the failed state and clears the start rate limit
	// of unit. The journal keeps its lines.
	ResetFailedUnit(ctx context.Context, unit string) error

	// EnableUnitFiles and DisableUnitFiles only touch the symlinks: unlike
	// systemctl they neither reload systemd nor call systemd-sysv-install
	// for a unit that has only a SysV script.
	EnableUnitFiles(ctx context.Context, units []string) error
	DisableUnitFiles(ctx context.Context, units []string) error
	Reload(ctx context.Context) error

	Timedate(ctx context.Context) (TimedateState, error)
	SetTimezone(ctx context.Context, timezone string) error
	SetNTP(ctx context.Context, enable bool) error
	// SetTime takes microseconds since the epoch.
	SetTime(ctx context.Context, usec int64) error
	ListTimezones(ctx context.Context) ([]string, error)

	NetworkReload(ctx context.Context) error
	NetworkReconfigure(ctx context.Context, intf string) error
	// NetworkDescribe returns the JSON description of networkd, the one
	// `networkctl list --json` prints.
	NetworkDescribe(ctx context.Context) (string, error)

	// Reboot goes through logind, which honours delay inhibitors. Unlike
	// `systemctl reboot` it does not fall back on reboot.target if logind fails.
	Reboot(ctx context.Context) error
}

// JobError is a job that did not end in JobDone.
type JobError struct {
	Unit   string
	Result string
}

func (e *JobError) Error() string {
	return fmt.Sprintf("%s: job %s", e.Unit, e.Result)
}

// JobUnfinished is the Result of a JobError when Wait gave up: the job may
// still be running.
const JobUnfinished = "unfinished"

// Job is a queued systemd job. It holds a bus connection until its one Wait
// returns, so every job must be waited for.
type Job struct {
	unit    string
	result  <-chan string
	release func()
}

// NewJob builds a Job whose result arrives on result. release, which may be
// nil, runs when Wait returns.
func NewJob(unit string, result <-chan string, release func()) *Job {
	return &Job{unit: unit, result: result, release: release}
}

// Wait blocks until the job ends and is nil only for JobDone: any other
// result, and giving up with ctx, is a JobError.
func (j *Job) Wait(ctx context.Context) error {
	if j.release != nil {
		defer j.release()
	}
	select {
	case r := <-j.result:
		if r == JobDone {
			return nil
		}
		return &JobError{Unit: j.unit, Result: r}
	case <-ctx.Done():
		return &JobError{Unit: j.unit, Result: JobUnfinished}
	}
}
