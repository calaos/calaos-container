package models

import (
	"errors"
	"testing"
	"time"
)

// holdLock simulates a run holding upgradeLock; upgrade tells an upgrade
// from a version check.
func holdLock(t *testing.T, upgrade bool) {
	t.Helper()
	if !upgradeLock.TryAcquire(1) {
		t.Fatal("upgradeLock already held")
	}
	upgrading.Store(upgrade)
	t.Cleanup(func() {
		upgrading.Store(false)
		upgradeLock.Release(1)
	})
}

func TestRebootGuardRefusedDuringUpgrade(t *testing.T) {
	holdLock(t, true)

	ran := false
	err := RebootGuard(func() error { ran = true; return nil })
	if !errors.Is(err, ErrUpgradeInProgress) || ran {
		t.Fatalf("err %v, fn ran %v", err, ran)
	}
}

// TestRebootGuardWaitsForCheck: behind a version check the guard waits for
// the lock, and no upgrade can start meanwhile.
func TestRebootGuardWaitsForCheck(t *testing.T) {
	if !upgradeLock.TryAcquire(1) {
		t.Fatal("upgradeLock already held")
	}

	ran := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- RebootGuard(func() error { close(ran); return nil })
	}()

	time.Sleep(50 * time.Millisecond)
	select {
	case <-ran:
		t.Fatal("fn ran while the check held the lock")
	default:
	}

	upgradeLock.Release(1)
	if err := UpgradeAll(); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("UpgradeAll while the guard waits: %v", err)
	}

	if err := <-result; err != nil {
		t.Fatal(err)
	}
	<-ran
	upgradeLock.Release(1) // kept by the successful guard
}

func TestRebootGuardTimesOut(t *testing.T) {
	prev := rebootLockWait
	rebootLockWait = 20 * time.Millisecond
	t.Cleanup(func() { rebootLockWait = prev })
	holdLock(t, false)

	ran := false
	err := RebootGuard(func() error { ran = true; return nil })
	if !errors.Is(err, ErrUpgradeInProgress) || ran {
		t.Fatalf("err %v, fn ran %v", err, ran)
	}
}

func TestRebootGuardKeepsLockAfterSuccess(t *testing.T) {
	if err := RebootGuard(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upgradeLock.Release(1) })

	if err := Upgrade("anything"); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("Upgrade after a reboot: %v", err)
	}
	if err := UpgradeAll(); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("UpgradeAll after a reboot: %v", err)
	}
}

func TestRebootGuardReleasesLockOnFailure(t *testing.T) {
	boom := errors.New("boom")
	if err := RebootGuard(func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if !upgradeLock.TryAcquire(1) {
		t.Fatal("lock still held after a failed reboot")
	}
	upgradeLock.Release(1)
}
