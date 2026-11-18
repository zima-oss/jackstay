// Package pinned is what jst review's pinned trees and jst test's clean copies share (W-052,
// docs/design/discard-and-clean-gate.md section 3): the words that name the process owning a tree
// or a copy, whether that process still runs, and a random name. A tree or copy is removed by a
// later run only when its owner is known to be gone.
package pinned

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/procs"
)

// OwnWords name this process after a prefix: its id and its start, so a reused id is not taken
// for it.
func OwnWords(prefix string) string {
	pid := os.Getpid()
	start := int64(0)
	if row, found, err := procs.Row(pid); err == nil && found {
		start = row.StartMicros
	}
	return fmt.Sprintf("%s%d %d", prefix, pid, start)
}

// Alive says whether words OwnWords wrote name a process still running (the same id and start).
// Words that cannot be read, or a process list that cannot be read, count as alive.
func Alive(prefix, words string) bool {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(words), prefix))
	if !strings.HasPrefix(strings.TrimSpace(words), prefix) || len(fields) != 2 {
		return true
	}
	pid, pidErr := strconv.Atoi(fields[0])
	start, startErr := strconv.ParseInt(fields[1], 10, 64)
	if pidErr != nil || startErr != nil {
		return true
	}
	row, found, err := procs.Row(pid)
	if err != nil {
		return true
	}
	return found && (start == 0 || row.StartMicros == start)
}

// RandomName is eight random hex digits.
func RandomName() string {
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	return hex.EncodeToString(random)
}
