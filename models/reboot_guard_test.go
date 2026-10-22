package models

import (
	"errors"
	"testing"
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

func TestRebootGuardAllowedDuringCheck(t *testing.T) {
	holdLock(t, false)

	ran := false
	if err := RebootGuard(func() error { ran = true; return nil }); err != nil || !ran {
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
