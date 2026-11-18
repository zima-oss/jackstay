package supervise

import "time"

// A Clock tells the time, pauses and wakes after a pause; tests give one that moves only when told
// (W-013). It is also a proctree.Clock.
type Clock interface {
	Now() time.Time
	Sleep(pause time.Duration)
	After(pause time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                             { return time.Now() }
func (realClock) Sleep(pause time.Duration)                  { time.Sleep(pause) }
func (realClock) After(pause time.Duration) <-chan time.Time { return time.After(pause) }

// RealClock is the machine's clock.
func RealClock() Clock { return realClock{} }
