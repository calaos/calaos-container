package models

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// LogSource is a row of the logs/sources table. Journal matches only ever
// come from here, never from a client.
type LogSource struct {
	ID        string
	Label     string
	Unit      string
	Container string
	Kernel    bool
}

const (
	LogSourceKernel = "kernel"
	LogSourceSystem = "system"

	// LogMessageMax is the cut applied to every MESSAGE.
	LogMessageMax = 2048
)

// LogSources is the table of docs/API.md, in its order. The units are those
// of the Services table; one with fallbacks is resolved by the caller.
var LogSources = []LogSource{
	{"calaos-server", "Calaos server", "calaos-server.service", "calaos-server", false},
	{"calaos-home", "Calaos Home (kiosk)", "calaos-home.service", "calaos-home", false},
	{"haproxy", "haproxy", "haproxy.service", "haproxy", false},
	{"mosquitto", "Mosquitto", "mosquitto.service", "mosquitto", false},
	{"zigbee2mqtt", "Zigbee2MQTT", "zigbee2mqtt.service", "zigbee2mqtt", false},
	{"influxdb", "InfluxDB", "influxdb.service", "influxdb", false},
	{"grafana", "Grafana", "grafana.service", "grafana", false},
	{"hue2mqtt", "hue2mqtt", "hue2mqtt.service", "hue2mqtt", false},
	{"envoy", "Envoy", "envoy.service", "envoy", false},
	{"calaos-container", "calaos-container", "calaos-container.service", "", false},
	{"olad", "OLA (olad)", "olad.service", "", false},
	{"ntp", "Time sync (NTP)", "chrony.service", "", false},
	{"networkd", "Network (networkd)", "systemd-networkd.service", "", false},
	{"ssh", "SSH", "ssh.service", "", false},
	{LogSourceKernel, "Kernel", "", "", true},
	{LogSourceSystem, "Whole system", "", "", false},
}

// LookupLogSource finds a row by exact id.
func LookupLogSource(id string) (LogSource, bool) {
	for _, s := range LogSources {
		if s.ID == id {
			return s, true
		}
	}
	return LogSource{}, false
}

