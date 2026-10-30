package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/models/structs"
	"github.com/calaos/calaos-container/sysbus"
	"github.com/calaos/calaos-container/sysbus/sysbustest"
)

// The legacy unprefixed routes are frozen: old kiosks and old CLIs parse
// these exact bodies. Regenerate only for an intended legacy change:
//
//	go test ./app -run TestLegacyGolden -update
var updateGolden = flag.Bool("update", false, "rewrite app/testdata/golden")

// stubBackend replaces every system call made by the handlers with
// deterministic fakes, restored on cleanup.
type stubBackend struct {
	commands map[string]stubResult // key: command and args joined by spaces

	checkErr      error
	newVersions   structs.ImageMap
	installed     structs.ImageMap
	installedErr  error
	upgradeErr    error
	upgradeAllErr error
	upgradedPkg   string
	job           *models.Job
	status        structs.Status
	sysInfo       *models.SystemInfo
	sysInfoErr    error
	nets          []*structs.NetInterface
	netsErr       error
	intfExists    bool
	intfExistsErr error
	configureErr  error

	installCodeFile string
	installLogFile  string

	rebootBlocked bool   // an upgrade holds the apt lock
	startLog      string // written to the log by the fake installer
	startRunning  bool   // the fake installer has not exited
}

// fakeProcess is a models.Process for a fake installer.
type fakeProcess struct{ done chan struct{} }

func newFakeProcess(running bool) *fakeProcess {
	p := &fakeProcess{done: make(chan struct{})}
	if !running {
		close(p.done)
	}
	return p
}

func (p *fakeProcess) Pid() int              { return 4242 }
func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) ExitCode() (int, bool) {
	select {
	case <-p.done:
		return 0, true
	default:
		return 0, false
	}
}

// stubRun is a job whose apt run returns err.
type stubRun struct {
	job       models.Job
	err       error
	completed bool
}

func (r *stubRun) Job() models.Job { return r.job }
func (r *stubRun) Run() error      { return r.err }
func (r *stubRun) Complete()       { r.completed = true }

type stubResult struct {
	out string
	err error
}

// stubBus keeps the reboot and the kiosk restart under the keys of
// stubBackend.commands, which the goldens share with the routes that do exec.
type stubBus struct{ *sysbustest.Bus }

func newStubBus() *stubBus {
	b := sysbustest.New()
	b.SetUnit("calaos-home.service", sysbus.UnitProperties{LoadState: "loaded"})
	return &stubBus{b}
}

func (b *stubBus) Reboot(ctx context.Context) error {
	_, err := runCommand("systemctl", "reboot")
	return err
}

func (b *stubBus) RestartUnit(ctx context.Context, unit string) (*sysbus.Job, error) {
	if _, err := runCommand("systemctl", "restart", "calaos-home"); err != nil {
		return nil, err
	}
	return b.Bus.RestartUnit(ctx, unit)
}

