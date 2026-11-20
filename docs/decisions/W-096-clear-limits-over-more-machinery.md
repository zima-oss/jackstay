# W-096 — Clear limits over more machinery

Date: 2026-11-20.

## Context

Review rounds kept asking for one more corner case to be covered, and each answer added machinery
to a place that already drew such findings. No mechanism covers everything. Separately, CI runner
minutes are rented, with a quota and a cost, and some work ran the same tests more than once on one
commit.

## Decision

- Every mechanism has simple, clear behaviour and explicit limits, and what it does not do is
  written down. Coverage is not taken to extremes.
- A place that keeps drawing corner cases is narrowed, or an existing solution is taken, rather than
  growing more machinery. Before inventing something, look at how others have already solved it.
- CI does not run the same test twice on one commit, and jobs and steps are not duplicated.
- A review finding that only asks to cover more exotic cases never takes a blocking severity. The
  reviewer's prompt says so.

## Consequences

Mechanisms stay small and their documentation names what they leave out, so a reader knows where
the edge is. Some rare cases stay uncovered by choice, recorded as limits. CI workflows are checked
for repeated tests and steps before they are added to.
