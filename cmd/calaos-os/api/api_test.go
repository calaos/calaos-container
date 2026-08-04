package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUpdatePackageDaemonError verifies that a non-2xx daemon reply carrying
// {"error":true,"msg":...} is surfaced as a Go error (makeHttpCall only fails
// on transport errors). A hung or silently-successful CLI on "package not
// found" was the observable symptom without this.
func TestUpdatePackageDaemonError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true,"msg":"package not found"}`))
	}))
	defer srv.Close()

	capi := NewCalaosApi(strings.TrimPrefix(srv.URL, "http://"))

	err := capi.UpdatePackage("tok", "nonexistent")
	if err == nil {
		t.Fatalf("expected an error for a 500 daemon reply, got nil")
	}
	if err.Error() != "package not found" {
		t.Fatalf("expected daemon msg to be surfaced, got %q", err.Error())
	}

	if err := capi.UpgradePackages("tok"); err == nil {
		t.Fatalf("expected an error for a 500 daemon reply (upgrade-all), got nil")
	}
}

// TestUpdatePackageOK verifies the success path returns nil.
func TestUpdatePackageOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"msg":"Upgrade done"}`))
	}))
	defer srv.Close()

	capi := NewCalaosApi(strings.TrimPrefix(srv.URL, "http://"))

	if err := capi.UpdatePackage("tok", "pkg"); err != nil {
		t.Fatalf("expected nil error on 200, got %v", err)
	}
	if err := capi.UpgradePackages("tok"); err != nil {
		t.Fatalf("expected nil error on 200 (upgrade-all), got %v", err)
	}
}
