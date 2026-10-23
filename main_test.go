package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	logger "github.com/calaos/calaos-container/log"
)

func TestMain(m *testing.M) {
	logging = logger.NewLogger("calaos-container")
	os.Exit(m.Run())
}

func TestWaitAndStopSelfUpgradeExitsCleanly(t *testing.T) {
	restart := make(chan struct{})
	close(restart)
	stopped := 0

	err := waitAndStop(make(chan os.Signal), make(chan error), restart, func() { stopped++ })
	if err != nil || stopped != 1 {
		t.Fatalf("err %v, stop called %d times; want a clean stop, exit status 0", err, stopped)
	}
}

func TestWaitAndStopListenerFailure(t *testing.T) {
	boom := errors.New("boom")
	listenErr := make(chan error, 1)
	listenErr <- boom
	stopped := 0

	err := waitAndStop(make(chan os.Signal), listenErr, make(chan struct{}), func() { stopped++ })
	if !errors.Is(err, boom) || stopped != 1 {
		t.Fatalf("err %v, stop called %d times", err, stopped)
	}
}

func TestWaitAndStopSignal(t *testing.T) {
	sig := make(chan os.Signal, 1)
	sig <- syscall.SIGTERM
	done := make(chan error, 1)
	go func() { done <- waitAndStop(sig, make(chan error), make(chan struct{}), func() {}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitAndStop ignored SIGTERM")
	}
}
