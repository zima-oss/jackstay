# W-090 — 0.6.0 stays readable by older pins, and Codex is the default reviewer again

Date: 2026-11-20.

## Context

The keys added after 0.5.11 kept `"jackstay_schema": 2`. Releases 0.5.10 and 0.5.11 refuse unknown
keys, so a project pinned to one of them failed on "unknown key `$schema`" instead of being told
it needs a newer jackstay. `jst adopt` wrote `$schema` on `main`, about 13 new keys were internal
limits nobody asked for, and `jst review` without `--reviewer` or `review.use` stopped choosing
Codex, while the AGENTS block of 0.5.10 and 0.5.11 tells agents to run plain `jst review`.

## Decision

- The keys added since 0.5.11 and `$schema` need `"jackstay_schema": 3`. With 1 or 2 the file is
  refused naming them, so an older jackstay says it needs a newer one.
- `$schema` and the schema's `$id` point at the release's tag, not `main`. `jst adopt` writes
  `$schema` and other schema-3 keys only when the pin is 0.6.0 or later.
- Internal limits are constants: the chat's sizes, quotas, retention, poll and acknowledgement
  timing, the review pool's retry and retention limits, and the timeout of a test's `requires`
  command. What users touch gets keys: `brief.max_age_hours` and `status.repos`.
- Without `--reviewer` or `review.use`, `jst review` runs Codex, and `jst review check` requires
  it. Where adopt and setup ask about review, Codex is the default choice.

## Consequences

A project moves to schema 3 in steps: the pin first, then the workflows that download it, then the
new keys. Changing a chat or pool limit is a code change. A project that relied on a Claude-only
round passing `jst review check` without `review.use` lists `claude-code` in `review.use`.
