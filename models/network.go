package models

import "net"

type NetIntf struct {
	Name       string `json:"name"`
	IPv4       string `json:"ipv4"`
	IPv6       string `json:"ipv6"`
	MAC        string `json:"mac"`
	IsLoopback bool   `json:"is_loopback"`
}

func GetAllNetInterfaces() (nets []*NetIntf, err error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}

		intf := &NetIntf{
			Name:       iface.Name,
			IsLoopback: iface.Flags&net.FlagLoopback != 0,
			MAC:        iface.HardwareAddr.String(),
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			// Skip IPv6 addresses
			if ipNet.IP.To4() == nil {
				intf.IPv6 = ipNet.IP.String()
			} else if ipNet.IP.To16() != nil {
				intf.IPv4 = ipNet.IP.String()
			}
		}

		nets = append(nets, intf)
	}

	return
}
