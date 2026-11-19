package models

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestMain keeps the tests off the host's /run/calaos-rollback.lock.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "models-rollback-lock")
	if err != nil {
		panic(err)
	}
	rollbackLockPath = filepath.Join(dir, "calaos-rollback.lock")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// holdRollbackLock takes the lock the way calaos_rollback does, on its own
// open file description, so flock treats the daemon as another process.
func holdRollbackLock(t *testing.T) {
	t.Helper()
	old := rollbackLockPath
	rollbackLockPath = filepath.Join(t.TempDir(), "calaos-rollback.lock")
	f, err := os.Create(rollbackLockPath) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.Close()
		rollbackLockPath = old
	})
}

func TestRebootGuardRefusedDuringRollback(t *testing.T) {
	holdRollbackLock(t)

	ran := false
	err := RebootGuard(func() error { ran = true; return nil })
	if !errors.Is(err, ErrRollbackInProgress) || ran {
		t.Fatalf("err %v, fn ran %v", err, ran)
	}
	if !upgradeLock.TryAcquire(1) {
		t.Fatal("a refused guard kept upgradeLock")
	}
	upgradeLock.Release(1)
}

func TestStartJobRefusedDuringRollback(t *testing.T) {
	holdRollbackLock(t)

	if _, err := StartJob(JobKindUpgradeAll, ""); !errors.Is(err, ErrRollbackInProgress) {
		t.Fatalf("StartJob: %v", err)
	}
	if !upgradeLock.TryAcquire(1) {
		t.Fatal("a refused job kept upgradeLock")
	}
	upgradeLock.Release(1)
}

// A free lock file, or none at all, changes nothing; and the probe must not
// keep the lock, since calaos_rollback takes it itself.
func TestRollbackLockFree(t *testing.T) {
	old := rollbackLockPath
	t.Cleanup(func() { rollbackLockPath = old })

	rollbackLockPath = filepath.Join(t.TempDir(), "absent.lock")
	if err := checkRollbackLock(); err != nil {
		t.Fatalf("no lock file: %v", err)
	}

	rollbackLockPath = filepath.Join(t.TempDir(), "free.lock")
	if err := os.WriteFile(rollbackLockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := RebootGuard(func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("err %v, fn ran %v", err, ran)
	}
	upgradeLock.Release(1)

	f, err := os.Open(rollbackLockPath) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the guard kept the lock: %v", err)
	}
}

// The probe runs under upgradeLock: with the upgrade lock held, a refusal says
// upgrade in progress without ever opening the rollback lock, whose path here
// cannot be opened.
func TestRollbackProbeAfterUpgradeLock(t *testing.T) {
	old := rollbackLockPath
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rollbackLockPath = filepath.Join(blocker, "lock") // ENOTDIR on open
	t.Cleanup(func() { rollbackLockPath = old })
	holdLock(t, true)

	if err := RebootGuard(func() error { return nil }); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("RebootGuard: %v", err)
	}
	if _, err := StartJob(JobKindUpgradeAll, ""); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("StartJob: %v", err)
	}
}

func TestStartJobRollbackLockFree(t *testing.T) {
	if err := os.WriteFile(rollbackLockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := StartJob(JobKindUpgradeAll, "")
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	r.Complete()
	if !upgradeLock.TryAcquire(1) {
		t.Fatal("upgradeLock still held after Complete")
	}
	upgradeLock.Release(1)
}
