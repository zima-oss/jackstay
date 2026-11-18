# W-039 — The guard refuses a command that neither the bash nor the zsh parser can read

Date: 2026-11-16.

## Context

A very small share of agents' commands (about one in seven thousand distinct commands measured)
cannot be parsed as bash or zsh; those examined were lines the shell itself would reject. The guard
could refuse them, or search their raw text for destructive words.

## Decision

- The guard refuses such a line (`unparsable`) with advice to write it plainly. It runs no word
  search over the raw text, and the refusal does not name a destructive form.
- Unchanged: a crash of the guard itself fails open, so a bug in the guard never blocks all work.

## Consequences

- An agent rewrites an unreadable command plainly; nothing unreadable runs through the hook.
- See [the guard's design](../design/guard.md) sections 2 and 10.
