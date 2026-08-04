package structs

import (
	"encoding/json"
	"testing"
)

func TestPkgStateConstants(t *testing.T) {
	cases := map[string]string{
		PkgStatePending:     "pending",
		PkgStateDownloading: "downloading",
		PkgStateUnpacking:   "unpacking",
		PkgStateInstalling:  "installing",
		PkgStateDone:        "done",
		PkgStateFailed:      "failed",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("constant value %q, want %q", got, want)
		}
	}
}

func TestPkgStatusJSONFieldNames(t *testing.T) {
	p := PkgStatus{Name: "calaos-base", State: PkgStateDownloading, Percent: 50}

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	wantKeys := map[string]any{
		"name":    "calaos-base",
		"state":   "downloading",
		"percent": float64(50),
	}
	if len(m) != len(wantKeys) {
		t.Fatalf("got keys %v, want exactly %v", m, wantKeys)
	}
	for k, want := range wantKeys {
		got, ok := m[k]
		if !ok {
			t.Fatalf("missing expected JSON key %q in %v", k, m)
		}
		if got != want {
			t.Errorf("key %q = %v, want %v", k, got, want)
		}
	}
}

func TestStatusJSONFieldNamesFullyPopulated(t *testing.T) {
	s := Status{
		Status:         "upgrading",
		CurrentPkg:     "calaos-base",
		Progress:       2,
		ProgressTotal:  10,
		TotalPercent:   35,
		CurrentStep:    "unpack",
		CurrentPercent: 60,
		PkgIndex:       2,
		PkgCount:       10,
		Packages: []PkgStatus{
			{Name: "calaos-base", State: PkgStateUnpacking, Percent: 60},
		},
		Message: "unpacking calaos-base",
		Error:   "",
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// "error" is empty string with omitempty -> must be absent.
	if _, ok := m["error"]; ok {
		t.Errorf("expected \"error\" to be omitted when empty, got present: %v", m["error"])
	}

	wantPresent := []string{
		"status", "current_pkg", "progress", "progress_total",
		"total_percent", "current_step", "current_percent",
		"pkg_index", "pkg_count", "packages", "message",
	}
	for _, k := range wantPresent {
		if _, ok := m[k]; !ok {
			t.Errorf("missing expected JSON key %q in %v", k, m)
		}
	}

	// Spot-check exact contract values/types.
	if m["status"] != "upgrading" {
		t.Errorf(`status = %v, want "upgrading"`, m["status"])
	}
	if m["total_percent"] != float64(35) {
		t.Errorf("total_percent = %v, want 35", m["total_percent"])
	}
	if m["current_step"] != "unpack" {
		t.Errorf(`current_step = %v, want "unpack"`, m["current_step"])
	}
	if m["pkg_index"] != float64(2) {
		t.Errorf("pkg_index = %v, want 2", m["pkg_index"])
	}
	if m["pkg_count"] != float64(10) {
		t.Errorf("pkg_count = %v, want 10", m["pkg_count"])
	}

	packages, ok := m["packages"].([]any)
	if !ok || len(packages) != 1 {
		t.Fatalf("packages = %v, want a single-element array", m["packages"])
	}
	pkg, ok := packages[0].(map[string]any)
	if !ok {
		t.Fatalf("packages[0] is not an object: %v", packages[0])
	}
	if pkg["state"] != "unpacking" {
		t.Errorf(`packages[0].state = %v, want "unpacking"`, pkg["state"])
	}
	if pkg["percent"] != float64(60) {
		t.Errorf("packages[0].percent = %v, want 60", pkg["percent"])
	}
}

// TestStatusJSONOmitEmpty verifies backward compatibility: a Status with
// only the historical fields set must marshal exactly like the old,
// pre-enrichment struct (aside from "total_percent" which has no
// omitempty and is always emitted, per the new contract).
func TestStatusJSONOmitEmpty(t *testing.T) {
	s := Status{Status: "idle"}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Only "status" and "total_percent" (no omitempty) should be present.
	wantKeys := map[string]any{
		"status":        "idle",
		"total_percent": float64(0),
	}
	if len(m) != len(wantKeys) {
		t.Fatalf("got keys %v, want exactly %v", m, wantKeys)
	}
	for k, want := range wantKeys {
		got, ok := m[k]
		if !ok {
			t.Fatalf("missing expected JSON key %q in %v", k, m)
		}
		if got != want {
			t.Errorf("key %q = %v, want %v", k, got, want)
		}
	}
}

// TestStatusBackwardCompatibleFields ensures the historical field/JSON tag
// pairs kept their exact names and semantics (consumed by existing UIs).
func TestStatusBackwardCompatibleFields(t *testing.T) {
	s := Status{
		Status:        "error",
		CurrentPkg:    "calaos-base",
		Progress:      3,
		ProgressTotal: 7,
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m["status"] != "error" {
		t.Errorf(`status = %v, want "error"`, m["status"])
	}
	if m["current_pkg"] != "calaos-base" {
		t.Errorf(`current_pkg = %v, want "calaos-base"`, m["current_pkg"])
	}
	if m["progress"] != float64(3) {
		t.Errorf("progress = %v, want 3", m["progress"])
	}
	if m["progress_total"] != float64(7) {
		t.Errorf("progress_total = %v, want 7", m["progress_total"])
	}
}

// TestStatusUnmarshalOldJSON ensures a JSON payload matching the old,
// pre-enrichment contract still unmarshals correctly into the enriched
// struct (rétro-compat).
func TestStatusUnmarshalOldJSON(t *testing.T) {
	old := `{"status":"upgrading","current_pkg":"calaos-base","progress":1,"progress_total":5}`

	var s Status
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if s.Status != "upgrading" {
		t.Errorf("Status = %q, want %q", s.Status, "upgrading")
	}
	if s.CurrentPkg != "calaos-base" {
		t.Errorf("CurrentPkg = %q, want %q", s.CurrentPkg, "calaos-base")
	}
	if s.Progress != 1 {
		t.Errorf("Progress = %d, want 1", s.Progress)
	}
	if s.ProgressTotal != 5 {
		t.Errorf("ProgressTotal = %d, want 5", s.ProgressTotal)
	}
}
