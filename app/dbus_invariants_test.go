package app

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/sysbus"
)

// countGuard replaces serviceGuard with one that refuses and counts its calls.
func countGuard(t *testing.T, err error) *int {
	t.Helper()
	n := new(int)
	prev := serviceGuard
	serviceGuard = func() (func(), error) {
		*n++
		return nil, err
	}
	t.Cleanup(func() { serviceGuard = prev })
	return n
}

func restartBody(t *testing.T, unit string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"unit": unit})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Any id that is not exactly a restartable row of the table stops at the
// lookup: no guard taken, nothing on the bus. The refusal is synchronous, so
// the check needs no wait.
func TestServiceRestartMutatedIDsNeverReachGuardOrBus(t *testing.T) {
	f := useFakeBus(t)
	(&stubBackend{}).install(t)
	guards := countGuard(t, models.ErrUpgradeInProgress)
	a := newHandlerTestApp(t)

	var seeds []string
	for _, s := range models.Services {
		seeds = append(seeds, s.ID, s.Unit)
	}
	seeds = append(seeds, "getty@tty1", "ssh", "chrony", "-", "a b")

	rng := rand.New(rand.NewSource(20261030)) //nolint:gosec // fixed seed keeps the property test reproducible
	mutate := []func(string) string{
		strings.ToUpper,
		func(s string) string { return " " + s },
		func(s string) string { return s + "\n" },
		func(s string) string { return s + ".service" },
		func(s string) string { return s + "@x" },
		func(s string) string { return "../" + s },
		func(s string) string { return s + "\x00" },
		func(s string) string { return strings.Repeat(s, 2) },
		func(s string) string { return s[:len(s)/2] },
		func(s string) string { return "--" + s },
	}
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		id := seeds[rng.Intn(len(seeds))]
		for n := 1 + rng.Intn(2); n > 0; n-- {
			id = mutate[rng.Intn(len(mutate))](id)
		}
		if s, ok := models.LookupService(id); ok && s.Restartable {
			continue
		}
		seen[id] = true
		status, r, _ := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
			ctype: "application/json", body: restartBody(t, id)})
		if status != 400 || r.Code != codeInvalidRequest {
			t.Fatalf("%q: %d %q", id, status, r.Code)
		}
	}
	if len(seen) < 100 {
		t.Fatalf("only %d distinct hostile ids: the generator is broken", len(seen))
	}
	if *guards != 0 {
		t.Fatalf("guard taken %d times for refused ids", *guards)
	}
	if c := f.Calls(); len(c) != 0 || f.Reads("UnitProperties") != 0 {
		t.Fatalf("bus reached: %q", c)
	}
}

// The upgrade guard is asked once per accepted request, for every verb of the
// table, and before anything is sent to the bus.
func TestServiceRestartGuardAppliesToEveryRestartableUnit(t *testing.T) {
	f := useFakeBus(t)
	(&stubBackend{}).install(t)
	guards := countGuard(t, models.ErrUpgradeInProgress)
	a := newHandlerTestApp(t)

	want := 0
	for _, s := range models.Services {
		status, r, _ := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
			ctype: "application/json", body: restartBody(t, s.ID)})
		if s.Restartable {
			want++
			if status != 409 || r.Code != codeUpgradeInProgress {
				t.Fatalf("%s: %d %q", s.ID, status, r.Code)
			}
		} else if status != 400 || r.Code != codeInvalidRequest {
			t.Fatalf("%s: %d %q", s.ID, status, r.Code)
		}
		if *guards != want {
			t.Fatalf("%s: guard taken %d times, want %d", s.ID, *guards, want)
		}
	}
	if want == 0 {
		t.Fatal("no restartable service in the table")
	}
	if c := f.Calls(); len(c) != 0 || f.Reads("UnitProperties") != 0 {
		t.Fatalf("bus reached: %q", c)
	}
}

// What an accepted restart sends is the table's unit for that id, or the
// fallback the table lists, and nothing else.
func TestServiceRestartSendsOnlyTableUnits(t *testing.T) {
	f := useFakeBus(t)
	allowed := map[string]bool{}
	for _, s := range models.Services {
		for _, u := range models.UnitCandidates(s.Unit) {
			f.SetUnit(u, loadedUnit)
			allowed[u] = true
		}
	}
	f.SetUnit("chrony.service", sysbus.UnitProperties{LoadState: "not-found", ActiveState: "inactive", SubState: "dead"})
	(&stubBackend{}).install(t)
	done := trackGuard(t)
	prev := restartDelay
	restartDelay = 0
	t.Cleanup(func() { restartDelay = prev })
	a := newHandlerTestApp(t)

	var want []string
	for _, s := range models.Services {
		if !s.Restartable {
			continue
		}
		unit := s.Unit
		if s.ID == "ntp" {
			unit = "systemd-timesyncd.service"
		}
		want = append(want, "RestartUnit "+unit)

		status, r, _ := v2Do2(t, a, httpCase{method: "POST", path: "/api/v2/system/services/restart",
			ctype: "application/json", body: restartBody(t, s.ID)})
		if status != 200 {
			t.Fatalf("%s: %d %+v", s.ID, status, r)
		}
		waitRestart(t, done)
	}

	got := f.Calls()
	for _, c := range got {
		_, unit, _ := strings.Cut(c, " ")
		if !allowed[unit] {
			t.Fatalf("call %q, unit not in the table", c)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
}
