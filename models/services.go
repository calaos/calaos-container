package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/calaos/calaos-container/sysbus"
)

const (
	ServiceKindService = "service"

	ActionRestart = "restart"
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionEnable  = "enable"
	ActionDisable = "disable"

	StartupEnabled  = "enabled"
	StartupDisabled = "disabled"
	StartupMasked   = "masked"
	StartupStatic   = "static"

	loadStateNotFound = "not-found"
	loadStateMasked   = "masked"

	unitFileGenerated = "generated"

	// olad ships a SysV script only: systemd generates its unit, and
	// EnableUnitFiles does not act on it.
	sysvServiceID = "olad"
)

// Bus is the system bus. Tests replace it with sysbustest.Bus.
var Bus sysbus.SystemdBus = sysbus.New()

// SysvInstall is not on the PATH of a systemd service. Tests replace it.
var SysvInstall = "/lib/systemd/systemd-sysv-install"

// Service is a row of the allowlist. Unit names only ever come from here.
type Service struct {
	ID      string
	Unit    string
	Actions []string
}

// Allows tells whether verb is one of the actions of the row.
func (s Service) Allows(verb string) bool {
	return slices.Contains(s.Actions, verb)
}

var (
	allActions  = []string{ActionRestart, ActionStart, ActionStop, ActionEnable, ActionDisable}
	restartOnly = []string{ActionRestart}
)

// Services is the allowlist, in the order of docs/API.md. calaos-server,
// haproxy, calaos-container and ssh are the way back in: stopping one leaves
// a box no client can repair. networkd drops its leases on a restart, and
// ntp is switched by system/time.
var Services = []Service{
	{"calaos-server", "calaos-server.service", restartOnly},
	{"calaos-home", "calaos-home.service", allActions},
	{"mosquitto", "mosquitto.service", allActions},
	{"zigbee2mqtt", "zigbee2mqtt.service", allActions},
	{"influxdb", "influxdb.service", allActions},
	{"grafana", "grafana.service", allActions},
	{"hue2mqtt", "hue2mqtt.service", allActions},
	{"envoy", "envoy.service", allActions},
	{sysvServiceID, "olad.service", allActions},
	{"ntp", "chrony.service", restartOnly},
	{"calaos-container", "calaos-container.service", restartOnly},
	{"haproxy", "haproxy.service", restartOnly},
	{"networkd", "systemd-networkd.service", []string{}},
	{"ssh", "ssh.service", restartOnly},
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
var (
	ErrUnitUnavailable  = errors.New("unit not available")
	ErrUnitNotInstalled = fmt.Errorf("%w: not installed", ErrUnitUnavailable)
	ErrUnitMasked       = fmt.Errorf("%w: masked", ErrUnitUnavailable)
)

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
	case p.LoadState == loadStateNotFound:
		return fmt.Errorf("%w: %s", ErrUnitNotInstalled, unit)
	case unitStartup(p) == StartupMasked:
		return fmt.Errorf("%w: %s", ErrUnitMasked, unit)
	}
	return nil
}

// QueueRestart restarts unit, the resolved unit of s. ssh is reloaded when it
// can be, so that the session in use survives.
func QueueRestart(ctx context.Context, bus sysbus.SystemdBus, s Service, unit string) (*sysbus.Job, error) {
	if err := CheckUnit(ctx, bus, unit); err != nil {
		return nil, err
	}
	if s.ID == "ssh" {
		return bus.ReloadOrRestartUnit(ctx, unit)
	}
	return bus.RestartUnit(ctx, unit)
}

