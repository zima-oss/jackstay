# jackstay: the current specification

jackstay (`jst`) is one Go binary that keeps a repository worked on by coding agents safe and
measured. Rulings that shape the whole: [W-036](../decisions/W-036-go-native-not-python-mimicry.md),
[W-040](../decisions/W-040-python-frozen-tests-by-change-readable-names.md),
[W-020](../decisions/W-020-version-tags.md), [W-013](../decisions/W-013-quality-testing-declarative.md),
[W-017](../decisions/W-017-review-fixes-and-threat-model.md).

## 0. This spec and the feature specs

This file is the map: the layout, the command table, the state, and the contracts of every command
that has no spec of its own. Each feature designed apart has one spec, and this file links to it
rather than repeating it. A command's flags and exit statuses are in its `--help`
(`internal/cli/testdata/help.txt`); a synopsis here names only what the contract needs.

| spec | what it holds |
|---|---|
| [guard.md](guard.md) | the guard hook (`jst guard`, `jst-guard`), its rules and `jst trash` |
| [discard-and-clean-gate.md](discard-and-clean-gate.md) | `jst discard`, the guard's advice, `jst test --commit`, the `guard.rules` settings |
| [testing.md](testing.md) | `jst test <scope>`, `list`, `plan`, `last`, the judges and the gate |
| [testing-compatibility.md](testing-compatibility.md), [testing-multirunner-proposal.md](testing-multirunner-proposal.md), [swift-structured-pilot.md](swift-structured-pilot.md) | the verdict baseline, the structured-evidence proposal and its Swift pilot |
| [review.md](review.md) | `jst review` |
| [release.md](release.md) | `jst release`, `jst changes` in a project with releases, `jst lint pr`'s release checks |
| [adopt.md](adopt.md) | `jst adopt`, profiles and the generated AGENTS block |
| [observability.md](observability.md) | the journal, `jst log`, `jst stats` |
| [land.md](land.md) | `jst land`, the hosting adapter, and where tests run: the scope graph, `jst test changed`, `uncovered`, `ci` (section 7) |
| [chat.md](chat.md) | the agent chat: `jst chat`, `jst hook claude` |
| [context-brief.md](context-brief.md) | `jst brief` |

When the code and a spec disagree, one of them is wrong and is fixed in the same change; neither
is history.

## 1. Layout: one module, one binary

```
go.mod             module github.com/zima-oss/jackstay (go 1.27.0; the toolchain line pins go1.27.4)
cmd/jst/main.go    the binary `jst`; imports every command's package, which registers itself
cmd/jst-licences   a release helper: goreleaser runs it with `go run` (section 6); not installed
cmd/jst-sitelinks  a docs helper: `just docs` runs it with `go run` on the built site (section 6); not installed
internal/<package> one package per command or concern (below)
bin/jst            runs the checkout's own build (.build/jst); projects use the installed copy
tests/go.sh        the `go` recipe: vet, every test with -race, a build
justfile           the checks (section 10)
.goreleaser.yaml   the release build (section 6)
install.sh         the quick install: a release for this machine, checked, then `jst install` (section 6)
site/              the documentation site, built with Hugo from docs/ (section 6)
.github/workflows/gate.yml   the gate on GitHub's runners (section 10)
.github/workflows/release.yml, pages.yml   publishing a release, publishing the site (section 6)
```

One binary answers as `jst`, `jackstay` and `jst-guard` (argv[0] makes it `jst guard`), links in
the installed folder (section 7.1).

| packages | commands and roles | spec |
|---|---|---|
| `adopt` | `jst adopt` | [adopt.md](adopt.md) |
| `guard`, `trash`, `discard`, `cleancopy`, `pinned` | `jst guard` (`latency`: its timing test), `jst trash`, `jst discard`, the clean copy of `jst test --commit` and what it shares with review's pinned trees | [guard.md](guard.md), [discard-and-clean-gate.md](discard-and-clean-gate.md) |
| `testing`, `judge` | `jst test <scope>`, `list`, `plan`; the judges | [testing.md](testing.md) |
| `receipts`, `baseline` | `jst test -- COMMAND`, `jst test last`, `jst receipts`, `jst baseline` | section 9 |
| `review` | `jst review` | [review.md](review.md) |
| `release`, `changes`, `lint`, `delivery` | `jst release`, `jst changes`, `jst lint pr`/`tree`; `delivery`: versioned local checkpoints of `collect` and adoption | [release.md](release.md), section 4 |
| `cards`, `landed`, `ids`, `series`, `records` | the `Card:` rules (shared with the guard), `jst cards`, `jst lint range`, `jst id`, numbered series, `jst new`, `jst view` | section 4 |
| `config`, `check`, `patterns`, `project`, `strictjson` | `jackstay.json` read strictly against one table; `jst check`; `jst patterns`; the project a command runs in; the strict reader of jackstay's JSON | section 3 |
| `homedir`, `expected` | `~/.jackstay` and its folders, one log line per write; the expected-projects list | sections 3.1, 7.7 |
| `supervise`, `proctree`, `procs`, `wait`, `flock`, `lock` | starting and stopping processes; `jst bg`, `jst procs`, `jst wait`; the machine-wide lock, `jst lock` | section 5 |
| `install`, `layout`, `prove`, `doctor`, `version` | `jst install`, `jst uninstall`, the prefix's layout, the probes, `jst doctor`; the version constant and build commit | section 7 |
| `land`, `hosting` | `jst land`; the host interface it asks | [land.md](land.md) |
| `status` | `jst status`: the one read-only overview, reading other packages through their public read functions | [W-090](../decisions/W-090-one-status-command.md) |
| `sessionchat`, `chatevent` | `jst chat`, `jst hook claude`; jackstay's own events published into the chat | [chat.md](chat.md) |
| `contextbrief` | `jst brief` | [context-brief.md](context-brief.md) |
| `choices` | the questions `jst setup` and `jst adopt` both put to a person (reviewer, chat) | [adopt.md](adopt.md) |
| `durable`, `testreport` | whole-or-nothing writes of small state files under a bounded lock; structured test reports carried from a run | sections 10, 9 |
| `gh`, `githubapi`, `remote`, `branch`, `tidy`, `owner` | `jst checks` and the GitHub adapter of `jst land`; the one GitHub client (`githubfake` for tests); the remote and default branch; `jst start`, `jst sync`, `jst tidy`, `jst owner` | section 8 |
| `gitcli` | the one adapter that runs git: a deadline, a stated environment, typed errors | section 5.2 |
| `telemetry`, `logs`, `stats` | the journal, `jst log`, `jst stats` | [observability.md](observability.md) |
| `cli` | the command table, `jst help`, `--help`, `jst version` | section 2 |
| `licences`, `sitelinks`, `installscript`, `docs`, `testrepo` | release licence files; the site's link check; tests of `install.sh` and of the documents; test repositories | sections 6, 10 |

- **Every command** is a package under `internal/` that registers itself in the table (section 2);
  `cmd/jst/main.go` imports it for that alone. A new `jackstay.json` key goes into
  `internal/config` with the command that reads it.
- **A new hook** is a command, never another argv[0] alias: the projects' settings name
  `jst-guard`.