var priorityNames = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// LogPriority maps a syslog level name to its number.
func LogPriority(name string) (int, bool) {
	for i, n := range priorityNames {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

// _TRANSPORT is read to tell kernel lines apart: they carry no unit.
const journalFields = "MESSAGE,PRIORITY,_SYSTEMD_UNIT,SYSLOG_IDENTIFIER,CONTAINER_NAME,_PID,_TRANSPORT,__CURSOR,__REALTIME_TIMESTAMP"

// JournalQuery holds validated values only: JournalArgs turns it into argv.
type JournalQuery struct {
	Sources  []LogSource
	Priority *int
	Since    *time.Time
	Until    *time.Time
	Cursor   string
	Forward  bool
}

// journalTime is a form journalctl parses whatever the box time zone.
func journalTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000000") + " UTC"
}

// JournalArgs is the journalctl argv. Every client value is one argv of the
// --name=value form, so a cursor starting with "-" cannot become an option.
// journalctl has no --before-cursor: backward paging is -r with
// --after-cursor, which then means "older than the cursor". journalctl
// refuses --since with a cursor: the reader applies it then (Window).
func JournalArgs(q JournalQuery) []string {
	args := []string{"-o", "json", "--no-pager", "-q", "--output-fields=" + journalFields}
	if !q.Forward {
		args = append(args, "-r")
	}
	if q.Cursor != "" {
		args = append(args, "--after-cursor="+q.Cursor)
	} else if q.Since != nil {
		args = append(args, "--since="+journalTime(*q.Since))
	}
	if q.Until != nil {
		args = append(args, "--until="+journalTime(*q.Until))
	}
	if q.Priority != nil {
		args = append(args, "--priority="+strconv.Itoa(*q.Priority))
	}
	return append(args, journalMatches(q.Sources)...)
}

// journalMatches ORs every source with "+". -k is not used for the kernel:
// it implies -b and is ANDed with the other matches.
func journalMatches(sources []LogSource) []string {
	var m []string
	add := func(term string) {
		if len(m) > 0 {
			m = append(m, "+")
		}
		m = append(m, term)
	}
	for _, s := range sources {
		switch {
		case s.ID == LogSourceSystem:
			return nil
		case s.Kernel:
			add("_TRANSPORT=kernel")
		default:
			add("_SYSTEMD_UNIT=" + s.Unit)
			if s.Container != "" {
				add("CONTAINER_NAME=" + s.Container)
			}
		}
	}
	return m
}

// Window says where an entry stands against since and until. Entries are
// read away from the start point, so one past the far bound ends the scan
// and one past the near bound is skipped.
type Window int

const (
	InWindow Window = iota
	SkipEntry
	PastWindow
)

func (q JournalQuery) Window(r JournalRecord) Window {
	if r.Realtime.IsZero() {
		return InWindow
	}
	older := q.Since != nil && r.Realtime.Before(*q.Since)
	newer := q.Until != nil && r.Realtime.After(*q.Until)
	switch {
	case q.Forward && newer, !q.Forward && older:
		return PastWindow
	case older, newer:
		return SkipEntry
	}
	return InWindow
}

// JournalRecord is one parsed `journalctl -o json` line.
type JournalRecord struct {
	Cursor    string
	Realtime  time.Time
	Unit      string
	Container string
	Kernel    bool
	Priority  int
	PID       *int
	// Message is redacted, not cut yet.
	Message string
	// TooLong: journalctl printed null, as it does above 4096 bytes.
	TooLong bool
}

var errNoCursor = errors.New("journal entry without cursor")

// ParseJournalLine reads one entry. MESSAGE is redacted here, before any
// other use.
func ParseJournalLine(line []byte) (JournalRecord, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return JournalRecord{}, err
	}
	var r JournalRecord
	r.Cursor, _ = journalString(raw["__CURSOR"])
	if r.Cursor == "" {
		return r, errNoCursor
	}
	if us, ok := journalString(raw["__REALTIME_TIMESTAMP"]); ok {
		if v, err := strconv.ParseInt(us, 10, 64); err == nil {
			r.Realtime = time.UnixMicro(v).UTC()
		}
	}
	r.Unit, _ = journalString(raw["_SYSTEMD_UNIT"])
	r.Container, _ = journalString(raw["CONTAINER_NAME"])
	transport, _ := journalString(raw["_TRANSPORT"])
	r.Kernel = transport == "kernel"

	// journald's default level for a line without PRIORITY is info.
	r.Priority = 6
	if p, ok := journalString(raw["PRIORITY"]); ok {
		if v, err := strconv.Atoi(p); err == nil && v >= 0 && v <= 7 {
			r.Priority = v
		}
	}
	if p, ok := journalString(raw["_PID"]); ok {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			r.PID = &v
		}
	}

	msg, ok := raw["MESSAGE"]
	if ok && string(msg) == "null" {
		r.TooLong = true
	} else if s, ok := journalString(msg); ok {
		r.Message = Redact(s)
	}
	return r, nil
}

// journalString reads a JSON journal value: a string, a byte array (binary
// data), or an array of those when the field repeats (first kept).
func journalString(v json.RawMessage) (string, bool) {
	if len(v) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, true
	}
	var b []int
	if json.Unmarshal(v, &b) == nil {
		buf := make([]byte, 0, len(b))
		for _, c := range b {
			if c < 0 || c > 255 {
				return "", false
			}
			buf = append(buf, byte(c))
		}
		return strings.ToValidUTF8(string(buf), "�"), true
	}
	var multi []json.RawMessage
	if json.Unmarshal(v, &multi) == nil && len(multi) > 0 {
		return journalString(multi[0])
	}
	return "", false
}

// Attribute names the requested source an entry belongs to: the first
// specific one it matches, else "system".
func (q JournalQuery) Attribute(r JournalRecord) string {
	for _, s := range q.Sources {
		switch {
		case s.Kernel:
			if r.Kernel {
				return s.ID
			}
		case s.ID == LogSourceSystem:
		case r.Unit == s.Unit || (s.Container != "" && r.Container == s.Container):
			return s.ID
		}
	}
	for _, s := range q.Sources {
		if s.ID == LogSourceSystem {
			return s.ID
		}
	}
	if len(q.Sources) > 0 {
		return q.Sources[0].ID
	}
	return LogSourceSystem
}

// CutMessage keeps at most LogMessageMax bytes, on a rune boundary.
func CutMessage(s string) (string, bool) {
	if len(s) <= LogMessageMax {
		return s, false
	}
	i := LogMessageMax
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i], true
}
