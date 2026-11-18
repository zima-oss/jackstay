package telemetry

import (
	"bytes"
	"os"

	"go.opentelemetry.io/otel/attribute"
)

// callerLimit is the most of a caller's arguments recorded.
const callerLimit = 1024

// splitArgs reads NUL-separated arguments (count of them; -1: all), at most callerLimit bytes.
func splitArgs(raw []byte, count int) []string {
	var found []string
	total := 0
	for len(raw) > 0 && (count < 0 || len(found) < count) && total < callerLimit {
		end := bytes.IndexByte(raw, 0)
		if end < 0 {
			end = len(raw)
		}
		word := string(raw[:end])
		if total+len(word) > callerLimit {
			word = word[:callerLimit-total]
		}
		found = append(found, word)
		total += len(word)
		if end == len(raw) {
			break
		}
		raw = raw[end+1:]
	}
	return found
}

// NoteCaller puts the parent process's arguments on the command span (section 2: the caller).
func NoteCaller() {
	if argv := CallerArgv(os.Getppid()); len(argv) > 0 {
		Annotate(attribute.StringSlice(AttrCallerArgv, argv))
	}
}
