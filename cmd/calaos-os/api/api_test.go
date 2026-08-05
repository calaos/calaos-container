package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// TestNewCalaosApiDefaultTimeout verifies that NewCalaosApi wires up a
// bounded default http.Client timeout (CC-023 item 3) instead of the
// previous zero-value (infinite) timeout, and that SetTimeout still
// overrides it for callers that need a different bound.
func TestNewCalaosApiDefaultTimeout(t *testing.T) {
	capi := NewCalaosApi("localhost:8000")
	if capi.apiClient.Timeout != DefaultTimeout {
		t.Fatalf("expected default timeout %s, got %s", DefaultTimeout, capi.apiClient.Timeout)
	}

	capi = NewCalaosApi("localhost:8000", SetTimeout(5*time.Second))
	if capi.apiClient.Timeout != 5*time.Second {
		t.Fatalf("expected overridden timeout 5s, got %s", capi.apiClient.Timeout)
	}
}

// TestCallTimesOutOnHungServer verifies that a server which accepts the TCP
// connection but never responds does not hang the CLI forever: the
// http.Client timeout (CC-023 item 3) must abort the call. Uses a short
// SetTimeout override to keep the test fast rather than waiting on
// DefaultTimeout.
func TestCallTimesOutOnHungServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Accept the connection but never write a response, and keep it
		// open until the test finishes, simulating a hung daemon.
		<-stop
	}()

	capi := NewCalaosApi(ln.Addr().String(), SetTimeout(200*time.Millisecond))

	start := time.Now()
	_, err = capi.UpdateCheck("tok")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a timeout error from a hung server, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("call took %s to fail, expected it to be bounded by the configured timeout", elapsed)
	}
}
