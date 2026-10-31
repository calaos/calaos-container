package models

import (
	"errors"
	"strings"
	"testing"
	"text/template"

	"github.com/calaos/calaos-container/models/structs"
)

func TestValidateInterfaceName(t *testing.T) {
	tests := []struct {
		name    string
		intf    string
		wantErr bool
	}{
		{"simple", "eth0", false},
		{"predictable name", "enp0s31f6", false},
		{"vlan", "eth0.100", false},
		{"underscore and dash", "br_lan-0", false},
		{"max length", strings.Repeat("a", 15), false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 16), true},
		{"path traversal", "../../etc/cron.d/x", true},
		{"encoded traversal", "..%2f..%2fetc%2fcron.d%2fx", true},
		{"slash", "eth0/../x", true},
		{"dot", ".", true},
		{"dotdot", "..", true},
		{"newline", "eth0\nName=eth1", true},
		{"space", "eth0 eth1", true},
		{"option like", "--all", true},
		{"short option like", "-h", true},
		{"nul byte", "eth0\x00", true},
		{"unicode", "éth0", true},
		{"route word apply", "apply", true},
		{"route word confirm", "confirm", true},
		{"route word revert", "revert", true},
		{"route word state", "state", true},
		{"route word list", "list", true},
		{"route word trial", "trial", true},
		{"route word upper case", "Apply", true},
		{"route word prefix", "applyx", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInterfaceName(tc.intf)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateInterfaceName(%q) = nil, want error", tc.intf)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateInterfaceName(%q) = %v, want nil", tc.intf, err)
			}
			if err != nil {
				var verr *ValidationError
				if !errors.As(err, &verr) {
					t.Fatalf("error %v is not a *ValidationError", err)
				}
				if verr.Field != "interface" {
					t.Fatalf("field = %q, want %q", verr.Field, "interface")
				}
			}
		})
	}
}

func TestValidateNetInterfaceConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    structs.NetInterface
		wantErr   bool
		wantField string
	}{
		{
			name: "nominal static",
			config: structs.NetInterface{
				IPv4:          "192.168.1.10/24",
				Gateway:       "192.168.1.1",
				DNSServers:    []string{"192.168.1.1", "9.9.9.9", "2001:4860:4860::8888"},
				SearchDomains: []string{"calaos.fr", "lan.example.com", "example.com."},
			},
		},
		{
			name:   "nominal dhcp",
			config: structs.NetInterface{DHCP: true},
		},
		{
			name: "dhcp with reported values round-tripped",
			config: structs.NetInterface{
				DHCP:       true,
				IPv4:       "10.0.0.5/8",
				Gateway:    "10.0.0.1",
				MAC:        "aa:bb:cc:dd:ee:ff",
				IPv6:       "fe80::1/64",
				State:      "online",
				DNSServers: []string{"10.0.0.1"},
			},
		},
		{
			name:      "static without address",
			config:    structs.NetInterface{},
			wantErr:   true,
			wantField: "ipv4",
		},
		{
			name:      "ipv4 garbage",
			config:    structs.NetInterface{IPv4: "not-an-ip"},
			wantErr:   true,
			wantField: "ipv4",
		},
		{
			name:      "ipv4 bad prefix",
			config:    structs.NetInterface{IPv4: "192.168.1.10/33"},
			wantErr:   true,
			wantField: "ipv4",
		},
		{
			name:      "ipv4 is v6",
			config:    structs.NetInterface{IPv4: "2001:db8::1/64"},
			wantErr:   true,
			wantField: "ipv4",
		},
		{
			name:      "ipv4 bare address accepted",
			config:    structs.NetInterface{IPv4: "192.168.1.10"},
			wantField: "",
		},
		{
			name:      "ipv4 directive injection",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24\nDNS=6.6.6.6"},
			wantErr:   true,
			wantField: "ipv4",
		},
		{
			name:      "gateway directive injection",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "192.168.1.1\nDNS=6.6.6.6"},
			wantErr:   true,
			wantField: "gateway",
		},
		{
			name:      "gateway carriage return injection",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "192.168.1.1\r[Match]\rName=*"},
			wantErr:   true,
			wantField: "gateway",
		},
		{
			name:      "gateway not an ip",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "gw.example.com"},
			wantErr:   true,
			wantField: "gateway",
		},
		{
			name:      "gateway with cidr",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "192.168.1.1/24"},
			wantErr:   true,
			wantField: "gateway",
		},
		{
			name:      "gateway with scope zone",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "fe80::1%eth0"},
			wantErr:   true,
			wantField: "gateway",
		},
		{
			name:      "dns not an ip",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", DNSServers: []string{"9.9.9.9", "dns.example.com"}},
			wantErr:   true,
			wantField: "dns_servers[1]",
		},
		{
			name:      "dns injection",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", DNSServers: []string{"9.9.9.9\nDomains=evil.example.com"}},
			wantErr:   true,
			wantField: "dns_servers[0]",
		},
		{
			name:      "dns empty entry",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", DNSServers: []string{""}},
			wantErr:   true,
			wantField: "dns_servers[0]",
		},
		{
			name:      "search domain injection",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", SearchDomains: []string{"ok.example.com\n[Match]\nName=*"}},
			wantErr:   true,
			wantField: "search_domains[0]",
		},
		{
			name:      "search domain with equal sign",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", SearchDomains: []string{"a=b"}},
			wantErr:   true,
			wantField: "search_domains[0]",
		},
		{
			name:      "search domain with ampersand",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", SearchDomains: []string{"a&b.example.com"}},
			wantErr:   true,
			wantField: "search_domains[0]",
		},
		{
			name:      "search domain empty label",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", SearchDomains: []string{"foo..example.com"}},
			wantErr:   true,
			wantField: "search_domains[0]",
		},
		{
			name:      "search domain leading dash label",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", SearchDomains: []string{"-bad.example.com"}},
			wantErr:   true,
			wantField: "search_domains[0]",
		},
		{
			name:      "search domain label too long",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", SearchDomains: []string{strings.Repeat("a", 64) + ".example.com"}},
			wantErr:   true,
			wantField: "search_domains[0]",
		},
		{
			name:      "search domain too long",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", SearchDomains: []string{strings.Repeat("a.", 200) + "example.com"}},
			wantErr:   true,
			wantField: "search_domains[0]",
		},
		{
			name:      "unrendered field injection",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", IPv6: "fe80::1/64\nIPv6AcceptRA=no"},
			wantErr:   true,
			wantField: "ipv6",
		},
		{
			name:      "tab in gateway",
			config:    structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "192.168.1.1\t"},
			wantErr:   true,
			wantField: "gateway",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNetInterfaceConfig(tc.config)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateNetInterfaceConfig(%+v) = nil, want error", tc.config)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateNetInterfaceConfig(%+v) = %v, want nil", tc.config, err)
			}
			if tc.wantField == "" {
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error %v is not a *ValidationError", err)
			}
			if verr.Field != tc.wantField {
				t.Fatalf("field = %q, want %q", verr.Field, tc.wantField)
			}
		})
	}
}

