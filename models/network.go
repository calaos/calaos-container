package models

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

type NetIntf struct {
	Name       string `json:"name"`
	IPv4       string `json:"ipv4"`
	Mask       string `json:"mask"`
	Gateway    string `json:"gateway"`
	IPv6       string `json:"ipv6"`
	MAC        string `json:"mac"`
	IsLoopback bool   `json:"is_loopback"`
	State      string `json:"state"`
}

type DNSConfig struct {
	Interface     string   `json:"interface"`
	DNSServers    []string `json:"dns_servers"`
	SearchDomains []string `json:"search_domains"`
}

type RawNetInterface struct {
	Name      string `json:"Name"`
	Flags     int    `json:"Flags"`
	IPv4      string `json:"IPv4Address"`
	IPv4Mask  int    `json:"IPv4Mask"`
	IPv6      string `json:"IPv6LinkLocalAddress"`
	MAC       string `json:"HardwareAddress"`
	State     string `json:"KernelOperationalStateString"`
	Addresses []struct {
		Family int    `json:"Family"`
		Scope  string `json:"ScopeString"`
		Prefix int    `json:"PrefixLength"`
	} `json:"Addresses"`
	Routes []struct {
		Family    int    `json:"Family"`
		Type      string `json:"TypeString"`
		Gateway   string `json:"Gateway"`
		Dest      string `json:"Destination"`
		PrefixLen int    `json:"DestinationPrefixLength"`
	} `json:"Routes"`
}

type RawDNSConfig struct {
	Link          int      `json:"Link"`
	CurrentDNS    []string `json:"CurrentDNSServer"`
	SearchDomains []string `json:"Domains"`
	LLMNR         string   `json:"LLMNR"`
	MulticastDNS  string   `json:"MulticastDNS"`
	DNSSEC        string   `json:"DNSSEC"`
	InterfaceName string   `json:"InterfaceName"`
}

func parseMask(prefixLen int) string {
	mask := (0xFFFFFFFF << (32 - prefixLen)) & 0xFFFFFFFF
	return fmt.Sprintf("%d.%d.%d.%d", (mask>>24)&0xFF, (mask>>16)&0xFF, (mask>>8)&0xFF, mask&0xFF)
}

func GetAllNetInterfaces() (nets []*NetIntf, err error) {
	cmd := exec.Command("/usr/bin/networkctl", "list", "--json=short")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var rawInterfaces []RawNetInterface
	if err := json.Unmarshal(output, &rawInterfaces); err != nil {
		return nil, err
	}

	for _, rawIntf := range rawInterfaces {
		netIntf := &NetIntf{
			Name:       rawIntf.Name,
			MAC:        rawIntf.MAC,
			State:      rawIntf.State,
			IsLoopback: (rawIntf.Flags & 0x8) != 0, // check IFF_LOOPBACK
		}

		// Look for IPv4 address and mask
		for _, addr := range rawIntf.Addresses {
			if addr.Family == 2 { // IPv4
				netIntf.IPv4 = fmt.Sprintf("%d.%d.%d.%d", addr.Scope[0], addr.Scope[1], addr.Scope[2], addr.Scope[3])
				netIntf.Mask = parseMask(addr.Prefix)
				break
			}
		}

		// Look for IPv6 address
		for _, addr := range rawIntf.Addresses {
			if addr.Family == 10 { // IPv6
				netIntf.IPv6 = rawIntf.IPv6
				break
			}
		}

		// Look for default gateway
		for _, route := range rawIntf.Routes {
			if route.Type == "unicast" && route.Gateway != "" {
				netIntf.Gateway = route.Gateway
				break
			}
		}

		nets = append(nets, netIntf)
	}

	return nets, nil
}

func GetDNSConfig() ([]*DNSConfig, error) {
	cmd := exec.Command("/usr/bin/resolvectl", "status")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var dnsConfigs []*DNSConfig
	var currentConfig *DNSConfig

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	dnsServerRegex := regexp.MustCompile(`DNS Servers? ([\d.]+)`)
	interfaceRegex := regexp.MustCompile(`Link (\d+) \(([^)]+)\)`)

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "Global") {
			currentConfig = &DNSConfig{Interface: "global"}
			dnsConfigs = append(dnsConfigs, currentConfig)
		} else if matches := interfaceRegex.FindStringSubmatch(line); len(matches) == 3 {
			currentConfig = &DNSConfig{Interface: matches[2]}
			dnsConfigs = append(dnsConfigs, currentConfig)
		}

		if dnsServerRegex.MatchString(line) {
			server := dnsServerRegex.FindStringSubmatch(line)[1]
			currentConfig.DNSServers = append(currentConfig.DNSServers, server)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return dnsConfigs, nil
}
