package receipts

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// systemVersion is the system's version (macOS's product version), else the system's name.
func systemVersion() string {
	if said, err := unix.Sysctl("kern.osproductversion"); err == nil && said != "" {
		return said
	}
	return runtime.GOOS
}

// systemBuild is the system's build, else the kernel's version.
func systemBuild() string {
	if said, err := unix.Sysctl("kern.osversion"); err == nil && said != "" {
		return said
	}
	var names unix.Utsname
	if err := unix.Uname(&names); err == nil {
		return unix.ByteSliceToString(names.Version[:])
	}
	return ""
}