// QueueAction runs start, stop, enable or disable on unit, the resolved unit
// of s, and returns the job left to wait for. limit bounds each step, not the
// whole sequence. A failed step stops it: neither half of enable or disable
// goes on alone.
func QueueAction(bus sysbus.SystemdBus, s Service, unit, verb string, limit time.Duration) (*sysbus.Job, error) {
	run := func(step func(ctx context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		return step(ctx)
	}
	queue := func(q func(context.Context, string) (*sysbus.Job, error)) (*sysbus.Job, error) {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		return q(ctx, unit)
	}

	switch verb {
	case ActionStart:
		return queue(bus.StartUnit)
	case ActionStop:
		return queue(bus.StopUnit)
	case ActionEnable, ActionDisable:
		if err := run(func(ctx context.Context) error { return setBootState(ctx, bus, s, unit, verb) }); err != nil {
			return nil, err
		}
		if err := run(bus.Reload); err != nil {
			return nil, err
		}
		if verb == ActionEnable {
			return queue(bus.StartUnit)
		}
		return queue(bus.StopUnit)
	}
	return nil, fmt.Errorf("unknown action %q", verb)
}

func setBootState(ctx context.Context, bus sysbus.SystemdBus, s Service, unit, verb string) error {
	switch {
	case s.ID == sysvServiceID:
		return sysvInstall(ctx, verb)
	case verb == ActionEnable:
		return bus.EnableUnitFiles(ctx, []string{unit})
	}
	return bus.DisableUnitFiles(ctx, []string{unit})
}

// sysvInstall does not go through the Executor: it logs a failure as an
// error, and is-enabled exits 1 on every read of a disabled olad.
func sysvInstall(ctx context.Context, verb string) error {
	cmd := exec.CommandContext(ctx, SysvInstall, verb, sysvServiceID) //nolint:gosec // both are constants of this package
	cmd.WaitDelay = runCancelGrace
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemd-sysv-install %s %s: %w: %s", verb, sysvServiceID, err, stderr.String())
	}
	return nil
}

func sysvStartup(ctx context.Context) string {
	err := sysvInstall(ctx, "is-enabled")
	if err == nil {
		return StartupEnabled
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		logging.Warnf("reading the boot state of %s: %v", sysvServiceID, err)
	}
	return StartupDisabled
}

// NormalizeStartup reduces a UnitFileState to the words of the API.
func NormalizeStartup(unitFileState string) string {
	switch unitFileState {
	case "enabled", "enabled-runtime", "linked", "linked-runtime", "alias":
		return StartupEnabled
	case "disabled":
		return StartupDisabled
	case "masked", "masked-runtime":
		return StartupMasked
	}
	return StartupStatic
}

func unitStartup(p sysbus.UnitProperties) string {
	if p.LoadState == loadStateMasked {
		return StartupMasked
	}
	return NormalizeStartup(p.UnitFileState)
}

func serviceStartup(ctx context.Context, s Service, p sysbus.UnitProperties) string {
	if s.ID == sysvServiceID && p.LoadState != loadStateMasked && p.UnitFileState == unitFileGenerated {
		return sysvStartup(ctx)
	}
	return unitStartup(p)
}

type ServiceState struct {
	ID          string   `json:"id"`
	Unit        string   `json:"unit"`
	Kind        string   `json:"kind"`
	Actions     []string `json:"actions"`
	Restartable bool     `json:"restartable"`
	ActiveState string   `json:"active_state"`
	SubState    string   `json:"sub_state"`
	Enabled     string   `json:"enabled"`
	Startup     string   `json:"startup"`
	BootUnit    string   `json:"boot_unit"`
	Since       *string  `json:"since"`
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
		st := ServiceState{
			ID:          s.ID,
			Unit:        cands[pick],
			Kind:        ServiceKindService,
			Actions:     s.Actions,
			Restartable: s.Allows(ActionRestart),
			BootUnit:    cands[pick],
		}
		if p.LoadState == loadStateNotFound {
			st.ActiveState = loadStateNotFound
		} else {
			st.ActiveState = p.ActiveState
			st.SubState = p.SubState
			st.Enabled = p.UnitFileState
			st.Startup = serviceStartup(ctx, s, p)
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
	s := time.Unix(int64(usec/1_000_000), 0).UTC().Format(time.RFC3339) //nolint:gosec // dbus timestamps are far below the int64 range
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
