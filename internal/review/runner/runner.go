// Package runner defines the request and result shared by review providers.
package runner

import "time"

// Outcomes describe how the reviewer subprocess ended before verdict validation.
const (
	OutcomeDone     = "done"
	OutcomeTimeout  = "timeout"
	OutcomeFailed   = "failed"
	OutcomeLogin    = "login"
	OutcomeNoAnswer = "no-answer"
	OutcomeRefused  = "refused"
)

// Request is one review provider invocation. Tree is the pinned checkout and Folder holds artifacts.
// Answered releases the start lock once the provider has started safely or finished.
type Request struct {
	Reviewer   string
	Command    string
	Model      string
	Effort     string
	Timeout    time.Duration
	Isolation  []string
	Tree       string
	Folder     string
	Prompt     []byte
	SchemaPath string
	Answered   func()
	Environ    []string
}

// Result describes a run and the provider-independent verdict file it wrote.
type Result struct {
	Argv       []string
	Version    string
	Started    time.Time
	Ended      time.Time
	Exit       int
	Outcome    string
	Reason     string
	AnswerPath string
}
