# Numbers, records and cards

Parallel sessions stop colliding on numbers and shared files: jackstay hands out the next free
number from one counter per project on the machine, keeps each record (a decision, a task) as a
file of its own instead of a row in a shared ledger, and reads which task card a change was for
from a `Card:` line in its message instead of hashes typed into cards.

## Take the next number: `jst id`

Declare a series in `jackstay.json`:

```json
"ids": {
  "D": {"files": "docs/decisions/D-{n}-*.md", "width": 3},
  "T": {"files": "docs/tasks/T-{n}-*.md", "width": 3, "also_in": ["docs/task-ledger.md"]}
}
```

```sh
jst id next D
jst id next T --count 3
jst id list D
```

`next` takes the highest number it can see, adds one, records it and prints it: `D-007`. It sees
every number it handed out before, every matching file in every worktree, every local branch and
remote-tracking ref, and the `also_in` files. So a number is never handed out twice on this
machine, whatever branch or worktree asks; a number never used is a gap, and gaps are fine.

`list` shows what was handed out, when, and by which session; `jst id peek D` shows what `next`
would hand out without recording it. It cannot see other machines, cloud sessions, or a number someone typed without asking until a
file with it exists. `jst lint tree` catches two files with one number before they land.

## Keep one file per record: `jst new` and `jst view`

A series listed under `records` is one file per record: a front matter of one JSON object between
`---` lines, then a body. `jst new` takes a number, copies the template, fills `id` (and `card`, and
each `--set`), and prints the path. `jst view` prints the table, and writes nothing: a committed
table would be the shared file again.

```json
"records": {
  "D": {"dir": "docs/decisions", "template": "docs/templates/decision.md",
        "columns": ["id", "status"], "before": "docs/decision-ledger.md"}
}
```

The template:

```markdown
---
{"id": "", "status": "open"}
---
# Title
```

```sh
jst new D --slug use-a-justfile --set status=open
jst view D --where status=open
```

The first prints `docs/decisions/D-008-use-a-justfile.md`.

`before` names an old ledger that stays frozen: `jst view` names it under the table, and
`jst lint tree` refuses a new row in it.

## Say which card a change is for: the `Card:` line

With a `cards` section, every change that reaches the cards branch says which task cards it is work
for, on lines of their own in its message:

```text
Card: T-140
Card: T-140, T-141
Card: none (dependency update)
```

```json
"cards": {
  "dir": "docs/tasks", "id": "T-[0-9]{3,}",
  "unit": "pr", "branch": "main",
  "since": "2026-11-20", "first_new": "T-170",
  "exempt_branches": ["bot/updates"], "snapshots": "docs/landed"
}
```

- `unit: "pr"`: the lines are in the pull request's description; a squash merge carries them to the
  branch. `unit: "commit"`: in each commit's message.
- Each ID must have a card file in `cards.dir`. `none` needs a reason in brackets and stands alone.
- `since`: commits before it are read loosely. `first_new`: cards from this number on keep
  `"commits": []`; nothing writes hashes into them.

```sh
jst cards T-140 T-141
jst cards --check
```

`jst cards` shows each card's state and what landed for it; `--check` lists only disagreements
between cards and history (exit 1 for one), and `--stale` the hashes in cards' `commits` fields
that are not on the branch. Every flag: `jst cards --help`.

Before a history rewrite that collapses commits, `jst cards snapshot --write` and commit the file;
after it, `jst cards compare` lists what was lost.

## Check the history and the tree: `jst lint range` and `jst lint tree`

`lint range` checks that every commit in a range has valid `Card:` lines. It is for what is pushed
straight to the cards branch:

```sh
jst lint range origin/main..HEAD
```

In a `pr` project a branch's own commits carry no line, so `lint range` on a branch is red by
design; such a project checks the description with `jst lint pr` ([releases](releases.md#jst-lint-pr)).

`jst lint tree` refuses two files of one series with the same number, a new card with hashes in
`commits`, a changed or deleted snapshot, a record file outside its series' folder, and a new row
in a frozen ledger.

Both exit 1 with each problem named by its rule id. Every flag and exit status:
`jst lint range --help`, `jst lint tree --help`.

## Where the `Card:` check runs

- On every pull request: the `card-line` workflow that `jst adopt` writes when the project has
  `cards` or `changes`. It downloads the pinned jackstay release and runs `jst lint pr --event`.
- Before a merge: `jst gh pr-land` judges the description and the merged tree.
- Before a push to the cards branch: the guard's `cards-push` rule (with the Claude Code hook).

## What can go wrong

- **`jst id next` exit 2**: a worktree folder is gone, a ref cannot be read, or the series is not
  declared. It never prints a number it could not record.
- **Two pull requests add the same number at the same moment**: both may pass; `jst lint tree` on
  the branch then fails, naming both files. Renumber one by hand.
- **A wrong card**: `Card: T-140` on work for T-141 passes. The line is as true as its writer.
- **A merge made with a message of its own** (a local `git merge --squash`) can lose the lines;
  `jst cards --check` lists such commits afterwards.

The design: [implementation.md, section 4](../design/implementation.md#4-git-and-files-jst-cards-jst-lint-jst-id-jst-new-jst-view-jst-changes-jst-check).
