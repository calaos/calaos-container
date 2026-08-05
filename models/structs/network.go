package structs

// NetInterface describes a network interface, both as reported by
// GET /api/network/list and as accepted by POST /api/network/:intf.
//
// The fields sent by a configuration request end up verbatim in a
// systemd-networkd unit file, so they are validated by
// models.ValidateNetInterfaceConfig before anything is written. Expected
// formats:
//
//	IPv4          - "192.168.1.10/24" (CIDR) or a bare IPv4 address; required
//	                unless DHCP is true. Rendered as "Address=".
//	Gateway       - a bare IP address, no scope zone. Rendered as "Gateway=".
//	DNSServers    - bare IP addresses. Rendered as "DNS=".
//	SearchDomains - DNS domain names ([a-zA-Z0-9.-], labels of 1..63 chars).
//	                Rendered as "Domains=".
//	Name          - ignored on input, always set from the URL parameter.
//
// No string field may contain whitespace, newlines, '[', ']', '=', '#', ';' or
// control characters, which would inject extra unit directives.
type NetInterface struct {
	Name          string   `json:"name,omitempty"`
	IPv4          string   `json:"ipv4"`
	Gateway       string   `json:"gateway,omitempty"`
	IPv6          string   `json:"ipv6,omitempty"`
	MAC           string   `json:"mac,omitempty"`
	IsLoopback    bool     `json:"is_loopback,omitempty"`
	State         string   `json:"state,omitempty"`
	DHCP          bool     `json:"dhcp"`
	DNSServers    []string `json:"dns_servers,omitempty"`
	SearchDomains []string `json:"search_domains,omitempty"`
}
