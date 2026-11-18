package telemetry

import "strings"

// Kind is how the exporter treats an attribute's value (docs/design/observability.md, section 7).
type Kind int

const (
	// Plain is a value jackstay chose from its own vocabulary (an outcome, a count, a rule name, a path
	// of jackstay's own store): written as it is.
	Plain Kind = iota + 1
	// Identity is an id or a digest jackstay or its neighbours generated: never masked, so two of them
	// stay distinct.
	Identity
	// Command is a command text or an argument list: reduced word by word (secrets and known
	// content removed; in `skeleton` mode, everything but its structure).
	Command
	// FreeText is a message: masked by the shape of secrets and cut to 4 KiB.
	FreeText
)

// Attribute names jackstay writes. Every name used on a span, an event or the resource must be here;
// an undeclared name is a programming error (KindOf reports it, and a test walks every path).
const (
	AttrCommand        = "jackstay.command"
	AttrArgs           = "jackstay.args"
	AttrCwd            = "jackstay.cwd"
	AttrWorktree       = "jackstay.worktree"
	AttrBranch         = "jackstay.branch"
	AttrHead           = "jackstay.head"
	AttrExitCode       = "jackstay.exit_code"
	AttrOutcome        = "jackstay.outcome"
	AttrSignal         = "jackstay.signal"
	AttrStoppedBy      = "jackstay.stopped_by"
	AttrKilled         = "jackstay.killed"
	AttrChildArgv      = "jackstay.child.argv"
	AttrChildPID       = "jackstay.child.pid"
	AttrCallerArgv     = "jackstay.caller.argv"
	AttrProject        = "jackstay.project"
	AttrBuildCommit    = "jackstay.build.commit"
	AttrTraceparentBad = "jackstay.traceparent.invalid"
	AttrTruncated      = "jackstay.truncated"
	AttrReason         = "jackstay.reason"
	AttrGitCalls       = "jackstay.git.calls"
	AttrGitSeconds     = "jackstay.git.seconds"

	AttrClaudeSession    = "jackstay.claude.session_id"
	AttrClaudeToolUse    = "jackstay.claude.tool_use_id"
	AttrClaudePrompt     = "jackstay.claude.prompt_id"
	AttrClaudeAgentID    = "jackstay.claude.agent_id"
	AttrClaudeAgentType  = "jackstay.claude.agent_type"
	AttrClaudeTranscript = "jackstay.claude.transcript"

	AttrCodexThread     = "jackstay.codex.thread_id"
	AttrCodexTurnIndex  = "jackstay.codex.turn.index"
	AttrCodexTurnFailed = "jackstay.codex.turn.failed"

	AttrReviewTask        = "jackstay.review.task"
	AttrReviewRound       = "jackstay.review.round"
	AttrReviewAttempt     = "jackstay.review.attempt"
	AttrReviewStore       = "jackstay.review.store"
	AttrReviewOutcome     = "jackstay.review.outcome"
	AttrReviewDisposition = "jackstay.review.disposition"
	AttrReviewReviewer    = "jackstay.review.reviewer"
	// AttrReviewFindings prefixes a count of open findings by severity (`jackstay.review.findings.blocker`).
	AttrReviewFindings = "jackstay.review.findings."

	AttrTestScope   = "jackstay.test.scope"
	AttrTestKey     = "jackstay.test.key"
	AttrTestHead    = "jackstay.test.head"
	AttrTestRunID   = "jackstay.test.run_id"
	AttrTestOutput  = "jackstay.test.output"
	AttrTestOutcome = "jackstay.test.outcome"
	AttrTestCount   = "jackstay.test.count"
	AttrTestStep    = "jackstay.test.step"

	AttrBgID       = "jackstay.bg.id"
	AttrBgName     = "jackstay.bg.name"
	AttrBgDeadline = "jackstay.bg.deadline"
	AttrBgGroup    = "jackstay.bg.group"
	AttrBgFiles    = "jackstay.bg.files"
	AttrBgGone     = "jackstay.bg.gone"
	AttrBgLooked   = "jackstay.bg.looked"

	AttrLockName      = "jackstay.lock.name"
	AttrLockPath      = "jackstay.lock.path"
	AttrLockHolderPID = "jackstay.lock.holder_pid"
	AttrLockQueued    = "jackstay.lock.queued"
	AttrWaitGaveUp    = "jackstay.wait.gave_up"

	AttrGuardOutcome     = "jackstay.guard.outcome"
	AttrGuardRules       = "jackstay.guard.rules"
	AttrGuardWarnings    = "jackstay.guard.warnings"
	AttrGuardCommand     = "jackstay.guard.command"
	AttrGuardCommandID   = "jackstay.guard.command_sha256"
	AttrGuardTool        = "jackstay.guard.tool"
	AttrGuardMillis      = "jackstay.guard.answer_ms"
	AttrGuardConfigError = "jackstay.guard.config_error"

	// Stable semantic conventions (semconv v1.45.0).
	AttrServiceName      = "service.name"
	AttrServiceVersion   = "service.version"
	AttrProcessPID       = "process.pid"
	AttrProcessParentPID = "process.parent_pid"
	AttrErrorType        = "error.type"
	AttrExceptionType    = "exception.type"
	AttrExceptionMessage = "exception.message"
	AttrExceptionStack   = "exception.stacktrace"
)

