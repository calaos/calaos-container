package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

const maxAlerts = 32

// Alert is an admin notification kept until calaos_server acknowledges it.
// It carries no secret: the approver alerts name the phone by device name
// and token_id, never by token.
type Alert struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	TS         string `json:"ts"`
	DeviceName string `json:"device_name,omitempty"`
	TokenID    string `json:"token_id,omitempty"`
}

// AlertData is the data of the event published for an alert.
type AlertData struct {
	ID         string `json:"id"`
	TS         string `json:"ts"`
	DeviceName string `json:"device_name,omitempty"`
	TokenID    string `json:"token_id,omitempty"`
}

func (a Alert) Data() AlertData {
	return AlertData{ID: a.ID, TS: a.TS, DeviceName: a.DeviceName, TokenID: a.TokenID}
}

type alertFile struct {
	Alerts []Alert `json:"alerts"`
}

// alertStore is guarded by Manager.mu.
type alertStore struct {
	path string
	list []Alert
}

func (as *alertStore) load() error {
	b, found, err := readSmallFile(as.path, 64*1024)
	if err != nil || !found {
		return err
	}
	var f alertFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	as.list = f.Alerts
	if len(as.list) > maxAlerts {
		as.list = as.list[len(as.list)-maxAlerts:]
	}
	return nil
}

func (as *alertStore) save() error {
	b, err := json.Marshal(alertFile{Alerts: as.list})
	if err != nil {
		return err
	}
	return writeFileAtomic(as.path, b)
}

// raise records the alert before it is published: an alert raised while
// calaos_server is away must still be replayed when it reconnects.
func (as *alertStore) raise(al Alert, now time.Time) (Alert, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return Alert{}, err
	}
	al.ID = hex.EncodeToString(raw)
	al.TS = now.UTC().Format(time.RFC3339)
	as.list = append(as.list, al)
	if len(as.list) > maxAlerts {
		as.list = as.list[len(as.list)-maxAlerts:]
	}
	return al, as.save()
}

func (as *alertStore) ack(ids []string) (int, error) {
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	kept := as.list[:0:0]
	for _, al := range as.list {
		if !drop[al.ID] {
			kept = append(kept, al)
		}
	}
	n := len(as.list) - len(kept)
	if n == 0 {
		return 0, nil
	}
	prev := as.list
	as.list = kept
	if err := as.save(); err != nil {
		as.list = prev
		return 0, err
	}
	return n, nil
}

func (as *alertStore) pending() []Alert {
	return append([]Alert(nil), as.list...)
}
