package models

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/calaos/calaos-container/sysbus"
)

const (
	ServiceKindService = "service"
	ServiceKindOneshot = "oneshot"

	loadStateNotFound = "not-found"
)

// Bus is the system bus. Tests replace it with sysbustest.Bus.
var Bus sysbus.SystemdBus = sysbus.New()

// Service is a row of the allowlist. Unit names only ever come from here.
type Service struct {
	ID          string
	Unit        string
	Kind        string
	Restartable bool
}

// Services is the allowlist, in the order of docs/API.md. calaos-container
// may be running apt, haproxy carries the admin session and networkd is
// reloaded by the network routes under their own guard: status only.
var Services = []Service{
	{"calaos-server", "calaos-server.service", ServiceKindService, true},
	{"calaos-home", "calaos-home.service", ServiceKindService, true},
	{"mosquitto", "mosquitto.service", ServiceKindService, true},
	{"zigbee2mqtt", "zigbee2mqtt.service", ServiceKindService, true},
	{"influxdb", "influxdb.service", ServiceKindService, true},
	{"grafana", "grafana.service", ServiceKindService, true},
	{"hue2mqtt", "hue2mqtt.service", ServiceKindService, true},
	{"envoy", "envoy.service", ServiceKindService, true},
	{"olad", "olad.service", ServiceKindService, true},
	{"ntp", "chrony.service", ServiceKindService, true},
	{"calaos-container", "calaos-container.service", ServiceKindService, false},
	{"haproxy", "haproxy.service", ServiceKindService, false},
	{"networkd", "systemd-networkd.service", ServiceKindService, false},
	{"ssh", "ssh.service", ServiceKindService, false},
}

// unitFallbacks lists, for a unit of the tables, the units tried in order
// when it is not installed. Calaos OS runs chrony (the calaos package
// depends on it, which removes timesyncd); a plain Debian keeps timesyncd.
var unitFallbacks = map[string][]string{
	"chrony.service": {"systemd-timesyncd.service"},
}

// UnitCandidates is unit followed by its fallbacks.
func UnitCandidates(unit string) []string {
	return append([]string{unit}, unitFallbacks[unit]...)
}

// HasUnitFallbacks tells whether unit must be resolved before use.
func HasUnitFallbacks(unit string) bool { return len(unitFallbacks[unit]) > 0 }

// firstInstalled is the index of the first row that is installed, 0 if none.
func firstInstalled(props []sysbus.UnitProperties) int {
	for i, p := range props {
		if p.LoadState != loadStateNotFound {
			return i
		}
	}
	return 0
}

// ResolveUnit picks the first candidate of unit that is installed, or unit
// itself when none is. A failed read is returned, so that it is not cached.
func ResolveUnit(ctx context.Context, bus sysbus.SystemdBus, unit string) (string, error) {
	cands := UnitCandidates(unit)
	props, err := bus.UnitProperties(ctx, cands...)
	if err != nil {
		return unit, err
	}
	if len(props) != len(cands) {
		return unit, fmt.Errorf("%d unit states for %d units", len(props), len(cands))
	}
	return cands[firstInstalled(props)], nil
}

// LookupService finds an allowlist row by exact id.
func LookupService(id string) (Service, bool) {
	for _, s := range Services {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

// ErrUnitUnavailable: the unit is not installed or is masked. Nothing was
// asked of systemd.
var ErrUnitUnavailable = errors.New("unit not available")

// CheckUnit is the gate before any mutation of a unit: a failed read counts
// as a refusal too, never as an installed unit.
func CheckUnit(ctx context.Context, bus sysbus.SystemdBus, unit string) error {
	props, err := bus.UnitProperties(ctx, unit)
	if err != nil {
		return err
	}
	if len(props) != 1 {
		return fmt.Errorf("%d unit states for %s", len(props), unit)
	}
	switch p := props[0]; {
	case p.LoadState == loadStateNotFound, p.LoadState == "masked",
		p.UnitFileState == "masked", p.UnitFileState == "masked-runtime":
		return fmt.Errorf("%w: %s", ErrUnitUnavailable, unit)
	}
	return nil
}

// QueueRestart restarts unit, the resolved unit of s: a oneshot is started,
// and ssh is reloaded when it can be, so that the session in use survives.
func QueueRestart(ctx context.Context, bus sysbus.SystemdBus, s Service, unit string) (*sysbus.Job, error) {
	if err := CheckUnit(ctx, bus, unit); err != nil {
		return nil, err
	}
	switch {
	case s.Kind == ServiceKindOneshot:
		return bus.StartUnit(ctx, unit)
	case s.ID == "ssh":
		return bus.ReloadOrRestartUnit(ctx, unit)
	}
	return bus.RestartUnit(ctx, unit)
}

type ServiceState struct {
	ID          string  `json:"id"`
	Unit        string  `json:"unit"`
	Kind        string  `json:"kind"`
	Restartable bool    `json:"restartable"`
	ActiveState string  `json:"active_state"`
	SubState    string  `json:"sub_state"`
	Enabled     string  `json:"enabled"`
	Since       *string `json:"since"`
}

// ServiceStates reads every unit of the allowlist. A row with fallbacks
// reports its first installed candidate.
func ServiceStates(ctx context.Context, bus sysbus.SystemdBus) ([]ServiceState, error) {
	var units []string
	for _, s := range Services {
		units = append(units, UnitCandidates(s.Unit)...)
	}
	props, err := bus.UnitProperties(ctx, units...)
	if err != nil {
		return nil, err
	}
	if len(props) != len(units) {
		return nil, fmt.Errorf("%d unit states for %d units", len(props), len(units))
	}

	states := make([]ServiceState, len(Services))
	next := 0
	for i, s := range Services {
		cands := UnitCandidates(s.Unit)
		pick := firstInstalled(props[next : next+len(cands)])
		p := props[next+pick]
		next += len(cands)
		st := ServiceState{ID: s.ID, Unit: cands[pick], Kind: s.Kind, Restartable: s.Restartable}
		if p.LoadState == loadStateNotFound {
			st.ActiveState = loadStateNotFound
		} else {
			st.ActiveState = p.ActiveState
			st.SubState = p.SubState
			st.Enabled = p.UnitFileState
			st.Since = unitSince(p.ActiveEnterTimestamp)
		}
		states[i] = st
	}
	return states, nil
}

// unitSince formats a timestamp in microseconds; 0 means never.
func unitSince(usec uint64) *string {
	if usec == 0 {
		return nil
	}
	s := time.Unix(int64(usec/1_000_000), 0).UTC().Format(time.RFC3339)
	return &s
}

// ServiceGuard refuses while an upgrade runs, like RebootGuard, but hands
// the lock back through release: holding it until the deferred restart has
// run keeps an upgrade from starting in between.
func ServiceGuard() (release func(), err error) {
	if err := acquireUpgradeLock(); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { upgradeLock.Release(1) }) }, nil
}
