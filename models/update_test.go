package models

import (
	"sync"
	"testing"
	"time"

	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/models/structs"
	"github.com/knadh/koanf/providers/confmap"
)

// setUpdateTimeConfig sets general.update_time in the shared config so the
// lifecycle tests do not depend on a config file being present.
func setUpdateTimeConfig(t *testing.T, v string) {
	t.Helper()

	err := config.Config.Load(confmap.Provider(map[string]interface{}{
		"general.update_time": v,
	}, "."), nil)
	if err != nil {
		t.Fatalf("failed to set update_time config: %v", err)
	}
}

func TestParseUpdateTime(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "valid hours", value: "12h", want: 12 * time.Hour},
		{name: "valid seconds", value: "5s", want: 5 * time.Second},
		{name: "empty (missing key)", value: "", wantErr: true},
		{name: "malformed", value: "notaduration", wantErr: true},
		{name: "zero rejected", value: "0s", wantErr: true},
		{name: "negative rejected", value: "-1h", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setUpdateTimeConfig(t, tc.value)

			got, err := parseUpdateTime()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseUpdateTime(%q) = %v, want error", tc.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUpdateTime(%q) returned unexpected error: %v", tc.value, err)
			}
			if got != tc.want {
				t.Fatalf("parseUpdateTime(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestGetNewVersionsReturnsCopy ensures callers cannot mutate the shared map
// through the value returned by GetNewVersions.
func TestGetNewVersionsReturnsCopy(t *testing.T) {
	setNewVersions(structs.ImageMap{
		"calaos-container": {Name: "calaos-container", Version: "2.0", CurrentVersion: "1.0"},
	}, nil)
	t.Cleanup(func() { setNewVersions(structs.ImageMap{}, nil) })

	got := GetNewVersions()
	got["injected"] = structs.Image{Name: "injected"}
	delete(got, "calaos-container")

	again := GetNewVersions()
	if _, ok := again["injected"]; ok {
		t.Fatal("mutating the returned map leaked into the shared state")
	}
	if _, ok := again["calaos-container"]; !ok {
		t.Fatal("deleting from the returned map leaked into the shared state")
	}
}

// TestNewVersionsConcurrentReadWrite is the regression test for the concurrent
// map read/write panic: GET /update/available marshalled the exported map while
// a check was rebuilding it. Run under -race, readers and writers must not
// race. It exercises both accessor paths (whole-map copy and single lookup).
func TestNewVersionsConcurrentReadWrite(t *testing.T) {
	t.Cleanup(func() { setNewVersions(structs.ImageMap{}, nil) })

	const (
		readers    = 16
		writers    = 4
		iterations = 200
	)

	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m := structs.ImageMap{}
				for k := 0; k < 10; k++ {
					name := "pkg" + string(rune('a'+k))
					m[name] = structs.Image{Name: name, Version: "1.0", CurrentVersion: "0.9"}
				}
				setNewVersions(m, map[string]bool{"pkgz": true})
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				for name, img := range GetNewVersions() {
					if img.Name != name {
						t.Errorf("inconsistent entry: key %q holds %q", name, img.Name)
						return
					}
				}
				getNewVersion("pkga")
			}
		}()
	}

	wg.Wait()
}

// TestInitShutdownLifecycle covers the WaitGroup pairing and the shutdown
// semantics: Init starts the periodic goroutine, Shutdown stops it and returns,
// a second Shutdown is a no-op, and a Shutdown after a failed Init does not
// panic on a nil channel. The interval is long enough that no tick fires, so no
// apt command is run.
func TestInitShutdownLifecycle(t *testing.T) {
	t.Cleanup(resetSharedStatus)

	setUpdateTimeConfig(t, "1h")
	if err := Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown() did not return: the check goroutine ignored the quit channel")
	}

	// Shutting down twice, and shutting down when Init never started the
	// goroutine, must both be no-ops rather than panics.
	Shutdown()
	Shutdown()
}

// TestInitRejectsInvalidUpdateTime ensures a bad update_time aborts startup by
// returning an error (main.go exits on it) instead of calling Fatalf from a
// goroutine. No goroutine must be left behind.
func TestInitRejectsInvalidUpdateTime(t *testing.T) {
	t.Cleanup(resetSharedStatus)

	setUpdateTimeConfig(t, "definitely-not-a-duration")

	if err := Init(); err == nil {
		Shutdown()
		t.Fatal("Init() accepted an invalid update_time, want an error")
	}

	// Init bailed out before spawning anything: Shutdown must still be safe.
	Shutdown()
}
