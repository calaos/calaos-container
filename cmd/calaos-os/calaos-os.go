package main

//This is the calaos-os CLI tool that interacts with calaos-container backend
//It's main purpose is to start/manage updates from CLI

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/calaos/calaos-container/cmd/calaos-os/api"
	"github.com/calaos/calaos-container/models/structs"

	"github.com/fatih/color"
	cli "github.com/jawher/mow.cli"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/raoulh/go-progress"
)

const (
	CharStar     = "\u2737"
	CharAbort    = "\u2718"
	CharCheck    = "\u2714"
	CharWarning  = "\u26A0"
	CharArrow    = "\u2012\u25b6"
	CharVertLine = "\u2502"

	TOKEN_FILE = "/run/calaos/calaos-ct.token"
)

var (
	blue       = color.New(color.FgBlue).SprintFunc()
	errorRed   = color.New(color.FgRed).SprintFunc()
	errorBgRed = color.New(color.BgRed, color.FgBlack).SprintFunc()
	green      = color.New(color.FgGreen).SprintFunc()
	cyan       = color.New(color.FgCyan).SprintFunc()
	bgCyan     = color.New(color.FgWhite).SprintFunc()
)

func exit(err error, exit int) {
	fmt.Println(errorRed(CharAbort), err)
	cli.Exit(exit)
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
		exit(err, 1)
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
			exit(err, 1)
		}

		imgs, err := a.UpdateImages(token)
		if err != nil {
			exit(err, 1)
		}

		t := table.NewWriter()
		t.SetOutputMirror(os.Stdout)
		t.AppendHeader(table.Row{"Image", "Version", "Source"})

		for _, e := range *imgs {
			t.AppendRow(table.Row{
				e.Name,
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
			exit(err, 1)
		}

		imgs, err := a.UpdateCheck(token)
		if err != nil {
			exit(err, 1)
		}

		if len(*imgs) == 0 {
			fmt.Printf("%s Already up to date.\n", green(CharCheck))
			return
		}

		fmt.Printf("Updates available:\n")

		t := table.NewWriter()
		t.SetOutputMirror(os.Stdout)
		t.AppendHeader(table.Row{"Image", "Current version", "New version", "Source"})

		for _, e := range *imgs {
			t.AppendRow(table.Row{
				e.Name,
				e.CurrentVerion,
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
			exit(err, 1)
		}

		if (*pkg) != "" {
			err = a.UpdatePackage(token, *pkg)
		} else {
			err = a.UpgradePackages(token)
		}

		//get status from API and wait until it returns idle
		bar := progress.New(100)
		bar.Format = progress.ProgressFormats[0]
		bar.ShowNumeric = false
		bar.ShowTextSuffix = true

		currentPkg := ""

		for {
			status, err := a.UpgradeStatus(token)
			if err != nil {
				exit(err, 1)
			}
			if status.Status == "idle" {
				break
			}

			if currentPkg != status.CurrentPkg {
				bar.SetTextSuffix(fmt.Sprintf("\t %s %s installed", green(CharCheck), status.CurrentPkg))
				bar.Set(100)
				fmt.Println()

				currentPkg = status.CurrentPkg
			}

			bar.SetTextSuffix(fmt.Sprintf("\t Installing %s", status.CurrentPkg))
			if status.ProgressTotal < 1 {
				bar.Set(0)
			} else {
				bar.Set(status.Progress * 100 / status.ProgressTotal)
			}

			time.Sleep(500 * time.Millisecond)
		}

		if err != nil {
			exit(err, 1)
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
			exit(err, 1)
		}

		nets, err := a.GetNetworkInterfaces(token)
		if err != nil {
			exit(err, 1)
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
			exit(err, 1)
		}

		ipCIDR, _ := toCIDR(*ip, *netmask)
		config := &structs.NetInterface{
			Name:    *intf,
			IPv4:    ipCIDR,
			Gateway: *gateway,
			DHCP:    false,
		}
		config.DNSServers = append(config.DNSServers, *dns...)

		err = a.ConfigureNetworkInterface(token, *intf, config)
		if err != nil {
			exit(err, 1)
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
			exit(err, 1)
		}

		config := &structs.NetInterface{
			DHCP: true,
		}

		err = a.ConfigureNetworkInterface(token, *intf, config)
		if err != nil {
			exit(err, 1)
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
