package models

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/calaos/calaos-container/models/structs"
)

const sharedUnit = "[Match]\nName=en*\n\n[Network]\nDHCP=yes\n"

type fakeNetworkctl struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (f *fakeNetworkctl) run(args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	if f.fail[cmd] {
		return errors.New("networkctl " + cmd + " failed")
	}
	return nil
}

func (f *fakeNetworkctl) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// useNetDirs points networkd's directories at a temp dir holding the image's
// shared config.network, and networkctl at a fake.
func useNetDirs(t *testing.T) (etc, run string, nc *fakeNetworkctl) {
	t.Helper()
	root := t.TempDir()
	etc, run = filepath.Join(root, "etc"), filepath.Join(root, "run")
	if err := os.MkdirAll(etc, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "config.network"), []byte(sharedUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	nc = &fakeNetworkctl{fail: map[string]bool{}}
	prevEtc, prevRun, prevCtl := NetworkEtcDir, NetworkRunDir, Networkctl
	NetworkEtcDir, NetworkRunDir, Networkctl = etc, run, nc.run
	t.Cleanup(func() { NetworkEtcDir, NetworkRunDir, Networkctl = prevEtc, prevRun, prevCtl })
	return etc, run, nc
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test temp file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var staticConfig = structs.NetInterface{
	IPv4:          "192.168.1.10/24",
	Gateway:       "192.168.1.1",
	DNSServers:    []string{"192.168.1.1", "9.9.9.9"},
	SearchDomains: []string{"lan"},
}

func TestConfigureNetInterfaceWritesItsOwnUnit(t *testing.T) {
	etc, run, nc := useNetDirs(t)
	if err := ConfigureNetInterface("eth0", staticConfig); err != nil {
		t.Fatal(err)
	}
	if got := dirFiles(t, etc); !reflect.DeepEqual(got, []string{"calaos_eth0.network", "config.network"}) {
		t.Fatalf("/etc holds %v", got)
	}
	if got := dirFiles(t, run); got != nil {
		t.Fatalf("/run holds %v", got)
	}
	want := readFile(t, "testdata/calaos_eth0_static.network")
	if got := readFile(t, filepath.Join(etc, "calaos_eth0.network")); got != want {
		t.Fatalf("unit:\n%s\nwant:\n%s", got, want)
	}
	if got := readFile(t, filepath.Join(etc, "config.network")); got != sharedUnit {
		t.Fatalf("config.network changed:\n%s", got)
	}
	fi, err := os.Stat(filepath.Join(etc, "calaos_eth0.network"))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("unit mode %v %v", fi, err)
	}
	if got := nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}

	// A second interface gets its own file; the first one is untouched.
	if err := ConfigureNetInterface("eth1", structs.NetInterface{DHCP: true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(etc, "calaos_eth0.network")); got != want {
		t.Fatal("configuring eth1 changed eth0")
	}
	if got := readFile(t, filepath.Join(etc, "calaos_eth1.network")); got != "[Match]\nName=eth1\n\n[Network]\nDHCP=yes\n" {
		t.Fatalf("eth1 unit:\n%s", got)
	}
}

func TestConfigureNetInterfaceRejectsWithoutWriting(t *testing.T) {
	etc, run, nc := useNetDirs(t)
	for _, intf := range []string{"../../etc/cron.d/x", "apply", "eth0/x", ".."} {
		if err := ConfigureNetInterface(intf, structs.NetInterface{DHCP: true}); err == nil {
			t.Fatalf("%q accepted", intf)
		}
	}
	if err := ConfigureNetInterface("eth0", structs.NetInterface{IPv4: "192.168.1.10/24", Gateway: "1.2.3.4\nDNS=6.6.6.6"}); err == nil {
		t.Fatal("injected gateway accepted")
	}
	if got := dirFiles(t, etc); !reflect.DeepEqual(got, []string{"config.network"}) {
		t.Fatalf("/etc holds %v", got)
	}
	if got := dirFiles(t, run); got != nil {
		t.Fatalf("/run holds %v", got)
	}
	if got := nc.take(); got != nil {
		t.Fatalf("networkctl ran: %v", got)
	}
}

func TestNetworkTrialConfirm(t *testing.T) {
	etc, run, nc := useNetDirs(t)
	if err := WriteNetworkTrial("eth0", staticConfig); err != nil {
		t.Fatal(err)
	}
	if err := ApplyNetworkTrial("eth0"); err != nil {
		t.Fatal(err)
	}
	if got := dirFiles(t, etc); !reflect.DeepEqual(got, []string{"config.network"}) {
		t.Fatalf("/etc holds %v during the trial", got)
	}
	if got := dirFiles(t, run); !reflect.DeepEqual(got, []string{"00-calaos-trial-eth0.network"}) {
		t.Fatalf("/run holds %v", got)
	}
	want := readFile(t, "testdata/calaos_eth0_static.network")
	if got := readFile(t, filepath.Join(run, "00-calaos-trial-eth0.network")); got != want {
		t.Fatalf("trial:\n%s", got)
	}
	if got := nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}

	if err := ConfirmNetworkTrial("eth0"); err != nil {
		t.Fatal(err)
	}
	if got := dirFiles(t, run); len(got) != 0 {
		t.Fatalf("/run holds %v after confirm", got)
	}
	if got := dirFiles(t, etc); !reflect.DeepEqual(got, []string{"calaos_eth0.network", "config.network"}) {
		t.Fatalf("/etc holds %v", got)
	}
	if got := readFile(t, filepath.Join(etc, "calaos_eth0.network")); got != want {
		t.Fatalf("confirmed unit:\n%s", got)
	}
	if got := readFile(t, filepath.Join(etc, "config.network")); got != sharedUnit {
		t.Fatal("config.network changed")
	}
	// Same content: no reconfigure.
	if got := nc.take(); !reflect.DeepEqual(got, []string{"reload"}) {
		t.Fatalf("networkctl %v", got)
	}

	if err := ConfirmNetworkTrial("eth0"); err == nil {
		t.Fatal("confirm without a trial succeeded")
	}
}

