# W-048 — Review findings are rated by likelihood and consequence; rare ones go to a later-findings card

Date: 2026-11-16.

## Context

Review rounds kept returning findings about contrived scenarios. Fixing every one made designs more
complex without making them safer in ordinary use.

## Decision

- Each finding is rated on two axes: **likelihood** (does it happen by accident in ordinary work,
  or does it need a deliberate or unusual setup) and **consequence** (lost work or a wrong decision
  that passes unnoticed, or an inconvenience that is visible at once).
- **Realistic** findings (ordinary, or severe) are fixed. **Rare** findings (unusual and mild) are
  not fixed; they are recorded with their reasoning in the project's later-findings card.
- The reviewer rates each finding; the person or agent running the review checks the rating.
- [W-019](W-019-repeated-findings-mean-redesign.md) still holds: a place that keeps drawing
  findings, rare ones included, is redesigned.

## Consequences

- `jst review` implements this as its `triage` setting (on by default): rare findings never block
  and are listed apart with a ready row for the later-findings card.
- See [review](../design/review.md) section 4.
