package supervise

// A Closer undoes what a run set up, in the reverse order it was set up, once (implementation.md section 10:
// a LIFO closer, passed as an instance, never global).
type Closer struct {
	steps []func()
}

// Add is one more step to undo; it is undone before every step added earlier.
func (closer *Closer) Add(step func()) {
	closer.steps = append(closer.steps, step)
}

// Close undoes every step, the last added first; a second Close does nothing.
func (closer *Closer) Close() {
	for index := len(closer.steps) - 1; index >= 0; index-- {
		closer.steps[index]()
	}
	closer.steps = nil
}
