package apt

// This file contains the //export directive: per cgo rules its preamble must
// not contain any C definition, only declarations (#include "apt.h"). The C
// shim calling goAptProgress lives in the preamble of apt.go.

// #include "apt.h"
import "C"

// goAptProgress is the cgo-exported bridge invoked by the C++ layer (through
// the progressBridge shim registered in apt.go) for every progress event of
// an apt transaction.
//
// It runs synchronously on the cgo thread executing the transaction, while
// apt's download/dpkg machinery is suspended: the registered callback must
// not block.
//
//export goAptProgress
func goAptProgress(stage, pkg *C.char, pkgPercent, totalPercent C.int, msg *C.char) {
	progressMu.Lock()
	cb := progressCb
	progressMu.Unlock()

	if cb == nil {
		return
	}

	cb(Progress{
		Stage:        C.GoString(stage),
		Pkg:          C.GoString(pkg),
		PkgPercent:   int(pkgPercent),
		TotalPercent: int(totalPercent),
		Msg:          C.GoString(msg),
	})
}
