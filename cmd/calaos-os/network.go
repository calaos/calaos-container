package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	cli "github.com/jawher/mow.cli"
	"github.com/jedib0t/go-pretty/v6/table"

	"github.com/calaos/calaos-container/cmd/calaos-os/api"
	"github.com/calaos/calaos-container/models/structs"
)

// ExitExpired: the change to confirm or revert is gone (rolled back after its
// deadline, or already confirmed or reverted).
const ExitExpired = 5

func cmdNetState(cmd *cli.Cmd) {
	cmd.Spec = ""
	cmd.Action = func() {
		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}
		st, err := api.NewCalaosApi(api.CalaosCtHost).NetworkState(token)
		if err != nil {
			exitNetErr(err)
		}
		printNetState(os.Stdout, st)
	}
}

func cmdNetApply(cmd *cli.Cmd) {
	cmd.Spec = "--intf=<name> (--dhcp | --ipv4=<a.b.c.d/n> [--gateway=<ip>] [--dns=<ip>...] [--search=<domain>...])"
	intf := cmd.StringOpt("intf", "", "interface to configure")
	dhcp := cmd.BoolOpt("dhcp", false, "use DHCP")
	ipv4 := cmd.StringOpt("ipv4", "", "static IPv4 address with its prefix, e.g. 192.168.1.10/24")
	gateway := cmd.StringOpt("gateway", "", "IPv4 gateway")
	dns := cmd.StringsOpt("dns", nil, "DNS server (repeatable, 4 at most)")
	search := cmd.StringsOpt("search", nil, "search domain (repeatable, 4 at most)")

	cmd.Action = func() {
		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}
		cfg := structs.NetInterface{
			Name: *intf, DHCP: *dhcp, IPv4: *ipv4, Gateway: *gateway,
			DNSServers: *dns, SearchDomains: *search,
		}
		ch, err := api.NewCalaosApi(api.CalaosCtHost).NetworkTrial(token, cfg)
		if err != nil {
			exitNetErr(err)
		}
		printNetApplied(os.Stdout, ch)
	}
}

func cmdNetConfirm(cmd *cli.Cmd) { netResolve(cmd, "confirm") }
func cmdNetRevert(cmd *cli.Cmd)  { netResolve(cmd, "revert") }
func netResolve(cmd *cli.Cmd, verb string) {
	cmd.Spec = "[CHANGE_ID]"
	id := cmd.StringArg("CHANGE_ID", "", "change to "+verb+" (default: the pending one)")

	cmd.Action = func() {
		token, err := getToken()
		if err != nil {
			exit(err, ExitConfig)
		}
		a := api.NewCalaosApi(api.CalaosCtHost)

		changeID := *id
		if changeID == "" {
			st, err := a.NetworkState(token)
			if err != nil {
				exitNetErr(err)
			}
			if st.Pending == nil {
				exit(errNoPendingChange, ExitExpired)
			}
			changeID = st.Pending.ChangeID
		}

		var res *api.NetworkResult
		if verb == "confirm" {
			res, err = a.NetworkConfirm(token, changeID)
		} else {
			res, err = a.NetworkRevert(token, changeID)
		}
		if err != nil {
			exitNetErrFor(err, verb)
		}
		if verb == "confirm" {
			fmt.Printf("%s Network change %s on %s confirmed, it is now permanent.\n", green(CharCheck), res.ChangeID, res.Intf)
		} else {
			fmt.Printf("%s Network change %s on %s reverted.\n", green(CharCheck), res.ChangeID, res.Intf)
		}
	}
}

var errNoPendingChange = errors.New("no network change is waiting: it was confirmed, reverted, or rolled back after its deadline")

func printNetState(w io.Writer, st *api.NetworkState) {
	t := table.NewWriter()
	t.SetOutputMirror(w)
	t.AppendHeader(table.Row{"Interface", "IP", "Gateway", "DNS", "Search", "MAC", "State", "DHCP"})
	for _, e := range st.Interfaces {
		if e.IsLoopback || e.Name == "lo" {
			continue
		}
		t.AppendRow(table.Row{e.Name, e.IPv4, e.Gateway, joinOrDash(e.DNSServers), joinOrDash(e.SearchDomains), e.MAC, e.State, e.DHCP})
	}
	t.SetStyle(table.StyleLight)
	t.Render()

	if st.Pending == nil {
		_, _ = fmt.Fprintln(w, "No pending change.")
		return
	}
	p := st.Pending
	_, _ = fmt.Fprintf(w, "Pending change %s on %s, reverts automatically in %d s.\n", p.ChangeID, p.Intf, p.ExpiresIn)
	printNetHow(w, p.ChangeID)
}

func printNetApplied(w io.Writer, ch *api.NetworkChange) {
	_, _ = fmt.Fprintf(w, "%s Change %s applied on %s.\n", green(CharCheck), ch.ChangeID, ch.Intf)
	if d := ch.Deadline(); !d.IsZero() {
		_, _ = fmt.Fprintf(w, "It reverts automatically at %s (%d s) unless confirmed.\n", d.Local().Format("15:04:05"), ch.ConfirmWithin)
	} else {
		_, _ = fmt.Fprintf(w, "It reverts automatically in %d s unless confirmed.\n", ch.ConfirmWithin)
	}
	printNetHow(w, ch.ChangeID)
}

func printNetHow(w io.Writer, id string) {
	_, _ = fmt.Fprintf(w, "%s Keep it:  calaos-os network confirm %s\n", cyan(CharArrow), id)
	_, _ = fmt.Fprintf(w, "%s Undo it:  calaos-os network revert %s\n", cyan(CharArrow), id)
}

func joinOrDash(s []string) string {
	if len(s) == 0 {
		return ""
	}
	out := s[0]
	for _, v := range s[1:] {
		out += "," + v
	}
	return out
}

func exitNetErr(err error)                 { exitNetErrFor(err, "") }
func exitNetErrFor(err error, verb string) { code, msg := netErrExit(err, verb); exit(msg, code) }

// netErrExit maps a daemon error to a message and an exit code. verb is
// "confirm" or "revert" for the calls that name an existing change, where
// invalid_request means the change is gone rather than a bad flag.
func netErrExit(err error, verb string) (int, error) {
	var ae *api.APIError
	switch {
	case errors.Is(err, api.ErrNoAdmin):
		return ExitServer, err
	case errors.As(err, &ae) && ae.Code == "network_pending":
		p, _ := api.NetworkPendingInfo(err)
		return ExitServer, fmt.Errorf("another network change (%s) is waiting for its confirmation, %d s left: run `calaos-os network confirm %s` or `calaos-os network revert %s` first",
			p.ChangeID, p.ExpiresIn, p.ChangeID, p.ChangeID)
	case errors.As(err, &ae) && ae.Code == "upgrade_in_progress":
		return ExitServer, errors.New("an upgrade is running, retry when it is done")
	case errors.As(err, &ae) && ae.Code == "invalid_request" && verb != "":
		return ExitExpired, fmt.Errorf("%w (%s)", errNoPendingChange, ae.Msg)
	case errors.As(err, &ae) && ae.Code == "invalid_request":
		return ExitUsage, err
	}
	return classifyAPIErr(err), err
}
