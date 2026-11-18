# W-075 — jackstay is released by the public repository's CI with GoReleaser; no release harness in the code

Date: 2026-11-18.

## Context

jackstay used to carry its own release machinery in the code: scripts and commands that served only
the project's own releases. That is harness, not product. Moving it to a private module would break
`go install` and clones for everyone else.

## Decision

- **No release harness in the code.** The project's own publishing and release-signing commands are
  removed. `jst release` (a product feature for users' projects) and `jst install`'s local signing
  ([W-070](W-070-sign-jst-at-install.md)) stay.
- **The release workflow.** `.goreleaser.yaml` and `.github/workflows/release.yml` run on a pushed
  `v*` tag, on GitHub-hosted runners. They build every platform (darwin/amd64, darwin/arm64,
  linux/amd64, linux/arm64), sign and notarise the macOS binaries with GoReleaser's `notarize.macos`,
  and publish checksums and the licence files.
- **The keys** (the Developer ID certificate and an App Store Connect API key) live only as secrets
  of a protected GitHub environment `release`, which requires a maintainer's approval and allows only
  `v*` tags.
- **One identifier.** Release binaries and `jst install`'s local signature both use the identifier
  `jst` with the same Developer ID team, so one macOS or firewall rule covers `jst` however it was
  installed.

## Consequences

- A release is a tag ([W-020](W-020-version-tags.md)) plus a maintainer's approval of the workflow.
- See [the specification](../design/implementation.md) section 6 and the guide's
  [install](../guide/install.md).
