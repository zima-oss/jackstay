package scopes

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/receipts"
)

// refusal is why a receipt may not stand in for this scope's run, (""
// when it may): a scope's receipt is used only when its words are a `receipts.commands` entry
// with `"reuse": true` (testing.md "Details of the behaviour").
func (run *leafRun) refusal() string {
	switch {
	case run.options.noReuse:
		return "--no-reuse"
	case run.getenv(receipts.NoReuseVariable) != "":
		return receipts.NoReuseVariable + " is set"
	case run.entry == nil:
		return "the command is not listed in receipts.commands"
	case run.entry["reuse"] != true:
		return "the command is not opted in (reuse is not true)"
	case textOf(run.entry["evidence"]) == "":
		return "the command has no evidence pattern in receipts.commands"
	}
	if _, written := run.receipts["env"].([]any); !written {
		return "jackstay.json does not write out receipts.env (the variables the tests read; it may be an empty list)"
	}
	branches := receipts.DefaultNoReuseBranches
	if listed, present := run.receipts["no_reuse_branches"]; present {
		branches = textsOf(listed)
	}
	git := gitcli.Git{Dir: run.root}
	branch, _ := git.Line(run.ctx, "symbolic-ref", "--short", "-q", "HEAD")
	if slices.Contains(branches, strings.TrimSpace(branch)) {
		return fmt.Sprintf("the branch is %s, where the command always runs", strings.TrimSpace(branch))
	}
	if tip := receipts.HeadIsTipOf(run.ctx, git, branches); tip != "" {
		return fmt.Sprintf("HEAD is the commit %s points at, where the command always runs", tip)
	}
	return ""
}

// lookUp stands a receipt in for the run when one exists and reuse is allowed (said and logged);
// under the lock (the run's own lookup) a repeat of an earlier pass is logged as `would-hit`
// whenever the receipt is not used, `forced` when a person asked for the run (--no-reuse,
// JACKSTAY_NO_REUSE): the measurement is made whether or not reuse is allowed.
func (run *leafRun) lookUp(key receipts.Key, underLock bool) bool {
	var receipt *receipts.Receipt
	whyNot := "the receipt store cannot be found"
	if run.storeOK {
		receipt, whyNot = run.store.Read(key.Value)
	}
	refusal := run.refusal()
	if refusal != "" || receipt == nil {
		if !underLock || run.entry == nil {
			return false
		}
		earlier, found := receipts.EarlierPass(receipts.ReadRecords(run.logPaths()), run.name, key.Value,
			receipts.MaxAge(run.receipts), run.clock.Now())
		if !found {
			return false
		}
		reason := refusal
		if reason == "" {
			reason = fmt.Sprintf("no usable receipt (%s)", whyNot)
		}
		forced := run.options.noReuse || run.getenv(receipts.NoReuseVariable) != ""
		if !run.log(receipts.EventWouldHit, run.words.argv, key.Value, map[string]any{"reason": reason,
			"saved_seconds": earlier["seconds"], "receipt": receipt != nil, "forced": forced}) {
			run.say("WARNING: the log could not be written (%s): that this run repeats an earlier pass (would-hit) is NOT in the record.",
				run.runLog.Path)
		}
		return false
	}
	where := "before-lock"
	if underLock {
		where = "under-lock"
	}
	if !run.log(receipts.EventReused, run.words.argv, key.Value, map[string]any{"saved_seconds": receipt.Seconds, "lookup": where,
		"receipt_of": receipt.Head}) {
		// Every reuse is counted, or it does not happen.
		run.say("a receipt exists, but the reuse could not be logged (%s); the command runs.", run.runLog.Path)
		return false
	}
	branch := receipt.Branch
	if branch == "" {
		branch = "none"
	}
	head := receipt.Head
	if len(head) > 12 {
		head = head[:12]
	}
	run.say("REUSED: nothing was run. An identical tree passed this scope before; this is that run's receipt.")
	run.say("  scope:   %s (%s)", run.leaf.name, shellWords(run.words.argv))
	run.say("  passed:  %s, took %s s, tests counted: %d", receipt.Ended, formatSeconds(receipt.Seconds), receipt.EvidenceCount)
	run.say("  where:   %s at %s (branch %s)", receipt.Worktree, head, branch)
	run.say("  receipt: %s", receipt.Path)
	run.say("  to run it anyway: jst test %s --no-reuse", run.leaf.name)
	run.say("outcome=reused")
	return true
}
