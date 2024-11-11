package models

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"os"
	"os/exec"
	"strconv"

	"github.com/calaos/calaos-container/models/structs"
)

type RawDNSServer struct {
	IfIndex int32
	Address []byte
}

type NetworkList struct {
	Interfaces []RawNetInterface `json:"Interfaces"`
}

type RawNetInterface struct {
	Name        string `json:"Name"`
	MAC         []byte `json:"HardwareAddress"`
	State       string `json:"OnlineState"`
	NetworkFile string `json:"NetworkFile"`
	Flags       int    `json:"Flags"`
	Addresses   []struct {
		Family       int    `json:"Family"` // 2 for IPv4, 10 for IPv6
		Address      []byte `json:"Address"`
		Broadcast    []byte `json:"Broadcast"`
		PrefixLength int    `json:"PrefixLength"`
		ConfigSource string `json:"ConfigSource"` // "DHCPv4" or "static"
	} `json:"Addresses"`
	Routes []struct {
		Family       int    `json:"Family"` // 2 for IPv4, 10 for IPv6
		Gateway      []byte `json:"Gateway"`
		ConfigSource string `json:"ConfigSource"` // "DHCPv4" or "static"
	} `json:"Routes"`
	DNS []struct {
		Family       int    `json:"Family"` // 2 for IPv4, 10 for IPv6
		Address      []byte `json:"Address"`
		ConfigSource string `json:"ConfigSource"` // "DHCPv4" or "static"
	} `json:"DNS"`
	SearchDomains []struct {
		Domain       string `json:"Domain"`
		ConfigSource string `json:"ConfigSource"` // "DHCPv4" or "static"
	} `json:"SearchDomains"`
}

type RawDNSConfig struct {
	Interface        string   `json:"interface"`
	CurrentDNSServer string   `json:"current_dns_server"`
	DNSServers       []string `json:"dns_servers"`
	SearchDomains    []string `json:"search_domains"`
}

func GetAllNetInterfaces() (nets []*structs.NetInterface, err error) {
	cmd := exec.Command("/usr/bin/networkctl", "list", "--json=short")
	output, err := cmd.Output()
	if err != nil {
		logging.Errorf("failed to get network interfaces: %v", err)
		return nil, err
	}

	var networkList NetworkList
	if err := json.Unmarshal(output, &networkList); err != nil {
		logging.Errorf("failed to unmarshal network interfaces: %v", err)
		logging.Debugf("output: %s", output)
		return nil, err
	}

	rawInterfaces := networkList.Interfaces

	for _, rawIntf := range rawInterfaces {
		netIntf := &structs.NetInterface{
			Name:       rawIntf.Name,
			MAC:        net.HardwareAddr(rawIntf.MAC).String(),
			State:      rawIntf.State,
			IsLoopback: (rawIntf.Flags & 0x8) != 0, // check IFF_LOOPBACK
		}

		for _, dns := range rawIntf.DNS {
			netIntf.DNSServers = append(netIntf.DNSServers, formatIPAddress(dns.Address))
		}

		for _, domain := range rawIntf.SearchDomains {
			netIntf.SearchDomains = append(netIntf.SearchDomains, domain.Domain)
		}

		// Look for IPv4 address and mask
		for _, addr := range rawIntf.Addresses {
			if addr.Family == 2 { // IPv4
				ip := formatIPAddress(addr.Address) + "/" + strconv.Itoa(addr.PrefixLength)
				netIntf.IPv4 = ip
				if addr.ConfigSource == "DHCPv4" {
					netIntf.DHCP = true
				}

				break
			}
		}

		// Look for IPv6 address
		for _, addr := range rawIntf.Addresses {
			if addr.Family == 10 { // IPv6
				netIntf.IPv6 = formatIPAddress(addr.Address) + "/" + strconv.Itoa(addr.PrefixLength)
				break
			}
		}

		// Look for default gateway
		for _, route := range rawIntf.Routes {
			if len(route.Gateway) > 0 {
				netIntf.Gateway = formatIPAddress(route.Gateway)
				break
			}
		}

		nets = append(nets, netIntf)
	}

	return nets, nil
}

func formatIPAddress(address []byte) string {
	if len(address) == 4 { // IPv4
		return fmt.Sprintf("%d.%d.%d.%d", address[0], address[1], address[2], address[3])
	} else if len(address) == 16 { // IPv6
		return fmt.Sprintf("%x:%x:%x:%x:%x:%x:%x:%x",
			(uint16(address[0])<<8)|uint16(address[1]),
			(uint16(address[2])<<8)|uint16(address[3]),
			(uint16(address[4])<<8)|uint16(address[5]),
			(uint16(address[6])<<8)|uint16(address[7]),
			(uint16(address[8])<<8)|uint16(address[9]),
			(uint16(address[10])<<8)|uint16(address[11]),
			(uint16(address[12])<<8)|uint16(address[13]),
			(uint16(address[14])<<8)|uint16(address[15]))
	}
	return ""
}

const (
	sdNetConfDhcpTemplate = `[Match]
Name={{ .Name }}

[Network]
{{- if .DHCP }}
DHCP=yes
{{- else }}
Address={{ .IPv4 }}
Gateway={{ .Gateway }}
{{- range .DNSServers }}
DNS={{ . }}
{{- end }}
{{- range .SearchDomains }}
Domains={{ . }}
{{- end }}
{{- end }}
`
)

type ConfMatchSection struct {
	Name string `toml:"Name"`
}

type ConfNetworkSection struct {
	Description string `toml:"Description"`
	DHCP        string `toml:"DHCP"`
	DNS         string `toml:"DNS"`
	Domains     string `toml:"Domains"`
}

func ConfigureNetInterface(intf string, config structs.NetInterface) error {
	tmpl, err := template.New("networkConfig").Parse(sdNetConfDhcpTemplate)
	if err != nil {
		err = fmt.Errorf("failed to parse network config template: %v", err)
		logging.Errorf("%v", err)
		return err
	}

	config.Name = intf

	//get network config file if already set for the interface
	cmd := exec.Command("/usr/bin/networkctl", "status", intf, "--json=pretty")
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	var rawIntf RawNetInterface
	if err := json.Unmarshal(output, &rawIntf); err != nil {
		return err
	}

	confFile := rawIntf.NetworkFile
	if confFile == "" {
		confFile = fmt.Sprintf("/etc/systemd/network/calaos_%s.network", intf)
	}

	file, err := os.Create(confFile)
	if err != nil {
		err = fmt.Errorf("failed to create network config file: %v", err)
		logging.Errorf("%v", err)
		return err
	}
	defer file.Close()

	err = tmpl.Execute(file, config)
	if err != nil {
		err = fmt.Errorf("failed to execute network config template: %v", err)
		logging.Errorf("%v", err)
		return err
	}

	//reload *.network file
	cmd = exec.Command("/usr/bin/networkctl", "reload")
	if err := cmd.Run(); err != nil {
		err = fmt.Errorf("failed to reload networkd: %v", err)
		logging.Errorf("%v", err)
		return err
	}

	//reconfigure interface
	cmd = exec.Command("/usr/bin/networkctl", "reconfigure", intf)
	if err := cmd.Run(); err != nil {
		err = fmt.Errorf("failed to reconfigure interface: %v", err)
		logging.Errorf("%v", err)
		return err
	}

	return nil
}
