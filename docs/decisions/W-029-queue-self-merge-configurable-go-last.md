# W-029 — Who may merge the release pull request is a project setting

Date: 2026-11-16.

## Context

Some projects let agents publish releases themselves; in others only a maintainer may merge the
release pull request. jackstay had to support both without hard-coding either.

## Decision

- `release.merge` in `jackstay.json` says who merges the release pull request: `owner` (only the
  project's maintainer; the pull request is a draft) or `agents` (any agent, through
  `jst gh pr-land`). The default is `owner`.
- A merge queue that lands green batches by itself is not built; whether it would merge on its own
  is planned as a project setting as well.

## Consequences

- A project that lets agents release says so explicitly in its settings.
- See [release](../design/release.md).
