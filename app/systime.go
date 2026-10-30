package app

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/sysbus"
)

const (
	timeCallLimit = 10 * time.Second
	timeCacheTTL  = time.Second
)

var timezoneRe = regexp.MustCompile(`^[A-Za-z0-9_+/][A-Za-z0-9_+\-/]{0,63}$`)

// The daemon only drives timedated: it never writes /etc/timezone and sets
// no TZ (docs/API.md, Box time).
var timeState struct {
	setMu sync.Mutex

	zonesMu sync.Mutex
	zones   []string

	// GET system/time is polled: one read of timedated per second at most.
	showMu sync.Mutex
	showAt time.Time
	show   *sysbus.TimedateState
}

type boxTime struct {
	Now          string  `json:"now"`
	Timezone     string  `json:"timezone"`
	NTP          bool    `json:"ntp"`
	Synchronized bool    `json:"synchronized"`
	RTC          *string `json:"rtc"`
}

type boxTimeSet struct {
	boxTime
	RestartRequired bool `json:"restart_required"`
}

func onTimedate(call func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeCallLimit)
	defer cancel()
	return call(ctx)
}

// timedateState reads timedated, from the cache unless fresh is set.
func timedateState(fresh bool) (sysbus.TimedateState, error) {
	timeState.showMu.Lock()
	defer timeState.showMu.Unlock()
	if !fresh && timeState.show != nil && time.Since(timeState.showAt) < timeCacheTTL {
		return *timeState.show, nil
	}
	var st sysbus.TimedateState
	err := onTimedate(func(ctx context.Context) (err error) {
		st, err = models.Bus.Timedate(ctx)
		return err
	})
	if err != nil {
		return sysbus.TimedateState{}, err
	}
	timeState.show, timeState.showAt = &st, time.Now()
	return st, nil
}

func resetBoxTimeCache() {
	timeState.showMu.Lock()
	timeState.show = nil
	timeState.showMu.Unlock()
}

func (a *AppServer) readBoxTime(fresh bool) (boxTime, error) {
	st, err := timedateState(fresh)
	if err != nil {
		return boxTime{}, err
	}
	return boxTime{
		Now:          a.now().UTC().Format(time.RFC3339),
		Timezone:     st.Timezone,
		NTP:          st.NTP,
		Synchronized: st.NTPSynchronized,
		RTC:          rtcTime(st.RTCTimeUSec),
	}, nil
}

// rtcTime formats the hardware clock; 0 is one that cannot be read.
func rtcTime(usec uint64) *string {
	if usec == 0 {
		return nil
	}
	s := time.UnixMicro(int64(usec)).UTC().Format(time.RFC3339)
	return &s
}

// timezones caches the first successful list: it only changes with tzdata.
func timezones() ([]string, error) {
	timeState.zonesMu.Lock()
	defer timeState.zonesMu.Unlock()
	if timeState.zones != nil {
		return timeState.zones, nil
	}
	var zones []string
	err := onTimedate(func(ctx context.Context) (err error) {
		zones, err = models.Bus.ListTimezones(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(zones) == 0 {
		return nil, errors.New("timedated listed no timezone")
	}
	timeState.zones = zones
	return zones, nil
}

func resetTimezonesCache() {
	timeState.zonesMu.Lock()
	timeState.zones = nil
	timeState.zonesMu.Unlock()
}

func (a *AppServer) v2SystemTime(c *fiber.Ctx) error {
	bt, err := a.readBoxTime(false)
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, bt)
}

func (a *AppServer) v2SystemTimezones(c *fiber.Ctx) error {
	zones, err := timezones()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, fiber.Map{"timezones": zones})
}

type timeStep struct {
	field string
	apply func(context.Context) error
	audit [2]string
}

func (a *AppServer) v2SystemTimeSet(c *fiber.Ctx) error {
	var b struct {
		Timezone *string `json:"timezone"`
		NTP      *bool   `json:"ntp"`
		Time     *string `json:"time"`
	}
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if b.Timezone == nil && b.NTP == nil && b.Time == nil {
		return errInvalid("no field to set")
	}

	var setTime time.Time
	if b.Time != nil {
		t, err := time.Parse(time.RFC3339, *b.Time)
		if err != nil {
			return errInvalid("invalid time: RFC 3339 time expected")
		}
		if y := t.UTC().Year(); y < 2020 || y > 2100 {
			return errInvalid("invalid time: year 2020 to 2100")
		}
		setTime = t
	}
	if b.Timezone != nil {
		if !timezoneRe.MatchString(*b.Timezone) {
			return errInvalid("unknown timezone")
		}
		zones, err := timezones()
		if err != nil {
			return errInternal(err)
		}
		if !contains(zones, *b.Timezone) {
			return errInvalid("unknown timezone")
		}
	}

	timeState.setMu.Lock()
	defer timeState.setMu.Unlock()

	before, err := a.readBoxTime(true)
	if err != nil {
		return errInternal(err)
	}
	ntpAfter := before.NTP
	if b.NTP != nil {
		ntpAfter = *b.NTP
	}
	if b.Time != nil && ntpAfter {
		return errInvalid("ntp is active")
	}

	var steps []timeStep
	if b.Timezone != nil {
		tz := *b.Timezone
		steps = append(steps, timeStep{"timezone", func(ctx context.Context) error {
			return models.Bus.SetTimezone(ctx, tz)
		}, [2]string{"tz", tz}})
	}
	if b.NTP != nil {
		ntp := *b.NTP
		steps = append(steps, timeStep{"ntp", func(ctx context.Context) error {
			return models.Bus.SetNTP(ctx, ntp)
		}, [2]string{"ntp", strconv.FormatBool(ntp)}})
	}
	if b.Time != nil {
		usec := setTime.Unix() * 1_000_000
		steps = append(steps, timeStep{"time", func(ctx context.Context) error {
			return models.Bus.SetTime(ctx, usec)
		}, [2]string{"time", "set"}})
	}

	applied := []string{}
	for _, s := range steps {
		if err := onTimedate(s.apply); err != nil {
			resetBoxTimeCache()
			e := errInternal(err)
			e.data = fiber.Map{"applied": applied, "failed": s.field}
			return e
		}
		applied = append(applied, s.field)
		auditField(c, s.audit[0], s.audit[1])
	}

	after, err := a.readBoxTime(true)
	if err != nil {
		resetBoxTimeCache()
		e := errInternal(err)
		e.data = fiber.Map{"applied": applied}
		return e
	}
	return sendOK(c, fiber.StatusOK, boxTimeSet{
		boxTime:         after,
		RestartRequired: b.Timezone != nil && *b.Timezone != before.Timezone,
	})
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
