package models

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ServiceKindService = "service"
	ServiceKindOneshot = "oneshot"
)

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

// ResolveUnitArgs is the systemctl argv printing the LoadState of every
// candidate of unit, in order.
func ResolveUnitArgs(unit string) []string {
	return append([]string{"show", "--no-pager", "-p", "LoadState", "--value"}, UnitCandidates(unit)...)
}

// ParseResolvedUnit picks the first candidate that is installed, or unit
// itself when none is or the output is not understood. systemctl separates
// the units with a blank line, even with --value.
func ParseResolvedUnit(unit, out string) string {
	cands := UnitCandidates(unit)
	states := strings.Fields(out)
	if len(states) != len(cands) {
		return unit
	}
	for i, st := range states {
		if st != "not-found" {
			return cands[i]
		}
	}
	return unit
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

// RestartArgs is the systemctl argv that restarts s (starts a oneshot).
func (s Service) RestartArgs() []string {
	verb := "restart"
	if s.Kind == ServiceKindOneshot {
		verb = "start"
	}
	return []string{verb, "--no-block", s.Unit}
}

// ServiceStateArgs is the single systemctl call reading every unit. Unix
// timestamps avoid parsing a local time zone abbreviation.
func ServiceStateArgs() []string {
	args := []string{"show", "--no-pager", "--timestamp=unix",
		"-p", "Id,LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp"}
	for _, s := range Services {
		args = append(args, UnitCandidates(s.Unit)...)
	}
	return args
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

// ParseServiceStates reads the output of ServiceStateArgs. systemctl prints
// one block per unit in argument order; the order is relied on rather than
// Id, which names the target when a unit is an alias. A row with fallbacks
// reports its first installed candidate.
func ParseServiceStates(out string) ([]ServiceState, error) {
	var blocks []map[string]string
	cur := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("systemctl show: unexpected line %q", line)
		}
		cur[k] = v
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	want := 0
	for _, s := range Services {
		want += len(UnitCandidates(s.Unit))
	}
	if len(blocks) != want {
		return nil, fmt.Errorf("systemctl show: %d units, want %d", len(blocks), want)
	}

	states := make([]ServiceState, len(Services))
	next := 0
	for i, s := range Services {
		cands := UnitCandidates(s.Unit)
		unit, b := cands[0], blocks[next]
		for j, u := range cands {
			if blocks[next+j]["LoadState"] != "not-found" {
				unit, b = u, blocks[next+j]
				break
			}
		}
		next += len(cands)
		st := ServiceState{ID: s.ID, Unit: unit, Kind: s.Kind, Restartable: s.Restartable}
		if b["LoadState"] == "not-found" {
			st.ActiveState = "not-found"
		} else {
			st.ActiveState = b["ActiveState"]
			st.SubState = b["SubState"]
			st.Enabled = b["UnitFileState"]
			since, err := parseUnixTimestamp(b["ActiveEnterTimestamp"])
			if err != nil {
				return nil, fmt.Errorf("systemctl show %s: %w", unit, err)
			}
			st.Since = since
		}
		states[i] = st
	}
	return states, nil
}

// parseUnixTimestamp reads "@<seconds>"; empty, "n/a" and "@0" mean never.
func parseUnixTimestamp(v string) (*string, error) {
	if v == "" || v == "n/a" {
		return nil, nil
	}
	raw, ok := strings.CutPrefix(v, "@")
	if !ok {
		return nil, fmt.Errorf("bad timestamp %q", v)
	}
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("bad timestamp %q", v)
	}
	if sec == 0 {
		return nil, nil
	}
	s := time.Unix(sec, 0).UTC().Format(time.RFC3339)
	return &s, nil
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
