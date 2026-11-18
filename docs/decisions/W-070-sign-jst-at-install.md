# W-070 — `jst install` signs the binary with a machine-configured identity and the identifier `jst`; `jst doctor` checks it

Date: 2026-11-17.

## Context

A binary built by Go carries only an ad-hoc signature with the identifier `a.out`, and each
`jst install` puts it at a new versioned path. macOS and firewalls such as LuLu tie their rules to a
signing identity and identifier, so they kept asking about `jst` after every install.

## Decision

- `jst install` signs the binary in the new version folder before it becomes live:
  `codesign --sign <identity> --identifier jst --options runtime --timestamp --force`, then verifies
  the signature and reads back the identifier and authority. Any failure refuses the install
  (exit 2) and nothing live changes.
- The identifier is `jst`, the same as the release archives carry
  ([W-075](W-075-release-through-public-ci.md)), so one firewall rule covers `jst` however it was
  installed.
- The identity is a setting of the machine, not of a project: one line in
  `~/.jackstay/.all/codesign-identity`. Without it, and always on Linux, nothing is signed.
- An install of the version already live re-signs it safely when its signature is not the
  configured identity's.
- `jst doctor` reports a configured identity that is missing, expired or revoked, and a live binary
  not signed by it.
- Development builds are not signed.

## Consequences

- See [the specification](../design/implementation.md) sections 7.4 and 7.7, and the guide's
  [install](../guide/install.md).
