# W-083 — CI runs the gate on Linux only

Date: 2026-11-18.

## Context

The gate workflow ran `just gate` twice: on `ubuntu-latest`, the check `main` requires, and on
`macos-latest`, a job no rule required. The macOS job could wait in GitHub's runner queue for over
an hour without starting, so a change whose required check had passed still showed a pending check,
and merge tooling that waits for every check could not land it. A macOS runner also costs more and
starts later than a Linux one, for code that is mostly the same on both systems.

## Decision

- `.github/workflows/gate.yml` has one job, `gate`, on `ubuntu-latest`. It is the only CI check
  and the one `main` requires.
- There is no macOS job. The macOS-only code (`*_darwin.go`) is checked by the maintainers'
  `just gate` on a Mac before an install and before a release.

## Consequences

- A pull request is judged by one check, which starts within minutes.
- A change that breaks only the macOS code can reach `main`; it is caught by the gate on a Mac
  before an install or a release, not by CI.
- The release workflow is unchanged ([W-075](W-075-release-through-public-ci.md)).
