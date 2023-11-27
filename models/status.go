package models

import (
	"sync"

	"github.com/calaos/calaos-container/models/structs"
)

type SafeStatus struct {
	sync.Mutex
	Status structs.Status
}

func (s *SafeStatus) SetStatus(st structs.Status) {
	s.Lock()
	defer s.Unlock()

	s.Status = st
}

func (s *SafeStatus) GetStatus() structs.Status {
	s.Lock()
	defer s.Unlock()

	return s.Status
}

var (
	upgradeStatus SafeStatus
)

func resetStatus() {
	upgradeStatus.SetStatus(structs.Status{
		Status: "idle",
	})
}

func UpdateStatus() (*structs.Status, error) {
	st := upgradeStatus.GetStatus()
	return &st, nil
}
