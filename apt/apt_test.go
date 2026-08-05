package apt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
	pkgs, err := GetCachePackages()
	if err != nil {
		// An environment without a usable apt state (no dpkg database, cache
		// lock held, not root) legitimately fails here. That failure being
		// reported at all is the point of CC-020; TestGetCachePackagesError
		// covers the reporting itself.
		t.Skipf("apt cache unavailable in this environment: %v", err)
	}

	for i, p := range pkgs {
		if p == nil {
			t.Fatalf("package %d is nil", i)
		}
		if p.Name == "" {
			t.Errorf("package %d has an empty name (%+v)", i, p)
		}
		if p.VersionNew == "" {
			t.Errorf("package %d has an empty new version (%+v)", i, p)
		}
	}
}

// TestCacheArrayBounds checks the raw C accessors reject out-of-range indices
// and tolerate a NULL handle (see cacheArrayProbeForTest).
func TestCacheArrayBounds(t *testing.T) {
	ok, count, err := cacheArrayProbeForTest()
	if err != nil {
		t.Skipf("apt cache unavailable in this environment: %v", err)
	}
	if !ok {
		t.Fatalf("aptCacheArrayGet bounds check failed (count=%d)", count)
	}
}

// TestGetCachePackagesError is the regression test for the core of CC-020: a
// broken apt setup must surface as an error, never as an empty package list
// (which callers would report as "already up to date").
//
// libapt reads APT_CONFIG once, when pkgInitConfig() runs in this package's
// init(), so the broken configuration cannot be installed from inside the
// running test binary: the test re-executes itself as a child with APT_CONFIG
// pointing at a config tree that does not exist.
func TestGetCachePackagesError(t *testing.T) {
	if os.Getenv("CALAOS_APT_BROKEN_CHILD") == "1" {
		pkgs, err := GetCachePackages()
		if err == nil {
			fmt.Printf("CHILD-UNEXPECTED-SUCCESS: %d packages\n", len(pkgs))
			os.Exit(2)
		}
		if pkgs != nil {
			fmt.Printf("CHILD-NON-NIL-LIST: %d packages\n", len(pkgs))
			os.Exit(3)
		}
		fmt.Printf("CHILD-ERROR: %v\n", err)
		os.Exit(0)
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, "broken.conf")

	// Point every apt directory at a path that cannot exist: opening the cache
	// must fail, and the reason must reach the caller.
	content := fmt.Sprintf("Dir %q;\nDir::State::status %q;\n",
		filepath.Join(dir, "nonexistent")+"/",
		filepath.Join(dir, "nonexistent", "status"))
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatalf("writing apt config: %v", err)
	}

	// os.Args[0] is this very test binary, not external input.
	//nolint:gosec // G204: re-executing the test binary is the point here.
	cmd := exec.Command(os.Args[0], "-test.run=TestGetCachePackagesError", "-test.v")
	cmd.Env = append(os.Environ(), "CALAOS_APT_BROKEN_CHILD=1", "APT_CONFIG="+cfg)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child did not report the broken apt setup as an error: %v\noutput:\n%s", err, out)
	}

	if !strings.Contains(string(out), "CHILD-ERROR:") {
		t.Fatalf("child output missing the propagated error:\n%s", out)
	}

	t.Logf("propagated error from the broken apt setup:\n%s", out)
}
