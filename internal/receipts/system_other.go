//go:build !darwin

package receipts

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// systemVersion is the system's name: only macOS has a product version.
func systemVersion() string {
	return runtime.GOOS
}

// systemBuild is the kernel's version, from uname.
func systemBuild() string {
	var names unix.Utsname
	if err := unix.Uname(&names); err == nil {
		return unix.ByteSliceToString(names.Version[:])
	}
	return ""
}
