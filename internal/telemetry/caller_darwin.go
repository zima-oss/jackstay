package telemetry

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// CallerArgv is a process's arguments, from the kernel (kern.procargs2), at most callerLimit
// bytes; nil when they cannot be read.
func CallerArgv(pid int) []string {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	// The executable's path, then NUL padding, then argv.
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return nil
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	return splitArgs(rest, count)
}
