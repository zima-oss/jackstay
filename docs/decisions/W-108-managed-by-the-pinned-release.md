# W-108 — jackstay's repository is managed by its pinned release

Date: 2026-11-23.

## Context

jackstay adopts itself: a test plans `jst adopt` on its own checkout and fails on any pending
change. The test planned with the version being built. Bumping the version to a new release made
it ask for what only that release brings (its `land` section, a workflow that downloads the
release), which cannot land before the release exists. A compiler built by itself meets the same
loop, and solves it with a stage-0 build: the tools that manage a source tree are a released
version, not the one being built.

## Decision

- jackstay's repository is managed by its pinned release (`adopt.pin`), not by the version in its
  source. The pin is declared in `jackstay.json` like a toolchain line: the release before the
  first pinned one is declared too (`v0.5.11`, the release the repository was managed by when
  this was decided). The file stays one the pinned release reads: schema 2, without `$schema`,
  until the configuration change that moves the pin to 0.6.0 raises it.
- The self-adoption test plans with the pinned version, not with `version.Version`; without a pin
  it fails. Written limit: it runs this source's adopt at that version, not the released binary,
  whose own AGENTS block may be older; that the pinned release reads the file is checked by hand
  at each pin.
- A release goes in this order: the version bump, the tag and the release, then the configuration
  change that adopts the new release and moves the pin to it.

## Consequences

- A version bump no longer fails the self-adoption test; what the new release would change in the
  repository lands after it, as it would in any adopted project.
- Declaring the pin is itself an adoption: its output (the AGENTS block's profiles item) lands with
  it, in the change a person merges.
- See [adopt.md](../design/adopt.md) section 13.
