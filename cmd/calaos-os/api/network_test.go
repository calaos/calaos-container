package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/calaos/calaos-container/models/structs"
)

func TestNetworkTrialAndResolve(t *testing.T) {
	var trialBody map[string]any
	var trialAuth, confirmBody string
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/network/trial": func(w http.ResponseWriter, r *http.Request) {
			trialAuth = r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &trialBody)
			reply(200, `{"error":false,"msg":"ok","data":{"change_id":"5d0c8e1f2a3b4c6d","intf":"eth0","applied_at":"2026-10-25T09:12:03Z","confirm_within":120}}`)(w, r)
		},
		"/api/v2/network/confirm": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			confirmBody = string(b)
			reply(200, `{"error":false,"msg":"ok","data":{"change_id":"5d0c8e1f2a3b4c6d","intf":"eth0","state":"confirmed"}}`)(w, r)
		},
		"/api/v2/network/state": reply(200, `{"error":false,"msg":"ok","data":{"interfaces":[],"pending":{"change_id":"abcd","intf":"eth0","applied_at":"2026-10-25T09:12:03Z","expires_in":87}}}`),
	})

	ch, err := capi.NetworkTrial("tok", structs.NetInterface{Name: "eth0", IPv4: "10.0.0.2/24", DNSServers: []string{"10.0.0.1"}})
	if err != nil || ch.ChangeID != "5d0c8e1f2a3b4c6d" || ch.ConfirmWithin != 120 {
		t.Fatalf("NetworkTrial: %+v %v", ch, err)
	}
	if got := ch.Deadline().UTC().Format("15:04:05"); got != "09:14:03" {
		t.Errorf("deadline %s", got)
	}
	if trialAuth != "Bearer tok" || trialBody["intf"] != "eth0" || trialBody["dhcp"] != false || trialBody["ipv4"] != "10.0.0.2/24" {
		t.Fatalf("trial request %q %v", trialAuth, trialBody)
	}
	if _, ok := trialBody["gateway"]; ok {
		t.Errorf("empty gateway sent: %v", trialBody)
	}
	if _, ok := trialBody["code"]; ok {
		t.Errorf("code sent: %v", trialBody)
	}

	res, err := capi.NetworkConfirm("tok", "5d0c8e1f2a3b4c6d")
	if err != nil || res.State != "confirmed" || confirmBody != `{"change_id":"5d0c8e1f2a3b4c6d"}` {
		t.Fatalf("NetworkConfirm: %+v %v %s", res, err, confirmBody)
	}

	st, err := capi.NetworkState("tok")
	if err != nil || st.Pending == nil || st.Pending.ExpiresIn != 87 {
		t.Fatalf("NetworkState: %+v %v", st, err)
	}
}

func TestNetworkErrors(t *testing.T) {
	capi := newFakeDaemon(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v2/network/trial": reply(409, `{"error":true,"code":"network_pending","msg":"a change is pending","data":{"change_id":"abcd","expires_in":42}}`),
	})
	_, err := capi.NetworkTrial("tok", structs.NetInterface{Name: "eth0", DHCP: true})
	p, ok := NetworkPendingInfo(err)
	if !ok || p.ChangeID != "abcd" || p.ExpiresIn != 42 {
		t.Fatalf("NetworkPendingInfo: %+v %v", p, err)
	}

	if _, err := newFakeDaemon(t, nil).NetworkState("tok"); err != ErrNoAdmin {
		t.Fatalf("old daemon: %v", err)
	}
}
