package models

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/calaos/calaos-container/apt"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models/structs"
)

// TestApplyAptProgressSliceAliasingRace guards against the Packages backing
// array being shared between a published/GetStatus snapshot and the internal
// state still mutated in place by applyAptProgress. A concurrent reader of
// published snapshots must not race with the cgo-thread writer. Run with -race.
func TestApplyAptProgressSliceAliasingRace(t *testing.T) {
	resetSharedStatus()
	initUpgradeStatus()

	sub := events.Subscribe()
	defer events.Unsubscribe(sub)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	read := func(s structs.Status) {
		for i := range s.Packages {
			_ = s.Packages[i].State
			_ = s.Packages[i].Percent
			_ = s.Packages[i].Name
		}
	}

	// Subscriber reading every published snapshot, like a WS/HTTP handler.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case ev := <-sub.Ch:
				if s, ok := ev.Data.(structs.Status); ok {
					read(s)
				}
			case <-stop:
				for {
					select {
					case ev := <-sub.Ch:
						if s, ok := ev.Data.(structs.Status); ok {
							read(s)
						}
					default:
						return
					}
				}
			}
		}
	}()

	// Concurrent GetStatus reader, like GET /api/update/status.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				read(upgradeStatus.GetStatus())
			}
		}
	}()

	pkgs := []string{"pkgA", "pkgB", "pkgC"}
	for _, p := range pkgs {
		applyAptProgress(apt.Progress{Stage: "pending", Pkg: p, PkgPercent: -1})
	}
	stages := []string{"download", "unpack", "configure", "install", "done"}
	for i := 0; i < 200; i++ {
		applyAptProgress(apt.Progress{
			Stage: stages[i%len(stages)], Pkg: pkgs[i%len(pkgs)],
			PkgPercent: i % 100, TotalPercent: i % 100,
		})
	}

	close(stop)
	wg.Wait()
}

// resetSharedStatus puts the shared upgradeStatus back to a known clean state
// so tests do not leak state into each other.
func resetSharedStatus() {
	upgradeStatus.Lock()
	upgradeStatus.Status = structs.Status{}
	upgradeStatus.lastPublish = time.Time{}
	upgradeStatus.Unlock()
}

func TestApplyAptProgressMapping(t *testing.T) {
	resetSharedStatus()
	initUpgradeStatus()

	// Two packages announced as pending.
	applyAptProgress(apt.Progress{Stage: "pending", Pkg: "pkgA", PkgPercent: -1, TotalPercent: 0})
	applyAptProgress(apt.Progress{Stage: "pending", Pkg: "pkgB", PkgPercent: -1, TotalPercent: 0})

	st := upgradeStatus.GetStatus()
	if st.PkgCount != 2 || len(st.Packages) != 2 {
		t.Fatalf("expected 2 packages, got PkgCount=%d len=%d", st.PkgCount, len(st.Packages))
	}
	if st.ProgressTotal != 2 {
		t.Fatalf("expected ProgressTotal=2 (retro-compat PkgCount), got %d", st.ProgressTotal)
	}
	if st.Packages[0].State != structs.PkgStatePending || st.Packages[1].State != structs.PkgStatePending {
		t.Fatalf("expected both packages pending, got %+v", st.Packages)
	}

	// Download pkgA.
	applyAptProgress(apt.Progress{Stage: "download", Pkg: "pkgA", PkgPercent: 30, TotalPercent: 10})
	st = upgradeStatus.GetStatus()
	if st.Packages[0].State != structs.PkgStateDownloading || st.Packages[0].Percent != 30 {
		t.Fatalf("expected pkgA downloading/30, got %+v", st.Packages[0])
	}
	if st.CurrentStep != "download" || st.CurrentPkg != "pkgA" || st.CurrentPercent != 30 {
		t.Fatalf("unexpected current fields: step=%q pkg=%q pct=%d", st.CurrentStep, st.CurrentPkg, st.CurrentPercent)
	}
	if st.PkgIndex != 1 || st.Progress != 1 {
		t.Fatalf("expected PkgIndex=1/Progress=1, got %d/%d", st.PkgIndex, st.Progress)
	}
	if st.TotalPercent != 10 {
		t.Fatalf("expected TotalPercent=10, got %d", st.TotalPercent)
	}

	// Unpack pkgA.
	applyAptProgress(apt.Progress{Stage: "unpack", Pkg: "pkgA", PkgPercent: 40, TotalPercent: 30})
	st = upgradeStatus.GetStatus()
	if st.Packages[0].State != structs.PkgStateUnpacking {
		t.Fatalf("expected pkgA unpacking, got %q", st.Packages[0].State)
	}

	// Configure pkgA -> installing.
	applyAptProgress(apt.Progress{Stage: "configure", Pkg: "pkgA", PkgPercent: 80, TotalPercent: 45})
	st = upgradeStatus.GetStatus()
	if st.Packages[0].State != structs.PkgStateInstalling {
		t.Fatalf("expected pkgA installing (configure), got %q", st.Packages[0].State)
	}

	// install stage also maps to installing.
	applyAptProgress(apt.Progress{Stage: "install", Pkg: "pkgA", PkgPercent: -1, TotalPercent: 50})
	st = upgradeStatus.GetStatus()
	if st.Packages[0].State != structs.PkgStateInstalling {
		t.Fatalf("expected pkgA installing (install), got %q", st.Packages[0].State)
	}
	// PkgPercent -1 must not overwrite the last known percent.
	if st.Packages[0].Percent != 80 {
		t.Fatalf("PkgPercent -1 should not change percent, got %d", st.Packages[0].Percent)
	}

	// Done pkgA -> done/100.
	applyAptProgress(apt.Progress{Stage: "done", Pkg: "pkgA", PkgPercent: 100, TotalPercent: 60})
	st = upgradeStatus.GetStatus()
	if st.Packages[0].State != structs.PkgStateDone || st.Packages[0].Percent != 100 {
		t.Fatalf("expected pkgA done/100, got %+v", st.Packages[0])
	}

	// Now pkgB moves through -> PkgIndex must be 2.
	applyAptProgress(apt.Progress{Stage: "download", Pkg: "pkgB", PkgPercent: 10, TotalPercent: 65})
	st = upgradeStatus.GetStatus()
	if st.PkgIndex != 2 || st.Progress != 2 {
		t.Fatalf("expected PkgIndex=2 for pkgB, got %d", st.PkgIndex)
	}

	// Error on pkgB -> failed.
	applyAptProgress(apt.Progress{Stage: "error", Pkg: "pkgB", PkgPercent: -1, TotalPercent: 65, Msg: "E: http://deb.example/pool/x.deb: boom"})
	st = upgradeStatus.GetStatus()
	if st.Packages[1].State != structs.PkgStateFailed {
		t.Fatalf("expected pkgB failed, got %q", st.Packages[1].State)
	}
	// apt messages stay in the journal: the progress goes to every subscriber.
	if st.Message != "" {
		t.Fatalf("apt message published: %q", st.Message)
	}
}

