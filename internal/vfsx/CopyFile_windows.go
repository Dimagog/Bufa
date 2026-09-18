package vfsx

import (
	"log/slog"
	"syscall"
	"unsafe"

	c "github.com/dimagog/bufa/internal/contract"
)

// Not a const only for Unit Test's sake
var UseCopyFileOS bool = true

var copyFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("CopyFileW")

func osCopyFile(src, dst string, failIfExists bool) {
	slog.Debug("CopyFileW", "src", src, "dst", dst)

	srcPtr := c.With("utf16 src '%s'", src).Check2(syscall.UTF16PtrFromString(src))
	dstPtr := c.With("utf16 dst '%s'", dst).Check2(syscall.UTF16PtrFromString(dst))

	failIfExistsFlag := uintptr(0)
	if failIfExists {
		failIfExistsFlag = 1
	}

	res, _, err := copyFileW.Call(
		uintptr(unsafe.Pointer(srcPtr)),
		uintptr(unsafe.Pointer(dstPtr)),
		failIfExistsFlag,
	)
	if res == 0 {
		c.Assert(err != nil, "CopyFileW failed without error")
	} else {
		c.Assert(err == syscall.Errno(0), "CopyFileW succeeded with unexpected error %v", err)
		err = nil
	}
	c.Checkf(err, "CopyFileW '%s' -> '%s'", src, dst)
}
