package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/calaos/calaos-container/cmd/calaos-os/api"
	"github.com/calaos/calaos-container/models/structs"
)

func TestNetErrExit(t *testing.T) {
	apiErr := func(code, msg, data string) error {
		return &api.APIError{Status: 409, Code: code, Msg: msg, Data: []byte(data)}
	}
	cases := []struct {
		name     string
		err      error
		verb     string
		wantCode int
		wantIn   string
	}{
		{"pending", apiErr("network_pending", "x", `{"change_id":"abcd","expires_in":42}`), "", ExitServer, "network confirm abcd"},
		{"invalid apply", apiErr("invalid_request", "ipv4 needs a prefix", ""), "", ExitUsage, "needs a prefix"},
		{"expired confirm", apiErr("invalid_request", "unknown change", ""), "confirm", ExitExpired, "rolled back"},
		{"expired revert", apiErr("invalid_request", "unknown change", ""), "revert", ExitExpired, "rolled back"},
		{"upgrade", apiErr("upgrade_in_progress", "x", ""), "", ExitServer, "upgrade"},
		{"old daemon", api.ErrNoAdmin, "", ExitServer, "not active"},
		{"other", errors.New("boom"), "", ExitServer, "boom"},
	}
	for _, c := range cases {
		code, err := netErrExit(c.err, c.verb)
		if code != c.wantCode || !strings.Contains(err.Error(), c.wantIn) {
			t.Errorf("%s: %d %q", c.name, code, err)
		}
	}
}

func TestPrintNetApplied(t *testing.T) {
	var b strings.Builder
	printNetApplied(&b, &api.NetworkChange{ChangeID: "5d0c8e1f2a3b4c6d", Intf: "eth0", AppliedAt: "2026-10-25T09:12:03Z", ConfirmWithin: 120})
	out := b.String()
	for _, want := range []string{"5d0c8e1f2a3b4c6d", "eth0", "120 s", "network confirm 5d0c8e1f2a3b4c6d", "network revert 5d0c8e1f2a3b4c6d"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestPrintNetState(t *testing.T) {
	st := &api.NetworkState{
		Interfaces: []structs.NetInterface{{Name: "lo", IsLoopback: true}, {Name: "eth0", IPv4: "10.0.0.2/24"}},
		Pending:    &api.NetworkPending{ChangeID: "abcd", Intf: "eth0", ExpiresIn: 87},
	}
	var b strings.Builder
	printNetState(&b, st)
	out := b.String()
	if strings.Contains(out, "│ lo ") || !strings.Contains(out, "10.0.0.2/24") || !strings.Contains(out, "87 s") {
		t.Errorf("state output %q", out)
	}
	b.Reset()
	st.Pending = nil
	printNetState(&b, st)
	if !strings.Contains(b.String(), "No pending change") {
		t.Errorf("no-pending output %q", b.String())
	}
}