// TestConfigureNetInterfaceRejectsBadInput makes sure ConfigureNetInterface
// refuses invalid input on its own, i.e. even if a caller forgets to validate.
// It must fail before any bus call or creating any file.
func TestConfigureNetInterfaceRejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		intf   string
		config structs.NetInterface
	}{
		{"path traversal", "../../etc/cron.d/x", structs.NetInterface{DHCP: true}},
		{"bad body", "eth0", structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "1.2.3.4\nDNS=6.6.6.6"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ConfigureNetInterface(tc.intf, tc.config)
			if err == nil {
				t.Fatalf("ConfigureNetInterface(%q, %+v) = nil, want error", tc.intf, tc.config)
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error %v is not a *ValidationError", err)
			}
		})
	}
}

// TestNetworkTemplateRendersLiterally locks down the html/template ->
// text/template switch: values must be written verbatim, without HTML entity
// escaping (&amp;, &#43;, ...).
func TestNetworkTemplateRendersLiterally(t *testing.T) {
	tmpl, err := template.New("networkConfig").Parse(sdNetConfDhcpTemplate)
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}

	tests := []struct {
		name   string
		config structs.NetInterface
		want   string
	}{
		{
			name: "static",
			config: structs.NetInterface{
				Name:          "eth0",
				IPv4:          "192.168.1.10/24",
				Gateway:       "192.168.1.1",
				DNSServers:    []string{"192.168.1.1", "9.9.9.9"},
				SearchDomains: []string{"calaos.fr"},
			},
			want: "[Match]\nName=eth0\n\n[Network]\nDHCP=no\nAddress=192.168.1.10/24\nGateway=192.168.1.1\nDNS=192.168.1.1\nDNS=9.9.9.9\nDomains=calaos.fr\n",
		},
		{
			name:   "no gateway",
			config: structs.NetInterface{Name: "eth0", IPv4: "192.168.1.10/24"},
			want:   "[Match]\nName=eth0\n\n[Network]\nDHCP=no\nAddress=192.168.1.10/24\n",
		},
		{
			name:   "dhcp",
			config: structs.NetInterface{Name: "eth0", DHCP: true},
			want:   "[Match]\nName=eth0\n\n[Network]\nDHCP=yes\n",
		},
		{
			// Not reachable through the API anymore (the charset check rejects
			// '&'), but it is what used to be corrupted into "&amp;" by
			// html/template.
			name: "no html escaping",
			config: structs.NetInterface{
				Name:          "eth0",
				IPv4:          "192.168.1.10/24",
				Gateway:       "192.168.1.1",
				SearchDomains: []string{"a&b.example.com", "x'y<z>.example.com"},
			},
			want: "[Match]\nName=eth0\n\n[Network]\nDHCP=no\nAddress=192.168.1.10/24\nGateway=192.168.1.1\nDomains=a&b.example.com\nDomains=x'y<z>.example.com\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			if err := tmpl.Execute(&out, tc.config); err != nil {
				t.Fatalf("failed to execute template: %v", err)
			}
			if out.String() != tc.want {
				t.Fatalf("rendered unit mismatch:\ngot:\n%s\nwant:\n%s", out.String(), tc.want)
			}
			if strings.Contains(out.String(), "&amp;") || strings.Contains(out.String(), "&#") {
				t.Fatalf("rendered unit contains HTML entities:\n%s", out.String())
			}
		})
	}
}
