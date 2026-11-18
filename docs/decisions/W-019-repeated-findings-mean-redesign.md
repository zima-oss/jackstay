# W-019 — A place that fails review two rounds running is redesigned; `pr-land` never touches local branches; `jst sync` merges only

Date: 2026-11-15.

## Context

Review rounds kept finding defects in the same places: patching each finding produced the next one.
Two such places were the local branch switch in `jst gh pr-land` and the rebase mode of `jst sync`,
both of which could overwrite ignored files.

## Decision

- **The rule:** when review finds defects in the same place in two rounds running, the next step is
  a redesign of that place that removes the class of defect, not another patch.
- **`jst gh pr-land`** never switches, pulls or deletes a local branch. It merges on GitHub with the
  head commit pinned and deletes only the remote branch; the local branch is left for `jst tidy`.
- **`jst sync`** merges only; `--rebase` is refused.

## Consequences

- `jst review` applies this rule itself: it reports a place as due for redesign after
  `repeat_rounds` rounds running (default 2).
- See [review](../design/review.md) and [the specification](../design/implementation.md) sections
  8.3 and 8.5.
