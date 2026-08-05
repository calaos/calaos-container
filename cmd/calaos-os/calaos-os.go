package main

//This is the calaos-os CLI tool that interacts with calaos-container backend
//It's main purpose is to start/manage updates from CLI

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/calaos/calaos-container/cmd/calaos-os/api"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models/structs"

	"github.com/fatih/color"
	cli "github.com/jawher/mow.cli"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/raoulh/go-progress"
)

const (
	CharAbort = "✘"
	CharCheck = "✔"
	CharArrow = "‒▶"

	TOKEN_FILE = "/run/calaos/calaos-ct.token"

	// upgradeOverallTimeout bounds how long cmdUpgrade will wait on the
	// event bus for a terminal (done/error) event before giving up, so a
	// daemon that stops publishing mid-upgrade doesn't hang the CLI forever.
	upgradeOverallTimeout = 30 * time.Minute
)

// Exit codes let calling shell scripts distinguish failure classes instead
// of receiving an undifferentiated 1 for everything. Scripts that only check
// `!= 0` are unaffected by this; scripts asserting `== 1` for non-usage
// failures may need updating.
const (
	ExitUsage   = 1 // bad CLI invocation / invalid user-supplied arguments
	ExitConfig  = 2 // local config/token problem (e.g. token file missing/unreadable)
	ExitNetwork = 3 // could not reach the daemon, or the connection was lost
	ExitServer  = 4 // the daemon was reached but reported/caused a failure
)

var (
	errorRed = color.New(color.FgRed).SprintFunc()
	green    = color.New(color.FgGreen).SprintFunc()
	cyan     = color.New(color.FgCyan).SprintFunc()
)

func exit(err error, exit int) {
	fmt.Println(errorRed(CharAbort), err)
	cli.Exit(exit)
}

// classifyAPIErr picks a network vs. server exit code for an error returned
// by the api package: transport-level failures (connection refused, DNS,
// timeout, connection lost...) are network errors; anything else means the
// daemon was reached and is treated as a server-side failure.
func classifyAPIErr(err error) int {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ExitNetwork
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return ExitNetwork
	}
	return ExitServer
}

// exitAPIErr exits with a network or server exit code depending on err, see
// classifyAPIErr.
func exitAPIErr(err error) {
	exit(err, classifyAPIErr(err))
}

func main() {
	a := cli.App("calaos-os", "Calaos OS tool")

	a.Spec = ""

	a.Command("list", "list installed images/pkg and updates", cmdList)
	a.Command("check-update", "check for any available updates", cmdCheck)
	a.Command("upgrade", "update images/pkg to the latest availble", cmdUpgrade)
	a.Command("network", "network related commands", func(cmd *cli.Cmd) {
		cmd.Command("list", "list network interfaces", cmdNetList)
		cmd.Command("configure", "configure network interface", func(cmd *cli.Cmd) {
			cmd.Command("static", "configure network interface with static IP", cmdNetConfigureStatic)
			cmd.Command("dhcp", "configure network interface with DHCP", cmdNetConfigureDHCP)
		})
	})

	if err := a.Run(os.Args); err != nil {
		exit(err, ExitUsage)
	}
}

func getToken() (string, error) {
	content, err := os.ReadFile(TOKEN_FILE)
	if err != nil {
		return "", fmt.Errorf("unable to read token: %v", err)
	}
	return strings.TrimSpace(string(content)), nil
}

func cmdList(cmd *cli.Cmd) {
	cmd.Spec = ""
	cmd.Action = func() {
		a := api.NewCalaosApi(api.CalaosCtHost)

		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		imgs, err := a.UpdateAvailable(token)
		if err != nil {
			exitAPIErr(err)
		}

		if len(*imgs) == 0 {
			fmt.Printf("%s No updates available.\n", green(CharCheck))
			return
		}

		t := table.NewWriter()
		t.SetOutputMirror(os.Stdout)
		t.AppendHeader(table.Row{"Image", "Current version", "New version"})

		for _, e := range *imgs {
			t.AppendRow(table.Row{
				e.Name,
				e.CurrentVersion,
				e.Version,
			})
		}

		t.SetStyle(table.StyleLight)
		t.Render()
	}
}

