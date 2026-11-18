# W-026 — The scope of the release features: typed changes, audience, version rules, app versions, prepare, the documented-change check

Date: 2026-11-16.

## Context

Projects released by hand or with tools that knew only `major`, `minor` and `patch`. Versions were
often not bumped or tagged, breaking changes carried no migration note, and apps need a build
number apart from the marketing version.

## Decision

The release features (`jst changes`, `jst release`) cover:

- **Change types**: breaking, feature, fix, deprecated, security, note; and an **audience**:
  consumer or internal.
- **Version rules**: the next version is computed from the entries, with a pre-1.0 rule; an
  override needs a recorded reason; 1.0 only by override; a breaking entry needs a migration note.
- **Tags are never moved**; a bad release is marked yanked.
- **Declared version files**; for apps, a marketing version apart from a monotonic build number,
  and notes for TestFlight and Sparkle exported from the entries.
- **`jst release prepare`** and a pull-request check that a change is documented.

Not in scope for now: an API-breakage check and a next-version preview in the pull request.

## Consequences

- See [release](../design/release.md) and the guide's [releases](../guide/releases.md).
