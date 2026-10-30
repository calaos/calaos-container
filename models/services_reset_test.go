package models

import (
	"context"
	"testing"
	"time"

	"github.com/calaos/calaos-container/sysbus"
	"github.com/calaos/calaos-container/sysbus/sysbustest"
)

// The state that decides whether a stop clears the unit is the one CheckUnit
// read before the action: a second read after the job is queued would see a
// unit that the stop itself may have just failed.
func TestQueueActionDoesNotReadTheUnit(t *testing.T) {
	mosquitto, _ := LookupService("mosquitto")
	for _, verb := range []string{ActionStart, ActionStop, ActionEnable, ActionDisable} {
		for _, failed := range []bool{true, false} {
			f := sysbustest.New()
			f.SetUnit("x.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: "failed"})
			job, err := QueueAction(f, mosquitto, "x.service", verb, failed, time.Second)
			if err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			if err := job.Wait(context.Background()); err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			if n := f.Reads("UnitProperties"); n != 0 {
				t.Fatalf("%s failed=%v: %d reads", verb, failed, n)
			}
		}
	}
}

// Restart clears whatever state the unit is in, and reads it once: for the
// gate only.
func TestQueueRestartResetsAnyState(t *testing.T) {
	ctx := context.Background()
	for _, active := range []string{"failed", "active", "inactive", ""} {
		f := sysbustest.New()
		f.SetUnit("x.service", sysbus.UnitProperties{LoadState: "loaded", ActiveState: active})
		job, err := QueueRestart(ctx, f, Service{ID: "mosquitto"}, "x.service")
		if err != nil {
			t.Fatalf("%q: %v", active, err)
		}
		if err := job.Wait(ctx); err != nil {
			t.Fatalf("%q: %v", active, err)
		}
		if n := f.Reads("UnitProperties"); n != 1 {
			t.Fatalf("%q: %d reads", active, n)
		}
		want := []string{"ResetFailedUnit x.service", "RestartUnit x.service"}
		if c := f.Calls(); len(c) != 2 || c[0] != want[0] || c[1] != want[1] {
			t.Fatalf("%q: %q", active, c)
		}
	}
}
