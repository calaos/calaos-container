package models

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"unicode"

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

// ---------------------------------------------------------------------------
// Input validation
//
// The generated systemd-networkd unit is a plain INI file made of "Key=Value"
// lines. Any user supplied value that carries a newline, a section header or an
// extra '=' would be read back by systemd-networkd as additional directives,
// and the interface name becomes part of the unit file path. Everything that
// reaches the file (or networkctl) is therefore validated against the exact
// syntax expected for its field, before a single byte is written.
// ---------------------------------------------------------------------------

const (
	// maxInterfaceNameLen is the kernel IFNAMSIZ limit (16 bytes including the
	// trailing NUL).
	maxInterfaceNameLen = 15

	maxDomainNameLen  = 253
	maxDomainLabelLen = 63
)

var (
	// interfaceNameRe restricts an interface name to the characters accepted by
	// the kernel. '/' is deliberately not part of the set: the name is
	// interpolated into a path under /etc/systemd/network/.
	interfaceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)

	// domainNameRe is the RFC-1035-ish charset allowed in a search domain.
	domainNameRe = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
)

// ValidationError reports a request field that failed validation. Callers can
// use errors.As to turn it into a 400 response.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Msg)
}

func invalidField(field, format string, args ...any) *ValidationError {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// ValidateInterfaceName checks that name is a syntactically valid network
// interface name. It guarantees the name cannot escape the
// /etc/systemd/network/ directory once interpolated into a file name, and that
// it cannot be mistaken for an option by networkctl.
func ValidateInterfaceName(name string) error {
	const field = "interface"

	if name == "" {
		return invalidField(field, "must not be empty")
	}
	if len(name) > maxInterfaceNameLen {
		return invalidField(field, "must be at most %d characters", maxInterfaceNameLen)
	}
	if !interfaceNameRe.MatchString(name) {
		return invalidField(field, "%q must match %s", name, interfaceNameRe.String())
	}
	// "." and ".." match the charset above but are path components.
	if name == "." || name == ".." {
		return invalidField(field, "%q is not a valid interface name", name)
	}
	// A leading '-' would be parsed as an option by networkctl ("--all").
	if strings.HasPrefix(name, "-") {
		return invalidField(field, "must not start with '-'")
	}

	return nil
}

// InterfaceExists reports whether name is one of the network interfaces
// currently present on the system. A non-nil error means the interface list
// could not be read at all (which is a server-side failure, not a bad request).
func InterfaceExists(name string) (bool, error) {
	nets, err := GetAllNetInterfaces()
	if err != nil {
		return false, err
	}

	for _, n := range nets {
		if n != nil && n.Name == name {
			return true, nil
		}
	}

	return false, nil
}

// checkNoDirectiveChars rejects any character that could break out of a single
// "Key=Value" line of the generated unit file. It is defense in depth: each
// field is also validated against its own syntax below, but this keeps a future
// template edit from turning a field into an injection vector.
func checkNoDirectiveChars(field, value string) error {
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r':
			return invalidField(field, "must not contain newlines")
		case unicode.IsSpace(r):
			return invalidField(field, "must not contain whitespace")
		case r == '[' || r == ']' || r == '=' || r == '#' || r == ';':
			return invalidField(field, "must not contain %q", string(r))
		case r < 0x20 || r == 0x7f:
			return invalidField(field, "must not contain control characters")
		}
	}

	return nil
}

// parseIPAddr validates a bare IP address (no prefix length, no scope zone).
func parseIPAddr(field, value string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, invalidField(field, "%q is not a valid IP address", value)
	}
	if addr.Zone() != "" {
		return netip.Addr{}, invalidField(field, "%q must not carry a scope zone", value)
	}

	return addr, nil
}

// validateIPv4Address validates the value rendered as "Address=". Both the CIDR
// form returned by GET /network/list (192.168.1.10/24, what systemd-networkd
// expects) and a bare address are accepted.
func validateIPv4Address(field, value string) error {
	if err := checkNoDirectiveChars(field, value); err != nil {
		return err
	}

	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return invalidField(field, "%q is not a valid IPv4 address in CIDR form", value)
		}
		if !prefix.Addr().Is4() {
			return invalidField(field, "%q is not an IPv4 address", value)
		}

		return nil
	}

	addr, err := parseIPAddr(field, value)
	if err != nil {
		return err
	}
	if !addr.Is4() {
		return invalidField(field, "%q is not an IPv4 address", value)
	}

	return nil
}

