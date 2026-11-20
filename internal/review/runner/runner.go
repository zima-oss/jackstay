// Package runner defines the request and result shared by review providers.
package runner

import (
	"time"

	"github.com/zima-oss/jackstay/internal/review/access"
)

// Outcomes describe how the reviewer subprocess ended before verdict validation.
const (
	OutcomeDone     = "done"
	OutcomeTimeout  = "timeout"
	OutcomeFailed   = "failed"
	OutcomeLogin    = "login"
	OutcomeNoAnswer = "no-answer"
	OutcomeRefused  = "refused"
)

// Request is one review provider invocation. Tree is the pinned source view and Folder holds artifacts.
// Answered releases the start lock once the provider has started safely or finished.
type Request struct {
	Reviewer       string
	Command        string
	Model          string
	Effort         string
	Timeout        time.Duration
	Isolation      []string
	Tree           string
	PinnedSource   bool
	RepositoryRoot string // excludes the live checkout from provider runtime path grants
	Folder         string
	Prompt         []byte
	SchemaPath     string
	Answered       func()
	Environ        []string
	// MCPServers are the MCP servers the project gives back to this reviewer; none by default.
	MCPServers map[string]access.MCPServer
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
