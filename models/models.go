package models

import (
	"sync"

	logger "github.com/calaos/calaos-container/log"
	cimg "github.com/calaos/calaos-container/models/structs"
	"golang.org/x/sync/semaphore"

	"github.com/sirupsen/logrus"
)

var (
	logging *logrus.Entry

	quitCheckUpdate chan interface{}
	wgDone          sync.WaitGroup
	muCheck         sync.Mutex
	upgradeLock     = semaphore.NewWeighted(1)

	//Stored new available versions
	NewVersions cimg.ImageMap
)

func init() {
	logging = logger.NewLogger("database")
}

// Init models
func Init() (err error) {

	quitCheckUpdate = make(chan interface{})
	go checkForUpdatesLoop()
	wgDone.Add(1)

	return
}

// Shutdown models
func Shutdown() {
	close(quitCheckUpdate)
	wgDone.Wait()
}
