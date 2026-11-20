# W-090 — 0.6.0 review: one command in the background, only missing answers paid again, one access profile

Date: 2026-11-20.

## Context

The 0.5.x reviewer pool capped the machine at one review, needed a person to run its worker, kept
an event store of its own and replayed a whole paid review, up to five times, after a crash. Pool
and review spoke through a marker on standard error. Reviewers were held tighter than their task
needs: Codex read only the source view, with no network and a `PATH` of `/usr/bin:/bin`. The
comparison of reviewers rested on one concurrent run per arm. The owner's words: "the reviewer is
not an enemy; efficiency comes first, everything else is insurance against accidents."

## Decision

- The pool is folded into `jst review`: `--background` (and the chat's `review.submit`) runs the
  round under `jst bg`, as many at once as `review.max_concurrent` allows, and its result reaches
  the sessions that asked as a `review.finished` chat event.
- After a crash or a failed reviewer, only the reviewers whose answer is missing run again. A
  saved answer is reused when its attempt asked the same question of the same reviewer settings:
  the round, the commits, the prompt, the schema, the review policy, and each reviewer's command,
  model, effort and isolation (not its deadline: a retry with a longer one reuses what was answered).
- Codex and Claude Code get one access profile: everything readable, read-only, with the network,
  the user's `PATH`, proxy, CA and provider settings; closed only are writes outside the reviewer's
  own temporary folder, the account's secrets and token-like variables other than the provider's
  own login, and the user's MCP servers, hooks and rules. A project can give chosen MCP servers
  back. Claude Code, like Codex, refuses to start inside an agent's sandbox (exit 6).
- The wrapper's worth is measured before the release: seeded defects, at least five sequential runs
  per arm, recall and precision, time per stage, model and cost (`just review-bench`).

## Consequences

The four parts land as separate changes, in the order above; each holds from its own change on.

`jst review pool` and the `review.pool` key are gone; there was no release with them. A review run
in the background is listed and stopped with `jst procs`. A changed model, effort, command or
isolation pays every reviewer again. Reviewers may now reach the network and read outside the view,
so instructions planted in a reviewed change could send out what a reviewer can read: the risk is
accepted for efficiency, and narrowed by denying the credentials by path and by name (the other
reviewer's login included) and leaving `.env` contents out of the patches. Both reviewers deny `.env`
files by a pattern; on Linux Codex expands it when the run starts, in and beside the checkout, to a
bounded depth.
