# W-059 — The core is runner-agnostic; an organisation's rules are a profile, data kept outside jackstay; an example profile

Date: 2026-11-17.

## Context

jackstay is a public tool used by projects with different conventions: one uses `just`, another
`make` or `npm`; organisations have their own required files, workflows and agent instructions.
None of that should be built into the core.

## Decision

- **The core does not care about a project's runner.** `jst test` scopes are commands in
  `jackstay.json`: `just`, `make`, `npm`, anything. [W-013](W-013-quality-testing-declarative.md)
  holds for all of jackstay's code.
- **Profiles.** `jst adopt` checks a project against jackstay's general reference and, when given
  one, a profile: data, not code (required files, how scopes are run, required workflows, additions
  to the AGENTS block, `jackstay.json` sections). `jst adopt --profile <path>`.
- **An organisation keeps its profile outside jackstay.**
- **jackstay's documentation carries an example profile** that describes every capability a profile
  has, with no project or organisation names.

## Consequences

- See [adopt](../design/adopt.md) section 6 and the guide's [adopt](../guide/adopt.md).