func TestApplyAptProgressTotalMonotonic(t *testing.T) {
	resetSharedStatus()
	initUpgradeStatus()

	seq := []apt.Progress{
		{Stage: "pending", Pkg: "p1", PkgPercent: -1, TotalPercent: 0},
		{Stage: "download", Pkg: "p1", PkgPercent: 50, TotalPercent: 20},
		{Stage: "unpack", Pkg: "p1", PkgPercent: 40, TotalPercent: 45},
		{Stage: "configure", Pkg: "p1", PkgPercent: 80, TotalPercent: 70},
		{Stage: "done", Pkg: "p1", PkgPercent: 100, TotalPercent: 100},
	}

	last := 0
	for _, p := range seq {
		applyAptProgress(p)
		got := upgradeStatus.GetStatus().TotalPercent
		if got < last {
			t.Fatalf("TotalPercent decreased: %d -> %d", last, got)
		}
		last = got
	}
	if last != 100 {
		t.Fatalf("expected final TotalPercent 100, got %d", last)
	}
}

func TestFinishUpgradeDone(t *testing.T) {
	resetSharedStatus()
	initUpgradeStatus()
	applyAptProgress(apt.Progress{Stage: "pending", Pkg: "p1", PkgPercent: -1, TotalPercent: 0})
	applyAptProgress(apt.Progress{Stage: "download", Pkg: "p1", PkgPercent: 50, TotalPercent: 30})

	finishUpgrade(nil)

	st := upgradeStatus.GetStatus()
	if st.Status != "done" {
		t.Fatalf("expected status done, got %q", st.Status)
	}
	if st.TotalPercent != 100 {
		t.Fatalf("expected TotalPercent 100, got %d", st.TotalPercent)
	}
	if st.Error != "" {
		t.Fatalf("expected no error, got %q", st.Error)
	}
	for _, p := range st.Packages {
		if p.State != structs.PkgStateDone || p.Percent != 100 {
			t.Fatalf("expected all packages done/100, got %+v", p)
		}
	}
}

func TestFinishUpgradeError(t *testing.T) {
	resetSharedStatus()
	initUpgradeStatus()
	applyAptProgress(apt.Progress{Stage: "pending", Pkg: "p1", PkgPercent: -1, TotalPercent: 0})
	applyAptProgress(apt.Progress{Stage: "download", Pkg: "p1", PkgPercent: 50, TotalPercent: 30})

	applyAptProgress(apt.Progress{Stage: "error", Pkg: "p1", PkgPercent: -1, TotalPercent: 30})

	finishUpgrade(errors.New("E: Failed to fetch http://deb.example/pool/p1.deb  /var/cache/apt/archives/partial"))

	st := upgradeStatus.GetStatus()
	if st.Status != "error" {
		t.Fatalf("expected status error, got %q", st.Status)
	}
	if st.Error != "upgrade failed: p1" {
		t.Fatalf("progress error %q, want the sanitized summary", st.Error)
	}
	// A failing package must not be forced to done.
	if st.TotalPercent == 100 {
		t.Fatalf("error path should not force TotalPercent to 100")
	}
}

