# W-090 — Landing in 0.6.0: one declarative `jst land` behind a hosting adapter

Date: 2026-11-20.

## Context

0.5.x lands a change in two half-flows, both written directly against GitHub's API. `jst gh
pr-land` merges but reads no local evidence (gate, review). The unreleased `jst queue` checks local
evidence but never merges, and its pull request mode needs an approval by someone other than the
author, so a solo maintainer cannot use it. The unreleased `jst receipts attest-ci`/`verify-ci`
sign CI receipts with jackstay's own key and have no producer or consumer. GitLab and Bitbucket are
planned for later releases. The maintainer ruled on the shape of 0.6.0 (this record keeps the part
about landing) and then on the landing design's open questions (W-091).

## Decision

- **One command.** `jst land`, driven by a declarative `land` section in `jackstay.json`
  (schema 3), replaces `jst gh pr-land`, its `jst gh` neighbours and the queue commands. They are
  removed in 0.6.0 with no aliases.
- **Adapters.** The engine knows no host; a hosting adapter declares its capabilities and the
  engine degrades by a fixed table. GitHub ships in 0.6.0; GitLab and Bitbucket follow.
- **Two autonomy modes**, chosen explicitly at adopt and setup: by default the agent merges when
  everything is green, while `jackstay.json`, `.github/workflows/**` and `.claude/settings.json`
  (and any listed paths) need a person; or the agent prepares and a person merges. A person is a
  change number typed at a terminal, or a merge on the host. Setup keeps machine defaults that adopt
  offers first.
- **Approval is jackstay's**: a clean `jst review` and the local gate on the merge tree. A review
  carries across a clean merge of the base. Publishing jackstay's evidence to the host as commit
  statuses is an optional switch, off by default.
- **Freshness** defaults to `merge-tree`: a base that moves after the final read is detected when
  the landed tree is reconciled. Host auto-merge ships only if a GitHub pilot proves that a stale
  candidate is refused.
- **Provenance** goes through GitHub attestations, verified with `gh attestation verify`;
  jackstay's own CI signing key is removed.
- **The guard** refuses `gh pr merge` and `glab mr merge` where a `land` section exists.
- **The upgrade** from 0.5.x takes three pull requests: the pin, the workflows, then schema 3.

## Consequences

The [landing design](../design/land.md) gives the policy keys, the adapter interface with its
GitHub, GitLab and Bitbucket mappings, the engine's steps, the landing record, the judging of
required checks, adopt, setup and doctor, and the test plan. Scripts that call `jst gh` move to
`jst land` and `jst checks`.
