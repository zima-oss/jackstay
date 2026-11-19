package timeline

import (
	"io"
	"testing"
	"time"
)

func TestWindowIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	for _, args := range [][]string{
		{"--since", "0"}, {"--since", "721h"}, {"--until", "2026-11-18T08:00:00Z"},
		{"--until", "2026-10-18T08:00:00Z"}, {"--limit", "0"}, {"--limit", "1001"}, {"extra"},
	} {
		if _, err := readWindow(args, now, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	asked, err := readWindow([]string{"--until", "2026-11-17T07:30:00Z", "--since", "5m", "--limit", "2"}, now, io.Discard)
	if err != nil || asked.limit != 2 || asked.until.Sub(asked.from) != 5*time.Minute {
		t.Fatalf("window %+v: %v", asked, err)
	}
}
