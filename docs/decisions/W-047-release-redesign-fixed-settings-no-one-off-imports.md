# W-047 — Release features simplified: no one-off imports; the settings file changes alone; prepare only prints; a GitHub release created once

Date: 2026-11-16.

## Context

Review rounds on the release features kept finding defects in the same places (see
[W-019](W-019-repeated-findings-mean-redesign.md)). The causes were features that did too much: a
one-off importer from another tool built into the product, and release tools that could change
`jackstay.json` in the middle of a workflow.

## Decision

- **One-off imports are not part of jackstay.** Converting another tool's files is done once with a
  throwaway script, then checked with `jst changes check`.
- **`jackstay.json` is fixed settings.** No release tool changes it. A pull request that changes
  `jackstay.json` changes nothing else (`jst lint` refuses a mix) and is a maintainer's to merge.
  Every check judges by the base's `jackstay.json`.
- **The working tree is git's.** `jst release prepare` prints the release and writes nothing. A
  command that writes needs a clean tree for its paths.
- **GitHub's side is small.** A GitHub release is created once, published, on its proven tag;
  GitHub's own rule picks "latest"; a yank changes the release's title.
- **Links on managed paths** are a settings error, checked once by the release validator.

## Consequences

- See [release](../design/release.md) and the guide's [releases](../guide/releases.md).
