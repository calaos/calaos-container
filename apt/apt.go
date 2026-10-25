// Package apt provides CGO bindings to libapt-pkg through the C++ layer in
// apt/ (also built as libcalaos-apt.so by meson). It exposes the list of
// upgradable packages plus full upgrade/install transactions with progress
// reporting.
package apt

/*
#cgo pkg-config: apt-pkg
#cgo CXXFLAGS: -std=c++17
#include <stdlib.h>
#include "apt.h"

// goAptProgress is implemented in Go (callbacks.go, //export goAptProgress).
// It is declared (not defined) here so the static bridge below can call it.
// Note: C function definitions live in this preamble on purpose — the cgo
// rule only forbids them in the preamble of the file containing //export.
extern void goAptProgress(char *stage, char *pkg, int pkgPercent,
                          int totalPercent, char *msg);

// progressBridge adapts the C callback signature to the exported Go symbol.
// It is registered by function pointer (aptSetProgressCallback), so that
// libcalaos-apt.so never references a Go symbol and remains linkable alone.
static void progressBridge(const char *stage, const char *pkg,
                           int pkg_percent, int total_percent, const char *msg)
{
	goAptProgress((char *)stage, (char *)pkg, pkg_percent, total_percent,
	              (char *)msg);
}

static void aptRegisterProgressBridge()
{
	aptSetProgressCallback(progressBridge);
}
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

type Pkg struct {
	Name           string
	VersionCurrent string
	VersionNew     string
	Held           bool // on hold: apt-get dist-upgrade keeps it back
}

// Progress is one progress event of an apt transaction, as defined by the
// CGO -> Go contract (see docs/backlog/apt-progress/README.md).
type Progress struct {
	Stage        string // pending|download|unpack|configure|install|done|error
	Pkg          string
	PkgPercent   int // 0-100, -1 = unknown
	TotalPercent int // 0-100, monotonic within a transaction
	Msg          string
}

var (
	// transactionMu serializes apt transactions: the C++ layer keeps a single
	// global progress state/callback, and the dpkg + archives locks make
	// concurrent transactions impossible anyway.
	transactionMu sync.Mutex

	// progressMu guards progressCb, the callback of the transaction currently
	// running (nil when idle).
	progressMu sync.Mutex
	progressCb func(Progress)

	// initErr records a failure of the one-shot C aptInit(). init() cannot
	// return an error and aborting the whole daemon at package load would be
	// worse, so the failure is kept here and returned by every entry point:
	// nothing works if libapt-pkg could not be initialized, and the caller
	// gets an explicit reason instead of an empty package list.
	initErr error
)

func init() {
	var cerr *C.char
	if C.aptInit(&cerr) != 0 {
		initErr = errors.New(cErrString(cerr, "apt initialization failed"))
	} else {
		freeCErr(cerr)
	}

	C.aptRegisterProgressBridge()
}

// cErrString converts an error message allocated by the C++ layer (strdup) to
// a Go string AND frees it — the C ABI hands over ownership of that buffer.
// fallback is used when C reported a failure without a message.
func cErrString(cerr *C.char, fallback string) string {
	if cerr == nil {
		return fallback
	}

	s := C.GoString(cerr)
	C.free(unsafe.Pointer(cerr))

	if s == "" {
		return fallback
	}

	return s
}

// freeCErr releases a C error buffer whose content is not needed. Success
// paths must not leak a message the C layer may have set anyway.
func freeCErr(cerr *C.char) {
	if cerr != nil {
		C.free(unsafe.Pointer(cerr))
	}
}

// GetCachePackages returns the upgradable packages known to the local apt
// cache (an empty slice when everything is up to date).
//
// It returns an error whenever the C++ layer could not produce a trustworthy
// list — apt/dpkg lock held by another process, broken package state,
// unsolvable dist-upgrade, libapt-pkg not initialized. Callers must not treat
// a failure as "no updates available".
//
// It holds transactionMu because libapt-pkg's globals (_config, _error,
// _system) and the dpkg/archives locks are shared with UpgradeAll and
// InstallPackage: draining the global error stack cannot be allowed to race
// with a transaction reporting its own failure. No progress callback ever
// calls back into this function, so the lock cannot deadlock a transaction.
func GetCachePackages() ([]*Pkg, error) {
	transactionMu.Lock()
	defer transactionMu.Unlock()

	if initErr != nil {
		return nil, initErr
	}

	var cerr *C.char

	arr := C.aptCacheGetArray(&cerr)
	if arr == nil {
		return nil, errors.New(cErrString(cerr, "failed to read the apt cache"))
	}
	defer C.aptCacheArrayFree(arr)

	// Success: no message expected, but never leak one.
	freeCErr(cerr)

	count := int(C.aptCacheArrayCount(arr))
	plist := make([]*Pkg, 0, count)

	for i := 0; i < count; i++ {
		p := (*C.Pkg)(C.aptCacheArrayGet(arr, C.int(i)))
		if p == nil {
			// Out of range: cannot happen for i < count, but the C accessor is
			// allowed to say no and dereferencing nil here would panic.
			continue
		}

		plist = append(plist, &Pkg{
			Name:           C.GoString(p.name),
			VersionCurrent: C.GoString(p.version_current),
			VersionNew:     C.GoString(p.version_new),
			Held:           p.held != 0,
		})
	}

	return plist, nil
}

// UpgradeAll runs a full dist-upgrade (download + install) of all upgradable
// packages. It blocks until the transaction finishes.
//
// cb (may be nil) receives progress events. It is invoked synchronously from
// the cgo thread while apt's download/dpkg machinery is suspended: it MUST
// return quickly and never block (hand slow work off to a channel/goroutine).
//
// The underlying C++ code fork/execs dpkg from the blocking cgo call, which
// already pins an OS thread; apt handles EINTR itself and waits on the exact
// child PID, so no runtime.LockOSThread is needed. If SIGURG preemption ever
// proves problematic in the field, run with GODEBUG=asyncpreemptoff=1.
func UpgradeAll(cb func(Progress)) error {
	return runTransaction(cb, func(errMsg **C.char) C.int {
		return C.aptUpgradeAll(errMsg)
	})
}

// InstallPackage installs (or upgrades) a single package by name, resolving
// dependencies. Same blocking/callback contract as UpgradeAll.
func InstallPackage(name string, cb func(Progress)) error {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	return runTransaction(cb, func(errMsg **C.char) C.int {
		return C.aptInstallPackage(cname, errMsg)
	})
}

// runTransaction registers cb as the active progress callback, runs the
// blocking C transaction and converts its char **errorMsg into a Go error.
func runTransaction(cb func(Progress), call func(**C.char) C.int) error {
	transactionMu.Lock()
	defer transactionMu.Unlock()

	if initErr != nil {
		return initErr
	}

	progressMu.Lock()
	progressCb = cb
	progressMu.Unlock()
	defer func() {
		progressMu.Lock()
		progressCb = nil
		progressMu.Unlock()
	}()

	var cerr *C.char

	ret := call(&cerr)
	if ret != 0 {
		return errors.New(cErrString(cerr, "apt transaction failed"))
	}

	freeCErr(cerr)

	return nil
}

// cacheArrayProbeForTest exercises the raw C accessors that GetCachePackages
// never reaches: the NULL handle, and out-of-range indices on a real handle
// (-1, count, count+1). It reports whether they all behaved (NULL result, no
// crash) — before CC-020 the bounds check was `idx > size()`, so idx == count
// called std::vector::at() out of range and threw std::out_of_range across the
// cgo boundary, which is undefined behaviour.
//
// It lives here and not in apt_test.go because cgo is not available in Go test
// files.
func cacheArrayProbeForTest() (ok bool, count int, err error) {
	// The NULL handle must be inert (count 0, no entry, free is a no-op).
	if C.aptCacheArrayCount(nil) != 0 || C.aptCacheArrayGet(nil, 0) != nil {
		return false, 0, nil
	}
	C.aptCacheArrayFree(nil)

	transactionMu.Lock()
	defer transactionMu.Unlock()

	if initErr != nil {
		return false, 0, initErr
	}

	var cerr *C.char

	arr := C.aptCacheGetArray(&cerr)
	if arr == nil {
		return false, 0, errors.New(cErrString(cerr, "failed to read the apt cache"))
	}
	defer C.aptCacheArrayFree(arr)

	freeCErr(cerr)

	count = int(C.aptCacheArrayCount(arr))

	for _, idx := range []int{-1, count, count + 1} {
		if C.aptCacheArrayGet(arr, C.int(idx)) != nil {
			return false, count, nil
		}
	}

	// ... while the last valid index must still resolve.
	if count > 0 && C.aptCacheArrayGet(arr, C.int(count-1)) == nil {
		return false, count, nil
	}

	return true, count, nil
}

// emitProgressForTest drives the C emitter (aptEmitProgress) so that tests
// can exercise the full C -> Go bridge path (progressBridge + goAptProgress),
// including the monotonic guard on TotalPercent.
func emitProgressForTest(stage, pkg string, pkgPercent, totalPercent int, msg string) {
	cStage := C.CString(stage)
	cPkg := C.CString(pkg)
	cMsg := C.CString(msg)
	defer C.free(unsafe.Pointer(cStage))
	defer C.free(unsafe.Pointer(cPkg))
	defer C.free(unsafe.Pointer(cMsg))

	C.aptEmitProgress(cStage, cPkg, C.int(pkgPercent), C.int(totalPercent), cMsg)
}

// resetProgressForTest clears the process-global monotonic guard so that
// tests do not depend on events emitted by earlier tests or -count runs.
func resetProgressForTest() {
	C.aptResetProgress()
}