func (s *stubBackend) install(t *testing.T) {
	t.Helper()

	prev := []any{runCommand, checkUpdates, getNewVersions, getInstalledPackages,
		startUpgradeJob, currentJob, updateStatus, getSystemInfo, getNetInterfaces,
		interfaceExists, configureNetInterface, installLogFile, installExitCodeFile,
		rebootGuard, startLoggedCommand}

	installMu.Lock()
	prevProc := installProc
	installProc = nil
	installMu.Unlock()

	// A test that brought its own fake bus keeps it.
	prevBus := models.Bus
	if _, own := models.Bus.(*sysbustest.Bus); !own {
		models.Bus = newStubBus()
	}

	runCommand = func(command string, args ...string) (string, error) {
		key := strings.Join(append([]string{command}, args...), " ")
		r, ok := s.commands[key]
		if !ok {
			return "", fmt.Errorf("unexpected command %q", key)
		}
		return r.out, r.err
	}
	checkUpdates = func() error { return s.checkErr }
	getNewVersions = func() structs.ImageMap {
		if s.newVersions == nil {
			return structs.ImageMap{}
		}
		return s.newVersions
	}
	getInstalledPackages = func() (structs.ImageMap, error) { return s.installed, s.installedErr }
	startUpgradeJob = func(kind, pkg string) (upgradeRun, error) {
		err := s.upgradeAllErr
		if kind == models.JobKindUpgrade {
			s.upgradedPkg = pkg
			err = s.upgradeErr
		}
		// Refusals come before any job exists.
		if err != nil && (err.Error() == models.ErrUpgradeInProgress.Error() || err.Error() == models.ErrPackageNotFound.Error()) {
			return nil, err
		}
		return &stubRun{job: models.Job{ID: "0123456789abcdef", Kind: kind, Package: pkg, Status: models.JobRunning}, err: err}, nil
	}
	currentJob = func() *models.Job { return s.job }
	updateStatus = func() (*structs.Status, error) {
		st := s.status
		return &st, nil
	}
	getSystemInfo = func() (*models.SystemInfo, error) { return s.sysInfo, s.sysInfoErr }
	getNetInterfaces = func() ([]*structs.NetInterface, error) { return s.nets, s.netsErr }
	interfaceExists = func(string) (bool, error) { return s.intfExists, s.intfExistsErr }
	configureNetInterface = func(string, structs.NetInterface) error { return s.configureErr }
	rebootGuard = func(fn func() error) error {
		if s.rebootBlocked {
			return models.ErrUpgradeInProgress
		}
		return fn()
	}
	startLoggedCommand = func(spec models.LoggedCommand) (models.Process, error) {
		if err := os.WriteFile(spec.LogFile, []byte(s.startLog), 0o600); err != nil {
			return nil, err
		}
		return newFakeProcess(s.startRunning), nil
	}

	installExitCodeFile = "/nonexistent/calaos_install.code"
	installLogFile = "/nonexistent/calaos_install.log"
	if s.installCodeFile != "" {
		installExitCodeFile = s.installCodeFile
	}
	if s.installLogFile != "" {
		installLogFile = s.installLogFile
	}

	t.Cleanup(func() {
		runCommand = prev[0].(func(string, ...string) (string, error))
		checkUpdates = prev[1].(func() error)
		getNewVersions = prev[2].(func() structs.ImageMap)
		getInstalledPackages = prev[3].(func() (structs.ImageMap, error))
		startUpgradeJob = prev[4].(func(string, string) (upgradeRun, error))
		currentJob = prev[5].(func() *models.Job)
		updateStatus = prev[6].(func() (*structs.Status, error))
		getSystemInfo = prev[7].(func() (*models.SystemInfo, error))
		getNetInterfaces = prev[8].(func() ([]*structs.NetInterface, error))
		interfaceExists = prev[9].(func(string) (bool, error))
		configureNetInterface = prev[10].(func(string, structs.NetInterface) error)
		installLogFile = prev[11].(string)
		installExitCodeFile = prev[12].(string)
		rebootGuard = prev[13].(func(func() error) error)
		startLoggedCommand = prev[14].(func(models.LoggedCommand) (models.Process, error))
		models.Bus = prevBus
		setInstallProc(prevProc)
	})
}

