package models

import (
	"sync"

	logger "github.com/calaos/calaos-container/log"
	cimg "github.com/calaos/calaos-container/models/structs"
	"golang.org/x/sync/semaphore"

	"github.com/sirupsen/logrus"
)

// Concurrency model of the update subsystem:
//
//   - upgradeLock (semaphore, capacity 1) serializes the long-running
//     operations: checkForUpdates and the upgrade jobs (StartJob to
//     Complete). It is acquired with TryAcquire, so a second caller is
//     rejected with "upgrade already in progress" instead of piling up
//     behind the first one.
//   - newVersionsMu protects the newVersions map only. It is held for map
//     access alone: never across an apt-get run, a cgo call or an upgradeLock
//     acquisition. Readers go through GetNewVersions()/getNewVersion(), the
//     writer through setNewVersions().
//   - Lock ordering: upgradeLock is always taken before newVersionsMu, never
//     the other way around, so the two cannot deadlock.
//   - upgradeStatus (models/status.go) carries its own mutex and is
//     independent of both.
//
// lifecycleMu guards the Init()/Shutdown() pair (quitCheckUpdate) so that a
// double Shutdown(), or a Shutdown() after a failed Init(), is a no-op instead
// of a panic on a closed/nil channel.
var (
	logging *logrus.Entry

	lifecycleMu     sync.Mutex
	quitCheckUpdate chan interface{}
	wgDone          sync.WaitGroup
	upgradeLock     = semaphore.NewWeighted(1)

	//Stored new available versions, guarded by newVersionsMu
	newVersionsMu sync.RWMutex
	newVersions   = cimg.ImageMap{}
)

func init() {
	logging = logger.NewLogger("database")
}

// Init models
func Init() (err error) {
	// Before any route is served, so no client ever sees a job "running"
	// that nothing runs.
	loadLastJob()

	// Initialize the shared upgrade status to "idle" so /api/update/status and
	// the /api/events snapshot report a well-defined state before any upgrade.
	resetStatus()

	// Validate the check interval before starting anything: main.go propagates
	// this error and exits with a clear message, instead of the goroutine
	// calling Fatalf and bypassing graceful shutdown.
	updateTime, err := parseUpdateTime()
	if err != nil {
		return err
	}

	lifecycleMu.Lock()
	quit := make(chan interface{})
	quitCheckUpdate = quit
	lifecycleMu.Unlock()

	// Add(1) BEFORE spawning the goroutine: its deferred Done() could otherwise
	// run first and panic the WaitGroup with a negative counter.
	wgDone.Add(1)
	go checkForUpdatesLoop(updateTime, quit)

	return nil
}

// Shutdown models. It is safe to call more than once, and safe to call when
// Init() failed before starting the goroutine.
func Shutdown() {
	lifecycleMu.Lock()
	if quitCheckUpdate != nil {
		close(quitCheckUpdate)
		quitCheckUpdate = nil
	}
	lifecycleMu.Unlock()

	wgDone.Wait()

	// apt cannot be cancelled: give a running job the chance to end before
	// systemd's stop timeout.
	jobsWG.Wait()
}
