# W-094 — An unusual and severe finding blocks only with an ordinary-work path

Date: 2026-11-20.

## Context

W-048 rates each review finding on two axes, and a finding that is `unusual` but `severe` counts as
realistic, so it can block. In practice reviewers rated many hand-built setups and deliberate misuse
that way (a repository replaced in place under a running session, a folder pruned and reused while a
notification waits), and those findings held changes past the round limit. jackstay guards against
accidents, not an adversary (W-017): a setup no ordinary work produces is rare, whatever it would cost.

## Decision

- `likelihood: unusual` means a hand-built setup or deliberate misuse.
- A finding rated `unusual` and `severe` blocks only when it names the ordinary-work path by which
  that setup arises by accident. Without that path it is rated `mild` for blocking: rare, deferred to
  the project's later-findings card.
- The reviewer's prompt states both rules. The session checks every rating against them before it
  calls a finding a blocker or asks for a round past the limit; the next step `jst review` prints for
  a blocking round says so.
- The finding's form does not change: the path is carried in its detail, and the predicate that
  computes blocking stays W-048's. W-019 is unchanged.

## Consequences

Fewer rounds are spent on hand-built setups. A session that keeps a finding as a blocker can say
which ordinary work leads to it. A reviewer that leaves out the path for a real accident costs a
re-rating by the session, not a missed blocker, since the session reads the detail before deciding.