// Event names (section 2).
const (
	EventGuardDecision    = "jackstay.guard.decision"
	EventTestVerdict      = "jackstay.test.verdict"
	EventReviewVerdict    = "jackstay.review.verdict"
	EventCodexTurn        = "jackstay.codex.turn"
	EventBgStarted        = "jackstay.bg.started"
	EventBgEnded          = "jackstay.bg.ended"
	EventBgStopped        = "jackstay.bg.stopped"
	EventBgRequestOutside = "jackstay.bg.requested_outside"
	EventBgHeld           = "jackstay.bg.held"
	EventBgCleaned        = "jackstay.bg.cleaned"
	EventLockAcquired     = "jackstay.lock.acquired"
	EventException        = "exception"
)

var kinds = map[string]Kind{
	AttrCommand: Plain, AttrArgs: Command, AttrCwd: Plain, AttrWorktree: Plain, AttrBranch: Plain,
	AttrHead: Identity, AttrExitCode: Plain, AttrOutcome: Plain, AttrSignal: Plain, AttrStoppedBy: Plain,
	AttrKilled: Plain, AttrChildArgv: Command, AttrChildPID: Identity, AttrCallerArgv: Command,
	AttrProject: Plain, AttrBuildCommit: Identity, AttrTraceparentBad: Plain, AttrTruncated: Plain,
	AttrReason: FreeText, AttrGitCalls: Plain, AttrGitSeconds: Plain,
	AttrClaudeSession: Identity, AttrClaudeToolUse: Identity, AttrClaudePrompt: Identity,
	AttrClaudeAgentID: Identity, AttrClaudeAgentType: Plain, AttrClaudeTranscript: Plain,
	AttrCodexThread: Identity, AttrCodexTurnIndex: Plain, AttrCodexTurnFailed: Plain,
	AttrReviewTask: Identity, AttrReviewRound: Identity, AttrReviewAttempt: Identity, AttrReviewStore: Plain,
	AttrReviewOutcome: Plain, AttrReviewDisposition: Plain, AttrReviewReviewer: Plain,
	AttrTestScope: Plain, AttrTestKey: Identity, AttrTestHead: Identity, AttrTestRunID: Identity,
	AttrTestOutput: Plain, AttrTestOutcome: Plain, AttrTestCount: Plain, AttrTestStep: Plain,
	AttrBgID: Identity, AttrBgName: Plain, AttrBgDeadline: Plain, AttrBgGroup: Plain, AttrBgFiles: Plain, AttrBgGone: Plain, AttrBgLooked: Plain,
	AttrLockName: Plain, AttrLockPath: Plain, AttrLockHolderPID: Identity, AttrLockQueued: Plain,
	AttrWaitGaveUp:   Plain,
	AttrGuardOutcome: Plain, AttrGuardRules: Plain, AttrGuardWarnings: Plain, AttrGuardCommand: Command,
	AttrGuardCommandID: Identity, AttrGuardTool: Plain, AttrGuardMillis: Plain, AttrGuardConfigError: Plain,
	AttrServiceName: Plain, AttrServiceVersion: Plain, AttrProcessPID: Identity, AttrProcessParentPID: Identity,
	AttrErrorType: Plain, AttrExceptionType: Plain, AttrExceptionMessage: FreeText, AttrExceptionStack: FreeText,
}

// prefixKinds are names declared by their prefix (one per severity, one per GenAI name).
var prefixKinds = []struct {
	prefix string
	kind   Kind
}{
	{AttrReviewFindings, Plain},
}

// Declare adds names a package of jackstay declares for itself (the GenAI adapter's). Called from init.
func Declare(names map[string]Kind) {
	for name, kind := range names {
		kinds[name] = kind
	}
}

// KindOf is how a name's value is treated, and false for a name nobody declared.
func KindOf(name string) (Kind, bool) {
	if kind, ok := kinds[name]; ok {
		return kind, true
	}
	for _, entry := range prefixKinds {
		if strings.HasPrefix(name, entry.prefix) {
			return entry.kind, true
		}
	}
	return 0, false
}
