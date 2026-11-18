# W-078 — After an install, jackstay names every remaining step

Date: 2026-11-18.

## Context

An install puts `jst` in place, but a working setup also needs steps the install does not take:
the install's `bin` on `PATH`, Claude Code's sandbox letting jackstay write its home, and each
project adopted. A user who is not told these steps finds the tool silently half-working.

## Decision

- `install.sh` ends by checking whether the install's `bin` is on `PATH` and prints the exact next
  steps: the `PATH` line when it is missing, `jst doctor`, and `jst adopt` in a project. It changes
  nothing outside the install prefix.
- Every problem `jst doctor` reports names its fix: the command to run, or what to change and where.
- The guide's getting-started page lists the steps an install leaves to the user.

## Consequences

- The install script stays short enough to read before piping it from the web.
- A setup command that makes these changes with the user's consent can be built later on top of
  `jst doctor`'s checks.
