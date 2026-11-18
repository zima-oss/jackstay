# W-085 — `jst adopt` lands the settings and the files in two pull requests

Date: 2026-11-18.

## Context

`jst adopt --apply --pin` wrote `jackstay.json` (the new `adopt.pin`) and the workflows rendered
from it in one change. Rule D0 of `jst lint pr` (settings alone,
[W-047](W-047-release-redesign-fixed-settings-no-one-off-imports.md)) refuses a pull request that
changes `jackstay.json` and anything else, so projects moving their pin split the change by hand
(issue #10).

## Decision

- `jst adopt --plan` and `--apply` take `--settings-only`: `jackstay.json` alone, its changes and
  its conflicts. An `--accept` of another file's conflict is refused with it.
- A change of both lands settings first: `jst adopt --apply --pin --settings-only`, merged by the
  owner; then `jst adopt --apply` on the merged base, which plans the other files again (their
  conflicts and accepts included), an ordinary pull request.
- An `--apply` that writes both says that D0 refuses them together and names the order.
- There is no files-first option and no generated follow-up command: the second step is the
  ordinary apply.
- D0 is unchanged: it cannot tell a generated workflow from a hand edit without running adopt in
  the linter.

## Consequences

- Between the two pull requests the base holds the new settings and the old workflows: CI still
  downloads the old pin, and `jst adopt --plan` there lists exactly the files left.
