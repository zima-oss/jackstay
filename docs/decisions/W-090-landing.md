# W-090 — Landing in 0.6.0: one `jst land` behind a hosting adapter, two autonomy modes

Date: 2026-11-20.

## Context

0.5.x lands a change in two half-flows, both written directly against GitHub's API. `jst gh
pr-land` merges; the unreleased `jst queue` checks local evidence but never merges, and needs an
approval by someone other than the author. GitLab and Bitbucket are planned for later releases.
The maintainer ruled on the shape of 0.6.0 (this record keeps the part about landing) and then on
the landing design's open questions (W-091). How thin the landing is, is
[W-092](W-092-thin-land-and-where-tests-run.md).

## Decision

- **One command.** `jst land` replaces `jst gh pr-land`, its `jst gh` neighbours and the queue
  commands, which are removed in 0.6.0 with no aliases.
- **Adapters.** The engine knows no host; a small hosting adapter maps it to one. GitHub ships in
  0.6.0; GitLab and Bitbucket follow.
- **Two autonomy modes**, chosen explicitly at adopt and setup: by default the agent merges when the
  required checks pass, while `jackstay.json`, `.github/workflows/**` and `.claude/settings.json`
  (and any listed paths) need a person; or the agent prepares and a person merges. A person is a
  change number typed at a terminal, or a merge on the host. Setup keeps the machine's answer as
  the one adopt offers first.
- **The guard** refuses `gh pr merge` and `glab mr merge`.
- **The upgrade** from 0.5.x takes three pull requests: the pin, the workflows, then schema 3.

## Consequences

The [landing design](../design/land.md) gives the policy keys, the adapter, the steps, the judging
of required checks, adopt and doctor. Scripts that call `jst gh` move to `jst land` and
`jst checks`.