func cmdCheck(cmd *cli.Cmd) {
	cmd.Spec = ""
	cmd.Action = func() {
		fmt.Printf("%s Checking for updates...\n", cyan(CharArrow))
		a := api.NewCalaosApi(api.CalaosCtHost)

		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		imgs, err := a.UpdateCheck(token)
		if err != nil {
			exitAPIErr(err)
		}

		if len(*imgs) == 0 {
			fmt.Printf("%s Already up to date.\n", green(CharCheck))
			return
		}

		fmt.Printf("Updates available:\n")

		t := table.NewWriter()
		t.SetOutputMirror(os.Stdout)
		t.AppendHeader(table.Row{"Image", "Current version", "New version"})

		for _, e := range *imgs {
			t.AppendRow(table.Row{
				e.Name,
				e.CurrentVersion,
				e.Version,
			})
		}

		t.SetStyle(table.StyleLight)
		t.Render()
	}
}

func cmdUpgrade(cmd *cli.Cmd) {
	cmd.Spec = "[PKG]"
	pkg := cmd.StringArg("PKG", "", "Package to upgrade. If not specified, all packages will be updated")

	cmd.Action = func() {
		if (*pkg) != "" {
			fmt.Printf("%s Upgrading package %s...\n", cyan(CharArrow), *pkg)
		} else {
			fmt.Printf("%s Upgrading all packages...\n", cyan(CharArrow))
		}

		a := api.NewCalaosApi(api.CalaosCtHost)

		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		// Connect to the event bus BEFORE issuing the upgrade request: the
		// POST is blocking on the daemon side and progress events can start
		// flowing as soon as it starts, so we must be subscribed first.
		conn, err := a.EventsWS(token)
		if err != nil {
			if errors.Is(err, api.ErrDaemonTooOld) {
				exit(fmt.Errorf("le daemon calaos-container est trop ancien et ne supporte pas /api/events. Redémarrez le service (systemctl restart calaos-container) puis réessayez"), ExitServer)
			}
			exitAPIErr(err)
		}
		defer func() { _ = conn.Close() }()

		// Run the (blocking) upgrade request in the background and collect
		// its outcome via a channel.
		postDone := make(chan error, 1)
		go func() {
			if (*pkg) != "" {
				postDone <- a.UpdatePackage(token, *pkg)
			} else {
				postDone <- a.UpgradePackages(token)
			}
		}()

		bar := progress.New(100)
		bar.Format = progress.ProgressFormats[0]
		bar.ShowNumeric = false
		bar.ShowTextSuffix = true

		// Decode incoming events in a dedicated goroutine so the main loop can
		// also react to the POST outcome: an upgrade rejected before it starts
		// (unknown package, upgrade already in progress...) never publishes any
		// terminal event, and waiting on the WebSocket alone would hang.
		stCh := make(chan structs.Status)
		readErr := make(chan error, 1)
		go func() {
			first := true
			for {
				var ev api.Event
				if err := conn.ReadJSON(&ev); err != nil {
					readErr <- err
					return
				}

				if ev.Type != events.EventUpgradeProgress {
					// Ignore unknown event types (the bus is extensible).
					continue
				}

				var st structs.Status
				if err := json.Unmarshal(ev.Data, &st); err != nil {
					continue
				}

				// The first event is the connection snapshot: it reflects the
				// daemon state BEFORE our upgrade request was sent (possibly
				// the stale terminal state of a previous upgrade), never ours.
				if first {
					first = false
					continue
				}

				stCh <- st
			}
		}()

		currentPkg := ""
		var st structs.Status
		var postErr error
		postCh := postDone
		var giveUp <-chan time.Time
		// overallDeadline is a wall-clock backstop: if the daemon stops
		// publishing events mid-upgrade (crash, restart...) the CLI must
		// not hang forever waiting on the WebSocket.
		overallDeadline := time.After(upgradeOverallTimeout)

		// clearBarLine erases the live progress-bar line so permanent
		// output (package-done lines, errors) never lands after the bar's
		// space padding. The bar library itself only ever repaints over
		// the current line with \r and cannot print at 100% (Set(total)
		// clears instead of painting), so all permanent lines are ours.
		clearBarLine := func() { fmt.Print("\r\x1b[2K") }

	readLoop:
		for {
			select {
			case s := <-stCh:
				// "idle" is the return-to-rest event (reset of a previous
				// upgrade): nothing to render.
				if s.Status == "idle" {
					continue
				}
				st = s

				// Global phases (download, cache rebuild...) publish
				// events with an empty CurrentPkg: they only move the
				// total bar and must not be treated as a package
				// transition, or each one would flush a bogus
				// "installed" line.
				if st.CurrentPkg != "" {
					if currentPkg != "" && currentPkg != st.CurrentPkg {
						clearBarLine()
						fmt.Printf("%s %s installed\n", green(CharCheck), currentPkg)
					}
					currentPkg = st.CurrentPkg
					bar.SetTextSuffix(fmt.Sprintf("  [%d/%d] %s (%s) %d%%", st.PkgIndex, st.PkgCount, st.CurrentPkg, st.CurrentStep, st.CurrentPercent))
				}
				bar.Set(st.TotalPercent)

				switch st.Status {
				case "done", "error":
					break readLoop
				}

			case err := <-readErr:
				clearBarLine()
				exit(fmt.Errorf("connexion au daemon perdue: %w", err), ExitNetwork)

			case postErr = <-postCh:
				postCh = nil
				if postErr != nil {
					// The upgrade request itself failed (network error, or
					// the daemon rejected it before starting: unknown
					// package, upgrade already in progress...). No terminal
					// event will ever be published for it, so there is
					// nothing to wait for: abort immediately instead of
					// polling the event bus.
					break readLoop
				}
				// The request succeeded; if the upgrade actually ran, its
				// terminal done/error event has already been published or
				// will arrive shortly. Leave a short grace period for it.
				giveUp = time.After(3 * time.Second)

			case <-giveUp:
				break readLoop

			case <-overallDeadline:
				clearBarLine()
				exit(fmt.Errorf("upgrade timed out after %s waiting for the daemon", upgradeOverallTimeout), ExitNetwork)
			}
		}

		_ = conn.Close()

		if postCh != nil {
			postErr = <-postDone
		}

		// Leave the bar line before printing any permanent outcome; a
		// no-op if the bar never painted (e.g. rejected upgrade).
		clearBarLine()

		if st.Status == "error" {
			exit(fmt.Errorf("%s", st.Error), ExitServer)
		}

		if postErr != nil {
			exitAPIErr(postErr)
		}

		if currentPkg != "" {
			fmt.Printf("%s %s installed\n", green(CharCheck), currentPkg)
		}
		fmt.Printf("%s Done.\n", green(CharCheck))
	}
}

