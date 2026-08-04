package models

import (
	"sync"
	"time"

	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models/structs"
)

// publishThrottle is the minimum delay between two hub publications that carry
// only a percentage tick (no state transition). Transitions are always
// published regardless of this throttle.
const publishThrottle = 100 * time.Millisecond

type SafeStatus struct {
	sync.Mutex
	Status structs.Status

	// lastPublish is the time of the last event published on the hub. It is
	// used to throttle percentage-only ticks.
	lastPublish time.Time
}

// cloneStatus returns a deep copy of st: the Packages slice is copied into a
// fresh backing array so the returned Status shares no mutable memory with st.
// This is required because the internal Status is mutated in place (element
// writes to Packages) by applyAptProgress on the cgo thread, while published
// snapshots and GetStatus results are read concurrently by HTTP/WS handlers.
func cloneStatus(st structs.Status) structs.Status {
	if st.Packages != nil {
		pkgs := make([]structs.PkgStatus, len(st.Packages))
		copy(pkgs, st.Packages)
		st.Packages = pkgs
	}
	return st
}

// isTransition reports whether the change from prev to next is a state
// transition (as opposed to a mere percentage tick). Transitions are always
// published on the hub, never throttled. A transition is a change of Status,
// CurrentPkg, CurrentStep or PkgIndex.
func isTransition(prev, next structs.Status) bool {
	return prev.Status != next.Status ||
		prev.CurrentPkg != next.CurrentPkg ||
		prev.CurrentStep != next.CurrentStep ||
		prev.PkgIndex != next.PkgIndex
}

func (s *SafeStatus) SetStatus(st structs.Status) {
	s.Lock()

	prev := s.Status
	s.Status = st

	transition := isTransition(prev, st)
	now := time.Now()
	publish := transition || now.Sub(s.lastPublish) >= publishThrottle
	if publish {
		s.lastPublish = now
	}

	// Deep-copy the snapshot while the lock is held: the internal Status is
	// mutated in place (Packages element writes) by later SetStatus calls, so
	// the published snapshot must NOT share the Packages backing array. The
	// Publish call itself happens after unlock so the (non-blocking) hub
	// broadcast never runs under lock.
	var snapshot structs.Status
	if publish {
		snapshot = cloneStatus(st)
	}

	s.Unlock()

	if publish {
		events.Publish(events.EventUpgradeProgress, snapshot)
	}
}

func (s *SafeStatus) GetStatus() structs.Status {
	s.Lock()
	defer s.Unlock()

	// Deep-copy so callers (HTTP handlers, applyAptProgress) get their own
	// Packages backing array and never race with in-place mutations of the
	// internal state.
	return cloneStatus(s.Status)
}

var (
	upgradeStatus SafeStatus
)

func resetStatus() {
	upgradeStatus.SetStatus(structs.Status{
		Status: "idle",
	})
}

func UpdateStatus() (*structs.Status, error) {
	st := upgradeStatus.GetStatus()
	return &st, nil
}