func TestNetworkTrialRevert(t *testing.T) {
	etc, run, nc := useNetDirs(t)
	if err := WriteNetworkTrial("eth0", staticConfig); err != nil {
		t.Fatal(err)
	}
	if err := RevertNetworkTrial("eth0"); err != nil {
		t.Fatal(err)
	}
	if got := dirFiles(t, run); len(got) != 0 {
		t.Fatalf("/run holds %v", got)
	}
	if got := dirFiles(t, etc); !reflect.DeepEqual(got, []string{"config.network"}) {
		t.Fatalf("/etc holds %v", got)
	}
	if got := nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}

	// Every step runs even when one fails, and a missing trial is no error.
	nc.fail["reload"] = true
	if err := RevertNetworkTrial("eth0"); err == nil {
		t.Fatal("failed reload not reported")
	}
	if got := nc.take(); !reflect.DeepEqual(got, []string{"reload", "reconfigure eth0"}) {
		t.Fatalf("networkctl %v", got)
	}
}

func TestValidateNetApply(t *testing.T) {
	ok := func(r NetApply) NetApply {
		if r.Intf == "" {
			r.Intf = "eth0"
		}
		return r
	}
	for _, tc := range []struct {
		name  string
		r     NetApply
		field string // "" when valid
	}{
		{"dhcp", NetApply{DHCP: true}, ""},
		{"static", NetApply{IPv4: "192.168.1.10/24", Gateway: "192.168.1.1", DNSServers: []string{"192.168.1.1"}, SearchDomains: []string{"lan"}}, ""},
		{"static without gateway", NetApply{IPv4: "10.0.0.2/8"}, ""},
		{"four dns and domains", NetApply{IPv4: "10.0.0.2/8", DNSServers: []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "10.0.0.1"}, SearchDomains: []string{"a", "b", "c", "d"}}, ""},
		{"prefix 32", NetApply{IPv4: "10.0.0.2/32"}, ""},

		{"route word", NetApply{Intf: "trial", DHCP: true}, "interface"},
		{"traversal", NetApply{Intf: "../x", DHCP: true}, "interface"},
		{"dhcp with ipv4", NetApply{DHCP: true, IPv4: "10.0.0.2/8"}, "dhcp"},
		{"dhcp with gateway", NetApply{DHCP: true, Gateway: "10.0.0.1"}, "dhcp"},
		{"dhcp with dns", NetApply{DHCP: true, DNSServers: []string{"10.0.0.1"}}, "dhcp"},
		{"dhcp with domain", NetApply{DHCP: true, SearchDomains: []string{"lan"}}, "dhcp"},
		{"static without ipv4", NetApply{}, "ipv4"},
		{"bare address", NetApply{IPv4: "192.168.1.10"}, "ipv4"},
		{"leading zero", NetApply{IPv4: "192.168.01.10/24"}, "ipv4"},
		{"prefix leading zero", NetApply{IPv4: "192.168.1.10/024"}, "ipv4"},
		{"prefix too long", NetApply{IPv4: "192.168.1.10/33"}, "ipv4"},
		{"prefix sign", NetApply{IPv4: "192.168.1.10/+24"}, "ipv4"},
		{"empty prefix", NetApply{IPv4: "192.168.1.10/"}, "ipv4"},
		{"ipv6", NetApply{IPv4: "fd00::1/64"}, "ipv4"},
		{"4 in 6", NetApply{IPv4: "::ffff:10.0.0.1/104"}, "ipv4"},
		{"newline", NetApply{IPv4: "10.0.0.2/8\nDNS=1.1.1.1"}, "ipv4"},
		{"gateway ipv6", NetApply{IPv4: "10.0.0.2/8", Gateway: "fe80::1"}, "gateway"},
		{"gateway cidr", NetApply{IPv4: "10.0.0.2/8", Gateway: "10.0.0.1/8"}, "gateway"},
		{"gateway leading zero", NetApply{IPv4: "10.0.0.2/8", Gateway: "10.0.0.01"}, "gateway"},
		{"gateway injection", NetApply{IPv4: "10.0.0.2/8", Gateway: "10.0.0.1\n[Match]"}, "gateway"},
		{"dns ipv6", NetApply{IPv4: "10.0.0.2/8", DNSServers: []string{"2001:db8::1"}}, "dns_servers[0]"},
		{"dns empty", NetApply{IPv4: "10.0.0.2/8", DNSServers: []string{"1.1.1.1", ""}}, "dns_servers[1]"},
		{"dns comma", NetApply{IPv4: "10.0.0.2/8", DNSServers: []string{"1.1.1.1,8.8.8.8"}}, "dns_servers[0]"},
		{"five dns", NetApply{IPv4: "10.0.0.2/8", DNSServers: []string{"1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4", "1.1.1.5"}}, "dns_servers"},
		{"five domains", NetApply{IPv4: "10.0.0.2/8", SearchDomains: []string{"a", "b", "c", "d", "e"}}, "search_domains"},
		{"domain injection", NetApply{IPv4: "10.0.0.2/8", SearchDomains: []string{"lan\nDNS=6.6.6.6"}}, "search_domains[0]"},
		{"domain comma", NetApply{IPv4: "10.0.0.2/8", SearchDomains: []string{"a,b"}}, "search_domains[0]"},
		{"domain empty", NetApply{IPv4: "10.0.0.2/8", SearchDomains: []string{""}}, "search_domains[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNetApply(ok(tc.r))
			if tc.field == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Field != tc.field {
				t.Fatalf("got %v, want a %s error", err, tc.field)
			}
		})
	}
}
