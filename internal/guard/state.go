package guard

import (
	"maps"
	"slices"
)

// The bounds of one reading (docs/design/guard.md section 3). Each is a constant with a test
// that reaches it.
const (
	maxPlaces       = 6        // folders a command may be in before the place is unknown
	maxValues       = 8        // texts a variable may hold at once before it is unknown
	maxForWords     = 24       // words of a `for` list its variable is bound to, each in turn
	maxReadings     = 64       // readings of one word before it is unknown
	maxValueBytes   = 64 << 10 // the longest text a word may hold before it is unknown
	maxDepth        = 8        // scripts inside scripts (`bash -c`, `$(…)`, `eval`, functions)
	maxSteps        = 20000    // simple commands read for one call, loop passes included
	loopPasses      = 6        // times a loop's body is read again before what it writes is unknown
	maxCommandBytes = 1 << 20  // the longest command text that is read at all
)

// A value is what a variable holds: one text, one of a few texts, or unknown (nil texts).
type value struct {
	texts []string
}

func unknownValue() value { return value{} }

func certain(text string) value { return value{texts: []string{text}} }

func (held value) known() bool { return held.texts != nil }

func (held value) equal(other value) bool {
	return held.known() == other.known() && slices.Equal(held.texts, other.texts)
}

// oneOf makes a value of the texts a variable may hold; more than maxValues is unknown.
func oneOf(found []string) value {
	if found == nil {
		return unknownValue()
	}
	sorted := slices.Clone(found)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	if len(sorted) == 0 || len(sorted) > maxValues {
		return unknownValue()
	}
	return value{texts: sorted}
}

// either is what a variable holds when it holds `first` on one path and `second` on another.
func either(first, second value, widen bool) value {
	if first.equal(second) {
		return first
	}
	if !first.known() || !second.known() || widen {
		return unknownValue()
	}
	return oneOf(append(slices.Clone(first.texts), second.texts...))
}

// A state is what is known at one point of a line, on every path that reaches it. It is never
// changed once made: every step makes a new one (with). A nil *state is a point nothing reaches.
type state struct {
	places   []string           // the folders the command may be in; nil: unknown
	vars     map[string]value   // what the line set; a name absent was not set by the line
	exported map[string]value   // what the line may have exported
	sure     map[string]bool    // exported on every path
	links    map[string]*string // links the line made: where each leads (nil: cannot be named)
	errexit  bool               // `set -e` holds (only ever in a shell process of its own)
	blind    bool               // something unread ran here: nothing outside the line is known
	globsOff bool               // `setopt`, `set -f`: how zsh globs from here is not known
}

func startState(folder string) *state {
	return &state{places: []string{folder}, vars: map[string]value{}, exported: map[string]value{},
		sure: map[string]bool{}, links: map[string]*string{}}
}

func (current *state) clone() *state {
	copied := *current
	copied.places = slices.Clone(current.places)
	copied.vars = maps.Clone(current.vars)
	copied.exported = maps.Clone(current.exported)
	copied.sure = maps.Clone(current.sure)
	copied.links = maps.Clone(current.links)
	return &copied
}

func (current *state) setVar(name string, held value) *state {
	next := current.clone()
	next.vars[name] = held
	return next
}

// placesOf makes the place set from what was found: unknown when one is unknown or there are too many.
func placesOf(found []string) []string {
	if found == nil {
		return nil
	}
	sorted := slices.Clone(found)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	if len(sorted) == 0 || len(sorted) > maxPlaces {
		return nil
	}
	return sorted
}

// forget is the state after something ran in this shell that nobody can read: no folder and no
// variable is known, nor the value of anything exported.
func (current *state) forget() *state {
	next := current.clone()
	next.places, next.blind = nil, true
	for name := range next.vars {
		next.vars[name] = unknownValue()
	}
	for name := range next.exported {
		next.exported[name] = unknownValue()
	}
	return next
}

// join is what holds when either of two states may be the case. With widen (a loop read again),
// a variable that differs is unknown rather than one of the two.
func join(first, second *state, widen bool) *state {
	switch {
	case first == nil:
		return second
	case second == nil, first == second:
		return first
	}
	next := &state{vars: map[string]value{}, exported: map[string]value{}, sure: map[string]bool{},
		links: map[string]*string{}}
	if first.places != nil && second.places != nil {
		next.places = placesOf(append(slices.Clone(first.places), second.places...))
	}
	for name, held := range first.vars {
		other, ok := second.vars[name]
		if !ok {
			next.vars[name] = unknownValue()
			continue
		}
		next.vars[name] = either(held, other, widen)
	}
	for name := range second.vars {
		if _, ok := first.vars[name]; !ok {
			next.vars[name] = unknownValue()
		}
	}
	maps.Copy(next.exported, first.exported)
	for name, held := range second.exported {
		if mine, ok := next.exported[name]; ok {
			next.exported[name] = either(mine, held, false)
		} else {
			next.exported[name] = held
		}
	}
	for name := range first.sure {
		if second.sure[name] {
			next.sure[name] = true
		}
	}
	for link, target := range first.links {
		other, ok := second.links[link]
		if ok && target != nil && other != nil && *target == *other {
			next.links[link] = target
		} else {
			next.links[link] = nil
		}
	}
	for link := range second.links {
		if _, ok := first.links[link]; !ok {
			next.links[link] = nil
		}
	}
	next.errexit = first.errexit && second.errexit
	next.blind = first.blind || second.blind
	next.globsOff = first.globsOff || second.globsOff
	return next
}

func joinAll(states ...*state) *state {
	var joined *state
	for _, each := range states {
		joined = join(joined, each, false)
	}
	return joined
}

func sameState(first, second *state) bool {
	if first == nil || second == nil {
		return first == second
	}
	if (first.places == nil) != (second.places == nil) || !slices.Equal(first.places, second.places) ||
		first.errexit != second.errexit || first.blind != second.blind || first.globsOff != second.globsOff {
		return false
	}
	return maps.EqualFunc(first.vars, second.vars, value.equal) &&
		maps.EqualFunc(first.exported, second.exported, value.equal) && maps.Equal(first.sure, second.sure) &&
		maps.EqualFunc(first.links, second.links, func(left, right *string) bool {
			return (left == nil) == (right == nil) && (left == nil || *left == *right)
		})
}

// unknownWrites makes unknown every variable `after` holds differently from `before`, and the
// folder when it moved: what a loop that never settles, or a trap, may have written.
func unknownWrites(before, after *state) *state {
	if before == nil || after == nil {
		return after
	}
	next := after.clone()
	for name, held := range after.vars {
		if old, ok := before.vars[name]; !ok || !old.equal(held) {
			next.vars[name] = unknownValue()
		}
	}
	if !slices.Equal(before.places, after.places) {
		next.places = nil
	}
	return next
}
