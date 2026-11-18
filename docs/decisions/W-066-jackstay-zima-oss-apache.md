# W-066 — The name jackstay: command `jst`, alias `jackstay`, `jackstay.json`, `~/.jackstay`, `JACKSTAY_*`; the zima-oss organisation; Apache-2.0

Date: 2026-11-17.

## Context

The tool's working name collided with an existing command-line tool and with names taken in the
main package registries. A public tool needed a name that is free everywhere and a short command
that does not clash with another binary on a user's path.

## Decision

- **The name is jackstay.** The command is **`jst`**, with **`jackstay`** as its alias. The hook
  binary is `jst-guard`.
- The files and variables use the full name: `jackstay.json`, `~/.jackstay`, `JACKSTAY_*`, the
  `jackstay-adopt` markers.
- The Go module is `github.com/zima-oss/jackstay`, published in the zima-oss GitHub organisation.
- **The licence is Apache-2.0.**
- The public repository starts from a fresh history.

## Consequences

- `go install github.com/zima-oss/jackstay/cmd/jst@latest` works from the public module path.
- See the guide's [install](../guide/install.md).