func TestFinishUpgradeKeepsFailedPackages(t *testing.T) {
	resetSharedStatus()
	initUpgradeStatus()
	applyAptProgress(apt.Progress{Stage: "pending", Pkg: "p1", PkgPercent: -1, TotalPercent: 0})
	applyAptProgress(apt.Progress{Stage: "pending", Pkg: "p2", PkgPercent: -1, TotalPercent: 0})
	applyAptProgress(apt.Progress{Stage: "error", Pkg: "p2", PkgPercent: -1, TotalPercent: 20})

	// Even on a "done" finish (partial), a failed package stays failed.
	finishUpgrade(nil)

	st := upgradeStatus.GetStatus()
	if st.Packages[0].State != structs.PkgStateDone {
		t.Fatalf("expected p1 done, got %q", st.Packages[0].State)
	}
	if st.Packages[1].State != structs.PkgStateFailed {
		t.Fatalf("expected p2 to stay failed, got %q", st.Packages[1].State)
	}
}

// TestSetStatusThrottling verifies transitions are always published, close
// percentage-only ticks are throttled, and the terminal event is never lost.
func TestSetStatusThrottling(t *testing.T) {
	resetSharedStatus()

	sub := events.Subscribe()
	defer events.Unsubscribe(sub)

	drain := func() []structs.Status {
		var out []structs.Status
		for {
			select {
			case ev := <-sub.Ch:
				if s, ok := ev.Data.(structs.Status); ok {
					out = append(out, s)
				}
			default:
				return out
			}
		}
	}

	// Transition: status upgrading with pkg -> always published.
	upgradeStatus.SetStatus(structs.Status{Status: "upgrading", CurrentPkg: "p1", CurrentStep: "download", PkgIndex: 1})
	// Percentage-only ticks in quick succession (same transition fields) ->
	// throttled: only the first (already counted above) publishes, the rest
	// within 100ms are dropped.
	for pct := 10; pct <= 90; pct += 10 {
		upgradeStatus.SetStatus(structs.Status{
			Status: "upgrading", CurrentPkg: "p1", CurrentStep: "download", PkgIndex: 1,
			CurrentPercent: pct, TotalPercent: pct,
		})
	}

	got := drain()
	// The initial transition must be there.
	if len(got) == 0 || got[0].CurrentPkg != "p1" {
		t.Fatalf("expected initial transition published, got %+v", got)
	}
	// The burst of same-transition ticks must have been throttled: we should
	// have far fewer than 1 (transition) + 9 (ticks) events.
	if len(got) > 3 {
		t.Fatalf("expected percentage ticks to be throttled, got %d events: %+v", len(got), got)
	}

	// A transition (new PkgIndex) is always published even right after ticks.
	upgradeStatus.SetStatus(structs.Status{Status: "upgrading", CurrentPkg: "p2", CurrentStep: "download", PkgIndex: 2})
	got = drain()
	found := false
	for _, s := range got {
		if s.PkgIndex == 2 && s.CurrentPkg == "p2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected transition to pkg p2 to be published, got %+v", got)
	}

	// Terminal event (status change) is a transition -> never dropped, even
	// immediately after another publish.
	upgradeStatus.SetStatus(structs.Status{Status: "done", TotalPercent: 100})
	got = drain()
	foundDone := false
	for _, s := range got {
		if s.Status == "done" {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected terminal 'done' event to be published, got %+v", got)
	}
}

// A step or status outside the vocabulary never carries a package name out.
func TestSetStatusClampsVocabulary(t *testing.T) {
	resetSharedStatus()
	initUpgradeStatus()
	t.Cleanup(resetSharedStatus)

	applyAptProgress(apt.Progress{Stage: "pending", Pkg: "pkgA", PkgPercent: -1})
	applyAptProgress(apt.Progress{Stage: "trigger:pkgA", Pkg: "pkgA", PkgPercent: -1})
	if st := upgradeStatus.GetStatus(); st.CurrentStep != "install" || st.Status != "upgrading" {
		t.Fatalf("unknown stage: step=%q status=%q", st.CurrentStep, st.Status)
	}
	upgradeStatus.SetStatus(structs.Status{Status: "error:pkgA", CurrentStep: "configure"})
	if st := upgradeStatus.GetStatus(); st.Status != "upgrading" || st.CurrentStep != "configure" {
		t.Fatalf("unknown status: step=%q status=%q", st.CurrentStep, st.Status)
	}
	for _, step := range []string{"pending", "download", "unpack", "configure", "install", "done", "error"} {
		upgradeStatus.SetStatus(structs.Status{Status: "upgrading", CurrentStep: step})
		if got := upgradeStatus.GetStatus().CurrentStep; got != step {
			t.Fatalf("step %q became %q", step, got)
		}
	}
}
