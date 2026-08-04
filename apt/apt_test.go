package apt

import (
	"reflect"
	"testing"
)

// captureProgress registers a progress callback collecting all events, and
// unregisters it on test cleanup.
func captureProgress(t *testing.T) *[]Progress {
	t.Helper()

	var events []Progress

	progressMu.Lock()
	progressCb = func(p Progress) { events = append(events, p) }
	progressMu.Unlock()

	t.Cleanup(func() {
		progressMu.Lock()
		progressCb = nil
		progressMu.Unlock()
	})

	return &events
}

// TestProgressBridge exercises the full C -> Go path: aptEmitProgress (C)
// -> progressBridge (C shim) -> goAptProgress (//export) -> registered Go
// callback, including C string/int conversions and the monotonic guard on
// the global percentage.
func TestProgressBridge(t *testing.T) {
	events := captureProgress(t)

	emitProgressForTest("download", "calaos-server", 42, 10, "fetching archive")
	emitProgressForTest("install", "calaos-server", -1, 55, "")
	// Lower global % than the previous event: the monotonic guard must
	// clamp TotalPercent to 55.
	emitProgressForTest("unpack", "other-pkg", 40, 30, "Unpacking other-pkg")

	want := []Progress{
		{Stage: "download", Pkg: "calaos-server", PkgPercent: 42, TotalPercent: 10, Msg: "fetching archive"},
		{Stage: "install", Pkg: "calaos-server", PkgPercent: -1, TotalPercent: 55, Msg: ""},
		{Stage: "unpack", Pkg: "other-pkg", PkgPercent: 40, TotalPercent: 55, Msg: "Unpacking other-pkg"},
	}

	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("progress events mismatch:\n got: %+v\nwant: %+v", *events, want)
	}
}

// TestProgressBridgeNoCallback checks that emitting without a registered
// callback is a safe no-op (no crash, nothing delivered).
func TestProgressBridgeNoCallback(t *testing.T) {
	emitProgressForTest("download", "pkg", 0, 60, "no listener")

	events := captureProgress(t)
	emitProgressForTest("done", "", 100, 100, "")

	if len(*events) != 1 || (*events)[0].Stage != "done" {
		t.Fatalf("expected only the 'done' event, got %+v", *events)
	}
}

// TestGetCachePackagesSmoke ensures listing upgradable packages through the
// C++ layer does not crash and returns well-formed entries.
func TestGetCachePackagesSmoke(t *testing.T) {
	pkgs := GetCachePackages()

	for i, p := range pkgs {
		if p == nil {
			t.Fatalf("package %d is nil", i)
		}
		if p.Name == "" {
			t.Errorf("package %d has an empty name (%+v)", i, p)
		}
	}
}
