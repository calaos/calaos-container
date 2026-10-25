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
	{"timesyncd", "systemd-timesyncd.service", ServiceKindService, true},
	{"calaos-ddns", "calaos-ddns.service", ServiceKindOneshot, true},
	{"calaos-container", "calaos-container.service", ServiceKindService, false},
	{"haproxy", "haproxy.service", ServiceKindService, false},
	{"networkd", "systemd-networkd.service", ServiceKindService, false},
	{"ssh", "ssh.service", ServiceKindService, false},
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
		args = append(args, s.Unit)
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
// Id, which names the target when a unit is an alias.
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
	if len(blocks) != len(Services) {
		return nil, fmt.Errorf("systemctl show: %d units, want %d", len(blocks), len(Services))
	}

	states := make([]ServiceState, len(Services))
	for i, s := range Services {
		b := blocks[i]
		st := ServiceState{ID: s.ID, Unit: s.Unit, Kind: s.Kind, Restartable: s.Restartable}
		if b["LoadState"] == "not-found" {
			st.ActiveState = "not-found"
		} else {
			st.ActiveState = b["ActiveState"]
			st.SubState = b["SubState"]
			st.Enabled = b["UnitFileState"]
			since, err := parseUnixTimestamp(b["ActiveEnterTimestamp"])
			if err != nil {
				return nil, fmt.Errorf("systemctl show %s: %w", s.Unit, err)
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
