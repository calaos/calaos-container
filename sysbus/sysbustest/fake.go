// Package sysbustest is an in-memory SystemdBus for tests, usable from any
// package: no system bus is reached.
package sysbustest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/calaos/calaos-container/sysbus"
)

// Bus records mutations in order and counts reads. Unknown units read as
// not-found, like systemd loading them on demand. The zero value is not
// usable: call New.
type Bus struct {
	mu         sync.Mutex
	units      map[string]sysbus.UnitProperties
	timedate   sysbus.TimedateState
	timezones  []string
	describe   string
	fail       map[string]error
	jobResults map[string]string
	holdJobs   bool
	pending    map[string][]chan string
	calls      []string
	reads      map[string]int
}

var _ sysbus.SystemdBus = (*Bus)(nil)

func New() *Bus {
	return &Bus{
		units:      map[string]sysbus.UnitProperties{},
		fail:       map[string]error{},
		jobResults: map[string]string{},
		pending:    map[string][]chan string{},
		reads:      map[string]int{},
	}
}

// SetUnit sets what UnitProperties answers for unit.
func (b *Bus) SetUnit(unit string, p sysbus.UnitProperties) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.units[unit] = p
}

// SetTimedate sets what Timedate answers.
func (b *Bus) SetTimedate(st sysbus.TimedateState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timedate = st
}

// SetTimezones sets what ListTimezones answers.
func (b *Bus) SetTimezones(zones ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timezones = zones
}

// SetDescribe sets what NetworkDescribe answers.
func (b *Bus) SetDescribe(json string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.describe = json
}

// Fail makes a call fail. key is a method name, or a recorded call
// ("NetworkReconfigure eth0") to target one argument. A nil err clears it.
func (b *Bus) Fail(key string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		delete(b.fail, key)
		return
	}
	b.fail[key] = err
}

// SetJobResult sets the result of the next jobs on unit; the default is done.
func (b *Bus) SetJobResult(unit, result string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.jobResults[unit] = result
}

// HoldJobs leaves later jobs running until FinishJob.
func (b *Bus) HoldJobs() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.holdJobs = true
}

// FinishJob ends the oldest held job of unit, as a JobRemoved signal would.
func (b *Bus) FinishJob(unit, result string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.pending[unit]
	if len(q) == 0 {
		panic(fmt.Sprintf("no held job on %s", unit))
	}
	q[0] <- result
	b.pending[unit] = q[1:]
}

// Calls returns the mutations so far, in order ("RestartUnit x.service",
// "SetNTP false", "NetworkReload").
func (b *Bus) Calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string{}, b.calls...)
}

// Take is Calls, and forgets them.
func (b *Bus) Take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	calls := b.calls
	b.calls = nil
	return append([]string{}, calls...)
}

// Reads counts the calls of a read method.
func (b *Bus) Reads(method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reads[method]
}

func (b *Bus) failure(method, call string) error {
	if err, ok := b.fail[call]; ok {
		return err
	}
	return b.fail[method]
}

func (b *Bus) mutate(method string, args ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mutateLocked(method, args...)
}

func (b *Bus) mutateLocked(method string, args ...string) error {
	call := strings.Join(append([]string{method}, args...), " ")
	b.calls = append(b.calls, call)
	return b.failure(method, call)
}

func (b *Bus) read(method string) error {
	b.reads[method]++
	return b.failure(method, method)
}

func (b *Bus) queue(method, unit string) (*sysbus.Job, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.mutateLocked(method, unit); err != nil {
		return nil, err
	}
	result := make(chan string, 1)
	if b.holdJobs {
		b.pending[unit] = append(b.pending[unit], result)
		return sysbus.NewJob(unit, result, nil), nil
	}
	r, ok := b.jobResults[unit]
	if !ok {
		r = sysbus.JobDone
	}
	result <- r
	return sysbus.NewJob(unit, result, nil), nil
}

func (b *Bus) UnitProperties(ctx context.Context, units ...string) ([]sysbus.UnitProperties, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.read("UnitProperties"); err != nil {
		return nil, err
	}
	out := make([]sysbus.UnitProperties, len(units))
	for i, u := range units {
		p, ok := b.units[u]
		if !ok {
			p = sysbus.UnitProperties{LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}
		}
		out[i] = p
	}
	return out, nil
}

func (b *Bus) StartUnit(ctx context.Context, unit string) (*sysbus.Job, error) {
	return b.queue("StartUnit", unit)
}

func (b *Bus) StopUnit(ctx context.Context, unit string) (*sysbus.Job, error) {
	return b.queue("StopUnit", unit)
}

func (b *Bus) RestartUnit(ctx context.Context, unit string) (*sysbus.Job, error) {
	return b.queue("RestartUnit", unit)
}

func (b *Bus) ReloadOrRestartUnit(ctx context.Context, unit string) (*sysbus.Job, error) {
	return b.queue("ReloadOrRestartUnit", unit)
}

func (b *Bus) EnableUnitFiles(ctx context.Context, units []string) error {
	return b.mutate("EnableUnitFiles", units...)
}

func (b *Bus) DisableUnitFiles(ctx context.Context, units []string) error {
	return b.mutate("DisableUnitFiles", units...)
}

func (b *Bus) Reload(ctx context.Context) error {
	return b.mutate("Reload")
}

func (b *Bus) Timedate(ctx context.Context) (sysbus.TimedateState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.read("Timedate"); err != nil {
		return sysbus.TimedateState{}, err
	}
	return b.timedate, nil
}

// The setters change the state a later Timedate reads, as timedated would.

func (b *Bus) SetTimezone(ctx context.Context, timezone string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.mutateLocked("SetTimezone", timezone); err != nil {
		return err
	}
	b.timedate.Timezone = timezone
	return nil
}

func (b *Bus) SetNTP(ctx context.Context, enable bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.mutateLocked("SetNTP", fmt.Sprint(enable)); err != nil {
		return err
	}
	b.timedate.NTP = enable
	return nil
}

func (b *Bus) SetTime(ctx context.Context, usec int64) error {
	return b.mutate("SetTime", fmt.Sprint(usec))
}

func (b *Bus) ListTimezones(ctx context.Context) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.read("ListTimezones"); err != nil {
		return nil, err
	}
	return append([]string{}, b.timezones...), nil
}

func (b *Bus) NetworkReload(ctx context.Context) error {
	return b.mutate("NetworkReload")
}

func (b *Bus) NetworkReconfigure(ctx context.Context, intf string) error {
	return b.mutate("NetworkReconfigure", intf)
}

func (b *Bus) NetworkDescribe(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.read("NetworkDescribe"); err != nil {
		return "", err
	}
	return b.describe, nil
}

func (b *Bus) Reboot(ctx context.Context) error {
	return b.mutate("Reboot")
}
