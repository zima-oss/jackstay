# W-087 — Reviewers read pinned Git objects on demand

Date: 2026-11-20.

## Context

Claude Code received patches in its initial request, which set a size ceiling for reviewable
ranges. Codex received a detached checkout and could run local Git commands. Both providers need
whole-file context, search and focused access to the full range and the current delta.

## Decision

- Each attempt creates one temporary view of the tracked files at the base, head and optional
  previous revision. It includes changed-path indexes, local patches and optional review context.
- The initial request contains instructions and references to those files. Each reviewer reads the
  parts it needs with its native tools. Claude Code keeps `Read`, `Glob` and `Grep`. Codex keeps
  read-only shell navigation and search in a scoped permission profile.
- Git metadata and checkout filters are excluded. A symlink is a file containing the target text.
  A submodule is represented by its recorded object ID. Both providers use the same view.
- The attempt removes the view after the providers finish, before publishing a successful round.

## Consequences

Review range size affects local disk and the amount reviewers choose to read. It no longer
sets the size of the initial request. The chosen model receives excerpts returned by its tools.
Codex needs a CLI that supports scoped permission profiles. See the
[review design](../design/review.md#31-the-tree-is-pinned) for the source layout and adapter
limits.