// newHandlerTestApp builds the real router with a provisioned token file.
func newHandlerTestApp(t *testing.T) *AppServer {
	t.Helper()
	tf := filepath.Join(t.TempDir(), "calaos-ct.token")
	if err := os.WriteFile(tf, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	resetTokenCache(t, tf)
	useAdminDirs(t)

	a, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return a
}

type httpCase struct {
	name    string
	method  string
	path    string
	auth    string // full Authorization header; "-" for none
	ctype   string
	body    string
	headers map[string]string
	backend stubBackend
}

func doCase(t *testing.T, a *AppServer, tc httpCase) (*http.Response, []byte) {
	t.Helper()

	var body io.Reader
	if tc.body != "" {
		body = strings.NewReader(tc.body)
	}
	req := httptest.NewRequest(tc.method, tc.path, body)
	switch tc.auth {
	case "":
		req.Header.Set("Authorization", "Bearer "+testToken)
	case "-":
	default:
		req.Header.Set("Authorization", tc.auth)
	}
	if tc.ctype != "" {
		req.Header.Set("Content-Type", tc.ctype)
	}
	for k, v := range tc.headers {
		req.Header.Set(k, v)
	}

	resp, err := a.appFiber.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", tc.method, tc.path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, b
}

const goldenLsblkInventory = `{"blockdevices":[` +
	`{"name":"/dev/sda","path":"/dev/sda","type":"disk","mountpoint":null,"mountpoints":[null],"children":[` +
	`{"name":"/dev/sda1","path":"/dev/sda1","type":"part","mountpoint":"/","mountpoints":["/"]}]},` +
	`{"name":"/dev/sdb","path":"/dev/sdb","type":"disk","mountpoint":null,"mountpoints":[null]}]}`

func legacyCases(t *testing.T) []httpCase {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}
	codeOK := write("ok.code", "0\n")
	codeBad := write("bad.code", "zero\n")
	logOK := write("ok.log", "line 1\nline 2\n")

	boom := errors.New("exit status 1: boom")
	sampleImages := structs.ImageMap{
		"calaos-server":    {Name: "calaos-server", Version: "4.5.1", CurrentVersion: "4.5.0"},
		"calaos-container": {Name: "calaos-container", Version: "1.8.0", CurrentVersion: "1.7.0"},
	}
	upgrading := structs.Status{
		Status: "upgrading", CurrentPkg: "calaos-server", Progress: 1, ProgressTotal: 2,
		TotalPercent: 40, CurrentStep: "unpack", CurrentPercent: 50, PkgIndex: 1, PkgCount: 2,
		Packages: []structs.PkgStatus{{Name: "calaos-server", State: "unpacking", Percent: 50}, {Name: "calaos-container", State: "pending"}},
	}
	nets := []*structs.NetInterface{
		{Name: "lo", IPv4: "127.0.0.1/8", IsLoopback: true, State: "carrier"},
		{Name: "eth0", IPv4: "192.168.1.10/24", Gateway: "192.168.1.1", MAC: "aa:bb:cc:dd:ee:ff", State: "routable", DHCP: true, DNSServers: []string{"192.168.1.1"}},
	}
	staticConf := `{"ipv4":"192.168.1.20/24","gateway":"192.168.1.1","dhcp":false,"dns_servers":["1.1.1.1"]}`

	return []httpCase{
		{name: "auth_missing", method: "GET", path: "/api/update/available", auth: "-"},
		{name: "auth_wrong", method: "GET", path: "/api/update/available", auth: "Bearer nope"},
		{name: "auth_lowercase_scheme", method: "GET", path: "/api/update/available", auth: "bearer " + testToken},
		// Deliberate legacy change: ?token= is honoured on /api/events only.
		{name: "auth_query_token", method: "GET", path: "/api/update/available?token=" + testToken, auth: "-"},
		{name: "events_query_token", method: "GET", path: "/api/events?token=" + testToken, auth: "-"},
		{name: "events_query_token_wrong", method: "GET", path: "/api/events?token=nope", auth: "-"},
		{name: "unknown_route", method: "GET", path: "/api/nope"},
		{name: "unknown_route_upper_case", method: "GET", path: "/API/NOPE"},
		{name: "update_available_upper_case", method: "GET", path: "/API/UPDATE/AVAILABLE"},
		{name: "session_header_ignored", method: "GET", path: "/api/network/list",
			headers: map[string]string{"X-Calaos-Admin-Session": "dead"}},
		{name: "wrong_method", method: "GET", path: "/api/system/reboot"},
		{name: "events_plain_get", method: "GET", path: "/api/events"},

		{name: "system_reboot_ok", method: "POST", path: "/api/system/reboot",
			backend: stubBackend{commands: map[string]stubResult{"systemctl reboot": {}}}},
		// Deliberate legacy change: no reboot while dpkg runs.
		{name: "system_reboot_in_progress", method: "POST", path: "/api/system/reboot",
			backend: stubBackend{rebootBlocked: true}},
		{name: "system_rollback_in_progress", method: "POST", path: "/api/system/rollback_snapshot",
			backend: stubBackend{rebootBlocked: true, commands: map[string]stubResult{"calaos_rollback": {}, "systemctl reboot": {}}}},
		{name: "system_reboot_err", method: "POST", path: "/api/system/reboot",
			backend: stubBackend{commands: map[string]stubResult{"systemctl reboot": {err: boom}}}},
		{name: "system_restart_ok", method: "POST", path: "/api/system/restart",
			backend: stubBackend{commands: map[string]stubResult{"systemctl restart calaos-home": {}}}},
		{name: "system_restart_err", method: "POST", path: "/api/system/restart",
			backend: stubBackend{commands: map[string]stubResult{"systemctl restart calaos-home": {err: boom}}}},
		{name: "system_fs_status_ok", method: "GET", path: "/api/system/fs_status",
			backend: stubBackend{commands: map[string]stubResult{"findmnt --json /": {out: `{"filesystems":[{"target":"/","source":"/dev/sda1","fstype":"btrfs","options":"rw"}]}`}}}},
		{name: "system_fs_status_err", method: "GET", path: "/api/system/fs_status",
			backend: stubBackend{commands: map[string]stubResult{"findmnt --json /": {err: boom}}}},
		{name: "system_fs_status_bad_json", method: "GET", path: "/api/system/fs_status",
			backend: stubBackend{commands: map[string]stubResult{"findmnt --json /": {out: "not json"}}}},
		{name: "system_rollback_ok", method: "POST", path: "/api/system/rollback_snapshot",
			backend: stubBackend{commands: map[string]stubResult{"calaos_rollback": {}, "systemctl reboot": {}}}},
		{name: "system_rollback_err", method: "POST", path: "/api/system/rollback_snapshot",
			backend: stubBackend{commands: map[string]stubResult{"calaos_rollback": {err: boom}}}},
		{name: "install_list_devices_ok", method: "GET", path: "/api/system/install/list_devices",
			backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {out: goldenLsblkInventory}}}},
		{name: "install_list_devices_err", method: "GET", path: "/api/system/install/list_devices",
			backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {err: boom}}}},
		{name: "install_start_bad_body", method: "POST", path: "/api/system/install/start", ctype: "application/json", body: `{"device":`},
		{name: "install_start_inventory_err", method: "POST", path: "/api/system/install/start", ctype: "application/json", body: `{"device":"/dev/sdb","confirm":true}`,
			backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {err: boom}}}},
		{name: "install_start_root_disk", method: "POST", path: "/api/system/install/start", ctype: "application/json", body: `{"device":"/dev/sda","confirm":true}`,
			backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {out: goldenLsblkInventory}}}},
		{name: "install_start_no_confirm", method: "POST", path: "/api/system/install/start", ctype: "application/json", body: `{"device":"/dev/sdb"}`,
			backend: stubBackend{commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {out: goldenLsblkInventory}}}},
		{name: "install_start_ok", method: "POST", path: "/api/system/install/start", ctype: "application/json", body: `{"device":"/dev/sdb","confirm":true}`,
			backend: stubBackend{installLogFile: filepath.Join(dir, "start.log"), startLog: "partitioning /dev/sdb\ndone\n",
				commands: map[string]stubResult{"lsblk --bytes --json --paths --output-all": {out: goldenLsblkInventory}}}},
		{name: "install_status_never_run", method: "GET", path: "/api/system/install/status"},
		{name: "install_status_bad_code", method: "GET", path: "/api/system/install/status",
			backend: stubBackend{installCodeFile: codeBad}},
		{name: "install_status_no_log", method: "GET", path: "/api/system/install/status",
			backend: stubBackend{installCodeFile: codeOK}},
		{name: "install_status_ok", method: "GET", path: "/api/system/install/status",
			backend: stubBackend{installCodeFile: codeOK, installLogFile: logOK}},
		{name: "system_info_ok", method: "GET", path: "/api/system/info",
			backend: stubBackend{sysInfo: &models.SystemInfo{Hostname: "calaos", Uptime: 3600, CPUUsagePercent: 12, MemoryUsagePercent: 34}}},
		{name: "system_info_err", method: "GET", path: "/api/system/info",
			backend: stubBackend{sysInfoErr: errors.New("GetSystemInfo: open /proc/uptime: no such file or directory")}},

		{name: "network_list_ok", method: "GET", path: "/api/network/list", backend: stubBackend{nets: nets}},
		{name: "network_list_err", method: "GET", path: "/api/network/list", backend: stubBackend{netsErr: boom}},
		{name: "network_configure_bad_name", method: "POST", path: "/api/network/eth0;rm", ctype: "application/json", body: staticConf},
		// The one accepted change: the route words of /api/v2/network/ are refused by the shared validator.
		{name: "network_configure_route_word", method: "POST", path: "/api/network/apply", ctype: "application/json", body: staticConf},
		{name: "network_configure_bad_body", method: "POST", path: "/api/network/eth0", ctype: "application/json", body: `{"ipv4":`},
		{name: "network_configure_invalid", method: "POST", path: "/api/network/eth0", ctype: "application/json", body: `{"ipv4":"999.1.1.1/24","dhcp":false}`},
		{name: "network_configure_exists_err", method: "POST", path: "/api/network/eth0", ctype: "application/json", body: staticConf,
			backend: stubBackend{intfExistsErr: boom}},
		{name: "network_configure_unknown", method: "POST", path: "/api/network/eth9", ctype: "application/json", body: staticConf},
		{name: "network_configure_err", method: "POST", path: "/api/network/eth0", ctype: "application/json", body: staticConf,
			backend: stubBackend{intfExists: true, configureErr: boom}},
		{name: "network_configure_ok", method: "POST", path: "/api/network/eth0", ctype: "application/json", body: `{"dhcp":true}`,
			backend: stubBackend{intfExists: true}},

		{name: "update_check_ok", method: "GET", path: "/api/update/check", backend: stubBackend{newVersions: sampleImages}},
		{name: "update_check_empty", method: "GET", path: "/api/update/check"},
		{name: "update_check_in_progress", method: "GET", path: "/api/update/check",
			backend: stubBackend{checkErr: errors.New("upgrade already in progress")}},
		{name: "update_check_apt_err", method: "GET", path: "/api/update/check",
			backend: stubBackend{checkErr: errors.New("apt-get update failed: exit status 100")}},
		{name: "update_available", method: "GET", path: "/api/update/available", backend: stubBackend{newVersions: sampleImages}},
		{name: "update_installed_ok", method: "GET", path: "/api/update/installed",
			backend: stubBackend{installed: structs.ImageMap{"calaos-server": {Name: "calaos-server", Version: "4.5.0"}}}},
		{name: "update_installed_err", method: "GET", path: "/api/update/installed",
			backend: stubBackend{installedErr: errors.New("GetInstalledCalaosPackages error: exit status 2, stderr: boom")}},
		{name: "update_upgrade_all_ok", method: "POST", path: "/api/update/upgrade-all"},
		{name: "update_upgrade_all_in_progress", method: "POST", path: "/api/update/upgrade-all",
			backend: stubBackend{upgradeAllErr: errors.New("upgrade already in progress")}},
		{name: "update_upgrade_all_apt_err", method: "POST", path: "/api/update/upgrade-all",
			backend: stubBackend{upgradeAllErr: errors.New("E: Sub-process /usr/bin/dpkg returned an error code (1)")}},
		{name: "update_upgrade_apt_err", method: "POST", path: "/api/update/upgrade?package=calaos-server",
			backend: stubBackend{upgradeErr: errors.New("E: Sub-process /usr/bin/dpkg returned an error code (1)")}},
		{name: "update_upgrade_ok", method: "POST", path: "/api/update/upgrade?package=calaos-server"},
		{name: "update_upgrade_not_found", method: "POST", path: "/api/update/upgrade?package=nope",
			backend: stubBackend{upgradeErr: errors.New("package not found")}},
		{name: "update_status_idle", method: "GET", path: "/api/update/status", backend: stubBackend{status: structs.Status{Status: "idle"}}},
		{name: "update_status_upgrading", method: "GET", path: "/api/update/status", backend: stubBackend{status: upgrading}},
	}
}

