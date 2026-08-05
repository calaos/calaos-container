package main

import (
	"errors"
	"fmt"
	"net"
	"testing"
)

// TestToCIDR verifies the IPv4+netmask -> CIDR conversion used by
// `network configure static`, and that it reports an error instead of
// silently degrading on invalid input (CC-023 item 2).
func TestToCIDR(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		mask    string
		want    string
		wantErr bool
	}{
		{name: "class C", ip: "192.168.1.10", mask: "255.255.255.0", want: "192.168.1.10/24"},
		{name: "class B", ip: "10.0.5.2", mask: "255.255.0.0", want: "10.0.5.2/16"},
		{name: "invalid ip", ip: "bogus", mask: "255.255.255.0", wantErr: true},
		{name: "invalid mask", ip: "192.168.1.10", mask: "bogus", wantErr: true},
		{name: "empty ip", ip: "", mask: "255.255.255.0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toCIDR(tt.ip, tt.mask)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("toCIDR(%q, %q) = %q, want error", tt.ip, tt.mask, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("toCIDR(%q, %q) unexpected error: %v", tt.ip, tt.mask, err)
			}
			if got != tt.want {
				t.Fatalf("toCIDR(%q, %q) = %q, want %q", tt.ip, tt.mask, got, tt.want)
			}
		})
	}
}

// TestClassifyAPIErr verifies that transport-level failures are classified
// as network errors and daemon-reported failures as server errors, so
// exitAPIErr surfaces distinct exit codes per failure class (CC-023 item 4).
func TestClassifyAPIErr(t *testing.T) {
	networkErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "net.OpError is a network error", err: networkErr, want: ExitNetwork},
		{name: "wrapped net.OpError is a network error", err: fmt.Errorf("dial: %w", networkErr), want: ExitNetwork},
		{name: "daemon message is a server error", err: errors.New("package not found"), want: ExitServer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAPIErr(tt.err); got != tt.want {
				t.Fatalf("classifyAPIErr(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestExitCodesAreDistinct guards against a future edit accidentally
// collapsing the failure classes back to the same code.
func TestExitCodesAreDistinct(t *testing.T) {
	codes := map[string]int{
		"usage":   ExitUsage,
		"config":  ExitConfig,
		"network": ExitNetwork,
		"server":  ExitServer,
	}
	seen := map[int]string{}
	for name, code := range codes {
		if other, ok := seen[code]; ok {
			t.Fatalf("exit code %d used by both %q and %q", code, other, name)
		}
		seen[code] = name
	}
}
