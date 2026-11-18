package layout

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Activation makes a version folder live (docs/design/implementation.md section 7.2, step 4). Every decision is read from the
// prefix as it is (Read), so a stop at any step is finished by running it again.
type Activation struct {
	Prefix string
	// AfterStep hears each change once it is made; an error stops the activation there (a test's
	// stop after a rename). Steps: "activate", "launcher <name>".
	AfterStep func(step string) error
}

// StepError is a change that failed half-way: the prefix is consistent, and running the same
// command again finishes it.
type StepError struct {
	Step string
	Err  error
}

func (problem *StepError) Error() string {
	return fmt.Sprintf("%s: %v (each launcher still runs one whole version; run the same command again to finish)", problem.Step, problem.Err)
}

func (problem *StepError) Unwrap() error { return problem.Err }

// ErrStopped is a test's stop after a step.
var ErrStopped = errors.New("stopped after the step (a test)")

func (activation Activation) step(name string, change func() error) error {
	if err := change(); err != nil {
		return &StepError{Step: name, Err: err}
	}
	if activation.AfterStep != nil {
		return activation.AfterStep(name)
	}
	return nil
}

// Activate makes the version folder at path live: lib/jackstay in one rename, and each launcher
// its fixed link. On a fresh prefix lib/jackstay comes first, so no launcher ever names nothing;
// every launcher runs whatever lib/jackstay names, so the switch itself is the one rename.
func (activation Activation) Activate(path string) error {
	state, err := Read(activation.Prefix)
	if err != nil {
		return err
	}
	if refusal := state.Refusal(); refusal != nil {
		return refusal
	}
	if state.Live == nil || state.Live.Name != filepath.Base(path) {
		if err := activation.step("activate", func() error { return ReplaceLink(Link(activation.Prefix), filepath.Base(path)) }); err != nil {
			return err
		}
	}
	for _, launcher := range state.Launchers {
		if launcher.Kind == Fixed {
			continue
		}
		name := launcher.Name
		if err := activation.step("launcher "+name, func() error { return ReplaceLink(launcher.Path, FixedTarget(name)) }); err != nil {
			return err
		}
	}
	return nil
}
