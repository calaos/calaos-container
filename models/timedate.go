package models

import (
	"fmt"
	"strings"
	"time"
)

// TimedateState is what `timedatectl show` reports.
type TimedateState struct {
	Timezone     string
	NTP          bool
	Synchronized bool
	RTC          *string
}

// ParseTimedateShow reads `timedatectl show`. RTCTimeUSec is printed in the
// box zone ("Sun 2026-10-25 10:12:03 CET"), so it is read in Timezone; an
// RTC that cannot be read gives nil.
func ParseTimedateShow(out string) (TimedateState, error) {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = v
		}
	}
	tz, ok := kv["Timezone"]
	if !ok {
		return TimedateState{}, fmt.Errorf("timedatectl show: no Timezone in %q", out)
	}
	st := TimedateState{
		Timezone:     tz,
		NTP:          kv["NTP"] == "yes",
		Synchronized: kv["NTPSynchronized"] == "yes",
	}
	if t, ok := parseTimedateTimestamp(kv["RTCTimeUSec"], tz); ok {
		s := t.UTC().Format(time.RFC3339)
		st.RTC = &s
	}
	return st, nil
}

// The abbreviation is not parsed: Go invents a zero offset for one it does
// not know, and "+04" style zones have none. Only the fall-back hour is
// ambiguous, which is acceptable for a display value.
func parseTimedateTimestamp(v, tz string) (time.Time, bool) {
	f := strings.Fields(v)
	if len(f) != 4 {
		return time.Time{}, false
	}
	loc := time.UTC
	if f[3] != "UTC" && f[3] != "GMT" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}, false
		}
		loc = l
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", f[1]+" "+f[2], loc)
	return t, err == nil
}
