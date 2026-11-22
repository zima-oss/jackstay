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
- A finding carries `ordinary_path`: the ordinary work by which an unusual setup arises by accident,
  empty when none does. An `unusual` finding with it empty is rare, however severe: deferred, never
  blocking. The reviewer's prompt says so.
- A transient read or write error of the local disk during an ordinary call is a rare hardware fault,
  whatever its path; the code need only pass the error to its caller, never taken as an outcome. A
  process killed between two writes (a session stopped, Ctrl-C, sleep, a reboot) is ordinary: a
  finding when, after restart and recovery, the state stays wrong or a message is lost or doubled.
- The session checks every rating, the path included, before it calls a finding a blocker or asks
  for a round past the limit; the next step `jst review` prints for a blocking round says so, and so
  does the agents' guidance. W-019 is unchanged.

## Consequences

Fewer rounds are spent on hand-built setups, and they stop holding a change at the round limit. A
reviewer that leaves the path out for a real accident defers a finding that should block; the
session, which reads every finding before landing, raises it again. Rounds made before this rule
have no paths: their unusual and severe findings count as rare when read again.
