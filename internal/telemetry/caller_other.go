//go:build !darwin

package telemetry

import (
	"os"
	"strconv"
)

// CallerArgv is a process's arguments, from /proc, at most callerLimit bytes; nil when they
// cannot be read.
func CallerArgv(pid int) []string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil
	}
	return splitArgs(raw, -1)
}