// renderGolden is what a golden file pins: status, content type and the exact body.
func renderGolden(resp *http.Response, body []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "status: %d\ncontent-type: %s\n\n", resp.StatusCode, resp.Header.Get("Content-Type"))
	b.Write(body)
	b.WriteString("\n")
	return b.Bytes()
}

func TestLegacyGolden(t *testing.T) {
	for _, tc := range legacyCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.backend
			s.install(t)
			a := newHandlerTestApp(t)

			resp, body := doCase(t, a, tc)
			got := renderGolden(resp, body)

			path := filepath.Join("testdata", "golden", tc.name+".golden")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path) //nolint:gosec // path built from a test case name
			if err != nil {
				t.Fatalf("missing golden file (run with -update on the frozen code): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("legacy response changed for %s %s\n--- want\n%s\n--- got\n%s", tc.method, tc.path, want, got)
			}
		})
	}
}

// TestLegacyGoldenUpgradeQuery pins that the legacy single-package upgrade
// still takes its package from the query string.
func TestLegacyGoldenUpgradeQuery(t *testing.T) {
	s := &stubBackend{}
	s.install(t)
	a := newHandlerTestApp(t)

	resp, _ := doCase(t, a, httpCase{method: "POST", path: "/api/update/upgrade?package=calaos-server"})
	if resp.StatusCode != http.StatusOK || s.upgradedPkg != "calaos-server" {
		t.Fatalf("status %d, upgraded %q", resp.StatusCode, s.upgradedPkg)
	}
}

// TestLegacyGoldenNoTokenFile pins the fail-closed body sent when no token is
// provisioned.
func TestLegacyGoldenNoTokenFile(t *testing.T) {
	(&stubBackend{}).install(t)
	a := newHandlerTestApp(t)
	resetTokenCache(t, filepath.Join(t.TempDir(), "missing.token"))

	resp, body := doCase(t, a, httpCase{method: "GET", path: "/api/update/available"})
	got := renderGolden(resp, body)
	want := "status: 401\ncontent-type: application/json\n\n{\"error\":true,\"msg\":\"unauthorized\"}\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
