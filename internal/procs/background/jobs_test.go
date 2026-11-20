package background

import (
	"testing"
	"time"
)

func TestJobsReportsStatesWithoutRemovingOrStopping(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 610, 611, "build", "session-a", time.Minute)
	h.listed(t, 620, 621, "", "", time.Minute)
	jobs := jobsOf(h.look.host, h.place)
	if len(jobs) != 2 || jobs[0].ID != "1800000000-610" || jobs[0].Name != "build" || jobs[0].State != "running" ||
		jobs[0].Cwd != "/work" || jobs[1].State != stateGone {
		t.Fatalf("jobs %+v", jobs)
	}
	if !h.place.listed("1800000000-620") {
		t.Fatal("reading the jobs removed a gone entry")
	}
	if got := eventPairs(t, h.place); len(got) != 0 {
		t.Fatalf("reading the jobs logged %v", got)
	}
}
