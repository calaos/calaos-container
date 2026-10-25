package app

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/models"
)

const (
	timeCmdLimit = 10 * time.Second
	timeCacheTTL = time.Second
)

var (
	runTimeCommand = models.RunCommandContext
	timezoneRe     = regexp.MustCompile(`^[A-Za-z0-9_+/][A-Za-z0-9_+\-/]{0,63}$`)
)

// The daemon only drives timedated: it never writes /etc/timezone and sets
// no TZ (docs/API.md, Box time).
var timeState struct {
	setMu sync.Mutex

	zonesMu sync.Mutex
	zones   []string

	// GET system/time is polled: one timedatectl per second at most.
	showMu sync.Mutex
	showAt time.Time
	show   *models.TimedateState
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

func timedatectl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeCmdLimit)
	defer cancel()
	return runTimeCommand(ctx, "timedatectl", args...)
}

// timedateState reads timedated, from the cache unless fresh is set.
func timedateState(fresh bool) (models.TimedateState, error) {
	timeState.showMu.Lock()
	defer timeState.showMu.Unlock()
	if !fresh && timeState.show != nil && time.Since(timeState.showAt) < timeCacheTTL {
		return *timeState.show, nil
	}
	out, err := timedatectl("show")
	if err != nil {
		return models.TimedateState{}, err
	}
	st, err := models.ParseTimedateShow(out)
	if err != nil {
		return models.TimedateState{}, err
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
		Synchronized: st.Synchronized,
		RTC:          st.RTC,
	}, nil
}

// timezones caches the first successful list: it only changes with tzdata.
func timezones() ([]string, error) {
	timeState.zonesMu.Lock()
	defer timeState.zonesMu.Unlock()
	if timeState.zones != nil {
		return timeState.zones, nil
	}
	out, err := timedatectl("list-timezones")
	if err != nil {
		return nil, err
	}
	zones := []string{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			zones = append(zones, l)
		}
	}
	if len(zones) == 0 {
		return nil, errors.New("timedatectl list-timezones: empty list")
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
	args  []string
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
		steps = append(steps, timeStep{"timezone", []string{"set-timezone", *b.Timezone}, [2]string{"tz", *b.Timezone}})
	}
	if b.NTP != nil {
		v := "false"
		if *b.NTP {
			v = "true"
		}
		steps = append(steps, timeStep{"ntp", []string{"set-ntp", v}, [2]string{"ntp", v}})
	}
	if b.Time != nil {
		steps = append(steps, timeStep{"time", []string{"set-time", setTime.UTC().Format("2006-01-02 15:04:05") + " UTC"}, [2]string{"time", "set"}})
	}

	applied := []string{}
	for _, s := range steps {
		if _, err := timedatectl(s.args...); err != nil {
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