// validateDomainName validates a DNS search domain, optionally fully qualified
// with a trailing dot.
func validateDomainName(field, value string) error {
	if value == "" {
		return invalidField(field, "must not be empty")
	}
	if len(value) > maxDomainNameLen {
		return invalidField(field, "must be at most %d characters", maxDomainNameLen)
	}
	if !domainNameRe.MatchString(value) {
		return invalidField(field, "%q is not a valid domain name", value)
	}

	name := strings.TrimSuffix(value, ".")
	if name == "" {
		return invalidField(field, "%q is not a valid domain name", value)
	}

	for _, label := range strings.Split(name, ".") {
		switch {
		case label == "":
			return invalidField(field, "%q contains an empty label", value)
		case len(label) > maxDomainLabelLen:
			return invalidField(field, "%q contains a label longer than %d characters", value, maxDomainLabelLen)
		case strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-"):
			return invalidField(field, "%q contains a label starting or ending with '-'", value)
		}
	}

	return nil
}

// ValidateNetInterfaceConfig validates every field of a configuration request
// that can end up in the generated unit file. It returns a *ValidationError
// describing the first offending field, so the caller can answer 400 without
// having touched the filesystem.
//
// NetInterface.Name is not validated here: ConfigureNetInterface always
// overwrites it with the (already validated) interface name taken from the URL.
func ValidateNetInterfaceConfig(config structs.NetInterface) error {
	if config.IPv4 == "" {
		if !config.DHCP {
			return invalidField("ipv4", "is required when dhcp is disabled")
		}
	} else if err := validateIPv4Address("ipv4", config.IPv4); err != nil {
		return err
	}

	if config.Gateway != "" {
		if err := checkNoDirectiveChars("gateway", config.Gateway); err != nil {
			return err
		}
		if _, err := parseIPAddr("gateway", config.Gateway); err != nil {
			return err
		}
	}

	for i, dns := range config.DNSServers {
		field := fmt.Sprintf("dns_servers[%d]", i)
		if err := checkNoDirectiveChars(field, dns); err != nil {
			return err
		}
		if dns == "" {
			return invalidField(field, "must not be empty")
		}
		if _, err := parseIPAddr(field, dns); err != nil {
			return err
		}
	}

	for i, domain := range config.SearchDomains {
		field := fmt.Sprintf("search_domains[%d]", i)
		if err := checkNoDirectiveChars(field, domain); err != nil {
			return err
		}
		if err := validateDomainName(field, domain); err != nil {
			return err
		}
	}

	// Fields that the template does not render today are still checked, so that
	// adding them to the template later cannot reintroduce directive injection.
	for _, f := range []struct{ field, value string }{
		{"ipv6", config.IPv6},
		{"mac", config.MAC},
		{"state", config.State},
	} {
		if err := checkNoDirectiveChars(f.field, f.value); err != nil {
			return err
		}
	}

	return nil
}

// sdNetConfDhcpTemplate is rendered with text/template on purpose: this is an
// INI unit file, not HTML. html/template would corrupt legitimate values (a '&'
// in a search domain becomes "&amp;") while providing no protection at all
// against the only escaping that matters here - newlines - which is handled by
// ValidateNetInterfaceConfig instead.
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

// ConfigureNetInterface writes the systemd-networkd unit for intf and applies
// it. Both intf and config are validated up front: nothing is executed and no
// file is touched unless every value is well-formed. Callers wanting to reject
// unknown interfaces (rather than creating a unit for a name that is merely
// syntactically valid) must additionally check InterfaceExists.
//
// Note: if the unit file is written but "networkctl reload"/"reconfigure"
// fails, the new file is kept on disk (no rollback) and the error is returned
// to the caller.
func ConfigureNetInterface(intf string, config structs.NetInterface) error {
	if err := ValidateInterfaceName(intf); err != nil {
		logging.Errorf("%v", err)
		return err
	}

	if err := ValidateNetInterfaceConfig(config); err != nil {
		logging.Errorf("%v", err)
		return err
	}

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
