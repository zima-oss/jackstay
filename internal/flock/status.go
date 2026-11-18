package flock

import (
	"fmt"
	"strings"
)

// A State is who runs under one lock and who waits for it.
type State struct {
	Lock string `json:"lock"`
	Held bool   `json:"held"`
	// Holder is the holder note while the lock is held (nil when it is free).
	Holder  *string  `json:"holder"`
	Waiting []string `json:"waiting"`
}

// A Report is the shared lock's state and each older lock's (`jst lock status --json`).
type Report struct {
	State
	Older []State `json:"older"`
}

func stateOf(lock string) (State, error) {
	held, err := HeldAt(lock)
	if err != nil {
		return State{}, err
	}
	waiting, err := WaitingAt(lock)
	if err != nil {
		return State{}, err
	}
	state := State{Lock: lock, Held: held, Waiting: waiting}
	if state.Waiting == nil {
		state.Waiting = []string{}
	}
	if state.Held {
		note := HolderNote(lock)
		state.Holder = &note
	}
	return state, nil
}

// Status reads the shared lock's state and each older lock's; it changes nothing (a waiting run
// that holds nothing is in the line of the lock it waits for, which is listed too). A lock or a
// line that cannot be read is an error: it is never reported free.
func Status(lock string, older []string) (Report, error) {
	state, err := stateOf(lock)
	if err != nil {
		return Report{}, err
	}
	report := Report{State: state, Older: []State{}}
	for _, path := range older {
		state, err := stateOf(path)
		if err != nil {
			return Report{}, err
		}
		report.Older = append(report.Older, state)
	}
	return report, nil
}

// Text is the report in words.
func (report Report) Text() string {
	var text strings.Builder
	fmt.Fprintf(&text, "jst lock: %s\n", report.Lock)
	switch {
	case report.Held && report.Holder != nil && *report.Holder != "":
		fmt.Fprintf(&text, "running: %s\n", *report.Holder)
	case report.Held:
		text.WriteString("running: a run that has not written its note yet\n")
	default:
		text.WriteString("running: nobody; the lock is free\n")
	}
	fmt.Fprintf(&text, "waiting: %d", len(report.Waiting))
	if len(report.Waiting) > 0 {
		text.WriteString(", first to go first:")
	}
	text.WriteString("\n")
	for number, words := range report.Waiting {
		fmt.Fprintf(&text, "  %d. %s\n", number+1, words)
	}
	for _, older := range report.Older {
		fmt.Fprintf(&text, "older lock %s: ", older.Lock)
		switch {
		case older.Held && older.Holder != nil && *older.Holder != "":
			fmt.Fprintf(&text, "held by %s", *older.Holder)
		case older.Held:
			text.WriteString("held by a run without a note")
		default:
			text.WriteString("free")
		}
		if len(older.Waiting) > 0 {
			fmt.Fprintf(&text, "; %d waiting", len(older.Waiting))
		}
		text.WriteString("\n")
	}
	return text.String()
}
