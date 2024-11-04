package models

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/calaos/calaos-container/models/structs"

	"github.com/godbus/dbus/v5"
)

type RawDNSServer struct {
	IfIndex int32
	Address []byte
}

type RawNetInterface struct {
	Name        string `json:"Name"`
	Flags       int    `json:"Flags"`
	IPv4        string `json:"IPv4Address"`
	IPv4Mask    int    `json:"IPv4Mask"`
	IPv6        string `json:"IPv6LinkLocalAddress"`
	MAC         string `json:"HardwareAddress"`
	State       string `json:"KernelOperationalStateString"`
	NetworkFile string `json:"NetworkFile"`
	Addresses   []struct {
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
	Interface        string   `json:"interface"`
	CurrentDNSServer string   `json:"current_dns_server"`
	DNSServers       []string `json:"dns_servers"`
	SearchDomains    []string `json:"search_domains"`
}

func parseMask(prefixLen int) string {
	mask := (0xFFFFFFFF << (32 - prefixLen)) & 0xFFFFFFFF
	return fmt.Sprintf("%d.%d.%d.%d", (mask>>24)&0xFF, (mask>>16)&0xFF, (mask>>8)&0xFF, mask&0xFF)
}

func GetAllNetInterfaces() (nets []*structs.NetInterface, err error) {
	cmd := exec.Command("/usr/bin/networkctl", "list", "--json=short")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var rawInterfaces []RawNetInterface
	if err := json.Unmarshal(output, &rawInterfaces); err != nil {
		return nil, err
	}

	dnsConfigs, _ := getDNSConfig()

	for _, rawIntf := range rawInterfaces {
		dnsConfig := &structs.DNSConfig{}
		for _, dns := range dnsConfigs {
			if dns.Interface == rawIntf.Name {
				dnsConfig.DNSServers = dns.DNSServers
				dnsConfig.SearchDomains = dns.SearchDomains
				break
			}
		}

		netIntf := &structs.NetInterface{
			Name:       rawIntf.Name,
			MAC:        rawIntf.MAC,
			State:      rawIntf.State,
			IsLoopback: (rawIntf.Flags & 0x8) != 0, // check IFF_LOOPBACK
			DNSConfig:  dnsConfig,
		}

		// Look for IPv4 address and mask
		for _, addr := range rawIntf.Addresses {
			if addr.Family == 2 { // IPv4
				ip, _ := toCIDR(fmt.Sprintf("%d.%d.%d.%d", addr.Scope[0], addr.Scope[1], addr.Scope[2], addr.Scope[3]), parseMask(addr.Prefix))
				netIntf.IPv4 = ip
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

func getDNSConfig() ([]*RawDNSConfig, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}

	var dnsConfigs []*RawDNSConfig

	for i := 1; i <= 64; i++ {
		linkPath := fmt.Sprintf("/org/freedesktop/resolve1/link/%d", i)
		linkObj := conn.Object("org.freedesktop.resolve1", dbus.ObjectPath(linkPath))

		var currentDNSServer RawDNSServer
		err := linkObj.Call("org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.resolve1.Link", "CurrentDNSServer").Store(&currentDNSServer)
		if err != nil {
			continue
		}

		logging.Debugf("CurrentDNSServer for link %d: %v", i, currentDNSServer)

		currentDNS := formatDNSAddress(currentDNSServer.Address)

		var dnsServersProp []RawDNSServer
		err = linkObj.Call("org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.resolve1.Link", "DNS").Store(&dnsServersProp)
		if err != nil {
			logging.Warnf("failed to get DNS servers for link %d: %v", i, err)
			continue
		}

		logging.Debugf("DNS servers for link %d: %v", i, dnsServersProp)

		dnsServers := parseDNSList(dnsServersProp)

		var domainsArray []struct {
			Domain string
			Route  bool
		}
		err = linkObj.Call("org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.resolve1.Link", "Domains").Store(&domainsArray)
		if err != nil {
			logging.Warnf("failed to get search domains for link %d: %v", i, err)
			continue
		}

		var searchDomains []string
		for _, domain := range domainsArray {
			searchDomains = append(searchDomains, domain.Domain)
		}

		ifname, _ := getInterfaceNameByIndex(i)
		dnsConfigs = append(dnsConfigs, &RawDNSConfig{
			Interface:        ifname,
			CurrentDNSServer: currentDNS,
			DNSServers:       dnsServers,
			SearchDomains:    searchDomains,
		})
	}

	return dnsConfigs, nil
}

func formatDNSAddress(address []byte) string {
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

func parseDNSList(value []RawDNSServer) []string {
	var dnsServers []string

	for _, entry := range value {
		addr := formatDNSAddress(entry.Address)
		if addr != "" {
			dnsServers = append(dnsServers, addr)
		}
	}
	return dnsServers
}

func getInterfaceNameByIndex(targetIndex int) (string, error) {
	netPath := "/sys/class/net/"
	interfaces, err := os.ReadDir(netPath)
	if err != nil {
		return "", fmt.Errorf("failed to read network interfaces: %v", err)
	}

	for _, iface := range interfaces {
		indexPath := filepath.Join(netPath, iface.Name(), "ifindex")
		indexBytes, err := os.ReadFile(indexPath)
		if err != nil {
			return "", fmt.Errorf("failed to read ifindex for interface %s: %v", iface.Name(), err)
		}

		index, err := strconv.Atoi(strings.TrimSpace(string(indexBytes)))
		if err != nil {
			return "", fmt.Errorf("failed to convert index to int for interface %s: %v", iface.Name(), err)
		}

		if index == targetIndex {
			return iface.Name(), nil
		}
	}

	return "", fmt.Errorf("no interface found with index %d", targetIndex)
}

const (
	sdNetConfDhcpTemplate = `[Match]
Name={{ .InterfaceName }}

[Network]
{{- if .UseDHCP }}
DHCP=yes
{{- else }}
Address={{ .Address }}
Gateway={{ .Gateway }}
{{- range .DNS }}
DNS={{ . }}
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

	//get network config file if already set for the interface
	cmd := exec.Command("/usr/bin/networkctl", "status", intf, "--json")
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

func toCIDR(ipStr, maskStr string) (string, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", fmt.Errorf("invalid ip : %s", ipStr)
	}

	mask := net.ParseIP(maskStr)
	if mask == nil {
		return "", fmt.Errorf("invalid mask : %s", maskStr)
	}

	ipMask := net.IPv4Mask(mask[12], mask[13], mask[14], mask[15])
	prefixSize, _ := ipMask.Size()

	return fmt.Sprintf("%s/%d", ipStr, prefixSize), nil
}

func fromCIDR(cidr string) (string, string, error) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("invalid CIDR : %s", cidr)
	}

	ipStr := ip.String()
	mask := net.IP(ipNet.Mask).String()

	return ipStr, mask, nil
}
