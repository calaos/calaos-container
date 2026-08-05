package app

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2/middleware/cors"
)

func TestCorsAllowOrigins(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		port     int
		override string
		want     string
		wantErr  bool
	}{
		{
			name: "derived from the loopback address",
			addr: "127.0.0.1",
			port: 8000,
			want: "http://127.0.0.1,http://127.0.0.1:8000",
		},
		{
			name: "derived from a non default port",
			addr: "127.0.0.1",
			port: 8014,
			want: "http://127.0.0.1,http://127.0.0.1:8014",
		},
		{
			name: "ipv6 literals are bracketed",
			addr: "::1",
			port: 8000,
			want: "http://[::1],http://[::1]:8000",
		},
		{
			name:     "override wins",
			addr:     "127.0.0.1",
			port:     8000,
			override: "http://calaos.local, https://calaos.local",
			want:     "http://calaos.local, https://calaos.local",
		},
		{
			name:     "wildcard override is kept as is",
			addr:     "127.0.0.1",
			port:     8000,
			override: "*",
			want:     "*",
		},
		{
			name:     "subdomain wildcard override",
			addr:     "127.0.0.1",
			port:     8000,
			override: "https://*.calaos.fr",
			want:     "https://*.calaos.fr",
		},
		{
			name: "empty address falls back to the loopback, never a wildcard",
			addr: "  ",
			port: 8000,
			want: "http://127.0.0.1,http://127.0.0.1:8000",
		},
		{
			name: "unusable port yields the bare origin only",
			addr: "127.0.0.1",
			port: 0,
			want: "http://127.0.0.1",
		},
		{
			name:    "malformed address is an error",
			addr:    "127.0.0.1 8000",
			port:    8000,
			wantErr: true,
		},
		{
			name:     "malformed override is an error",
			addr:     "127.0.0.1",
			port:     8000,
			override: "calaos.local",
			wantErr:  true,
		},
		{
			name:     "override with a path is an error",
			addr:     "127.0.0.1",
			port:     8000,
			override: "http://calaos.local/ui",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := corsAllowOrigins(tc.addr, tc.port, tc.override)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("corsAllowOrigins(%q, %d, %q) = %q, want an error",
						tc.addr, tc.port, tc.override, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("corsAllowOrigins(%q, %d, %q): unexpected error: %v",
					tc.addr, tc.port, tc.override, err)
			}
			if got != tc.want {
				t.Errorf("corsAllowOrigins(%q, %d, %q) = %q, want %q",
					tc.addr, tc.port, tc.override, got, tc.want)
			}
		})
	}
}

// The cors middleware panics on a malformed origin, so whatever we accept must
// be accepted by it too.
func TestCorsAllowOriginsAcceptedByMiddleware(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "0.0.0.0", "::1", "localhost"} {
		origins, err := corsAllowOrigins(addr, 8000, "")
		if err != nil {
			t.Fatalf("corsAllowOrigins(%q): unexpected error: %v", addr, err)
		}

		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("cors middleware panicked on %q: %v", origins, r)
				}
			}()
			_ = cors.New(cors.Config{AllowOrigins: origins})
		}()
	}
}

func TestValidateOriginsRejectsEmptyEntries(t *testing.T) {
	if err := validateOrigins("http://127.0.0.1,"); err == nil {
		t.Error("validateOrigins: expected an error for a trailing empty origin")
	}
	if err := validateOrigins("http://"); err == nil {
		t.Error("validateOrigins: expected an error for a hostless origin")
	}
}

func TestHTTPOrigin(t *testing.T) {
	if got := httpOrigin("127.0.0.1", ""); got != "http://127.0.0.1" {
		t.Errorf("httpOrigin = %q", got)
	}
	if got := httpOrigin("::1", "8000"); !strings.Contains(got, "[::1]:8000") {
		t.Errorf("httpOrigin = %q, want a bracketed ipv6 host", got)
	}
}
