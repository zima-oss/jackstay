package inventory

import "time"

// Retry calls work until it succeeds, at most attempts times, waiting pause between calls; it
// returns the last error when every call failed.
func Retry(attempts int, pause time.Duration, work func() error) error {
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if last = work(); last == nil {
			return nil
		}
		time.Sleep(pause)
	}
	return nil
}
