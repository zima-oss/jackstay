# W-085 — `jst adopt` lands the settings and the files in two pull requests

Date: 2026-11-18.

## Context

`jst adopt --apply --pin` wrote `jackstay.json` (the new `adopt.pin`) and the workflows rendered
from it in one change. Rule D0 of `jst lint pr` (settings alone, [W-047](W-047-release-redesign-fixed-settings-no-one-off-imports.md)) refuses a pull
request that changes `jackstay.json` and anything else, so projects moving their pin split the
change by hand (issue #10).

## Decision

- `jst adopt --plan` and `--apply` take `--only settings` (`jackstay.json` alone) or `--only files`
  (every other file, rendered from the settings the run would leave). The conflicts are not split:
  nothing is written while one is left.
- A pin move lands settings first: `jst adopt --apply --pin --only settings`, merged by the owner;
  then `jst adopt --apply` on the merged base writes the workflows, an ordinary pull request.
- An `--apply` without `--only` that changes both says so and names that order; one with `--only`
  names the step left, and the `--accept` IDs whose writes belong to the other part.
- D0 is unchanged: it cannot tell a generated workflow from a hand edit without running adopt in
  the linter.

## Consequences

- Between the two pull requests the base holds the new settings and the old workflows: CI still
  downloads the old pin, and `jst adopt --plan` there lists exactly the workflows still to write.
- The files-first order also lands, but between its two pull requests `jst adopt --plan` without
  `--pin` would render the workflows back to the old pin.
