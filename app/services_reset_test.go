package app

import (
	"testing"

	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/sysbus"
)

// One read of the unit per action, whatever the verb: the failed state the
// stop acts on comes from the gate, not from a second look.
func TestServiceActionReadsTheUnitOnce(t *testing.T) {
	failed := sysbus.UnitProperties{LoadState: "loaded", ActiveState: "failed", UnitFileState: "enabled"}
	for _, verb := range []string{models.ActionStart, models.ActionStop, models.ActionEnable, models.ActionDisable} {
		t.Run(verb, func(t *testing.T) {
			f := useFakeBus(t)
			f.SetUnit("mosquitto.service", failed)
			(&stubBackend{}).install(t)
			a := newHandlerTestApp(t)

			if status, _, body := postAction(t, a, verb, "mosquitto"); status != 200 {
				t.Fatalf("%d %s", status, body)
			}
			if n := f.Reads("UnitProperties"); n != 1 {
				t.Fatalf("%d reads", n)
			}
		})
	}
}
