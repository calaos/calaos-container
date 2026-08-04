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
)

func init() {
	C.aptInit()
	C.aptRegisterProgressBridge()
}

func GetCachePackages() (plist []*Pkg) {
	arr := C.aptCacheGetArray()
	defer C.aptCacheArrayFree(arr)

	for i := 0; i < int(C.aptCacheArrayCount(arr)); i++ {
		p := (*C.Pkg)(C.aptCacheArrayGet(arr, C.int(i)))

		pkg := &Pkg{
			Name:           C.GoString(p.name),
			VersionCurrent: C.GoString(p.version_current),
			VersionNew:     C.GoString(p.version_new),
		}

		plist = append(plist, pkg)
	}

	return
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
	if cerr != nil {
		defer C.free(unsafe.Pointer(cerr))
	}

	if ret != 0 {
		if cerr != nil {
			return errors.New(C.GoString(cerr))
		}
		return errors.New("apt transaction failed")
	}

	return nil
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