- Standard library first; a well-kept library where it does a job better (`mvdan.cc/sh/v3`,
  `go-github`, OpenTelemetry's Go SDK). No package imitates another language's runtime
  ([W-036](../decisions/W-036-go-native-not-python-mimicry.md)).
- **Names say what they hold**
  ([W-040](../decisions/W-040-python-frozen-tests-by-change-readable-names.md)): no single-letter or
  cryptic identifier outside tests; golangci-lint's `varnamelen` enforces it.
- Only `internal/supervise` and `internal/gitcli` start, wait for or signal a process.
- `CGO_ENABLED=0`: one static file per platform. macOS and Linux are supported
  ([W-069](../decisions/W-069-jackstay-supports-linux.md)); `*_darwin.go` files in `procs`,
  `receipts`, `supervise`, `telemetry` and `testing` have Linux variants beside them.

## 2. The command table

`jst ARGS…` looks up `ARGS[0] ARGS[1]` where a command has two words (`lint pr`), else `ARGS[0]`,
in one table each package fills in its `init` (`cli.Register`); the longer entry wins, and two
entries of one name are a programming error. A command not in the table is named with the usage on
standard error, exit 2. `jst` alone prints the usage with exit 2, `jst help` with exit 0.
`jackstay` is the same; `jst-guard ARGS…` is `jst guard ARGS…`.

Help is the table's: `jst help` lists every command with its summary, and `--help` answers before
a command runs ([adopt.md](adopt.md) section 9; every command's text is the golden file
`internal/cli/testdata/help.txt`). `jst version` prints `jackstay <version> (<commit>)`.

Every command runs inside its span ([observability.md](observability.md)): begun before it runs,
ended with its exit status; a panic is recorded with its stack and then goes on.

The guard's hook answers exit 0 and fails open on its own errors ([guard.md](guard.md) section 7).

Each command's spec is named in its package's row of section 1 (all under `internal/`).

## 3. State and the project file

### 3.1 The user-level folder: `~/.jackstay`

State lives in `~/.jackstay`, found from the account's home in the password database, not `HOME`
([W-008](../decisions/W-008-state-in-dot-jackstay.md)). `JACKSTAY_HOME`, an absolute path, replaces
it. `~/x` in a setting is expanded from the account's home too. Shared state is under `.all`; a
project's is under its **marker name**: small letters, digits, `_`, `-` and a non-leading `.` stay;
a capital is `^` and the small letter (`WebApp` is `^web^app`, since macOS's disk ignores case);
every other character is `%XX` of its UTF-8 bytes; an empty name is `unknown`.

    .all/runs.jsonl                         the run record: every start and end of a `jst test` run, reuses, would-be reuses (section 9)
    .all/heavy-suite.lock                   the machine-wide lock (section 5.4)
    .all/heavy-suite.lock.holder            who holds it
    .all/heavy-suite.lock.queue/            who waits, one ticket each
    .all/expected-projects                  the list `jst doctor` checks (section 7)
    .all/codesign-identity                  the signing identity `jst install` uses (section 7)
    .all/procs/<id>.json, <id>.stop, sessions/   what `jst bg` supervises (section 5.1)
    .all/reviews/                           `jst review`'s shared state (review.md)
    .all/chat/                              the agent chat's store and its pusher (chat.md section 7)
    .all/context-brief/                     `jst brief`'s briefs (context-brief.md)
    .all/delivery/                          versioned checkpoints of adoption (section 1); `jst changes collect`
                                            keeps its own in the repository's Git folder (`jackstay-changes.json`)
    .all/setup.json                         the answers `jst setup` keeps for adopt to offer first (adopt.md)
    .all/telemetry/                         the journal of calls with no project (observability.md)
    .trash/                                 `jst trash` (guard.md section 10)
    <project>/receipts/<key>.json           receipts (section 9)
    <project>/receipts/<key>.failed.<when>-<process>   a failure of that key, one file each
    <project>/outputs/<run_id>.log          the output of the newest runs (section 9)
    <project>/durations.jsonl               per-class durations (testing.md section 4)
    <project>/clean-copies/                 copies `--commit` makes (discard-and-clean-gate.md)
    <project>/guard-seen                    touched by every hook call from a session (guard.md)
    <project>/ids/<series>.jsonl            the number counters (`jst id`)
    <project>/reviews/                      `jst review`'s rounds (review.md)
    <project>/telemetry/                    the project's journal (observability.md)

A log line is one JSON object appended in one write, so processes never mix lines; a log that
cannot be written never fails its tool, except `jst test`, which checks its evidence first.
`jst setup` lets Claude Code's sandbox write only what jst writes from a sandboxed command
(`doctor.SandboxGrants`: the journals, the heavy-suite lock with its queue and holder note,
`procs`, `chat`, `session-chat`, `context-brief`, each project's `ids`, the trash). Evidence (receipts and
failure notes, review rounds, the run log, the delivery record, the expected
projects) stays read-only there, so a sandboxed agent cannot forge a pass (#59): `jst test` and
`jst review` run outside it. Before running, `jst test` appends-opens the run log, makes and removes
a file in the project's receipts (`receipts.UnwritableEvidence`) and removes
the receipt it replaces; when one fails it runs nothing. Outside the sandbox every session
of the account may write here, so receipts and the run record can be forged: a known limit,
not a boundary (guard.md).

### 3.2 The project file: `jackstay.json`

A project's settings are `jackstay.json` at its root, committed. A command uses the nearest one at
or above its folder; anything of that name stops the search (a folder, a broken link), so a project
above never stands in for one whose file cannot be read.

The file is read strictly: one JSON object, no key twice in one object, every number finite.
`"jackstay_schema"` is required, 1 to 3. The sections `cards`, `ids`, `records`, `changes`,
`procs`, `release`, `github`, `review` and `adopt` need 2. The sections and keys a jackstay before
0.6.0 does not know (`config.Stage3Sections`, `config.Schema3Keys`) need 3. A file for a later
jackstay is refused with the advice to upgrade, never half-read. `project`, a non-empty string,
names the project. Without it the name is the main checkout's folder name, else the folder's own.
Inside a clean copy that `--commit` made, the copy's marker names the project
([discard-and-clean-gate.md](discard-and-clean-gate.md) section 3).

Every section is optional. Keys, types and defaults are one table, `config.Table` in
`internal/config`; a new key goes into it with the command that reads it.

| section | read by | specified in |
|---|---|---|
| `guard` | the guard; `jst tidy` (`regenerable`) | guard.md; `rules`: discard-and-clean-gate.md section 9 |
| `lock` | `jst lock`, `jst test` | section 5.4 |
| `receipts` | `jst test`, `jst receipts` | section 9 |
| `test` | `jst test <scope>`, `list`, `plan` | testing.md |
| `baseline` | `jst baseline` | section 9.6 |
| `github` | `jst checks`, `jst land`, `jst tidy`, `jst sync`, `jst start`, `jst lint pr`, `jst release` | section 8.1 |
| `land` | `jst land` | land.md section 1 |
| `procs` | `jst bg` | section 5.1 |
| `trash` | `jst trash` | guard.md section 10 |
| `cards`, `ids`, `records`, `changes` | `jst lint`, `jst id`, `jst new`, `jst changes`, the guard's push rule | section 4 |
| `release` | `jst release` | release.md |
| `review` | `jst review` | review.md |
| `adopt` | `jst adopt` | adopt.md |
| `telemetry` | the journal | observability.md section 10 |

**Checked in two passes.** First every key against the table: an unknown key (the allowed keys
named), a wrong type, a pattern that does not compile. Then each section's meaning, as its spec
states it. The meaning checks this file owns:

- `receipts`: a `commands` entry needs exactly one of `argv` and `argv_prefix`, naming a word.
  `"reuse": true` needs `argv`, an `evidence` pattern, and `receipts.env` and `receipts.toolchain`
  written out (an empty list counts). An `evidence` pattern needs a group.
- `lock`: an `also_hold` entry needs `path` (absolute or `~/…`) and `held_env` (a variable's name).
- `baseline`: `kinds` names are unique; `merge_unit` is `pr` or `commit`.
- `guard`: a `regenerable` entry may not mean everything (`*`, `**`, `.`, `..`, `/`, `*/*`,
  `**/*`).

**A section is usable or not on its own.** A problem never lets a section be half-read, so a
misspelt safety setting is never silently off. A broken section stops only the commands that read
it; the file's own keys (`jackstay_schema`, `project`, an unknown section) stop every command.
`jst test -- …` reads `lock` and `receipts`, so a broken `test` section stops `jst test <scope>`
and not it. On a broken section the guard applies its built-in rules and says so; `jst lock` and
`jst test` exit 2 and run nothing (`not-started`); `jst tidy` notes it and skips GitHub or
`guard.regenerable`.

Without a `jackstay.json` the guard applies its built-in rules, `jst lock` and `jst test` name the
project after the main checkout's folder, and `jst test -- COMMAND` only records: no lock, no key,
and a run that ends 0 is `exited-0`, never `passed` (section 9).

**Patterns are RE2**, Go's dialect ([W-036](../decisions/W-036-go-native-not-python-mimicry.md)):
no lookaround, no backreferences.

`jst check [DIR]` judges every section and the rules other packages own (`review`, `test`,
release); exit 0 usable, 2 naming every problem or no file. It also warns about a scope or step
with no timeout, and a step over a 60 s median before one under 10 s. `jst patterns [DIR...]` lists
each pattern RE2 does not take; exit 0 none, 1 some, 2 a file missing or not JSON.

## 4. Git and files: `jst cards`, `jst lint`, `jst id`, `jst new`, `jst view`, `jst changes`, `jst check`

These commands read the repository on every call and keep almost nothing of their own: what a
change was for is read from commit messages; numbers and records are files and file names
([W-004](../decisions/W-004-stage-0-and-ground-rules.md),
[W-009](../decisions/W-009-stage-1-scope-history-and-remote.md)). How to use them is the
[records guide](../guide/records.md). Their sections, with the example in that guide:

- `cards`: `dir`, `id`, `unit` and `since` are required. `unit` is `pr` or `commit`; `since` is a
  `YYYY-MM-DD` date; `id` is RE2 without inline flags (it is used inside larger patterns);
  `first_new` matches `id` and ends in its number.
- `ids.<S>`: `S` is letters and digits starting with a letter. `files` holds `{n}` exactly once,
  in the file's name (no `{n}` or glob in the folder), inside the repository. `width` is 1 to 9.
- `records.<S>`: needs an `ids.<S>` entry whose `files` is `<dir>/<S>-{n}-*.md`, and `dir` and
  `template`.
- `changes`: `dir`, `into` and `heading` (with `{version}`) are required; `newest` is `first` or
  `last`.
- `procs.default_minutes` is not above `procs.max_minutes`.

Paths are written from the repository's root and stay inside it. A **commit** is new when its date
is on or after `cards.since`; a **card** is new from `cards.first_new` on. Neither mark is read
from git, so no rewrite moves them.

### 4.1 The `Card:` line: the convention, `jst cards`, `jst lint`

A card names its work by a line in the change's message, not by commit hashes in its front matter:
a squash merge's hash exists only after the merge.

#### The convention

A change that reaches `cards.branch` names its cards on lines of their own, from the first column:

    Card: T-140
    Card: T-140, T-141
    Card: docs/work/tasks/T-140-empty-state.md
    Card: none (handoff after the release)

- With `unit: "pr"` the lines are in the pull request's description (a squash merge carries it);
  with `unit: "commit"`, in each commit's message.
- A card is its ID or its file's path in `cards.dir` (the ID starts the name, then `-` or `.`; the
  file ends in `.md`). Items are comma-separated, lines add up, an ID twice counts once; backquotes
  and a trailing full stop are allowed. Each ID matches `cards.id` and has a file directly in
  `cards.dir` in the tree judged; another project's card is `Card: none (<project> <ID>)`.
- `none` needs a non-empty reason in brackets and stands alone. Line ends are normalised first.
- The line says the change is work for the card, not that it is finished: `state` stays in the
  card, by hand. A new card keeps `"commits": []`.

**Every line that starts with `Card: ` is read, not git's trailers**: GitHub appends
`Co-authored-by:` after the description, and git then no longer reads `Card:` as a trailer. A line
of prose that starts so is refused unless it parses. The rules are one package, `internal/cards`,
shared by `jst lint`, `jst cards`, `jst land` and the guard's push rule. Rule ids:
`card-line-missing`, `card-line-unreadable`, `card-none-reason`, `card-unknown`.

#### `jst cards`: what landed, read from history

`jst cards` shows each card's state and what landed; `--check` shows the disagreements below (exit
1 on one); `--stale` lists `commits` hashes not on the branch (`--commit-map` adds new hashes). It
edits no card. History is `cards.branch` (default `main`; the remote-tracking copy unless the local
one is ahead; `--ref` overrides), first parents for `pr`, every non-merge commit for `commit`,
dated by **author date**.

"Landed" for a card is every such commit whose `Card:` line names it, joined with the snapshots in
`cards.snapshots` (section 4.4). An entry gives the date, author, subject, pull-request number (a
`(#N)` ending the subject) and current hash; the hash is printed, never stored. A commit before
`since` is read loosely: every ID found on its `Card:` line counts; without a line, an ID in its
subject is reported apart as `named in the subject`.

| `--check` kind | Disagreement |
|---|---|
| `no-card-line` | a commit on or after `since` with no `Card:` line |
| `unreadable-card-line` | a commit on or after `since` whose line does not parse, or mixes `none` with a card |
| `no-card-file` | a `Card:` line naming an ID with no card file |
| `closed-with-nothing-landed` | a new card `done` or `verified` with nothing landed |
| `ready-with-work-landed` | a card `ready` with something landed |
| `hashes-in-a-new-card` | a new card with anything in `commits` |

#### `jst lint`: the check

`jst lint pr` judges a pull request's description and its tree; `jst lint range A..B` judges every
commit in the range; `jst lint tree` applies the tree's rules only. Flags: `jst lint pr --help`.

**Rules for the lines** (`pr`, `range`): at least one `Card:` line; each parses; every ID has a card
file in the tree judged; `none` has a reason and stands alone. No description is
`card-line-missing`.

**Rules for the tree** (`tree`, and part of `pr`):

| Rule | What it refuses |
|---|---|
| `duplicate-number` | two files of one series with one number, counted as `jst id` counts (section 4.2), or a number too large to count |
| `new-card-commits` | a new card whose `commits` is not an empty list |
| `snapshot-changed`, `snapshot-deleted` | a snapshot file that differs from, or is gone since, the commit that first added it |
| `record-out-of-place` | a record file (`<S>-<n>[-…].md`) outside its series' `dir` |
| `frozen-ledger-row` | a row of a series' `before` file naming a number at or above the lowest record file the series ever had |

An empty `cards.dir` is exit 2. Tree rules follow the judged tree's `jackstay.json`; line rules and
exemptions follow the base's, so a pull request cannot loosen its check. A head in
`cards.exempt_branches` gets the tree rules only. The release rules and `jst lint pr`'s inputs are
[release.md](release.md) section 7. `lint range` judges commits by **committer date** against
`since`, reads cards at the tip and ignores `unit` (a `pr` project checks descriptions instead).

Exit: 0 fine; 1 a rule broken, each named with its rule id and line or path (for `pr`, also a head
that does not merge cleanly with its base); 2 the request or `jackstay.json` is unusable; 5 an event
file or a `git log` record of an unexpected shape.

#### Where the check runs

- **On the pull-request runner**: the `card-line` workflow `jst adopt` writes
  ([adopt.md](adopt.md)). It listens for `opened`, `reopened`, `synchronize`, `ready_for_review`
  **and `edited`**, checks out the merge with the base, downloads the pinned release with its
  checksums, and runs `jst lint pr --event "$GITHUB_EVENT_PATH"`. It runs only for branches of the
  same repository.
- **Before a merge, locally**: `jst land`, where the base's `cards.unit` is `pr`, judges the
  description exactly as it sends it for the merge commit's message, against the files of the
  in-memory merge (`git merge-tree`) of the head with the base commit it judged, by the base's
  `cards` section, and, where the base has `changes`, its changelog rule (D1: an entry or a
  `Changes: none (reason)` line); the recheck before the merge stops if the description changes.
  The tree's rules are the `card-line` workflow's, a required check (W-092): no local gate on the
  tree.
- **When `jst land` opens the pull request**: `--card ID` (repeatable; `--card none --reason TEXT`)
  adds the line to the description it writes. Where the base asks for the line, it opens no pull
  request whose description it cannot judge first (no `--card` and no `--body-file`) or whose
  description fails the rules on the merge with the base's tip; it stops before the push. `--card`
  for a pull request that is open already is refused. A base whose Card line is in the
  description refuses `land.method: rebase`, which lands no description.
- **Before a push to the branch itself**: the guard's `cards-push` and
  `card-push-unreadable-config` rules ([guard.md](guard.md) section 5). A project that pushes to
  its branch runs `jst lint range origin/main..HEAD` and `jst lint tree` in its gate.

**Limits.** A wrong card passes (`Card: T-140` on work for T-141). A merge the check did not gate
(an unprotected branch, a merge with its own message, a push from outside a session) is not
stopped; `jst cards --check` lists it afterwards. A local `git merge --squash` indents every folded
line, so it loses them. Two pull requests that land at once, each adding a file with one number,
both pass; `jst lint tree` on the branch then fails, naming both, and one is renumbered by hand.
Off the first-parent line in a `pr` project only the merge commit's message is read. A runner that
cannot download jackstay, or does not know the project's `jackstay_schema`, is red. GitHub
unreachable locally makes `jst lint pr --pr` exit 2.

### 4.2 The counter of numbers: `jst id`

One counter per machine serves every session and worktree, since a "next free number" taken on a
branch collides on the default branch ([W-004](../decisions/W-004-stage-0-and-ground-rules.md),
[W-009](../decisions/W-009-stage-1-scope-history-and-remote.md)). Under one `flock` per project,
`jst id next` takes the highest number it sees, adds one, records it and only then prints it
(`peek` hands out nothing; `list` shows what was handed out). It sees the largest of: the record
`~/.jackstay/<project>/ids/<SERIES>.jsonl`; the series' folder in every worktree on disk,
committed or not; the same folder in the tree of every local branch and remote-tracking ref; and
the series' numbers in the main checkout's `also_in` files.

A file holds a number when its name begins as the pattern's up to `{n}` (`*` and `?` keep their
glob meaning), then the number, then a non-digit or nothing (`internal/series`, shared with
`jst lint tree`). So the counter never falls behind a number made by hand, and a deleted state
folder loses only numbers with no file yet. An unused number is a gap; nothing is renumbered. An ID
is the series, a dash, and the number padded to `width`; `--count` takes 1 to 100.

**Failure falls towards no number** (exit 2, the place named): the record cannot be written or holds
a line that is not a record; a number above 10^12 in the record or a file name; a ref, worktree or
tree git cannot read; a missing `also_in` file; a series `jackstay.json` lacks. A number it could
not record is never printed; of several, those recorded before are named as gaps. A caller killed
after recording leaves a gap. A dead lock holder's `flock` is freed by the system.

One machine only: a number from elsewhere is seen once a file holds it; section 4.1's check keeps
duplicates off the branch.

### 4.3 One file per record: `jst new`, `jst view`, `jst changes`

Shared appended files are where merges conflict, so each record of a `records` series is one file.

#### Records

Usage: [records guide](../guide/records.md). A record is `<records.S.dir>/<ID>-<slug>.md`: one JSON object between two `---` lines (read
strictly, line ends normalised), then a body in the project's words.

- `jst new` takes a number, copies the template and sets `id`, `card` (matching `cards.id`) and
  each `--set` key, nothing else; the slug is lower-case letters, digits and dashes, at most 80; the
  file is created exclusively.
- `jst view` reads the working tree's record files in number order and prints the `columns`
  (default `id`) and each body's first line. **It writes no file, and nothing rendered is
  committed**: a committed table is the shared file again.
- The old ledger (`before`) stays, frozen: `jst view` names it under the table, and `jst lint tree`
  refuses a new row in it.
- A record that changes is an edit of its own file; two sessions conflict only on the same record.

`jst new` exits 2 with no file on any problem (an existing name also leaves a gap). A record
`jst view` cannot read is named after the other rows, exit 1: never a silently short table.

#### Changelog entries

One file per entry in `changes.dir`, written with the change; typed entries and releases are
[release.md](release.md). Without a `release` section:

- `jst changes new --slug SLUG` writes `<slug>.md` from `changes.template` (`{card}` and `{slug}`
  filled in), or a one-line entry; `--type`, `--audience` and `--title` make it typed.
- `jst changes collect --version V` runs once per release, in the release commit. It writes one
  section under `heading` at the place `changes.newest` says (`last` by default), entries in the
  order of release.md section 2, and removes the entry files (`git rm` for tracked ones).
  `--dry-run` only prints. In a project with `release` it exits 2 and names `jst release prepare`.

`collect` exits 1 and collects nothing when it finds no entry, an invalid entry, uncommitted changes
in `changes.into` or `changes.dir`, an untracked entry, or a changelog or entry that changes
meanwhile. Two runs in one checkout take turns on a lock in the git folder. Before replacing the
changelog it records HEAD, the new changelog blob and each entry's path, mode and blob in the
worktree's git folder. A rerun at the same HEAD and changelog removes only unchanged tracked entries
that record names; a heading with no unfinished record leaves entries in place (exit 1). A release
that forgets `collect` leaves the entries for the next section.

### 4.4 What a history rewrite keeps

**jackstay writes no commit hash into any file it keeps or commits.** Numbers and records are files;
`Card:` lines are read from messages on every call. What a rewrite does to messages:

| Rewrite | `Card:` lines and `(#N)` | What changes |
|---|---|---|
| GitHub squash merge with the description | kept | — |
| plain `git rebase`, `commit --amend` keeping the message, `cherry-pick` | kept | every hash; the committer's date |
| a rebase that folds or drops commits (`fixup`, `squash`, `drop`, an emptied commit, merges without `--rebase-merges`) | **lost** for each message folded or dropped | as above |
| `git filter-repo` on contents or paths | kept in the commits that remain | hashes from the first changed commit; tags move |
| `git filter-repo` with a message callback | whatever the callback leaves | — |
| a local `git merge --squash` with the default message | **lost** (indented) | — |
| a collapse into one commit, a new root, a shallow graft | **lost** | everything |

After a rewrite that keeps messages, `jst cards` shows the same entries with new hashes. Cards' old
`commits` are wrong after any rewrite (`--stale --commit-map` prints old and new). Records, entries
and numbers survive whatever leaves the last tree unchanged.

#### The snapshot

For a collapse, the one loss git cannot prevent, there is a snapshot (W-009). `jst cards snapshot
--write` writes `<cards.snapshots>/landed-<date>[-n].json`: every landed and `named in the subject`
entry with the author's date, author, subject, pull-request number and hash **as it was**. It is
committed before the rewrite, created exclusively, and never edited (`jst lint tree` refuses a
change or deletion).

`jst cards` shows the union of history and every snapshot. An entry is the same when card and
pull-request number match, or, without a number, card, author date, author and subject. Old hashes
are a record, never resolved. `jst cards compare FILE`, run in the rewritten clone before the push,
lists every snapshot entry history no longer has and how many commits landed after the snapshot's
`head_then`; it exits 1 for either. The steps: snapshot and commit; rewrite in a fresh clone (the
guard refuses `filter-repo` elsewhere); `compare`; `jst lint tree`; the force-push is the owner's.

**Limits.** Without a snapshot a collapse loses old entries silently. A message edited so it still
parses, or a changed subject or author date, makes `compare` report an entry lost (and, without a
pull-request number, `jst cards` show it twice). Two entries of one card with the same author, date
and subject and no number are one.

### 4.5 State files of these commands

| File | Fields |
|---|---|
| `~/.jackstay/<project>/ids/lock` | empty; held with `flock(LOCK_EX)` while a number is taken |
| `~/.jackstay/<project>/ids/<SERIES>.jsonl` | one object per line, appended in one `write` with `O_APPEND`, mode 0600: `series`, `number` (integer, 0 to 10^12), `id`, `at`, `epoch`, `worktree`, `session` (from the session note, section 5.1; `null` when unknown), `pid` (the caller's parent). Read strictly: a line that is not such a record stops the counter. |
| `<cards.snapshots>/landed-<date>[-n].json` (committed in the project) | `format: "jackstay-cards-snapshot"`, `version: 1`, `written`, `branch`, `head_then`, `note`, `entries[]` of `card`, `kind` (`landed` or `named`), `author_date`, `author`, `subject`, `pr` (number or `null`), `hash_then`. Read strictly (one document, no member twice). |
| `<records.S.dir>/<ID>-<slug>.md` (committed) | the template's front matter with `id` (and `card`, and `--set` keys) filled in |
| `<changes.dir>/<slug>.md` (committed) | an entry: [release.md](release.md) section 2 |

`jst cards`, `jst lint`, `jst view` and `jst check` keep no state.

## 5. Processes: `jst bg`, `jst procs`, `jst wait`, `jst lock`

How to use these commands is the [background guide](../guide/background.md); this section is their
contract.

### 5.1 Background processes: `jst bg`, `jst procs`

A process is supervised only because it was started through `jst bg` (no hook learns a background
process id). `jst bg -- COMMAND` runs in the foreground of its own background Bash call and:

- runs the command in a **session of its own** without a terminal, passing on INT, TERM, HUP, QUIT;
- **stops it at a wall-clock deadline**: `--for`, else `procs.default_minutes` (30), never above
  `procs.max_minutes` (240; said when the cap applies);
- **stops it when its session is gone**: every 30 s it checks that the process it started under is
  the same (id and start time) and not a zombie;
- with `--keep`, has neither: only `jst procs stop` (answered within 0.25 s) ends it;
- keeps `~/.jackstay/.all/procs/<id>.json` while it runs, and stops what the command left in its
  session (TERM, 5 s, KILL).

A stop signals the group and the session's members outside it. Before a stop for a deadline or a
lost session, `lsof` (20 s bound) is asked: a regular file open for writing from descriptor 3 on,
outside `/tmp`, `/var/folders`, `/var/tmp` and `$TMPDIR`, **holds** the process (`held`, asked again
at each check). No answer from `lsof`, or `jst procs stop`, stops it all the same.

**Which session.** The guard hook notes, for its nearest ancestor that is not a shell, a wrapper,
Python or jackstay, the `session_id` it serves (`procs/sessions/<pid>-<start>`). `jst bg` takes the
nearest ancestor with a note; without one it watches a likely session process (`session unknown`),
or with none supervises by the deadline only, and says so.

**`jst procs`** lists this session's entries (`--all` every one): `running` or `held` while the
supervisor lives, `orphaned` when only its group does, else `gone`. It removes gone entries and
stale notes, and stops an orphan past its deadline whose leader is proven by its start. **`jst
procs stop`** asks a living supervisor through `<id>.stop` (15 s), and signals a group itself only
when the supervisor is proven gone and the leader proven. A NAME means this session's entries; with
no known session it is refused. The guard counts background calls made without `jst bg`.

Exit of `jst bg`: the command's status (128+N for signal N); 2 a bad request; 126 the command could
not start; 127 not found; stopped by a signal, it ends by that signal. `jst procs`: 0; `stop` 1 when
something was not stopped; 2 a bad request. `--watch PID` and `--check-every S` are for tests only.

**Failures.** With `--keep`, an unwritable state folder stops the command at once. Unconfirmed
cleanup keeps the entry as `cleanup-incomplete` and turns success into 1. An orphan that cannot be
proven or signalled is named with what to run; an unanswered stop is exit 1, the request kept. Not
seen: processes not started through `jst bg`, daemons, a subagent ended inside a living session.

| File | Fields |
|---|---|
| `~/.jackstay/.all/procs/<id>.json` | id `<epoch s>-<pid>`; `id`, `name`, `supervisor`, `supervisor_start`, `group`, `started`, `leader_start`, `deadline` (epoch seconds; `null` with `--keep`), `keep`, `session`, `watched{pid, start, name}`, `cwd`, `command`, `status` (`running` or `held`), `held` (the files). Written whole (temporary file, sync, rename), mode 0600 folder 0700. `orphaned` and `gone` are states shown, not written. |
| `~/.jackstay/.all/procs/<id>.stop` | empty: a request to stop, removed with the entry |
| `~/.jackstay/.all/procs/sessions/<pid>-<start>` | `session`, `pid`, `start` (whole seconds), `name`; written by the guard hook, removed once the process is gone |

### 5.2 The process model: signals, deadlines, process groups, the closer

Every command jackstay starts and lives beside goes through `internal/supervise`, and every stop
through `internal/proctree`. There are two ways to start:

- a **session** of its own without a terminal (`jst test <scope>`, `jst bg`), stopped as a session
  tree: processes remembered by session and parent links, sampled every second, found by session
  even when reparented to process 1;
- a **process group** of its own, given the terminal when jackstay has one (`jst lock`,
  `jst test -- COMMAND`), stopped as the group plus every session recorded in
  `JACKSTAY_LOCK_SESSIONS_FILE`. A process that left the group (`setsid`, a double fork) runs on.

The grace between SIGTERM and SIGKILL: 30 s for `jst lock` and `jst test --`
(`JACKSTAY_LOCK_GROUP_WAIT` replaces it), 10 s for a scope's session and a recorded session, 5 s
for `jst bg`. After SIGKILL the killed have 5 s more.

**Identity.** A process is its id and start (microseconds): a reused id is never signalled, and a
group or session only while a remembered member is in it. The leader's end is learnt without
reaping (kqueue `NOTE_EXIT`, `waitid` with `WNOWAIT`); it is reaped last, after which nothing is
signalled. With no readable process table the stop goes to the group by id, its end unconfirmed.

**The agent sandbox** (macOS) reads the process table but refuses signals to processes the command
did not start (EPERM: "run outside the sandbox"; another account's: "not this account's"), and its
`lsof` silently lists only the caller's processes. So a view resting on `lsof` (`jst tidy`, section
8.4) is complete only when `lsof` named the folder of every process of the account that ran
throughout its walk (same start before and after); otherwise it is partial and names up to eight
unseen processes (`proctree.View.Unseen`).

**Signals.** One loop owns SIGINT, SIGTERM, SIGHUP and SIGQUIT (one ignored at start stays ignored)
and passes each on. The first starts the stop; the grace runs from it, so a command ignoring TERM
is killed when it ends. A stopped run ends by the signal (the caller sees 130, 143, 129); QUIT
exits 131.

**After the command's own end** the lock and record are freed only when the group is gone: leftovers
get 0.3 s, then TERM, the grace, KILL and 5 s; survivors are named. In
`JACKSTAY_LOCK_SESSIONS_FILE` a run under a group records each session it starts (`<session id>
<leader start µs>`), and the group's supervisor stops what is left of them after any end.

**The terminal.** When jackstay's group has the terminal, the command's group is given it before
exec (retried without it on ENOTTY, EPERM or EOPNOTSUPP) and taken back after. SIGTSTP is ignored
while a group's command runs and while `jst lock` waits.

**Output** is read line by line, escape sequences removed; readers get 10 s after the stop, and
output not read to its end is not a pass (section 9.3). Never `exec.CommandContext`, never
`Cmd.Wait`. One closer undoes setup, last first. Clocks are injected
([W-013](../decisions/W-013-quality-testing-declarative.md)); terminal tests (`just terminal`) run
outside the gate.

Variables between nested runs: `JACKSTAY_LOCK_HELD` (section 5.4), `JACKSTAY_LOCK_SESSIONS_FILE`,
and `JACKSTAY_TEST_RUN_ID` / `JACKSTAY_TEST_RUN_PROJECT`, which make a nested `jst test` of the same
project part of the outer run (`inside`, section 9.5).

### 5.3 `jst wait`

One wait for one condition with a deadline ([W-007](../decisions/W-007-tools-from-the-usage-study.md);
usage: [background guide](../guide/background.md)). Default deadline 540 s, under a tool call's 10
minutes; it prints the matching line and the last `--tail` (20) lines of the final 64 KiB.

- The `--in` path may start missing; an existing one must be a regular file. One that disappears
  or changes kind is unavailable, and an unavailable path is never quiet. A file cut short, replaced
  (a new inode) or rewritten in place without growing is read again from its start.
- Matching reads 64 KiB windows and checks the deadline between them. A line over 1 MiB ends it
  with exit 1, naming the file and the limit.
- `--fail RE` ends at once with 1, wins over a wanted line in the same piece, and counts before its
  line end; with `--pid` the process's last words are checked too.
- `--while-pid P` ends with 1 when the writer dies first, after one more look; it does not cut
  `--quiet` short. `--new` counts only lines written after the start.
- A zombie counts as ended. The first known start time identifies the PID, so a reused PID does
  not keep the wait alive; when the kernel withholds the row, only existence is checked.

Exit 0 it happened; 1 it cannot any more, or a `--fail` line; 2 a wrong request; 3 the deadline.

### 5.4 The machine-wide test lock: `jst lock`

**Off by default** ([W-005](../decisions/W-005-lock-off-by-default.md)): `lock.enabled` or `jst test
--lock` turns it on, `--no-lock` off; `jst lock -- COMMAND` always locks, holding one `flock` for the
machine, `~/.jackstay/.all/heavy-suite.lock` (`JACKSTAY_LOCK` replaces it). A project turns it on
only when `jst baseline` shows a need (section 9.6).

- **In turn.** Runs queue by tickets in `heavy-suite.lock.queue/` (clock nanoseconds and pid); a
  dead run's ticket is passed over and stale ones removed. A waiter polls up to once a second and
  every minute says how long, how many are ahead and who last took the lock.
- **Freed only when the command's group is gone** (section 5.2). What survives SIGKILL is named,
  and the lock freed.
- **Nested runs.** The command gets `JACKSTAY_LOCK_HELD=<realpath of the lock>:<pid>`; a locked
  command under a holder does not lock again once the claim is proved: this lock, held, by a living
  ancestor (where the table cannot be read, the holder note must name the pid).
- **The holder note** (`heavy-suite.lock.holder`): `project P: WORDS in DIR, the command's process
  group G (the lock's PID), since …`, written only once every lock the run needs is held. The
  project is `--project`, else `jackstay.json`'s, else the main checkout's folder; the words are the
  command's, `--note-words` or `JACKSTAY_LOCK_NOTE_WORDS`.
- **`jst lock status`** lists who runs and who waits, in order, for each lock.
- **`--wait SECONDS`** gives up with status 75, having run nothing; without it a run waits.
- **Older locks** (`lock.also_hold`, `--also-hold PATH=VARIABLE`): a project's earlier lock file is
  held with the shared one, and its variable set, so both scripts exclude each other. One an
  ancestor holds is skipped. A run waits in one line only while holding nothing: it tries the
  others without waiting, and when one is busy gives everything back and waits in that one's line,
  so no two runs deadlock (a run that gave the shared lock back rejoins its line's end). An older
  lock that is the shared lock's file, or not absolute, is exit 2; one named twice is taken once.
- **`JACKSTAY_LOCK_OUTCOME_FILE`** tells the lock's statuses from the command's: the lock empties
  it at the start and writes `ran`, `gave-up` (75), `unusable` (2) or `cannot-start` (127); empty
  means it was stopped while waiting. This variable, `JACKSTAY_LOCK_NOTE_WORDS` and
  `JACKSTAY_LOCK_GROUP_WAIT` are not passed to the command.

Exit: the command's status; 2 a lock, `jackstay.json` or the words cannot be used (also inside the
sandbox: "Heavy runs go outside the agent sandbox"); 75 gave up ("Nothing was run"); 127 cannot
start; stopped by a signal, it ends by it. A holder killed outright frees the lock while its command
may run on; the note names the group. The lock is advisory, and not in a cache folder (a deleted
lock file becomes a second lock).

## 6. Builds and releases

**The build.** `.goreleaser.yaml` builds `./cmd/jst` for darwin and linux, amd64 and arm64, with
`CGO_ENABLED=0`, `-trimpath`, `-s -w` and the commit in `-ldflags`. Each platform's
`jackstay_<version>_<os>_<arch>.tar.gz` holds `jst`, `LICENSE` and, under `licenses/<module>/`, the
licence files (`LICENSE*`, `PATENTS`, `NOTICE*`, `COPYING*`) of every compiled-in module; one
`checksums.txt` covers them. The before hook `go run ./cmd/jst-licences "$JST_LICENCES"` copies
those files (modules from `go list -deps ./cmd/jst` for darwin and linux) into a folder it must
create; a module without a licence file stops the release. GoReleaser publishes nothing.

**macOS signing** ([W-075](../decisions/W-075-release-through-public-ci.md)). GoReleaser signs and
notarises through quill (no Mac, no keychain) with the Developer ID certificate (`MACOS_SIGN_P12`,
`MACOS_SIGN_PASSWORD`) and an App Store Connect key (`MACOS_NOTARY_KEY`, `MACOS_NOTARY_KEY_ID`,
`MACOS_NOTARY_ISSUER_ID`). It waits for Apple at most **18 minutes** (Apple refuses a token over 20
minutes; quill's lives the timeout plus 2); a slower answer or a refusal fails before publishing.
The identifier is `jst`, as `jst install` uses (section 7.4). Without `MACOS_SIGN_P12` a local
snapshot is unsigned; the release workflow refuses to run without all five secrets.

**Publishing** is `.github/workflows/release.yml`, run by a pushed `v*` tag in `zima-oss/jackstay`
only, on `ubuntu-latest` in the `release` environment (the secrets, the owner's approval), with
`contents: write`, `id-token: write`, `attestations: write` and `artifact-metadata: write`. Actions
are pinned by commit hash; GoReleaser runs pinned through `go run`, Go's build cache off. Before
approving, the maintainer has run `just gate` on a Mac on the tagged commit
([W-083](../decisions/W-083-no-macos-job-in-ci.md)). In order:

1. Refuse a tag that is not annotated, not the commit checked out, not on main, or not
   `v` + `internal/version`'s `Version`.
2. `just gate` on the tagged tree (Linux; section 10).
3. GoReleaser builds, signs and notarises with that tag (`GORELEASER_CURRENT_TAG`,
   `--skip=publish,announce`).
4. `actions/attest` signs build provenance for each archive, bound to the repository, workflow and
   commit; a failure stops the release.
5. Publish by release id: the tag must still name the commit built, and no release or draft of it
   may exist (a confirmed 404; W-020: never redone). Create a draft, upload the four archives and
   `checksums.txt`, check the draft holds exactly them by GitHub's sha256 digests, check again that
   nothing was published meanwhile, publish the draft, and check the tag's release is that id. A
   failure before publishing leaves at most its own draft, deleted before a rerun; one after may
   have published, so its assets are checked against `checksums.txt` before anything is repeated.

Nothing is published from a maintainer's machine; `just release-snapshot` builds locally only.

**The quick install** ([W-076](../decisions/W-076-quick-install-and-docs-site.md)) is `install.sh`,
POSIX `sh` with `set -eu`, served from `main` and as the site's `/install.sh`; its settings, checks
and exits are the [install guide](../guide/install.md#the-quick-install). Beyond them: it runs from
`main` on its last line, so a cut-short download only defines functions; curl always uses `-q
--proto =https --proto-redir =https --tlsv1.2`; only Darwin or Linux on x86_64/amd64 or
arm64/aarch64 (Rosetta gets arm64) gets past the first check; the archive's `checksums.txt` line is
matched by whole name; it runs `jst install --binary` from a removed `mktemp -d` folder with
`JACKSTAY_TELEMETRY=off`, never `sudo`, writing nothing outside the prefix
([W-078](../decisions/W-078-setup-after-install.md)). `JACKSTAY_RELEASES` names a fake releases page
for tests. `internal/installscript` runs the README's one-liner under `sh` and `dash` against such a
page over TLS, covering each refusal, and checks the guide, home page and script carry one
one-liner; `just check` runs shellcheck.

**The documentation site** ([W-076](../decisions/W-076-quick-install-and-docs-site.md),
[W-077](../decisions/W-077-docs-site-for-users-only.md) amended by
[W-093](../decisions/W-093-docs-for-people-and-agents.md)), Hugo and Hextra from `site/`, carries the
home page, the guide by task, the generated reference and the contributors' pages; the design and
the decisions stay on GitHub. For agents it writes `/llms.txt`, `/llms-full.txt` and each page's
Markdown.

- `site/hugo.yaml` mounts `docs/` and the root's Markdown files; `site/content/_content.gotmpl`
  makes a page of each file of `docs/guide` and `docs/reference`, titled by its `# Title`. The
  guide's `README.md` orders the sidebar: its `## ` headings that the adapter names are the groups,
  and a page goes to the group its first link is under; a page moved into a group keeps its old
  address as a redirect.
- The Markdown served to agents takes each link's address from the link hook (below), so it is
  absolute; a reference definition split over two lines stops the build.
- The link hook (`site/layouts/_markup/render-link.html`) turns a link to a guide file into its
  page and one to another mounted file into a GitHub link, checking each `#fragment` against
  GitHub's heading ids; a dead link stops the build.
- `cmd/jst-sitelinks` (`internal/sitelinks`) checks the built site: every `href` and `src` to the
  site's host stays under `/jackstay/`, reaches a written file and names an existing `id`.
- Hugo runs pinned through `go run`; the theme is pinned by `site/go.mod` and `site/go.sum`;
  FlexSearch is vendored in `site/assets/lib/`. `just docs` builds with `--panicOnWarning` and runs
  `jst-sitelinks`; `just site` checks the `.build/site` the workflow publishes.
- `pages.yml` (in `zima-oss/jackstay`, on a push to `main` touching `docs/guide/`, `site/`,
  `CONTRIBUTING.md`, `install.sh`, the justfile or itself, or by hand) builds with `contents: read`,
  `pages: read` and deploys with `pages: write`, `id-token: write` in `github-pages`; one deployment
  at a time, never cancelled.

**Runners** (a project's `card-line` and `release` workflows) download the pinned version's archive
with `gh release download` and check it against `checksums.txt`.

## 7. Install, uninstall, doctor

How to install, update and remove is the [install guide](../guide/install.md).

### 7.1 The layout

`P` is the prefix: `--prefix DIR`, else `~/.local` of the account (`homedir.AccountHome`, never
`$HOME`).

```
P/bin/jst                -> ../lib/jackstay/jst         a fixed link, never rewritten
P/bin/jackstay           -> ../lib/jackstay/jackstay    a fixed link
P/bin/jst-guard          -> ../lib/jackstay/jst-guard   a fixed link
P/lib/jackstay           -> jackstay-0.5.4-1a2b3c4-1795…-4242   the one activation point
P/lib/jackstay-0.5.4-1a2b3c4-1795…-4242/
    jst                  the Go binary (regular file, 0755)
    jackstay             -> jst
    jst-guard            -> jst  (argv[0]'s base name makes it `jst guard`)
    VERSION              "0.5.4 (1a2b3c4)"
    SOURCE               the checkout it was built from, "binary FILE", or "release"
P/lib/.jackstay-install.lock   the install lock (flock), shared by install and uninstall
```

A version folder (`jackstay-<version>-<commit>-<seconds>-<pid>`, `-2`… when taken) is
**jackstay's** only when it holds a `VERSION`, an executable `jst` and the two links. It is made as
`<name>.partial`, renamed whole and never written again. A **launcher** is `fixed` (exactly the
link above), `absent` or `foreign`; a `foreign` launcher or a `lib/jackstay` that is not a link to
a folder of jackstay's is never touched (install refuses, uninstall keeps and names it). Install,
uninstall and doctor all decide from `layout.Read(P)`.

### 7.2 `jst install`

**Where the binary comes from.**
- A checkout (`--checkout DIR`, else the nearest one above): built with `CGO_ENABLED=0 go build
  -trimpath` under a 10-minute deadline, from the commit's git archive when the build inputs
  (`cmd`, `internal`, `go.mod`, `go.sum`) are clean, else from the working tree with `+changes`.
- `--binary FILE`, or with neither flag and no checkout the running binary (`SOURCE` `release`).

Its `jst version` text, `jackstay <version> (<commit>)`, is `VERSION`. A `go install` build names
its tag (`v0.5.11`) or pseudo-version commit, so a tag-named and a hash-named build of one version
are two commits of it.

**Refused** (exit 2, nothing changed): a commit `unknown` or `+changes` (a `+changes` checkout build
is taken only into another `--prefix`, for tests); the install lock held; a `foreign` launcher or a
`lib/jackstay` not jackstay's; a version lower than the live one (no rollback); the same version
from another commit into the account's prefix ([W-020](../decisions/W-020-version-tags.md)); a
signing or proof that fails (sections 7.3, 7.4); an unreadable signing setting; a failed build.

**The steps.** Each change is one `rename(2)` and each decision is read from the prefix, so a stop
at any step is finished by running install again.

1. Read the signing setting (section 7.4), get the binary, read its version.
2. Take the install lock; `layout.Read`; the refusals.
3. Same `VERSION` live: probe the live launchers; on a failed proof, or a signature to repair
   (section 7.4), prepare and prove a replacement beside the executable before renaming it in.
   Another version: make the folder, sign it when configured, prove it. A failure here leaves the
   live files intact.
4. **Activate** (skipped when already live with every launcher `fixed`): `lib/jackstay` is
   replaced by the new folder in one rename, then each non-fixed launcher by its link. Every
   launcher runs what `lib/jackstay` names, so that one rename switches all three.
5. **Prune** (section 7.5).

Output names the version, launchers, folder and the version before ("already installed" when it
was), then each path removed. Exit 0 installed or already installed; 2 refused; 1 a rename failed
half-way (the message names the step; install again finishes it).

### 7.3 Proving a folder (before any rename that would make it live)

Each probe runs through `supervise.Capture` (30 s, in `/`, a fresh temporary `JACKSTAY_HOME`, other
`JACKSTAY_*` dropped):

- `FOLDER/jst version` and `FOLDER/jackstay version` print `jackstay <VERSION's text>`, exit 0;
- `FOLDER/jst-guard` given `{"tool_name":"Bash","cwd":"/","session_id":"jackstay-prove","tool_input":{"command":"git reset --hard"}}`
  answers `permissionDecision: deny` (read as JSON).

### 7.4 Signing at install ([W-070](../decisions/W-070-sign-jst-at-install.md))

So that a firewall's rule follows `jst` across installs, a new folder's binary is signed with the
machine's identity and identifier `jst` before it is proved:

1. `codesign --sign <identity> --identifier jst --options runtime --timestamp --force <folder>/jst`
   (`--timestamp` reaches Apple's timestamp server);
2. `codesign --verify --strict <folder>/jst`;
3. `codesign --display --verbose=2 <folder>/jst`: the identifier and first authority must match.

A failing step is exit 2; the folder is removed and the message names the fix. A binary already
signed by a certificate with identifier `jst` that verifies strictly (a release's) is kept as it
is, since re-signing would lose its notarisation. With an identity configured, a live binary with
an unsigned, ad-hoc, other-identifier or invalid signature (on macOS, also a working ad-hoc one
meeting a verified incoming one) is replaced: staged in `<folder>/resign.partial/` under the lock,
signed, probed, then renamed over `<folder>/jst`; a failed preparation is exit 2.

**The identity setting** is one line in `~/.jackstay/.all/codesign-identity`, the name as
`security find-identity -v -p codesigning` prints it: the machine's, so not an environment variable
or `jackstay.json`. Without it, and on Linux, nothing is signed. `jst doctor` reports what fails
this rule (section 7.7); `jst install --binary` with the live version mends it.

### 7.5 What is kept and what is pruned

Install keeps the live folder and **the version before** (live when it began; on a reinstall, the
newest with another `VERSION`) and removes every other folder and `.partial` name; a failed removal
is a warning.

### 7.6 `jst uninstall`

Under the install lock it removes the `fixed` launchers first (a hook meanwhile gets 127, never a
half-removed folder), then `lib/jackstay`, then every folder of jackstay's and `.partial` name;
foreign ones and the lock file stay. `~/.jackstay` stays, with a warning that hooks naming the
launchers now run unguarded. Exit 0 (also "nothing (it was not installed here)"), 1
something could not be removed, 2 usage, a held lock or an unreadable prefix.

### 7.7 `jst doctor`

The prefix is `--prefix`, else the running binary's install, else `~/.local`. Every check is a
function of `doctor.Inputs`, so tests use fixtures and a fixed clock. A finding has a **kind**;
problems make exit 1 and each names its **fix** ([W-078](../decisions/W-078-setup-after-install.md);
`TestEveryProblemNamesItsFix`): a broken install's is the one-liner with `JACKSTAY_PREFIX='P'`
(never an uninstall); `sandbox-blocks-state`'s `jst setup`, a narrower `denyWrite` or another
`JACKSTAY_HOME`, never removing a deny; `sandbox-writes-evidence`'s `jst setup` for an entry naming
the home itself, else narrowing the entry by hand.

| check | finding |
|-------|---------|
| the prefix can be read | `layout-unreadable` |
| every launcher `fixed`, `lib/jackstay` a folder of jackstay's | `launcher-missing`, `launcher-not-fixed`, `link-missing`, `folder-incomplete` |
| leftover `.partial` names; the install lock held | notes `partial-left`, `install-running` |
| `P/bin/jst-guard` refuses the probe of section 7.3, with a home of its own | `guard-does-not-refuse` |
| `P/bin/jst version` equals the live `VERSION` | `version-mismatch` |
| the `SOURCE` checkout has no build-input change since the installed commit (other files: a fact) | `checkout-moved` |
| on macOS: the signing setting, the identity, the live signature (section 7.4) | `codesign-setting-unreadable`, `codesign-identity-invalid`, `codesign-uninspected`, `binary-not-signed` |
| the guard's spans of the last 7 days in every project's journal | `failed-open`, `config-error` per project; ignored tools as a fact |
| each project's `guard-seen` mark and the expected list | note `guard-quiet` (quiet over 3 days); `expected-never-called`; `guard-never-called` (no mark, nothing expected); `expected-unreadable` |
| `~/.claude/settings.json`'s sandbox enabled and a grant (for each project folder and expected project) not writable by it | `sandbox-blocks-state` |
| an `allowWrite` entry covering or reaching into an evidence path no `denyWrite` covers | `sandbox-writes-evidence` |
| the lock and its queue, or else the lock's folder, can be written | `lock-unwritable` |
| the project around the working folder is adopted (`adopt.Check`, [adopt.md](adopt.md) section 10) | `adoption` |

`--json` prints `{"facts": {…}, "problems": [{"kind", "text", "fix"}], "notes": [{"kind", "text"}]}`;
as text each problem is followed by `  fix: …`. The sandbox check reads only the account's
`~/.claude/settings.json`; a pattern's part before its first `*?[` is resolved as a real path and
the rest matched per element (`**` any number); a `denyWrite` covering a grant wins. Setup removes
only `allowWrite` entries naming the home itself, makes each grant before naming it, and writes
the settings through the ordered model adopt uses, with a backup beside them.

**`--expect NAME` / `--forget NAME`** change `~/.jackstay/.all/expected-projects` (sorted marker
names) under `flock` on `expected-projects.lock` (up to 30 s), written atomically; `--forget` also
drops the project's `guard-seen`. A blank name is exit 2; a failed change, exit 1. A list that
exists and cannot be read whole is an error, never empty.

## 8. GitHub and branches: `jst checks`, the `github` section, `jst tidy`, `jst start`, `jst sync`, `jst owner`

jackstay talks to GitHub through one client, `internal/githubapi` (go-github and GraphQL), shared by
`jst checks`, `jst land`, `jst tidy`, `jst lint pr` and `jst release`
([W-050](../decisions/W-050-go-part3-design-github-account-all-steps-here.md)). It runs the `gh` CLI
only to ask for a token; git pushes, fetches and deletes remote branches. How to use these commands
is the [branches and GitHub guide](../guide/branches-and-github.md).

### 8.1 The `github` section: which account

The account jackstay acts as, so it never depends on, or changes, `gh`'s active one.

| key | meaning |
|---|---|
| `account` | required in the section: the login jackstay acts as (letters, digits, single hyphens, at most 39) |
| `remote` | the remote that names the repository and that `sync`, `start`, `tidy` fetch (section 8.2) |
| `repository` | `owner/name`, when not read from the remote's URL |
| `wait` | `timeout_seconds`, `interval_seconds`, `log_lines`: defaults for `jst checks wait`, `run-wait` and `jst land`; flags win |

**Two ways to a token**, chosen once per call:

- **Inside GitHub Actions** (`GITHUB_ACTIONS=true`): `GH_TOKEN` only; `account` is not read and
  `GET /user` not asked (an installation token cannot answer it). No `GH_TOKEN`: exit 2.
- **Elsewhere**: the account is `JACKSTAY_GITHUB_ACCOUNT`, else `github.account`; its token comes
  from `gh auth token --hostname github.com --user <account>`, which does not switch `gh`'s active
  one. `GET /user` once per call: another login is exit 2. An account not signed in is exit 2
  naming `gh auth login --hostname github.com`; a keychain or TLS failure is named apart. A
  `GH_TOKEN`, `GITHUB_TOKEN`, `GH_HOST` or enterprise variable is ignored, and said once.

Without `github.account`, outside Actions, `jst checks`, `jst land` and `jst lint pr --pr` exit 2 and `jst tidy`
skips `pr-merged`; offline commands need none.

**The account is the API's, not git's.** git's credentials (the URL's user, a credential helper,
which for `gh` answers with the *active* account, an SSH key) are not verified. Before a push or a
deletion, `jst land` checks that every effective push URL of the remote (`pushurl` and rewrites
included) names the pull request's repository, and the deletion requires GitHub to confirm the
merged head's repository, branch and SHA. jackstay never rewrites a remote or git's settings.

**The repository** is `github.repository`, else the remote's configured URL (`insteadOf` not
applied); a pull request's URL wins. A host other than github.com is exit 2.
`JACKSTAY_GITHUB_API_URL` (tests) must be loopback http.

**Retries.** Only reads are retried, on 5xx and network errors: four calls about 1, 2 and 4 s apart
(±50 %), inside the deadline. Writes are never retried; a lost answer is settled by reading again.
A primary rate limit waits for its reset inside the deadline, else exit 1; a secondary one honours
`Retry-After`.

**Checks** are one GraphQL query of the head's `statusCheckRollup`, paged to the end: the newest run
of each check name per suite, and other apps' commit statuses. A cancelled check is superseded only
when a bounded listing of runs for the commit shows a newer run of the same workflow (and the same
event and branch when known); that run's matching job is judged, pending until it appears. Buckets:
SUCCESS pass; SKIPPED, NEUTRAL skipping; FAILURE, ERROR, TIMED_OUT, ACTION_REQUIRED,
STARTUP_FAILURE fail; CANCELLED cancel (failing); anything else pending. No checks at all is
pending. The head is compared after the rows are read, and the pull request's identity again before
landing.

A pull request is found by its tip with GraphQL `search`. A failed step's log is its job's log cut
by the step's times, else the last lines, saying so.

### 8.2 The remote and the default branch

The remote is `github.remote` (one that names no remote is an error), else `origin`, else what the
local `main` (then `master`) tracks, else the only remote; anything else is an error naming them.
The default branch is the remote's as fetched (`<remote>/HEAD`, else `/main`, `/master`), else the
local `main`, else `master`. `jst land` takes the base from the host's own default branch.

### 8.3 `jst checks` and `jst land`

    jst checks wait [PR|URL] [--timeout S] [--interval S] [--lines N] [--any-head]
    jst checks status [PR|URL]
    jst checks run-wait WORKFLOW [--ref BRANCH|SHA] [--repo URL] [--timeout S] [--interval S] [--appear S]

Sessions that wait on GitHub by hand write many loop shapes around `gh pr checks`, some of them
wrong; these commands replace them ([W-006](../decisions/W-006-tidy-and-github-tools.md)).

- `wait` waits until every check on the pull request's **current** head settles (default: this
  branch's pull request); a red check prints its failing step's log if time remains (`--lines`
  per check). One deadline covers lookup, requests, retries and optional error detail.
  `--timeout 0` allows one look with a ten-second request budget. A head that moves while waiting
  is exit 4 (checks for the old head say nothing about the new one), or with `--any-head` is
  followed. `status` is the same, once. Every check is waited for: `jst checks` does not read the
  base's rules.
- `run-wait` waits for one workflow run to complete, on a branch or a commit (a SHA prefix of 7 or
  more); the run may take `--appear` seconds (default 120) to be scheduled.

Defaults: `--timeout` 1800 s, `--interval` 20 s, `--lines` 25, or the `github.wait` values.
Exit: 0 fine; 1 not fine, the reason printed; 2 the request was wrong (also no account, a wrong
login, a host that is not github.com); 3 still pending at the deadline; 4 the head moved while
waiting; 5 an external format is not what jackstay assumes.

Landing a change (`jst land`, `jst land status`, `jst land find`) is specified in
[land.md](land.md): the `land` section, the hosting adapter, who may merge, required checks and the
exit states. They replace 0.5.x's `jst gh pr-land`, `pr-status` and `pr-find`; `jst checks`
replaces `jst gh ci-wait`, `ci-status` and `run-wait` with the same flags.

### 8.4 `jst tidy`

The guard's advice for deleting branches and worktrees: `jst tidy` asks git, GitHub and the process
table first ([W-006](../decisions/W-006-tidy-and-github-tools.md)). It fetches, reports, and removes
only with `--fix`. An open pull request keeps a branch whatever git says.

| A branch is finished when | It is kept when |
|---|---|
| every commit is on the default branch, the remote's or the local one (`merged`) | it is the default branch, or checked out in a kept worktree |
| merging it into the default branch changes no file and takes no work back (`adds-nothing`, the guard's test) | a pull request from it is open (`pr-open`) |
| GitHub lists a merged pull request from it whose head is or holds its tip (`pr-merged`) | it has commits after its merged pull request's head |
| with no pull request of its name, GitHub's search by its tip finds a merged one whose head is or holds it (`pr-merged`) | a pull request that search finds is open |
| | none of the above can be shown (`own-work`) |

A worktree is removed when its branch is finished (detached: its commit is on the default branch or
held by a ref) and:

- `git status` shows nothing modified, staged or untracked, and no file is skip-worktree or
  assume-unchanged;
- every ignored path is regenerable (the guard's names and `guard.regenerable`, except an entry that
  matches nearly anything, an `.env`, or one with a slash) or agents' state (`.cc-writes`,
  `.DS_Store`, `.omc/` and `.omx/`'s `state`, `logs`, `sessions`);
- it is not locked and not the main checkout;
- no process works in it, `jst tidy` included, and the view of processes is complete (section 5.2).

`--fix` judges each item again without fetching, then runs `git worktree remove` without `--force`
and `git update-ref -d refs/heads/<name> <tip>` at the tip judged; an item called off ends 1.
Records of named worktrees whose folder is gone are pruned; a sweep leaves them and exits 1.
Remote branches are not touched. Whatever cannot be shown keeps its item, reported: no GitHub (no
`pr-merged`), a failed fetch (old refs), an unreadable or partial process view (every worktree,
always inside the sandbox), an unusable `jackstay.json` (no `guard.regenerable`). Exit 0 nothing
finished is left; 1 something finished stays or a removal failed; 2 a wrong request. Not seen:
stashes, other hosts, a process holding a file from elsewhere.

### 8.5 `jst sync` and `jst start`

([W-007](../decisions/W-007-tools-from-the-usage-study.md)).
**`jst sync` merges only** ([W-019](../decisions/W-019-repeated-findings-mean-redesign.md)):
`--rebase` is exit 2, because a rebase can write over or delete an ignored file neither end's tree
has. It fetches (section 8.2) and refuses (exit 2) a merge, rebase, cherry-pick or revert under way,
uncommitted or untracked work, assume-unchanged or skip-worktree files, or a detached HEAD. On the
default branch it fast-forwards; elsewhere it merges with `git merge`'s message. A conflict ends 1,
the files named and the merge left in place.

**The ignored file.** A three-way merge writes over an ignored file silently, so HEAD is first
compared with the merge's result made in memory (`git merge-tree --write-tree`): a path it adds that
is untracked here, holds untracked files, or meets a file where a folder must be stops the sync
(exit 1, named). Both merges run with `--no-overwrite-ignore`.

`--push` pushes exactly this branch to its upstream (`git push <remote> HEAD:<branch>`, with `-u`
the first time), never a bare `git push`; it is decided before the merge.

**`jst start NAME`** fetches and makes the branch on the fetched default branch (or `--from REF`),
with no upstream, in a worktree at `.claude/worktrees/<name>` when git ignores that folder, else
`<main checkout>-worktrees/<name>` (`--path` chooses; `/` becomes `-`). With no default branch git
can name it asks for `--from`. An invalid name, an existing branch or folder, or a `REF` that is
not a commit is exit 2.

Exit for both: 0 done or nothing to do; 1 a conflict or git refused; 2 the request.

### 8.6 `jst owner`

Reads what the person said in a Claude Code transcript ([W-006](../decisions/W-006-tidy-and-github-tools.md)):
typed, queued (a typed copy within 5 s shown once) and `AskUserQuestion` answers in both spellings
(`The user answered:`, `Your questions have been answered:`), a declined question being its own
kind. `--session` is a substring of a session file's name or `all` (default the newest); `--grep`
is case-insensitive RE2; `--stats` shows where the session's tool calls went. The transcripts are in
Claude Code's projects folder (`CLAUDE_CONFIG_DIR`'s, else the account's), under the checkout's real
path with every non-ASCII-alphanumeric character as `-`.

Exit 0 read (filters matching nothing still 0, said); 1 nothing to read; 2 a bad request; 5 the
transcript format or place changed (a transcript with no message from the person is a changed
format); 6 a widget answer in an unknown spelling.

## 9. Receipts and the baseline: `jst test -- COMMAND`, `jst receipts`, `jst baseline`

`jst test <scope>` is [testing.md](testing.md) and builds on this section. `jst test -- COMMAND` is
the raw form: any command, judged, recorded and keyed by the tree. How to use receipts is the
[tests guide](../guide/tests.md).

### 9.1 `jst test -- COMMAND`

It reads `lock` and `receipts` only and runs the command in a process group (section 5.2), locked
only when asked (section 5.4); every run is recorded. A green, counted run stores a **receipt** keyed
by the tree; for a command opted in, a receipt of the same key is printed and nothing runs. A
`"reuse": false` command is still keyed, and a repeat is logged as `would-hit`. With the lock on,
the lookup is made before the lock and again under it.

### 9.2 The key

A SHA-256 over:

- the project's name, the command's words, its folder relative to the checkout, jackstay's version;
- each input's path, kind (file, link, missing), permission bits and content. Inputs are the tracked
  files under `receipts.inputs` and the untracked files the committed `.gitignore` files do not
  ignore, less `receipts.exclude`, plus `receipts.extra_files`. Only committed ignore rules hide a
  file (`.git/info/exclude` and global ignores hide nothing). The working tree is read, not the
  index. A link is its target's name, and its content when it is a file. An `exclude` pattern
  matches the whole path from the root (`*.md` at the root, `**/*.md` anywhere);
- what each `receipts.toolchain` command prints, the system's version and build, the machine kind,
  and every variable matching `receipts.env`; `MAKEFLAGS`, `MFLAGS` and `MAKEOVERRIDES` always
  (less the job server's address);
- the commits `receipts.refs` name and HEAD's merge bases with `receipts.merge_bases` (a command's
  entry may add to both);
- the input lists, the evidence and veto patterns;
- HEAD's commit, only with `receipts.include_head`.

No key (no receipt, no reuse, no `would-hit`) when: there is no `jackstay.json` or no
`receipts.inputs`; an input is not a plain path inside the checkout or the inputs are no file; a
folder (a submodule) or a link to one is an input; a `.gitignore` is untracked, differs from HEAD or
is hidden by assume-unchanged or skip-worktree (except an untracked one inside an already ignored
folder); a ref cannot be resolved, a toolchain command fails (60 s each) or git fails (120 s each).
`jst receipts key -- COMMAND` prints the key, its material and whether a receipt would be reused.

**Not in the key**, so tests depending on them are not candidates for reuse: unlisted variables;
ignored files the build reads (a `.gitignore` outside the inputs unless in `extra_files`);
anything outside the checkout; empty untracked folders; flakiness.

### 9.3 Outcomes, evidence and the veto

The last `jst test:` line on standard error names exactly one outcome. The outcomes, their
meanings and exit statuses are one table, in the [tests guide](../guide/tests.md#read-the-last-jst-test-line).
The contract behind it:

- `passed` only when the command ran now, ended 0, its output was read to its end, its tests were
  **counted** above zero and no veto line was seen. Nothing else is `passed`, and only `passed`
  stores a receipt.
- `exited-0`: ended 0 with nothing to count its tests (no `evidence` pattern, or the `exit` judge);
  the line reads `outcome=exited-0 (no test count: not proof that tests ran)`. Never a pass in any
  count.
- `failed` with status 1 when the status was 0 but a veto line was seen; otherwise the command's
  own status.
- `unjudged` (77) when the output could not be read to its end, a leftover could not be confirmed
  gone, or the evidence group matched something that is no number.
- `stopped` / `stopped-waiting`: asked to stop after the start, or while waiting for the lock
  (nothing ran); it ends by that signal. `gave-up` (75) and `not-started` (2) ran nothing; a
  command that cannot start is 127. `timed-out` (124) is a scope's (testing.md section 3);
  `skipped`, `not-run`, `left-to-ci` and `needs-other-system` are a graph's ([land.md](land.md)
  sections 7.2 and 7.4).

A command's own 2, 75–78 shows as `failed`, so the line, not the status, is read. An unwritable
record line adds a `WARNING` just before it and never turns a pass into a failure.

**Counted** is the entry's `evidence` pattern, one number group summed over matching lines of both
streams (`Executed ([0-9]+) tests?`): a floor, not a total. A `veto` pattern names a failure line
whatever the status, with or without evidence. Escape sequences are removed first; scope judges are
[testing.md](testing.md) section 2. If a leftover holds the output open (`server &`), the group is
stopped so the last line is read; not read within 10 s is `unjudged`.

**Output.** Each run's output is kept in `~/.jackstay/<project>/outputs/<run_id>.log` (newest 30; an
unfinished one up to a week) and named in its record. A run that did not pass prints up to 40
failure lines and the output's path before its outcome line ([testing.md](testing.md) section 7).
A reuse prints the receipt on standard error, nothing on standard output.

### 9.4 When a receipt is written, removed and used

**Written** (receipt schema 3: `schema`, `key`, `outcome`, `project`, `command`, `seconds`,
`evidence_count`, `started`/`started_epoch`, `ended`/`ended_epoch`, `material`, `jackstay`,
`run_id`, `worktree`, `head`, `branch`) only when: status 0; the output read to its end; no veto;
evidence above zero; a key could be made; the key after the run equals the key before; no input was
rewritten meanwhile, even to the same content (identity and times compared); and no run of the same
key failed after this one started. Why a pass stored none is its record's `not_stored_kind`:
`no-key`, `tree-changed`, `input-rewritten`, `failed-since`, `write-failed`, `no-evidence`.

**Removed** before the key's command runs again, so a stopped or failed run leaves none. A run that
does not pass leaves a failure note (`<key>.failed.<when>-<process>`, one file each, never
rewritten): a pass that started before the newest note stores nothing, and a receipt whose run
started before it is never used. **Pruned**: whoever writes a receipt or note removes the project's
receipts and notes older than the larger of `receipts.max_age_hours` and 24 hours.

**Used** only when all hold:

- the command is in `receipts.commands` by its exact words (`argv`) with `"reuse": true` and a
  usable `evidence` pattern (`argv_prefix`, longest match, is for `"reuse": false` only);
- `receipts.env` and `receipts.toolchain` are written out;
- no `--no-reuse` and no `JACKSTAY_NO_REUSE`;
- the branch is not in `receipts.no_reuse_branches` (default `main`, `master`), and HEAD is not the
  commit such a branch, local or any remote's, points at (a detached HEAD at `origin/main` is
  `main`);
- the receipt is at most `receipts.max_age_hours` old (default 72; up to five minutes in the future
  counts), holds this key and `passed`, has this jackstay's schema, and no failure note is newer
  than its run's start;
- the reuse can be written to the record.

So on `main` the command always runs. `jst receipts list` lists stored receipts.

### 9.5 The run record and `jst receipts stats`

`~/.jackstay/.all/runs.jsonl`, one JSON object per line, written before any span describes it
(observability.md section 9). Every line has `at`, `epoch`, `event`, `project`, `command`, `key`,
`version`, `worktree`, `head`, `branch`, and `inside` (the outer run's id) for a run nested in a
`jst test` of the same project, which every count skips. A started command writes two lines joined
by `run_id` (`<nanoseconds hex>-<pid>`):

- `start`, just before the command starts: `run_id`, `pid`, `started_epoch`, `locked`;
- `run`, when it has ended: `run_id`, `pid`, `locked`, `outcome`, `started_epoch` and `ended_epoch`
  (the command's own), `seconds`, `evidence`, `receipt_removed`, `output`, and per outcome
  `evidence_count`, `stored` or `not_stored` and `not_stored_kind` (`passed`, `exited-0`); `status`
  and `veto_line` (`failed`); `why` (`unjudged`); `signal` (`stopped`). A scope adds its fields
  ([testing.md](testing.md) section 7).

A `start` with no `run` is a run that died silently or still goes; reports call it `unfinished`.
`gave-up` and `stopped-waiting` are `run` lines with `waited_seconds` and no `run_id`. Beside them:
every `reused` (`saved_seconds`, `lookup`, `receipt_of`) and every `would-hit` (`reason`,
`saved_seconds`, `receipt`, `forced`). A `would-hit` is counted from the record, not a stored
receipt: the key's last `passed` run counted tests, is within the age limit, and no run of the key
failed or ran nothing since. So a project that never gets a receipt is still measured.

`jst receipts stats` sums runs, reuses and seconds saved per project (`exited-0` apart; forced runs
apart). Every failure falls towards running the command; the one way to a false green is an input
the key does not hold (section 9.2).

### 9.6 `jst baseline`

It measures one project over UTC days from transcripts, git and the run record, changing nothing.
The `baseline` section (or `--config`) gives `kinds` (a name and an RE2 pattern over one simple
command; `gate` marks one before a merge), `merge_unit`, `branch` and `transcripts`. It reports test
runs by kind (attempted, refused, denied, started, wall time; a call under 5 s is not timed),
merges, overlap of gate runs (with `--also` projects), Bash commands today's guard would refuse
(`--examples FILE` writes them; private text), and the run record's outcomes, reuses and lock
evidence.

**Whether simultaneous runs cause a problem** ([W-005](../decisions/W-005-lock-off-by-default.md))
splits recorded runs by what ran beside them for at least 5 s: `other` (another project's run),
`same` (only the same project's), `alone`. Per split: `runs`; `not_well` (`failed`, `unjudged`,
`stopped`); `then_passed_unchanged` (the key passed again in a run starting after the failure and
ending within two hours, its tree unchanged). `lock_evidence.shown` is true only when the span is
at least 7 days (`span-under-7-days`), with at least 50 runs (`under-50-runs`) on at least 5 days
(`runs-on-under-5-days`), at least 20 alone (`under-20-alone`), at least 5 re-passed failures beside
another run (`under-5-repassed-beside`), at a rate at least 3 times the alone rate and above zero
(`rate-not-3-times-alone`); `why_not` names each that failed. It shows a correlation, never a cause,
and is a proposal, never a switch.

Not seen: Codex sessions, terminal runs outside a session, results behind `| tail`, runs not
through `jst test`, runs with no or a moved key.

## 10. Versions, checks and practices ([W-037](../decisions/W-037-current-go-and-dependencies.md))

Each was the newest release at its source when pinned:

| what | version | released |
|------|---------|----------|
| Go (`go.mod`: `go 1.27.0`, `toolchain go1.27.4`) | 1.27.4 | latest stable on go.dev/dl when pinned |
| mvdan.cc/sh/v3 (the guard's parser) | v3.14.1 | 2026-09-06 |
| golang.org/x/sys (`flock`, the process table on darwin) | v0.50.0 | 2026-11-06 |
| golang.org/x/sync (`errgroup`) | v0.25.0 | 2026-10-15 |
| github.com/google/go-github/v93 (`internal/githubapi`) | v93.0.0 | 2026-10-15 |
| go.opentelemetry.io/otel, `/trace`, `/sdk`, and `proto/otlp` with protobuf | [observability.md](observability.md) section 1 | |
| goreleaser (`go run` pinned in the justfile and `release.yml`) | v2.20.2 | 2026-11-12 |
| golangci-lint | v2.15.0 | 2026-10-16 |
| govulncheck (golang.org/x/vuln) | v1.9.0 | 2026-10-11 |
| gosec | v2.30.0 | 2026-10-23 |
| golang.org/x/net (`internal/sitelinks`'s HTML parser; not in the binary) | v0.63.0 | 2026-11-17 |
| Hugo (`go run` pinned in the justfile; the site, section 6) | v0.170.1 | 2026-11-17 |
| the Hextra theme (`site/go.mod`) | v0.13.0 | 2026-09-29 |
| FlexSearch (vendored in `site/assets/lib`, the version Hextra names) | 0.8.143 | the npm registry |
| actions/checkout, actions/setup-go, extractions/setup-just (`gate.yml`, `release.yml`, `pages.yml`) | v7.0.1, v7.0.0, v4 | pinned by full commit hash |
| actions/configure-pages, actions/upload-pages-artifact, actions/deploy-pages (`pages.yml`) | v6.0.0, v5.0.0, v5.0.1 | pinned by full commit hash |

A tool runs pinned (`go run module/cmd@version`), never `@latest`; an update changes its line here
and in the justfile (and `release.yml`). Each workflow action is pinned by its full commit hash,
its version in a comment.

**Checks** are `just` recipes ([W-035](../decisions/W-035-justfile-not-makefile.md)): `check`
(gofmt over `cmd` and `internal`, `go vet`, golangci-lint v2 per `.golangci.yml`, shellcheck on
`install.sh`), `go` (`tests/go.sh`: `go vet`, every test with `-race`, a build), `integration`
(`//go:build integration`, real git or processes, `-race`), `security` (govulncheck; gosec at high
severity and medium confidence, `.claude/` left out) and `docs` (links and anchors, every decision
listed, the site built with no warning or broken link: section 6). **The gate**
([W-040](../decisions/W-040-python-frozen-tests-by-change-readable-names.md)), `just gate` (the
`gate` scope), runs `check`, `go`, `integration`, `security` and `docs` in order, stops at the first
failure, and prints `Ran N tests`. Outside it: `terminal` (needs a terminal the sandbox refuses; run
before an install) and `guard-latency` (timing is no pass or fail on a shared machine). The justfile
sets `GOCACHE`, `GOMODCACHE`, `GOLANGCI_LINT_CACHE` and `HUGO_CACHEDIR` for every recipe.

**The workflow** `.github/workflows/gate.yml` runs `just gate` on `ubuntu-latest` for every pull
request, push to `main` and by hand: Go from `go.mod`'s toolchain line, read-only permissions,
credentials not kept, a 30-minute limit, a pull request's older run cancelled. `main` requires it.
The Vitest producer check runs separately. There is no macOS job
([W-083](../decisions/W-083-no-macos-job-in-ci.md)): `*_darwin.go` is checked by the maintainers'
`just gate` on a Mac before an install and a release.

This recipe set, the `just gate` workflow and the separate `go` and `integration` runs are the state
before the migration to the scope graph; the 0.6.0 target (one Go scope with the integration tests,
one `jst test ci (linux)` job) is [land.md](land.md) section 7.6.

**Practices:**
- The commit is injected at build (`-X …/internal/version.Commit`), else the build info's
  revision (`+changes` dirty, `unknown` none). No logging library: spans in the journal.
- Strict JSON (`internal/strictjson`, `encoding/json/v2`) refuses unknown and repeated members,
  trailing data and oversize. Atomic writes: temporary file, `Sync`, `Rename`.
- Interfaces declared where used, faked by hand; standard `testing`, no assertion library; tests
  clear inherited `JACKSTAY_*` and work in `t.TempDir()`.
- Outside reads retry with jittered backoff inside the context; writes never retry; no goroutine's
  error is lost (`errgroup`). Shipped texts are `//go:embed`ded.
- Not taken: lenient config libraries, RFC 7807 errors, `os.Exit` below `main`, `@latest`, unpinned CI.

## 11. Not in this spec

Ideas kept for later are not specified here, and nothing of them is built until it becomes a spec.
Among them: a mail hook, not built. jackstay keeps no merge queue of its own; a person queues a
change in the host's ([land.md](land.md)).
