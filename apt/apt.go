package apt

// #cgo pkg-config: apt-pkg
// #include "apt.h"
import (
	"C"
)

type Pkg struct {
	Name           string
	VersionCurrent string
	VersionNew     string
}

func init() {
	C.aptInit()
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