func cmdNetList(cmd *cli.Cmd) {
	cmd.Spec = ""
	cmd.Action = func() {
		a := api.NewCalaosApi(api.CalaosCtHost)

		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		nets, err := a.GetNetworkInterfaces(token)
		if err != nil {
			exitAPIErr(err)
		}

		t := table.NewWriter()
		t.SetOutputMirror(os.Stdout)
		t.AppendHeader(table.Row{"Interface", "IP", "Gateway", "MAC", "State", "DHCP"})

		for _, e := range *nets {
			if e.IsLoopback {
				continue
			}

			t.AppendRow(table.Row{
				e.Name,
				e.IPv4,
				e.Gateway,
				e.MAC,
				e.State,
				e.DHCP,
			})
		}

		t.SetStyle(table.StyleLight)
		t.Render()
	}
}

func cmdNetConfigureStatic(cmd *cli.Cmd) {
	cmd.Spec = "INTERFACE IPV4 NETMASK [GATEWAY] [DNS...]"
	intf := cmd.StringArg("INTERFACE", "", "Interface to configure")
	ip := cmd.StringArg("IPV4", "", "IPv4 address")
	netmask := cmd.StringArg("NETMASK", "", "Netmask")
	gateway := cmd.StringArg("GATEWAY", "", "Gateway")
	dns := cmd.StringsArg("DNS", nil, "DNS servers")

	cmd.Action = func() {
		fmt.Printf("%s Configuring interface %s...\n", cyan(CharArrow), *intf)

		a := api.NewCalaosApi(api.CalaosCtHost)

		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		ipCIDR, err := toCIDR(*ip, *netmask)
		if err != nil {
			exit(err, ExitUsage)
		}
		config := &structs.NetInterface{
			Name:    *intf,
			IPv4:    ipCIDR,
			Gateway: *gateway,
			DHCP:    false,
		}
		config.DNSServers = append(config.DNSServers, *dns...)

		err = a.ConfigureNetworkInterface(token, *intf, config)
		if err != nil {
			exitAPIErr(err)
		}

		fmt.Printf("%s Done.\n", green(CharCheck))
	}
}

func cmdNetConfigureDHCP(cmd *cli.Cmd) {
	cmd.Spec = "INTERFACE"
	intf := cmd.StringArg("INTERFACE", "", "Interface to configure")

	cmd.Action = func() {
		fmt.Printf("%s Configuring interface %s...\n", cyan(CharArrow), *intf)

		a := api.NewCalaosApi(api.CalaosCtHost)

		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}

		config := &structs.NetInterface{
			DHCP: true,
		}

		err = a.ConfigureNetworkInterface(token, *intf, config)
		if err != nil {
			exitAPIErr(err)
		}

		fmt.Printf("%s Done.\n", green(CharCheck))
	}
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
