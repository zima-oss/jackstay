# W-034 — The release pull request carries a request; the writing GitHub App commits, tags and publishes the release

Date: 2026-11-16.

## Context

A release pull request that carries the version bump and changelog can land out of date when
another pull request reaches the base first, so the release would not match what was approved.

## Decision

- The release pull request changes one file, the request: the next version and the exact list of
  entries (blob for blob). Its description shows the release as it would be written.
- On each push to the base, the workflow compares a landed request with what the base holds. When
  they agree exactly, it commits the prepared release to the base with the writing GitHub App, tags
  that commit and publishes the release. Otherwise nothing is released and the pull request is
  refreshed.
- Who may merge the request is the project's setting `release.merge`
  ([W-029](W-029-queue-self-merge-configurable-go-last.md)).

## Consequences

- A release holds exactly the entries that were approved; there is no out-of-date release to repair.
- The writing App pushes one commit per release to the base, so a branch ruleset must allow it.
- See [release](../design/release.md) and the guide's [releases](../guide/releases.md).
