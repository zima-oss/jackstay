package landed

const cardsHelp = `usage: jst cards [ID...] [--json] [--ref REF]
       jst cards --check [--json]
       jst cards --stale [--commit-map FILE]
       jst cards snapshot [--write]
       jst cards compare FILE

Each card's state and its commits, read from the history of cards.branch.
  ID...               only these cards
  --json              as JSON
  --ref REF           read REF's history instead of cards.branch
  --check             only the disagreements between cards and history
  --stale             hashes in cards' commits fields that are not on the branch
  --commit-map FILE   with --stale: a file of old and new hashes, to print both
  snapshot            print what landed, as a snapshot
  --write             with snapshot: write it to <cards.snapshots>/landed-<date>.json
  compare FILE        after a history rewrite: the entries of snapshot FILE that history lost

exit: 0 shown (--stale too, whatever it lists); 1 --check found a disagreement, or compare found
an entry history no longer has or commits that landed after the snapshot; 2 the request,
jackstay.json, the cards folder, git or a file cannot be used; 5 git log gave a record of another
shape.

example: jst cards W-012 W-013
`

const rangeHelp = `usage: jst lint range A..B

exit: 0 every commit is valid; 1 problems, each named; 2 the request, jackstay.json, the card
files or git cannot be used; 5 git log gave a record of another shape.

example: jst lint range origin/main..HEAD
`
