package telemetry

import "testing"

// Finish gives up on a write that never ends once its bound is over: the bound's own test. The
// write is held for good (the process's lock is taken), so only the bound can return Finish; were
// it ignored, the test would hang rather than pass.
func TestFinishStopsWaitingAtItsBound(t *testing.T) {
	t.Setenv(FinishWaitVariable, "1ms")
	Reset()
	t.Cleanup(Reset)
	held := current
	held.lock.Lock()
	defer held.lock.Unlock()
	Finish(0)
	if held.finished {
		t.Fatal("the write finished while its lock was held")
	}
}

// The wait is FinishBound unless the variable names another positive duration.
func TestFinishBoundReadsTheVariable(t *testing.T) {
	for given, want := range map[string]string{"": FinishBound.String(), "90s": "1m30s", "nonsense": FinishBound.String(),
		"-1s": FinishBound.String()} {
		t.Setenv(FinishWaitVariable, given)
		if got := finishBound().String(); got != want {
			t.Fatalf("%q: %s, want %s", given, got, want)
		}
	}
}
