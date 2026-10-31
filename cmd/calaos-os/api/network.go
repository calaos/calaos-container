package api

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/calaos/calaos-container/models/structs"
)

// NetworkChange is the daemon's answer to a trial: the change to confirm.
type NetworkChange struct {
	ChangeID      string `json:"change_id"`
	Intf          string `json:"intf"`
	AppliedAt     string `json:"applied_at"`
	ConfirmWithin int    `json:"confirm_within"`
}

// Deadline is applied_at + confirm_within; zero when applied_at is unreadable.
func (c NetworkChange) Deadline() time.Time {
	t, err := time.Parse(time.RFC3339, c.AppliedAt)
	if err != nil {
		return time.Time{}
	}
	return t.Add(time.Duration(c.ConfirmWithin) * time.Second)
}

// NetworkPending is the change waiting for its confirmation.
type NetworkPending struct {
	ChangeID  string `json:"change_id"`
	Intf      string `json:"intf"`
	AppliedAt string `json:"applied_at"`
	ExpiresIn int    `json:"expires_in"`
}

type NetworkState struct {
	Interfaces []structs.NetInterface `json:"interfaces"`
	Pending    *NetworkPending        `json:"pending"`
}

// NetworkResult is the answer of confirm and revert.
type NetworkResult struct {
	ChangeID string `json:"change_id"`
	Intf     string `json:"intf"`
	State    string `json:"state"`
}

func (capi *CalaosApi) NetworkState(token string) (*NetworkState, error) {
	var out NetworkState
	if err := capi.callV2(token, "GET", "/network/state", nil, &out); err != nil {
		return nil, adminCallErr(err)
	}
	return &out, nil
}

// NetworkTrial applies a change on the bearer-only alias of network/apply: no
// push code, same timer and rollback.
func (capi *CalaosApi) NetworkTrial(token string, cfg structs.NetInterface) (*NetworkChange, error) {
	body := struct {
		Intf          string   `json:"intf"`
		DHCP          bool     `json:"dhcp"`
		IPv4          string   `json:"ipv4,omitempty"`
		Gateway       string   `json:"gateway,omitempty"`
		DNSServers    []string `json:"dns_servers,omitempty"`
		SearchDomains []string `json:"search_domains,omitempty"`
	}{cfg.Name, cfg.DHCP, cfg.IPv4, cfg.Gateway, cfg.DNSServers, cfg.SearchDomains}
	var out NetworkChange
	if err := capi.callV2(token, "POST", "/network/trial", body, &out); err != nil {
		return nil, adminCallErr(err)
	}
	return &out, nil
}

func (capi *CalaosApi) NetworkConfirm(token, changeID string) (*NetworkResult, error) {
	return capi.networkResolve(token, "/network/confirm", changeID)
}

func (capi *CalaosApi) NetworkRevert(token, changeID string) (*NetworkResult, error) {
	return capi.networkResolve(token, "/network/revert", changeID)
}

func (capi *CalaosApi) networkResolve(token, path, changeID string) (*NetworkResult, error) {
	var out NetworkResult
	body := struct {
		ChangeID string `json:"change_id"`
	}{changeID}
	if err := capi.callV2(token, "POST", path, body, &out); err != nil {
		return nil, adminCallErr(err)
	}
	return &out, nil
}

// NetworkPendingInfo returns the change blocking a request, from a
// network_pending error.
func NetworkPendingInfo(err error) (*NetworkPending, bool) {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "network_pending" {
		return nil, false
	}
	var p NetworkPending
	_ = json.Unmarshal(ae.Data, &p)
	return &p, true
}
