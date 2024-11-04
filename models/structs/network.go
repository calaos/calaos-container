package structs

type NetInterface struct {
	Name       string `json:"name,omitempty"`
	IPv4       string `json:"ipv4"`
	Gateway    string `json:"gateway,omitempty"`
	IPv6       string `json:"ipv6,omitempty"`
	MAC        string `json:"mac,omitempty"`
	IsLoopback bool   `json:"is_loopback,omitempty"`
	State      string `json:"state,omitempty"`
	DHCP       bool   `json:"dhcp"`
	DNSConfig  *DNSConfig
}

type DNSConfig struct {
	DNSServers    []string `json:"dns_servers,omitempty"`
	SearchDomains []string `json:"search_domains,omitempty"`
}
