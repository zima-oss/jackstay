# jackstay: the current specification

jackstay (`jst`) is one Go binary that keeps a repository worked on by coding agents safe and
measured: a guard hook that refuses destructive commands, test runs that are judged, recorded and
keyed by the tree, background processes with deadlines, numbers and records as files, branch and
GitHub chores, cold reviews, releases, and a local journal of what it did. Rulings that shape the
whole: [W-036](../decisions/W-036-go-native-not-python-mimicry.md) (each command
designed from its spec), [W-040](../decisions/W-040-python-frozen-tests-by-change-readable-names.md)
(readable names; tests by what changed), [W-020](../decisions/W-020-version-tags.md) (one version,
one commit, a tag), [W-013](../decisions/W-013-quality-testing-declarative.md) (declarative; no
test depends on time), [W-017](../decisions/W-017-review-fixes-and-threat-model.md) (the guard is
against accidents, not an adversary).

## 0. This spec and the feature specs

This file specifies the binary as a whole and every command that has no spec of its own. The
features designed apart each have one, and this file points to them rather than repeating them:

| spec | what it holds |
|---|---|
| [guard.md](guard.md) | the guard hook (`jst guard`, `jst-guard`), its rules and `jst trash` |
| [discard-and-clean-gate.md](discard-and-clean-gate.md) | `jst discard`, the guard's advice, `jst test --commit`, the `guard.rules` settings |
| [testing.md](testing.md) | `jst test <scope>`, `list`, `plan`, `last`, the judges and the gate |
| [review.md](review.md) | `jst review` |
| [release.md](release.md) | `jst release`, `jst changes` in a project with releases, `jst lint pr`'s release checks |
| [adopt.md](adopt.md) | `jst adopt`, profiles and the generated AGENTS block |
| [observability.md](observability.md) | the journal, `jst log`, `jst stats` |

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

One binary answers under three names, each a link to it in an installed version folder
(section 7.1): `jst`, `jackstay` (the same command under its long name) and `jst-guard` (argv[0]'s
base name makes it `jst guard`, the Claude Code hook the projects' settings name).

| package | role |
|---------|------|
| `internal/adopt` | `jst adopt`: brings a project to jackstay's reference — `jackstay.json`, the AGENTS block, the guard hook, the workflows ([adopt.md](adopt.md)) |
| `internal/baseline` | `jst baseline`: test runs, merges and destructive commands counted from transcripts, git and the run log; reads only (section 9) |
| `internal/branch` | `jst start` and `jst sync` (section 8) |
| `internal/cards` | the `Card:` line rules, pure, shared by the guard's push rule and `jst lint` |
| `internal/changes` | `jst changes`: one file per changelog entry ([release.md](release.md) section 2; section 4) |
| `internal/check` | `jst check [DIR]`: is a project's `jackstay.json` usable (section 3.2) |
| `internal/cleancopy` | a clean copy of a commit, made and removed: `jst test --commit` ([discard-and-clean-gate.md](discard-and-clean-gate.md) section 3) |
| `internal/cli` | the command table, `jst version`, `jst help`, `--help` (section 2) |
| `internal/config` | `jackstay.json`: found, read strictly, checked against one table of keys and types (section 3) |
| `internal/delivery` | Versioned local checkpoints shared by changelog collection and adoption |
| `internal/discard` | `jst discard PATH…`: back to HEAD, what is dropped kept in the trash ([discard-and-clean-gate.md](discard-and-clean-gate.md)) |
| `internal/docs` | the check of jackstay's own documents (a test only; the gate's `docs` part) |
| `internal/doctor` | `jst doctor` (section 7.7) |
| `internal/expected` | the list of projects whose guard is expected to be heard from: read, changed under a lock, written atomically |
| `internal/flock` | the machine-wide lock heavy runs share: `flock`, tickets, the holder's words |
| `internal/gh` | `jst checks`: waiting on GitHub's checks and runs; the GitHub side of `jst land` (section 8) |
| `internal/gitcli` | the one adapter that runs git: a deadline, a stated environment, typed errors; each consumer declares the small interface it needs |
| `internal/githubapi` | the one GitHub client: go-github with the project's account's token, reads retried, GraphQL for checks and search; `githubfake` for tests |
| `internal/guard` | `jst guard`, the hook that refuses destructive commands ([guard.md](guard.md)); `latency` holds the hook's timing test |
| `internal/homedir` | `~/.jackstay` (or `JACKSTAY_HOME`), its shared `.all` folder and per-project folders, one log line in one write |
| `internal/ids` | `jst id`: one counter of numbers per project on this machine (section 4) |
| `internal/install` | `jst install`, `jst uninstall`, signing at install (section 7) |
| `internal/installscript` | the tests of `install.sh` against a fake release (a test only; section 6) |
| `internal/sitelinks` | the links of the built documentation site, for `cmd/jst-sitelinks` (section 6) |
| `internal/judge` | reads a test command's output and names its outcome: `swift-test`, `steps`, `exit` ([testing.md](testing.md)) |
| `internal/landed` | `jst cards` and `jst lint range` (section 4) |
| `internal/licences` | the licence files of the third-party modules compiled into `jst`, for a release archive (section 6) |
| `internal/layout` | the installed layout under a prefix: `Read` → `State`, the version folders, `Activate`, `Prune`, the install lock (section 7.1) |
| `internal/lint` | `jst lint pr` and `jst lint tree` (section 4; [release.md](release.md) section 7) |
| `internal/lock` | `jst lock` and `jst lock status` (section 5) |
| `internal/logs` | `jst log`: what jackstay did, from its journal ([observability.md](observability.md)) |
| `internal/owner` | `jst owner`: what the owner typed in a Claude Code session, `--stats` (section 8) |
| `internal/patterns` | `jst patterns`: the patterns of a `jackstay.json` that RE2 does not take (section 4) |
| `internal/pinned` | what review's pinned trees and the test's clean copies share: the owner process of a tree, whether it runs |
| `internal/procs` | the note of which Claude Code session a process serves; `jst procs`; `background` is `jst bg` (section 5) |
| `internal/proctree` | a tree of processes known by id and start time, and the one way jackstay stops one |
| `internal/project` | the project a command runs in: its `jackstay.json` folder, sections, name, paths |
| `internal/prove` | the probes of a version folder before it becomes live (section 7.3) |
| `internal/receipts` | `jst test -- COMMAND`, `jst test last`, `jst receipts` (section 9) |
| `internal/records` | `jst new` and `jst view` (section 4) |
| `internal/release` | `jst release …` ([release.md](release.md)) |
| `internal/remote` | which remote a project's work goes to, its default branch as fetched, its GitHub repository |
| `internal/review` | `jst review …`: cold reviews in rounds with optional Codex and Claude Code providers ([review.md](review.md)) |
| `internal/series` | a numbered series: which file names hold its numbers, how an ID is written, front matter |
| `internal/stats` | `jst stats`: named reports over the journal ([observability.md](observability.md)) |
| `internal/strictjson` | the one strict reader of jackstay's own JSON formats (`encoding/json/v2`) |
| `internal/supervise` | starts a command and stays beside it until nothing of it is left: a session without a terminal, or a group given the terminal; `Capture` for a question to another program |
| `internal/telemetry` | OpenTelemetry spans of every command into the project's journal; `journal`, `query`, `mask`, `genai` ([observability.md](observability.md)) |
| `internal/testing` | `jst test <scope>`, `jst test list`, `jst test plan`: scopes, judges, timeouts, the gate of steps ([testing.md](testing.md)) |
| `internal/testrepo` | git repositories for tests: fresh folder, fixed dates and author, jackstay's variables cleared |
| `internal/tidy` | `jst tidy` (section 8) |
| `internal/trash` | `jst trash`: undoable deletion into `~/.jackstay/.trash/` ([guard.md](guard.md) section 10) |
| `internal/version` | the version constant, the one place it lives, and the commit set at build |
| `internal/wait` | `jst wait` (section 5) |

- **Every command** is a package under `internal/` that registers itself in the table (section 2);
  `cmd/jst/main.go` imports it for that alone.
- **A new key in `jackstay.json`** goes into `internal/config` with the command that reads it.
- **A new hook** is a command of the binary, never another argv[0] alias: `jst-guard` stays the
  only alias, because the projects' settings name it (`"$HOME/.local/bin/jst-guard"` for the
  default prefix; `jst adopt` writes the running install's).
- Standard library first; a well-kept library where it does a job better than jackstay's own code
  would (the guard's shell parser, `mvdan.cc/sh/v3`; GitHub, `go-github`; spans, OpenTelemetry's
  Go SDK). No package imitates another language's runtime
  ([W-036](../decisions/W-036-go-native-not-python-mimicry.md)).
- **Names say what they hold**
  ([W-040](../decisions/W-040-python-frozen-tests-by-change-readable-names.md)): no single-letter or
  cryptic identifier in the framework's code — parameters, locals, receivers and named results
  included; tests may use them. golangci-lint's `varnamelen` enforces it (`_test.go` excluded),
  review does the rest.
- No package starts, waits for or signals a process except through `internal/supervise` or
  `internal/gitcli`.
- `CGO_ENABLED=0`: one static file per platform. macOS and Linux are supported
  ([W-069](../decisions/W-069-jackstay-supports-linux.md)); five files are built for macOS only
  (`*_darwin.go` in `procs`, `receipts`, `supervise`, `telemetry`, `testing`), with Linux variants
  beside them.

## 2. The command table

`jst ARGS…` looks up `ARGS[0] ARGS[1]` where a command has two words (`lint pr`), else `ARGS[0]`,
in one table that each command's package fills in its `init` (`cli.Register`); the longer entry
wins, and two entries of one name are a programming error. There is nothing behind the table: a
command not in it is answered with a sentence naming it and the usage on standard error, exit 2.
`jst` with no command prints the usage with exit 2, `jst help` with exit 0. `jackstay ARGS…` is the
same. `jst-guard ARGS…` (argv[0]'s base name) is `jst guard ARGS…`.

Help is the table's: `jst help` lists every command with its summary; `jst COMMAND [WORD] --help`
(or `-h`), given as the only word after the command's name, prints its summary and usage and runs
nothing; `jst help COMMAND [WORD]` is the same; the first word of two-word commands with `--help`
(`jst changes --help`) lists those commands. Each usage names every option, the exit statuses
beyond 0, 1 and 2, and an example ([adopt.md](adopt.md) section 9). `jst version` prints
`jackstay <version> (<commit>)`.

Every command runs inside its span ([observability.md](observability.md)): begun before it runs,
ended with its exit status; a panic is recorded with its stack and then goes on.

The guard's hook answers exit 0 in every case and fails open on its own errors
([guard.md](guard.md) section 7). A binary that cannot start (removed, wrong architecture)
gives the shell's 126/127, which Claude Code shows and does not block on.

| command | specified in |
|---------|--------------|
| `adopt` | [adopt.md](adopt.md) |
| `baseline` | section 9 |
| `bg` | section 5 |
| `cards` | section 4 |
| `changes check`, `changes collect`, `changes new`, `changes view` | section 4; [release.md](release.md) section 2 |
| `check` | section 3.2 (the `test` warnings: [testing.md](testing.md)) |
| `discard` | [discard-and-clean-gate.md](discard-and-clean-gate.md) section 1 |
| `doctor` | section 7.7 (the adoption drift: [adopt.md](adopt.md) section 10) |
| `gh` | section 8 |
| `guard` (hook, `check`) | [guard.md](guard.md); its rules per project: [discard-and-clean-gate.md](discard-and-clean-gate.md) section 9 |
| `help`, `version` | section 2 |
| `id` | section 4 |
| `install`, `uninstall` | section 7 |
| `lint pr` | section 4; [release.md](release.md) section 7 |
| `lint range`, `lint tree` | section 4 |
| `lock`, `lock status` | section 5 |
| `log` | [observability.md](observability.md) section 12 |
| `new`, `view` | section 4 |
| `owner` | section 8 |
| `patterns` | section 3.2 |
| `procs`, `procs stop` | section 5 |
| `receipts`, `receipts key`, `receipts list`, `receipts stats` | section 9 |
| `release notes`, `release override`, `release pr`, `release prepare`, `release publish`, `release yank` | [release.md](release.md) |
| `review`, `review check`, `review last`, `review list`, `review prune`, `review show` | [review.md](review.md) |
| `start`, `sync` | section 8 |
| `stats` | [observability.md](observability.md) sections 11 and 12 |
| `test <scope>`, `test list`, `test plan`, `test last` | [testing.md](testing.md); `--commit`: [discard-and-clean-gate.md](discard-and-clean-gate.md) section 3 |
| `test -- COMMAND` | section 9 |
| `tidy` | section 8 |
| `trash` | [guard.md](guard.md) section 10 |
| `wait` | section 5 |

## 3. State and the project file

### 3.1 The user-level folder: `~/.jackstay`

State lives in one user-level folder, `~/.jackstay`, found from the account's home in the password
database and not from `HOME` ([W-008](../decisions/W-008-state-in-dot-jackstay.md); the name since
[W-066](../decisions/W-066-jackstay-zima-oss-apache.md)). `JACKSTAY_HOME` replaces it; it must be
an absolute path, so every checkout names the same folder (the tests use it). `~/x` in a setting
is expanded from the account's home too.

What every project shares is under `.all`. What is one project's is under the project's **marker
name**: every small letter, digit, `_`, `-` and a `.` that is not first stays; a capital letter is
`^` and the small letter (`WebApp` is `^web^app`: macOS's disk does not tell `Demo` from `demo`);
every other character is `%XX` of its UTF-8 bytes; an empty name is `unknown`. So no project
becomes `.all`, leaves the folder, or shares a folder with another.

    .all/runs.jsonl                         the run record: every start and end of a `jst test` run, reuses, would-be reuses (section 9)
    .all/heavy-suite.lock                   the machine-wide lock (section 5.4)
    .all/heavy-suite.lock.holder            who holds it
    .all/heavy-suite.lock.queue/            who waits, one ticket each
    .all/expected-projects                  the list `jst doctor` checks (section 7)
    .all/codesign-identity                  the signing identity `jst install` uses (section 7)
    .all/procs/<id>.json, <id>.stop, sessions/   what `jst bg` supervises (section 5.1)
    .all/reviews/                           `jst review`'s shared state (review.md)
    .all/telemetry/                         the journal of calls with no project (observability.md)
    .trash/                                 `jst trash` (guard.md section 10)
    <project>/receipts/<key>.json           receipts (section 9)
    <project>/receipts/<key>.failed.<when>-<process>   a failure of that key, one file each
    <project>/outputs/<run_id>.log          the output of the newest runs (section 9)
    <project>/durations.jsonl               per-class durations (testing.md section 4)
    <project>/gate-marks/, gate-bars/       the gate's step marks and bars (testing.md section 5)
    <project>/clean-copies/                 copies `--commit` makes (discard-and-clean-gate.md)
    <project>/guard-seen                    touched by every hook call from a session (guard.md)
    <project>/ids/<series>.jsonl            the number counters (`jst id`)
    <project>/reviews/                      `jst review`'s rounds (review.md)
    <project>/telemetry/                    the project's journal (observability.md)

A log line is one JSON object appended in a single write, so lines of several processes never
mix; a log that cannot be written never fails the tool that writes it (each tool says what it does
instead). Claude Code's Bash sandbox writes only the folders in `sandbox.filesystem.allowWrite`, so
`~/.jackstay` must be in that list for a run inside a sandboxed session to be recorded; `jst doctor`
says so when it is not (section 7).

Everything in `~/.jackstay` may be written by every session of this account: receipts, step marks
and the run record can be forged. That is a known limit, not a boundary (guard.md).

### 3.2 The project file: `jackstay.json`

A project says what it wants in `jackstay.json` at the root of its checkout, committed, so every
worktree has it. The file used is the nearest `jackstay.json` at or above the folder a command
runs in; anything of that name stops the search, also a folder or a broken link, so a project
above never stands in for one whose file cannot be read.

The file is read strictly: one JSON object, no key written twice in one object, every number
finite. `"jackstay_schema"` is required: 1 or 2. The sections `cards`, `ids`, `records`,
`changes`, `procs`, `release`, `github`, `review` and `adopt` need 2. A file written for a later
jackstay (a higher number) is refused with the advice to upgrade, never half-read. `project`, a
non-empty string, names the project; without it the name is the main checkout's folder name (a
worktree's `.git` file names it), else the folder's own. Inside a clean copy that `--commit` made,
the copy's marker names the project instead (discard-and-clean-gate.md).

The sections, each optional, and the keys of each (one table, `config.Table`):

| section | keys | read by | specified in |
|---|---|---|---|
| `guard` | `regenerable`, `deny[]` (`id`, `env`, `command`, `unless`, `why`, `instead`), `rules` (per rule id: `level`, `paths`) | the guard; `jst tidy` (`regenerable`) | guard.md |
| `lock` | `enabled`, `also_hold[]` (`path`, `held_env`) | `jst lock`, `jst test` | section 5.4 |
| `receipts` | `inputs`, `extra_files`, `exclude`, `env`, `refs`, `merge_bases`, `toolchain`, `include_head`, `no_reuse_branches`, `max_age_hours`, `commands[]` (`argv`, `argv_prefix`, `reuse`, `evidence`, `veto`, `refs`, `merge_bases`) | `jst test`, `jst receipts` | section 9 |
| `test` | `judge`, `timeout`, `expect`, `leftovers[]` (`glob`, `fail`), `scopes`, `steps` | `jst test <scope>`, `list`, `plan` | testing.md |
| `baseline` | `kinds[]` (`name`, `pattern`, `gate`), `merge_unit`, `branch`, `transcripts` | `jst baseline` | section 9.6 |
| `github` | `account`, `remote`, `repository`, `wait` (`timeout_seconds`, `interval_seconds`, `log_lines`) | `jst checks`, `jst land`, `jst tidy`, `jst sync`, `jst start`, `jst lint pr`, `jst release` | section 8.1 |
| `procs` | `default_minutes`, `max_minutes` | `jst bg` | section 5.1 |
| `trash` | `keep_days` | `jst trash` | guard.md section 10 |
| `cards`, `ids`, `records`, `changes` | section 4 | `jst lint`, `jst id`, `jst new`, `jst changes`, the guard's push rule | section 4 |
| `release` | `merge`, `branch`, `base`, `tag`, `first`, `files[]`, `build` | `jst release` | release.md |
| `review` | `reviewers`, `use`, `brief`, `template`, … | `jst review` | review.md |
| `adopt` | `pin`, `profile`, `exceptions` | `jst adopt` | adopt.md |
| `telemetry` | `keep_days`, `compress_after_days`, `commands` | the journal | observability.md |

A new key comes with the command that reads it, into the table in `internal/config`.

**Checked in two passes, section by section.** First every key against the table: an unknown key
(with the keys allowed there named), a value of the wrong type (a non-empty string, a whole
number, a number above 0, a boolean, a list, an object), a pattern that does not compile. Then
each section's meaning, where its types are right:

- `receipts`: a `commands` entry without exactly one of `argv` and `argv_prefix`, or naming no
  word; `"reuse": true` with `argv_prefix`, or without an `evidence` pattern; an `evidence` pattern
  without a group (it could not count); `"reuse": true` in a file that does not write out
  `receipts.env` and `receipts.toolchain` (each may be an empty list);
- `lock`: an `also_hold` entry without `path` or `held_env`, with a `path` that is not absolute
  (or `~/…`), or with a `held_env` that is not a variable's name;
- `guard`: a `regenerable` entry that would make everything deletable (`*`, `**`, `.`, `..`, `/`,
  `*/*`, `**/*`); a `rules` id that is not a built-in rule, a level other than `refuse`, `warn` and
  `off`, a level set for a rule that says a command cannot be judged, `paths` on a rule that takes
  none, a path exception that names no folder below the project's top or below `~/`, or lies in
  `.git` ([W-053](../decisions/W-053-guard-rules-configured-per-project.md),
  [W-055](../decisions/W-055-every-guard-rule-configurable-now.md));
- `baseline`: two `kinds` of one name; a `merge_unit` other than `pr` and `commit`;
- `test`: a scope that lists `steps` while `receipts.toolchain` is not written out; the rest of its
  meaning (placeholders, judges, steps naming nothing or themselves) is `internal/testing`'s and
  `jst check` asks it (testing.md);
- `github`: section 8.1;
- `telemetry`: days from 1 to 3650, `keep_days` not below `compress_after_days`, `commands`
  `masked` or `skeleton`;
- the sections of section 4: section 4 (a `records` series without an `ids` entry, for example).

**A section is usable or not on its own.** A misspelt safety setting must not be a setting that
is silently off, so a problem never lets a section be half-read; but a broken section takes
nothing away from a command that does not read it. The file's own keys (`jackstay_schema`,
`project`, an unknown section) always count: with one of them wrong, every command that reads the
file stops. Otherwise each command names the sections it reads and is stopped only by those
(`jst test -- …` reads `lock` and `receipts`, so a broken `test` section stops every
`jst test <scope>` and not it; `jst lock` reads `lock`; `jst sync` and `jst start` read `github`).
A section a command does not need and that cannot be used is left out of what it reads. How each
command fails on its section is in its own section: the guard applies its built-in rules only and
says so on every call (guard.md); `jst lock` and `jst test` exit 2 and run nothing
(`not-started`); `jst tidy` notes it and does not ask GitHub, or does not apply
`guard.regenerable`.

Without a `jackstay.json` the guard applies its built-in rules, `jst lock` and `jst test` name the
project after the main checkout's folder, and `jst test -- COMMAND` is a recorder and nothing
more: no lock, no key, a `start` and a `run` line per run with its outcome and seconds; with
nothing to count its tests, a run that ends 0 is `exited-0`, never `passed` (section 9).

**Patterns are RE2**, Go's dialect ([W-036](../decisions/W-036-go-native-not-python-mimicry.md)):
no lookaround, no backreferences.

    jst check [DIR]

says whether the project's `jackstay.json` (found at or above `DIR`, default the working folder) is
usable, for the project's own gate. It judges every section, also those no command run here would
read, and then the rules other packages own: `review`'s, the `test` section's meaning, and with a
`changes` section the release rules (which read the tree). Exit 0 when the whole file is usable;
2 naming every problem, or when there is no file. A malformed file (a list where an object
belongs) is a named problem, not a crash. On a usable file it also warns about the `test`
section: a scope or step that runs with no timeout (none of its own, none for the section), and a
scope that runs a step measured above 60 s (the median of the run record) before one measured
below 10 s.

    jst patterns [DIR...]

lists every pattern of each `jackstay.json` that RE2 does not take, with where it is and why;
whatever else is wrong with the file is not its concern. Exit 0 every pattern is RE2, 1 some are
not, 2 a file is missing or is not JSON.

## 4. Git and files: `jst cards`, `jst lint`, `jst id`, `jst new`, `jst view`, `jst changes`, `jst check`

These commands read the repository on every call and keep almost nothing of their own: what a
change was for is read from commit messages, numbers and records are files and file names. Scope
and rulings: [W-004](../decisions/W-004-stage-0-and-ground-rules.md),
[W-009](../decisions/W-009-stage-1-scope-history-and-remote.md); state under `~/.jackstay`:
[W-008](../decisions/W-008-state-in-dot-jackstay.md). The problems they remove were measured on the
work of projects that adopt jackstay: merge conflicts in shared files, numbers handed out twice,
cards closed by extra commits, hand-written waiting loops.

**The project file.** Five sections of `jackstay.json` belong to these commands and to `jst bg`
(section 5.1); each needs `"jackstay_schema": 2`, and a file that has one of them with schema 1 is
refused as a whole, so an older jackstay never half-reads it.

```json
{
  "jackstay_schema": 2,
  "project": "example",
  "cards": {
    "dir": "docs/work/tasks", "id": "T-[0-9]{3,}",
    "unit": "pr", "branch": "main",
    "since": "2026-11-20", "first_new": "T-170",
    "exempt_branches": ["bot/updates"], "snapshots": "docs/work/landed"
  },
  "ids": {
    "T":  {"files": "docs/work/tasks/T-{n}-*.md", "width": 3},
    "CHK": {"files": "docs/work/checks/LC-{n}-*.md", "width": 3, "also_in": ["docs/work/check-ledger.md"]}
  },
  "records": {
    "CHK": {"dir": "docs/work/checks", "template": "docs/work/templates/check.md",
           "columns": ["id", "card", "status"], "before": "docs/work/check-ledger.md"}
  },
  "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}"},
  "procs": {"default_minutes": 30, "max_minutes": 240}
}
```

`jst check` (and every command that reads a section) makes the file unusable, naming the key,
when: a key is unknown; `cards` lacks `dir`, `id`, `unit` or `since`; `cards.unit` is not `pr` or
`commit`; `cards.since` is not a `YYYY-MM-DD` date; `cards.id` does not compile as RE2 or carries
inline flags (it is used inside larger patterns); `cards.first_new` does not match `cards.id` or
does not end in its number; a series name is not letters and digits starting with a letter;
`ids.<S>.files` is missing, holds `{n}` other than exactly once, puts `{n}` or a glob in the
folder rather than the file's name, or leaves the repository; `width` is not 1 to 9; a `records`
series has no `ids` entry, lacks `dir` or `template`, or its `ids.<S>.files` is not
`<dir>/<S>-{n}-*.md` (the name `jst new` writes); `changes` lacks `dir`, `into` or `heading`, its
`heading` has no `{version}`, or `newest` is not `first` or `last`; `procs.default_minutes` is
above `procs.max_minutes`. Every path the file names is written from the repository's root and
stays inside it; links are followed and refused when they lead out.

Two marks say where the rules begin, and neither is read from git, so no rewrite moves them: a
**commit** is new when its date is on or after `cards.since`; a **card** is new when its number
is `cards.first_new` or above (same prefix).

### 4.1 The `Card:` line: the convention, `jst cards`, `jst lint`

Without it, a card records its commits as hashes typed into its front matter; many such hashes
are not on the default branch, and a squash merge's hash exists only after the merge, so a card
that must list it is closed by a further commit (measured).

#### The convention

A change that reaches `cards.branch` says which cards it is work for, in its message, on lines of
their own that start at the first column:

    Card: T-140
    Card: T-140, T-141
    Card: docs/work/tasks/T-140-empty-state.md
    Card: none (handoff after the release)

- `unit: "pr"`: the lines are in the pull request's description; a squash merge with the
  description as the commit's message carries them to the branch.
  `unit: "commit"`: the lines are in each commit's message.
- One or more cards, separated by commas; several `Card:` lines add up. A card is named by its ID
  or by its file's path in `cards.dir` (`./` allowed; the ID is the start of the file's name,
  followed by `-` or `.`; the file ends in `.md`). Backquotes around an item and a full stop after
  it are allowed, nothing else. An ID named twice counts once.
- Each ID matches `cards.id` and has a card file directly in `cards.dir` in the tree judged.
- `none` needs a non-empty reason in brackets and stands alone: a message with a `none` line and
  a line naming a card is refused.
- Work for another project's card is written against this project's own card, or as
  `Card: none (<other project> <ID>)`. An ID of another project is not a card here.
- Line ends are normalised before reading (a description edited on GitHub may carry carriage
  returns).
- The line says "this change is work for that card". It does not say the card is finished: a card
  built in four pull requests has four. `state` stays in the card, set by hand (W-004).
- A new card keeps `"commits": []`; nothing writes a hash into a card numbered `first_new` or
  above.

**The message is read by lines, not by git's trailer parser.** GitHub appends its own paragraph
(`Co-authored-by: …`) after the description, and git then no longer reads `Card:` as a trailer
(measured). jackstay reads every line
that starts with `Card: `. The cost: a line of prose that starts with `Card: ` is read too, and is
refused unless it parses, which is the safe side.

The reading is one package, `internal/cards`, shared by `jst lint pr`, `jst lint range`,
`jst cards`, `jst land` and the guard's push rule, so they never judge one message two ways.
Its rule ids: `card-line-missing`, `card-line-unreadable`, `card-none-reason`, `card-unknown`.

#### `jst cards`: what landed, read from history

    jst cards [ID...] [--json] [--ref REF]   each card: its state (from the file) and what landed for it
    jst cards --check [--json]               only the disagreements below; exit 1 when there is one
    jst cards --stale [--commit-map FILE]    hashes in cards' `commits` that are not on the branch
    jst cards snapshot [--write]             section 4.4
    jst cards compare FILE                   section 4.4

History is read from `cards.branch` (default `main`): the remote-tracking copy when it is at or
ahead of the local branch (a worktree's local branch is often behind), the local branch when it is
ahead, the remote-tracking one when they went apart; `--ref` overrides. It is read along the first
parent where `unit` is `pr`, and every non-merge commit where it is `commit` (a merge of the
default branch into a branch, fast-forwarded, would otherwise hide earlier commits behind a second
parent). Commits are dated by their **author date** here.

"Landed" for a card is every such commit whose message has a `Card:` line naming it, joined with
the entries of every snapshot in `cards.snapshots` (section 4.4). Each entry gives the commit's
date, author, subject, pull-request number (the `(#N)` its subject ends in, when it does) and
current hash. The hash is printed, never stored.

- A commit on or after `since` is read strictly, as above.
- A `Card:` line in a commit before `since` is read loosely: every ID of this project found on the
  line counts, whatever else it holds; a line with none is passed over.
- A commit before `since` with no `Card:` line: an ID in its **subject** is reported as
  `named in the subject`, apart from `landed`. It is a mention, not a statement — subjects often
  name cards they are not work for — and it is shown because it is all old history holds.

`--check` reports and changes nothing:

| Kind | Disagreement | Read as |
|---|---|---|
| `no-card-line` | a commit on or after `since` with no `Card:` line | something reached the branch past the check: a direct push, a merge made with another message |
| `unreadable-card-line` | a commit on or after `since` whose line does not parse, or mixes `none` with a card | a line the check did not see |
| `no-card-file` | a `Card:` line naming an ID with no card file | a typo the check did not see |
| `closed-with-nothing-landed` | a new card that is `done` or `verified` with nothing landed | closed without a change, or the change carries another card's line |
| `ready-with-work-landed` | a card `ready` with something landed | work on a card nobody opened, or the state was not moved |
| `hashes-in-a-new-card` | a new card with anything in `commits` | the old habit |

`--stale` lists each entry of a card's `commits` that is not a hash or not an ancestor of the
branch; with `--commit-map` (the file `git filter-repo` leaves) it prints the new hash beside each
old one. It edits no card: cards are the project's.

#### `jst lint`: the check

    jst lint pr (--event FILE | --pr N | --body-file F) [--rev REV] [--base B] [--head H] [--head-branch NAME]
    jst lint range A..B          every commit in the range has valid Card lines
    jst lint tree [--rev REV]    the tree's rules only

**Rules for the lines** (`lint pr`, `lint range`): at least one `Card:` line; each parses; every ID
has a card file in the tree judged; `none` has a reason and stands alone. A pull request with no
description fails with `card-line-missing`.

**Rules for the tree** (`lint tree`, and part of `lint pr`), with their ids:

| Rule | What it refuses |
|---|---|
| `duplicate-number` | two files of one series with the same number, counted as `jst id` counts (section 4.2): any file in the series' folder whose name begins as the pattern's up to the number holds it (`T-170.md`, `T-170-x.md`, `T-170-x.md.orig`); a number too large to count is refused too |
| `new-card-commits` | a new card whose `commits` is not an empty list |
| `snapshot-changed` | a snapshot file that differs from the commit that first added it |
| `snapshot-deleted` | a snapshot file once added and now gone |
| `record-out-of-place` | a record file (`<S>-<n>[-…].md`) outside its series' `dir` |
| `frozen-ledger-row` | a table row (`|` …) of a series' `before` file naming a number at or above the lowest record file the series has ever had (in the tree or in the history behind it); with no record file ever, nothing is refused |

A tree whose `cards.dir` holds no file is not guessed at: exit 2. The tree's rules follow the
judged tree's own `jackstay.json`; the pull request's line rules and exemptions follow the base's
(so a pull request cannot exempt or loosen itself). A pull request whose head branch is in the
base's `cards.exempt_branches` is judged by the tree's rules only (a bot's branch, whose
description the bot writes). `jst lint pr` also applies the release rules and judges changelog
entries: [release.md](release.md) section 7, which owns its inputs (event, `--pr`, a queue copy,
the merge with the base) and the release branch's own rule.

`lint range` judges only commits whose **committer date** is on or after `since` (all of them
without a `since`), so that an old author date does not exempt a commit pushed now; a commit
rebased after `since` therefore needs a line. Card files are read from the range's tip. It judges
every commit it is given and does not read `unit`: it is for what is pushed to `cards.branch`. In a
`pr` project a branch's commits carry no line, so `lint range origin/main..HEAD` on a branch is red
by design; such a project checks the description (`lint pr`) instead.

Exit: 0 fine; 1 a rule broken, each named with its rule id and the line or path (for `pr`, also a
head that does not merge with its base without conflicts); 2 the request or `jackstay.json` is
unusable; 5 an event file that is not what GitHub documents (`pr`), or a `git log` record of another
shape (`range`).

#### Where the check runs

- **On the pull-request runner**: the `card-line` workflow that `jst adopt` writes
  ([adopt.md](adopt.md)). It listens for `opened`, `reopened`, `synchronize`, `ready_for_review`
  **and `edited`** (the description is what is checked), checks out the merge with the base,
  downloads the pinned jackstay release with its checksums, and runs
  `jst lint pr --event "$GITHUB_EVENT_PATH"`. It is a workflow of its own, so an edit of the
  description restarts only this check. It runs only for branches of the same repository.
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
- **Before a push to the branch itself**: the guard refuses a `git push` that would move
  `cards.branch` unless every commit it adds has valid lines (`cards-push`), and refuses what it
  cannot read about such a push (`card-push-unreadable-config`): [guard.md](guard.md)
  section 5. A project that pushes to its branch runs `jst lint range origin/main..HEAD` and
  `jst lint tree` in its gate, so the answer comes before the push is tried.

#### Failure behaviour

| What goes wrong | What happens |
|---|---|
| The description has no `Card:` line, or an ID without a card | The check is red and names it; `jst land` does not merge. |
| The pull request is merged anyway (an unprotected branch, the button on GitHub) | Nothing stops it. `jst cards --check` lists the commit afterwards. |
| The merge is made with a message of its own (`gh pr merge --body`, a local `git merge --squash`) | The line may be lost: a local squash indents every folded message (tried on a real squash). Listed by `--check`. |
| The runner cannot download jackstay | The step fails, red, with GitHub's words. It is not skipped. |
| The runner's jackstay does not know the project's `jackstay_schema` | Exit 2, red, naming both. |
| GitHub cannot be reached or signed into locally | `jst lint pr --pr` exits 2; `jst land` stops and says so. |
| Two pull requests land at the same moment, each adding a file with one number | Both can pass: each judged the base before the other merged. `jst lint tree` on the branch then fails for everyone, naming both files; one is renumbered by hand. |
| The guard cannot tell what a push would add | Refused, as the guard refuses what it cannot read. |
| The card files are not where `cards.dir` says | `jst cards` and `jst lint` exit 2. Nothing is guessed. |
| A commit is on the branch twice over (a revert and a re-land) | Both are listed; nothing is netted. |

#### What it cannot see

- **A wrong card.** `Card: T-140` on work for T-141 passes. The line is as true as its writer.
- Whether a card is finished, verified or accepted: that is `state`, by hand.
- Work that has not reached the branch: branches and open pull requests are not read.
- A merge the check did not gate; it is only reported afterwards.
- A push from a terminal or a session whose hook is not loaded: seen afterwards by `--check`, not
  stopped.
- Old history: commits before `since` are read loosely; old cards keep their `commits` as they are.
- Work for another project's card, except as the text of a `none` reason.
- Commits off the first-parent line in a `pr` project: only the merge commit's own message is read.

### 4.2 The counter of numbers: `jst id`

"Next free number" taken on a branch collides on the default branch, and sessions renumber or ask
for numbers in chat. So numbers stay sequential and come from one counter on this machine, shared
by every session and worktree ([W-004](../decisions/W-004-stage-0-and-ground-rules.md)).

    jst id next SERIES [--count N] [--json]   hands out the next number(s), recorded: T-169
    jst id list [SERIES] [--json]             what was handed out, when, by which session, and whether a file has it yet
    jst id peek SERIES                        what `next` would print; hands out nothing

Under one lock per project (`flock`, freed by the system with its holder), `next` takes the
highest number it can see, adds one, records it, and prints it only once recorded. "Can see" is
the largest of:

- every number in `~/.jackstay/<project>/ids/<SERIES>.jsonl`;
- every file name in the series' folder in every worktree of the repository as it is on disk,
  committed or not (`git worktree list`);
- the same folder in the tree of every local branch and remote-tracking ref (`for-each-ref`,
  `cat-file --batch-check`, `ls-tree`);
- every number of the series written in the `also_in` files (an old ledger's rows) of the main
  checkout.

A file holds a number when its name begins as the pattern's does up to `{n}` (its `*` and `?` keep
their glob meaning), then the number, then anything but a digit, or nothing (`internal/series`,
shared with `jst lint tree`). So the counter cannot fall behind a number someone made by hand, and
a deleted state folder loses only numbers handed out that have no file yet. A number handed out
and never used is a gap; gaps are allowed and nothing is renumbered. An ID is the series' name, a
dash, and the number padded with zeros to `width`. `--count` takes 1 to 100.

All numbered series of a project go through it: cards, decisions, records; a card's records are
not numbered inside the card ([W-009](../decisions/W-009-stage-1-scope-history-and-remote.md)).

#### Failure behaviour

| What goes wrong | What happens |
|---|---|
| The state folder or the record cannot be written | Exit 2, no number. A number it could not record is never printed; of several, those recorded before are named as gaps. |
| The record holds a line that is not one of its records, or a number above 10^12 | Exit 2, the line named: a partial view could repeat a number. |
| The lock is held by a caller that died | The system frees the `flock`; the next caller goes on. |
| git cannot list a ref or a worktree, a listed worktree's folder is not there, or a ref's tree cannot be read | Exit 2, no number, the place named. A partial view could repeat a number. |
| A file holds a number above 10^12 | Exit 2: the series cannot be counted (never wraps, never skipped). |
| An `also_in` file is missing | Exit 2. |
| `jackstay.json` has no such series | Exit 2, naming the series there are. |
| The caller is killed after the number is recorded and before it is printed | A gap. |

#### What it cannot see

- A number someone typed without asking, until a file with it exists in some worktree or ref on
  this machine. The check before a merge (section 4.1) then finds two files with one number; that
  check, not the counter, keeps a duplicate off the branch, with one hole: two merges at one
  moment.
- Another machine or a cloud session (W-004: one machine).
- A session that does not call it.
- A number used only in prose (a decision cited before its file exists).

### 4.3 One file per record: `jst new`, `jst view`, `jst changes`

Nearly all merge conflicts in the projects measured were in files everybody appends to: ledgers
and changelogs (measured). A record of a series
listed under `records` is therefore one file, and the file is the row.

#### Records

    jst new SERIES --slug SLUG [--card ID] [--set KEY=VALUE]...       takes the number, writes the file, prints its path
    jst view SERIES [--where KEY=VALUE]... [--format table|list|json]  the rendered table, on standard output

A record is `<records.S.dir>/<ID>-<slug>.md`: one JSON object between two `---` lines (front
matter, read strictly: one object, no member twice, nothing after it; line ends normalised), then
a body in the project's own words.

- `jst new` takes one number from the counter (section 4.2), copies the template, sets `id`, and
  `card` when given (it must match `cards.id`; only then is a `cards` section needed), and each
  `--set` key (not `id`); it writes nothing else. A slug is lower-case letters, digits and dashes,
  at most 80. The file is created exclusively, inside the repository. The project's own checker
  stays the judge of what a record must hold.
- `jst view` reads the record files of the working tree, in number order, and prints the
  `columns` (default `id`) and the first line of each body (a heading's `#` taken off); `--where`
  keeps records whose field equals the value (all must hold); `json` gives each record's front
  matter with `summary` and `file`. **It writes no file, and nothing rendered is committed**: a
  committed table is the shared file again.
- The old ledger file (`before`) stays where it is, frozen: its rows keep their numbers and place.
  `jst view` names it under the table and does not parse it; `jst lint tree` refuses a new row in
  it (section 4.1). Whether old rows are ever split into files is each project's decision.
- A record that changes (a check going from `partial` to `passed`) is an edit of its own file. Two
  sessions conflict only when they edit the same record. Rows are not immutable; a correction is
  an edit, not a new file.

#### Changelog entries

`jst changes new`, `view`, `check` and `collect` keep a changelog as one file per entry in
`changes.dir`, written in the commit that makes the change. Typed entries, the next version and
the release pull request are [release.md](release.md) sections 1 to 7. In a project **without** a
`release` section:

- `jst changes new --slug SLUG [--card ID]` writes `<slug>.md` from `changes.template` (optional;
  `{card}` and `{slug}` are filled in), or a one-line entry; `--type`, `--audience`, `--title`
  make it a typed entry.
- `jst changes collect --version V [--date D] [--dry-run]` is run once per release, in the release
  commit: it writes one section under `heading` (`{version}`, `{date}`, the date defaulting to
  today) at the place `changes.newest` says (`last`, the default, or `first`), with the entries in
  the order release.md section 2 gives (untyped ones in the byte order of their file names), and
  removes the entry files (`git rm` for tracked ones). The changelog then has one writer, the
  release. `--dry-run` prints the section and changes nothing. In a project with `release`,
  `collect` refuses (exit 2) and names `jst release prepare`.

#### Failure behaviour

| What goes wrong | What happens |
|---|---|
| No number can be taken | `jst new` exits 2 and writes no file. |
| The template is missing, or its front matter is not one JSON object | Exit 2, the path named. |
| The records folder is not there | Exit 2. |
| A file with that name exists | Exit 2; the number is spent, a gap. |
| A record file cannot be read | `jst view` prints the other rows, then names the file on standard error and exits 1. A table that silently lacks a row would be worse than the conflict it replaced. |
| Two branches add a file with one number | Not a merge conflict. `jst lint tree` is red on the second pull request. |
| `collect` finds no entry | Exit 1. |
| `changes.into` or `changes.dir` has uncommitted changes, an entry is untracked (including ignored files), or the changelog changes while the section is made | Exit 1, nothing collected. |
| An invalid entry | Exit 1, nothing collected, each named. |
| An entry changes while being collected | It is kept and named; exit 1. |
| `collect` is stopped halfway | Before replacing the changelog, jackstay records the current HEAD, the new changelog blob, and the path, mode and blob of each collected entry in the worktree’s Git directory. The same record identifies each retry and retains the latest outcome. A rerun at the same HEAD and changelog removes only unchanged tracked entries named by the unfinished record. An existing heading without a matching unfinished record leaves entries in place (exit 1). |
| Two `collect` runs in one checkout | A lock in the repository's git folder makes them take turns. |
| A release forgets `collect` | The entries stay and go into the next section; `jst changes view` before a tag shows them. |

#### What it cannot see

- Whether a record is true, complete, or what the project's checker wants.
- Two records for one thing; a record that should have been an edit of an older one.
- Shared indexes a project keeps by hand (a task index, a decision index): still shared files.

### 4.4 What a history rewrite keeps

A project may have to rewrite or clean its git history; a command that reads history says what
survives. **The rule: jackstay writes no commit hash into any file it keeps or commits.** Numbers
and records are files and file names; `Card:` lines are read from messages on every call;
background processes are not about git. What follows is about messages (each case below was
tried on real merges).

| Rewrite | `Card:` lines | `(#N)` in subjects | What changes |
|---|---|---|---|
| GitHub squash merge, message from the description | kept | kept | — |
| A plain `git rebase`, `commit --amend` without a new message, `cherry-pick` | kept | kept | every hash; the committer's date (the author's stays) |
| A rebase that folds or drops commits (`fixup`, `squash`, `drop`, `--autosquash`; a commit that becomes empty; merge commits without `--rebase-merges`) | **lost** for each message folded away or dropped | same | as above |
| `git filter-repo` on contents (`--replace-text`) | kept | kept | every hash from the first changed commit; tags move; a hash quoted in a message is rewritten |
| `git filter-repo` on paths | kept in the commits that remain | same | a commit that becomes empty is dropped, its line with it |
| `git filter-repo` with a message callback | whatever the callback leaves | same | — |
| A local `git merge --squash` with the default message | **lost**: every folded line is indented | lost | — |
| A collapse of old history into one commit, a new root, a shallow graft | **lost** | lost | everything |

So: `jst cards` after a rewrite that keeps messages shows the same entries with new hashes;
cards' old `commits` fields are wrong after any rewrite (`--stale --commit-map` prints old and
new; the project decides); hashes quoted in documents are not rewritten; a pull request's page
and `refs/pull/<n>/head` keep the old commits, so `(#N)` still names the right pull request;
records, entries and numbers survive whatever leaves the last tree unchanged, and the counter
counts again from the files.

#### The snapshot

For the one loss git cannot prevent — a collapse — there is a snapshot in a file (W-009).

    jst cards snapshot [--write]   print, or write `<cards.snapshots>/landed-<date>[-n].json`
    jst cards compare FILE         after a rewrite: which of the file's entries history no longer has

The snapshot lists every landed and every `named in the subject` entry: the author's date, the
author, the subject, the pull-request number and the hash **as it was**. It is committed before
the rewrite (in a collapse, in the tree the collapse keeps), written once with exclusive create
(the next free `-n` when the date's name is taken) and never edited: `jst lint tree` refuses a
change to a snapshot after the commit that added it, and its deletion. A later rewrite adds
another file.

`jst cards` shows the union of history and every snapshot in `cards.snapshots`. An entry is the
same when the card and the pull-request number match, or, without a number, the card, the
author's date, the author and the subject (a rebase rewrites the committer's date and keeps the
author's); a commit that only names a card in its subject does not stand for a landed entry. After
a rewrite that kept messages the snapshot adds nothing; after a collapse it supplies what the
messages no longer do. Each entry says which source it came from. Old hashes are a record, never
resolved.

`jst cards compare FILE`, run in the rewritten clone before anything is pushed, lists every entry
the snapshot has and history has not, and says how many commits landed after the snapshot's
`head_then` (they are in no snapshot: write a new one first). It exits 1 for either. The steps
around a rewrite: `jst cards snapshot --write` and commit; rewrite in a fresh clone (the guard
refuses `git filter-repo` and `filter-branch` except in a throwaway clone); `jst cards compare`;
`jst lint tree`; the force-push is the owner's decision.

#### What it cannot see

- A rewrite nobody announced: without a snapshot a collapse loses the old entries, and
  `jst cards` shows fewer without knowing more existed.
- A message changed so that it still parses (`T-140` turned into `T-141`): `compare` reports
  T-140's entry lost and does not say where it went.
- A rewrite that changes subjects or author dates: an entry without a pull-request number no
  longer matches its snapshot entry, `compare` reports it lost, and `jst cards` shows it twice.
- Two entries of one card with the same author, date and subject and no pull-request number are
  one.
- Anything on GitHub.

### 4.5 State files of these commands

| File | Fields |
|---|---|
| `~/.jackstay/<project>/ids/lock` | empty; held with `flock(LOCK_EX)` while a number is taken |
| `~/.jackstay/<project>/ids/<SERIES>.jsonl` | one object per line, appended in one `write` with `O_APPEND`, mode 0600: `series`, `number` (integer, 0 to 10^12), `id`, `at`, `epoch`, `worktree`, `session` (from the session note, section 5.1; `null` when unknown), `pid` (the caller's parent). Read strictly: a line that is not such a record stops the counter. |
| `<cards.snapshots>/landed-<date>[-n].json` (committed in the project) | `format: "jackstay-cards-snapshot"`, `version: 1`, `written`, `branch`, `head_then`, `note`, `entries[]` of `card`, `kind` (`landed` or `named`), `author_date`, `author`, `subject`, `pr` (number or `null`), `hash_then`. Read strictly (one document, no member twice). |
| `<records.S.dir>/<ID>-<slug>.md` (committed) | the template's front matter with `id` (and `card`, and `--set` keys) filled in |
| `<changes.dir>/<slug>.md` (committed) | an entry: [release.md](release.md) section 2 |

`~/.jackstay` is `JACKSTAY_HOME` when that is set (an absolute path). `jst cards`, `jst lint`,
`jst view` and `jst check` keep no state.

## 5. Processes: `jst bg`, `jst procs`, `jst wait`, `jst lock`

### 5.1 Background processes: `jst bg`, `jst procs`

Background commands started by a session (watchers, log followers, servers for a check) outlive
the purpose they were started for; Claude Code gives no hook a process id and says nothing of what
becomes of a background command when its session ends
(measured). So jackstay cannot learn of a
process after the fact: a process is supervised because it was started through `jst bg`.

    jst bg [--for MINUTES] [--name NAME] [--keep] -- COMMAND ...   start, supervise, return the command's status
    jst procs [--all] [--json]                                     what is supervised: id, name, state, age, deadline, session, folder, command
    jst procs stop [--all] ID|NAME...                              stop (TERM, 5 s, KILL): by ID whoever started it; by NAME this session's

`jst bg` is what a session writes where it would write a long-lived background command. It stays
in the foreground of its own Bash call (the session starts it with the tool's background flag)
and:

- runs the command in a **session of its own** without a terminal, and passes a request to stop
  (INT, TERM, HUP, QUIT) on to it; its signal handlers are installed before the command starts;
- **stops it at a deadline**: `--for` (above 0), else `procs.default_minutes` (30), never more
  than `procs.max_minutes` (240; said when the cap applies). The deadline is wall-clock: a Mac
  asleep past it stops the command at the first check after waking. It needs no file, no hook and
  no process list: `jst bg` is the command's parent;
- **stops it when its Claude Code session is gone**: every 30 s it checks that the process it was
  started under is still the same process (id and start time) and not a zombie;
- answers `jst procs stop` through a stop file within a quarter of a second;
- writes `~/.jackstay/.all/procs/<id>.json` at the start and removes it at the end (a file that
  cannot be written is said on standard error; the deadline and the session check still hold);
- with `--keep`, has no deadline and no session check: something the owner wants left running,
  stopped only by `jst procs stop`;
- after the command ends, stops what it left in its session (TERM, 5 s, KILL): nothing supervises
  it after that;
- writes its own lines from a goroutine, so a standard error nobody reads cannot block the
  supervision.

A stop signals the command's group and the members of its session outside the group (found by
their session even when reparented). Before a stop for a deadline or a lost session, it asks
`lsof` (bounded at 20 s) what the group and those members hold open: a regular file open for
writing, from descriptor 3 on (standard output and error are where a watcher's output goes),
outside the temporary folders (`/tmp`, `/var/folders`, `/var/tmp`, `$TMPDIR`) **holds** it — it is
not stopped, its entry says `held` with the files, and it is asked again at each check and stopped
once nothing is held (never reap a process in the middle of a write). Where `lsof` gives no answer, the deadline wins and the command
is stopped all the same, since its starter declared it disposable by then. A stop asked for by
`jst procs stop` makes no exception.

**Which session.** The guard hook runs before every Bash call and is given `session_id`. It notes,
for its nearest ancestor that is not a shell, a wrapper (`env`, `timeout`, `nice`, `nohup`,
`caffeinate`), Python or jackstay, which session that process serves:
`~/.jackstay/.all/procs/sessions/<pid>-<start>`. `jst bg` walks up its own ancestors to the
nearest one with such a note: that is its session and the process it watches. Without a note it
watches its nearest ancestor that may be a session's own, and shows `session unknown`; with no
such ancestor, or no process table, it supervises by the deadline only and says so. Inside the
agent sandbox the process table is readable and `jst bg` signals only what it started, so it works
whole there.

**`jst procs`** lists the entries of the session it is run in (when it can tell which) and says how
many others there are; `--all` lists every one. An entry's state is `running` or `held` while its
supervisor lives (the same process by id and start), `orphaned` when the supervisor is gone and
its group is not, `gone` otherwise. On the way it does what no supervisor is left to do: a gone
entry is removed; an orphan past its deadline whose leader is proven by its start is stopped (or
held, as above); session notes of processes that no longer exist are removed.

**`jst procs stop`** asks a living supervisor through `<id>.stop` and waits up to 15 s for its entry
to go; no process identity is guessed. It signals a group itself only when the supervisor is
proven gone and the group's leader is proven by its start time. A NAME stops this session's
entries of that name; when jackstay cannot tell which session this is, a NAME is refused (two such
terminals would both be "no session"): an ID, or `--all` with the NAME, says which.

Starts, ends, stops, holds and removals are events of the running command's span in jackstay's
journal; the guard marks each Bash call that sets the background flag without `jst bg`, so the
share of unsupervised background calls is known (`jst stats background`;
[observability.md](observability.md)). It refuses none.

A wait that needs no background is not this command's business: `jst checks wait` and `jst wait`
wait in the foreground with a deadline and leave nothing behind. `jst bg` is for what must go on
after the call returns.

Exit of `jst bg`: the command's own status (128+N for signal N); 2 a bad request; 126 the command
could not start; 127 it was not found; asked to stop by a signal, `jst bg` ends by that signal.
`jst procs`: 0; `stop` 1 when a name or id was not stopped; 2 a bad request. `--watch PID` and
`--check-every S` exist for jackstay's own tests only.

#### Failure behaviour

| What goes wrong | What happens |
|---|---|
| The state folder cannot be written | A command with a deadline runs under its usual supervision. For `--keep`, jackstay requests immediate shutdown, since `jst procs stop` would have no record. |
| Cleanup leaves processes or cannot be confirmed | The entry stays as `cleanup-incomplete`. A command that otherwise succeeded returns 1, and `jst procs stop` reports that cleanup was not confirmed. |
| No session note is found | Watched by its nearest possible session process; listed with `session unknown`. |
| No session process can be told, or no process table | Supervised by the deadline only, and said. |
| `jst bg` is killed outright | Its entry stays; `jst procs` shows `orphaned`, and stops it once past its deadline when its leader is proven. |
| The supervisor does not answer a stop within 15 s | Exit 1; the request stays, and it stops the command when it can. |
| An orphan's leader cannot be proven, or this command may not signal it (the sandbox, another account) | Not signalled; the group is named with what to run. |
| The command leaves its session and its process tree (a daemon) | It cannot be caught. |
| A process holds a file open for writing at the deadline | Not stopped; `held`, in `jst procs` and the journal. |
| `lsof` gives no answer at a deadline | Stopped all the same; the journal says the look was not possible. |

#### What it cannot see

- **Anything not started through `jst bg`.** A plain background command is as invisible as before;
  the guard only counts the requests.
- **A subagent that ended while its session lives.** It is bounded by the deadline, not ended when
  the subagent stops: no documented variable tells a command its subagent's id.
- Sessions whose hook writes no note (another agent's sessions): supervised by the deadline and
  the nearest possible session process.
- What a process is for: `jst procs` shows the command and the name it was given.

#### State

| File | Fields |
|---|---|
| `~/.jackstay/.all/procs/<id>.json` | id `<epoch s>-<pid>`; `id`, `name`, `supervisor`, `supervisor_start`, `group`, `started`, `leader_start`, `deadline` (epoch seconds; `null` with `--keep`), `keep`, `session`, `watched{pid, start, name}`, `cwd`, `command`, `status` (`running` or `held`), `held` (the files). Written whole (temporary file, sync, rename), mode 0600 folder 0700. `orphaned` and `gone` are states shown, not written. |
| `~/.jackstay/.all/procs/<id>.stop` | empty: a request to stop, removed with the entry |
| `~/.jackstay/.all/procs/sessions/<pid>-<start>` | `session`, `pid`, `start` (whole seconds), `name`; written by the guard hook, removed once the process is gone |

### 5.2 The process model: signals, deadlines, process groups, the closer

Every command jackstay starts and lives beside goes through one package, `internal/supervise`, and
every stop through one, `internal/proctree`.

**Two ways to start, and only two.**

- A **session** of its own without a terminal: `jst test <scope>` and `jst bg`. It is stopped as a
  session tree: the processes remembered by session and parent links, sampled every second while
  the command runs, found by their session even when reparented to process 1.
- A **process group** of its own, given the terminal when jackstay is in front of one: `jst lock`
  and `jst test -- COMMAND`. It is stopped as the group plus every session recorded in
  `JACKSTAY_LOCK_SESSIONS_FILE` (below). A process that left the group (`setsid`, a double fork, a
  daemon) is not caught and runs on.

The grace between SIGTERM and SIGKILL is each command's: 30 s for `jst lock` and
`jst test --` (`JACKSTAY_LOCK_GROUP_WAIT`, seconds, replaces it), 10 s for a scope's session and for
a recorded session, 5 s for `jst bg`. After SIGKILL the killed have 5 s more to be gone.

**Identity.** A process is known by its id and its start (microseconds): an id the kernel gives
again to a later process is never signalled, and a group or session is signalled only while a
remembered member is in it. The command's leader is not reaped until the end: its end is learnt
without reaping (kqueue's `NOTE_EXIT` on macOS, `waitid` with `WNOWAIT` on Linux), so its id, and
its group's and session's, cannot be given to another process meanwhile. It is reaped last, once
the table shows nothing of its group (and session) but its zombie, or after the last SIGKILL and
its wait, with the survivors named; nothing is signalled after the reap. Where the process table
cannot be read the whole stop (TERM, the grace, KILL, the wait) goes to the group by its id, and
the outcome says that whether anything is left is unconfirmed.

**What the agent sandbox allows** (measured inside it, on macOS): the process table
(sysctl `kern.proc.all`) is readable and `getsid` answers for other processes, but signalling a
process the command did not start fails with EPERM, and `lsof` exits 0 listing only the caller's
own processes, without saying so. So EPERM on a process of this account is reported as "the
sandbox cannot signal it from here: run outside the sandbox"; EPERM on another account's (`sudo`
in a test) as "not this account's", named loudly and not waited for; ESRCH means gone. A view that
rests on `lsof` (`jst tidy`, section 8.4) must prove that `lsof` can see this account's processes,
which the sandbox's silent answer does not. So it is complete only when `lsof` named the folder of
every process of this account that ran throughout its walk: in the table read before it and in the
one read after, as the same process (its start unchanged). A process that started while `lsof`
walked the table (its id new, or given again) counts when `lsof` named it, in the folder or not,
but one it did not reach proves nothing about what `lsof` can see. It stands where a process
started a moment after the look stands. Processes start all the time (a session's `sleep 1` in a
wait loop, `git`, an app's helper): measured 2026-11-18 outside the sandbox, requiring such a process made the view
partial in 2 of 5 calls and kept a finished worktree, and requiring every process of a later table
never let it finish on a busy machine. A session that works in a folder has processes that live
there longer than a look (its agent, its shell, `jst bg`), and those hold it; `--fix` looks again
just before it removes. A folder counts only for a process running after the walk as the one `lsof`
saw. The processes `lsof` did not name make the view partial, and the reason names up to eight of
them (`proctree.View.Unseen`); more is the sandbox, and the reason says so.

**Signals.** One loop owns the requests to stop: SIGINT, SIGTERM, SIGHUP and SIGQUIT, each caught
only when it was not ignored at jackstay's start; an ignored one (SIGHUP under `nohup`) stays
ignored, in jackstay and in the command. Every request is passed on to the group or session (held
until the command exists). The first starts the stop and cancels the run's context with it as the
cause; a later one is passed on and does not shorten the wait. The grace runs from the first
request and bounds the command itself too: a command that ignores the stop (`trap "" TERM`) is
killed with its group when the grace is over, while it still runs, so it cannot hang the caller.
The stop, the clearing of what is left and the record each run under a context of their own,
never one already cancelled. A run that was stopped ends by the signal: for INT, TERM and HUP the
default action is restored and the signal sent to jackstay itself (the caller sees 130, 143,
129); QUIT cannot be raised again without Go's runtime dump, so it exits 131. A SIGTERM ignored
at jackstay's start is not kept for the command: Go does not report it (a written limit).

**After the command's own end.** The lock (and the run's record) is freed only when the command's
group is gone. A command that ended by itself and left processes in its group (`sleep 600 &`, a
server a test forgot) is given 0.3 s to settle; what is still there is named, sent SIGTERM, given
the grace, sent SIGKILL and given 5 s more. A command that was asked to stop or died by a signal:
what is left has the rest of the grace from the first request, then SIGKILL. What is still there
after SIGKILL is named, and the run goes on.

**The sessions file.** `JACKSTAY_LOCK_SESSIONS_FILE` names a file in which a run started under a
group (`jst lock`, `jst test --`) records each session it starts (`jst test <scope>`), one line
`<session id> <its leader's start in microseconds>`. The group's supervisor stops what is left of
those sessions after any end, also when the run that wrote them was killed outright; a session
whose id now belongs to another leader is skipped.

**The terminal.** When jackstay's group is in front of its terminal, the command's group is given
it at the start (the child puts its group in front before exec); a start refused with ENOTTY,
EPERM or EOPNOTSUPP is made again without it (the command never ran). Afterwards jackstay takes
the terminal back with SIGTTOU ignored meanwhile. SIGTSTP (Ctrl-Z) is ignored while a group's
command runs, and while `jst lock` waits in line: a stopped waiter would keep its place, and
everyone's behind it.

**Output.** Both streams go through pipes, read line by line (escape sequences taken out before
any pattern is tried); the command is reaped by `Process.Wait`, never `Cmd.Wait`, and is never
started with `exec.CommandContext` (whose cancel kills the leader only). After the stop the
readers have 10 s to reach the end of the output; a run whose output was not read to its end is
not a pass (section 9.3).

**Concurrency and cleanup.** The readers, the sampling, the deadline and the stop file run under
`errgroup`; no goroutine logs and drops an error. What a run sets up (a caught signal, a pipe, a
file) is undone by one closer, last added first, once; the closer is passed as an instance, never
global.

**No time in tests** ([W-013](../decisions/W-013-quality-testing-declarative.md)). The clock, the
timers, the lock's poll schedule and the stop's pauses are injected: deadlines, graces, `--wait`
and day windows are tested with a fake clock. Real-process tests (build tag `integration`) move by
acknowledgements (a child writes "ready", "got TERM") and use generous outer timeouts only to end
a broken test. The tests that need a terminal (`integration && terminal`, `just terminal`) open
`/dev/ptmx`, which the agent sandbox refuses: they run outside it before an install, not in the
gate.

The environment contract between nested runs: `JACKSTAY_LOCK_HELD` (section 5.4),
`JACKSTAY_LOCK_SESSIONS_FILE`, and `JACKSTAY_TEST_RUN_ID` / `JACKSTAY_TEST_RUN_PROJECT`, which name
the `jst test` run a command runs inside, so a nested `jst test` of the same project is recorded
as part of it (`inside`, section 9.5).

### 5.3 `jst wait`

    jst wait --pid P | --file F [--nonempty] | --gone F | --line RE --in F | --quiet S --in F
             [--fail RE] [--while-pid P] [--new] [--timeout S] [--tail N]

One way to wait for one thing, with a deadline, instead of a hand-written `until …; do sleep`
loop ([W-007](../decisions/W-007-tools-from-the-usage-study.md), from counts of such loops in
agent sessions). Exactly one condition: a process has
ended; a file exists (and holds something); a file or folder is gone; a line of the `--in` file
matches (the line is printed); the `--in` file has not grown for `S` seconds. The deadline is
540 s by default, under a tool call's 10 minutes; `nan`, `inf` and a negative number are refused.
On the way out it prints the matching line and the last lines from the final 64 KiB of the file
(`--tail`, default 20). The `--in` path may start out missing. An existing path must point to a
regular file. If it disappears or changes to another kind while waiting, the wait treats it as
unavailable, and an unavailable path cannot count as quiet.

Pattern matching reads a snapshot of the log in 64 KiB windows and checks the deadline between
them. A line longer than 1 MiB ends pattern matching with exit 1 and names the file and the limit.

- `--fail RE` (needs `--in`) ends at once with 1 on a line such as `error:`, and wins over a
  wanted line read in the same piece of output; it counts even before its line end. With `--pid`
  the process's last words are looked at for it too.
- `--while-pid P` ends with 1 when the writing process dies before the condition, after one more
  look (its last line, ended or not, still counts); `--quiet` is not cut short by it.
- `--new` counts only lines written after `jst wait` started.
- A file cut short, replaced by another (a new inode), or written over in place without growing is
  read again from its start.
- A process that has ended but is not yet reaped (a zombie) counts as ended. The first known
  start time identifies the PID, so a replacement process with the same PID does not keep the
  wait alive. When the kernel withholds a process row, the wait can check only whether the PID
  exists.

Exit 0 it happened; 1 it cannot happen any more, or a `--fail` line was seen; 2 the request is
wrong; 3 the deadline came first.

### 5.4 The machine-wide test lock: `jst lock`

**Off by default** ([W-005](../decisions/W-005-lock-off-by-default.md)): `jst test` takes no lock.
It is turned on for one project by `"lock": {"enabled": true}` in its `jackstay.json`, or for one
call by `jst test --lock`; `--no-lock` turns it off for one call. `jst lock -- COMMAND` is the
explicit tool and always locks. The lock is turned on for a project only when simultaneous runs
are seen to cause a problem there, as `jst baseline` measures it (section 9.6); nothing turns it
on by itself. Until then a test that fails on a busy machine is fixed as a test. What counts as
heavy is each project's decision on the day it turns the lock on; focused runs do not take it.

    jst lock [--project NAME] [--wait SECONDS] [--also-hold PATH=VARIABLE]... [--note-words WORDS] -- COMMAND [ARG ...]
    jst lock status [--json]

`jst lock -- COMMAND` runs a heavy command holding one `flock` shared by every checkout of every
project on this machine: `~/.jackstay/.all/heavy-suite.lock` (`JACKSTAY_LOCK`, an absolute path,
replaces it). The command runs in a process group of its own given the terminal (section 5.2).

- **In turn.** Runs take their turn in the order they came: a ticket each in
  `heavy-suite.lock.queue/`, named by the clock's nanoseconds and the process id, made under a
  `making-…` name and renamed into place once locked. A dead run's ticket is passed over (nobody
  holds it); a `making-…` file a run killed while it made its ticket left is removed by the next
  run once it is a minute old and nobody holds it, and so is a ticket numbered more than a day
  ahead of the clock.
- **Polling.** A waiting run looks again after 0.05 s, then 1.5 times longer each time, up to once
  a second (five times a second for the run that is next), and starts over when the line moves.
  Every minute it says that it waits, for how long, how many are ahead of it, and who last took
  the lock.
- **Freed only when the command's group is gone**, after any end (section 5.2): TERM, the grace
  (30 s), KILL, 5 s. If something is still there after SIGKILL the lock says so, names it, and is
  freed. A process that left the group runs on without the lock.
- **Nested runs.** The command gets `JACKSTAY_LOCK_HELD=<realpath of the lock>:<pid>`. A locked
  command started under a holder does not take the lock again; the claim is proved, not believed:
  it names this lock, the lock is held, the named process lives and is an ancestor (where the
  table cannot be read, the holder note must name it as "(the lock's PID)").
- **The holder note** (`heavy-suite.lock.holder`): `project P: WORDS in DIR, the command's process
  group G (the lock's PID), since …`. It is written only once every lock the run needs is held, so
  a run that gives up never names itself. The project is `--project`, else `jackstay.json`'s, else
  the main checkout's folder name; the words are the command's, or `--note-words`, else
  `JACKSTAY_LOCK_NOTE_WORDS` (`jst test` passes the person's command, so the note does not show its
  inner wrapper).
- **`jst lock status`** (`--json`) says who runs and who waits, in order, for the shared lock and
  for each older lock the project names. Each waiting run's words are in its ticket.
- **`--wait SECONDS`** gives up after that long with status 75, having run nothing. Without it a
  run waits as long as it takes.
- **An older lock held too** (`lock.also_hold`, or `--also-hold PATH=VARIABLE`): the run holds a
  project's own earlier lock file together with the shared one and sets the variable that lock's
  script reads, so runs of the earlier script and of jackstay exclude each other and neither waits
  for itself.
  - An older lock an ancestor already holds (its variable names a proved holder) is skipped.
  - A run waits in line for one lock only while it holds none: it takes the shared lock in its
    turn and tries each older lock without waiting; when one is busy it gives everything back,
    waits in that lock's line, and with it in hand tries the rest. No order of taking can lock two
    runs against each other. The cost: a run that gave the shared lock back rejoins the end of its
    line; `--wait` bounds that.
  - An older lock that is the shared lock's own file is refused (exit 2); one named twice is taken
    once; a path that is not absolute is refused.
- **`JACKSTAY_LOCK_OUTCOME_FILE`**: the lock's own statuses (2, 75, 127) are also statuses a
  command can end with. A caller that must tell them apart names a file; the lock empties it at
  the start and writes one word: `ran` (the status is the command's), `gave-up` (75), `unusable`
  (2) or `cannot-start` (127). An empty file means the lock was stopped while it waited.
  `JACKSTAY_LOCK_OUTCOME_FILE`, `JACKSTAY_LOCK_NOTE_WORDS` and `JACKSTAY_LOCK_GROUP_WAIT` are not
  passed on to the command.

Exit: the command's own status; 2 a lock, `jackstay.json` or the words cannot be used; 75 gave up
waiting ("Nothing was run"); 127 the command cannot start; stopped by a signal, it ends by that
signal (stopped while waiting: nothing ran).

| What goes wrong | What happens |
|---|---|
| The lock's folder cannot be made or opened (the agent sandbox) | Status 2, with words ("Heavy runs go outside the agent sandbox"); the command does not run. |
| `jackstay.json`'s `lock` section cannot be used, or an older lock is the shared lock's own file or not absolute | Status 2, with words; the command does not run. |
| The command leaves processes in its group | They are stopped (TERM, 30 s, KILL, 5 s) before the lock is freed. |
| The command ignores the request to stop | It is killed with its group 30 s after the first request, while it still runs. |
| The holder is killed outright (`kill -9`, a harness that kills its own group) | The system frees the lock at once; the command may run on unlocked. The holder note names its process group to stop. |
| A waiting run is killed | Its ticket is passed over by the next run that looks; nobody waits behind it. |
| The command cannot start | Status 127, with words. |
| `--wait` passes | Status 75, "Nothing was run". |
| The lock file is deleted while held | A run that locks a file no longer at the path looks again; the next run makes a new file, which is a second lock. That is why it is not in a cache folder. |

The lock is advisory: a test command typed without it is not stopped.

## 6. Builds and releases

- **The build.** `.goreleaser.yaml` builds `./cmd/jst` for darwin/amd64, darwin/arm64,
  linux/amd64 and linux/arm64 with `CGO_ENABLED=0`, `-trimpath` and `-s -w`, the commit set by
  `-ldflags`. One archive per platform, `jackstay_<version>_<os>_<arch>.tar.gz`, holding the binary
  `jst`, jackstay's `LICENSE` (Apache-2.0) and, under `licenses/<module>/`, the licence files
  (`LICENSE*`, `PATENTS`, `NOTICE*`, `COPYING*`) of every third-party module compiled into `jst`
  (MIT and BSD-3-Clause ask for them; `docs/licences.md` lists the modules), plus `checksums.txt`.
  goreleaser's before hook `go run ./cmd/jst-licences "$JST_LICENCES"` writes them into a new folder
  the run owns (the workflow and `just release-snapshot` name one under a fresh `mktemp -d`;
  jst-licences refuses a folder that exists, and a run without the variable fails at once): it lists
  the modules with `go list -deps ./cmd/jst` for each operating system released (`GOOS` darwin and
  linux, the architecture inherited) and copies their files from the module cache, so the set
  follows `go.mod` with no list to keep; a module without a licence file stops the release.
  GoReleaser publishes nothing (`release.disable`); the workflow does (below).
- **macOS: Developer ID and notarisation** ([W-075](../decisions/W-075-release-through-public-ci.md)).
  GoReleaser signs and notarises the macOS binaries itself, before they are archived
  (`notarize.macos`, which uses quill and needs no Mac and no keychain): signed with the Developer ID
  Application certificate (`MACOS_SIGN_P12`, the .p12 base64'd, and `MACOS_SIGN_PASSWORD`),
  notarised with an App Store Connect API key (`MACOS_NOTARY_KEY`, the whole .p8 file base64'd, PEM
  markers included; `MACOS_NOTARY_KEY_ID`; `MACOS_NOTARY_ISSUER_ID`), waiting for Apple's answer
  (`wait: true`) for at most **18 minutes**: Apple refuses an API token that lives longer than 20
  minutes, and quill makes the token live the timeout plus 2 minutes; most answers come within a few
  minutes. A slower answer, or a refusal, fails the release before anything is published, and the
  workflow is run again. quill signs with the binary's name, `jst`, as the identifier; `jst install`
  signs with the same identifier on a machine whose jackstay home names an identity (section 7.4):
  one identifier names `jst` everywhere (W-075).
  Signing is enabled by `MACOS_SIGN_P12` being set, so a local snapshot without the secrets builds
  unsigned archives; the release workflow refuses to run without all five.
- **Publishing** is `.github/workflows/release.yml`, run by a pushed `v*` tag in the public
  repository (`zima-oss/jackstay`; the job does not run elsewhere) on a GitHub-hosted
  `ubuntu-latest` runner, in the `release` environment (which holds the five secrets and requires the
  owner's approval), with `contents: write`, `id-token: write`, `attestations: write` and
  `artifact-metadata: write` permissions; every action is pinned by
  its full commit hash, and GoReleaser is run pinned from Go's checksum database
  (`go run github.com/goreleaser/goreleaser/v2@<version>`), with Go's build cache off (no cache a pull
  request could have written reaches a release). Before the owner approves the run, `just gate` has
  passed on a Mac on the tagged commit: CI runs the gate on Linux only, so this is where the
  macOS-only code is checked ([W-083](../decisions/W-083-no-macos-job-in-ci.md)). In order:
  1. It refuses a tag that is not annotated (W-020's `git tag -a`), not the commit checked out, not
     on main, or not naming the source's version (`v` and `internal/version`'s `Version`), and a
     run without all five secrets.
  2. `just gate` on the tagged tree itself. The gate runs here rather than relying on the gate
     workflow's check on main: that check belongs to another run, which a job with only
     `contents: write` cannot read, and running it on the tagged tree proves exactly what is
     released.
  3. GoReleaser builds, signs and notarises with that tag (`GORELEASER_CURRENT_TAG`, so another tag
     on the same commit is not taken) and publishes nothing (`--skip=publish,announce`).
  4. `actions/attest` signs GitHub build provenance for each archive's name and SHA-256 digest,
     bound by the workflow certificate to the repository, release workflow and tagged commit.
     Failure stops before a release is published. The attestation is held by GitHub's attestation
     API; `checksums.txt` remains a separate integrity check.
  5. The workflow publishes, naming the release only by the id GitHub gives it: the tag on GitHub
     must still name the commit built (`git ls-remote`); no published release of the tag may exist
     (only a confirmed 404 lets it on: a release is never redone, W-020), nor a draft of it (one an
     earlier run left is read and deleted by the maintainer first); it creates a draft
     (`POST repos/<repo>/releases`), uploads the four archives and `checksums.txt` to that id, checks
     that the draft holds exactly them by GitHub's sha256 digests, asks again that no release of the
     tag was published meanwhile, publishes the draft by its id (`PATCH … draft=false`), and checks
     that the tag's published release is that id. A run that fails before the `PATCH` leaves at most
     a draft of its own, which the maintainer deletes before the run is repeated. One that fails
     during or after the `PATCH` may have published: the maintainer reads the release by the id the
     log names and checks its assets against `checksums.txt` before anything is repeated (a published
     release is never redone).

  Nothing is published from a maintainer's machine; no maintainer-side signing tool is part of the
  code ([W-075](../decisions/W-075-release-through-public-ci.md)). `just release-check` is
  GoReleaser's `check`, and `just release-snapshot` builds every archive into a new folder of its
  own (it says which; no earlier build is touched), with nothing published.
- **The quick install** ([W-076](../decisions/W-076-quick-install-and-docs-site.md)) is `install.sh`
  at the repository's root, POSIX `sh` with `set -eu`, served from `main`
  and beside the site. The documented one-liner is
  `script=$(curl -q --proto '=https' --tlsv1.2 -fsSL <its address>) && sh -c "$script"`: `sh` runs
  the script only once curl has downloaded all of it, and a failed download runs nothing and exits
  with curl's status (a pipe into `sh` would start on a partial download and report `sh`'s
  status). Every curl, the
  documented ones included, starts with `-q`, so a user's `~/.curlrc` (an `insecure` line, for one)
  is not read. The guide also shows the two-step form (download into a fresh `mktemp -d` folder,
  read, run) for whoever reads the script first. `internal/installscript` runs the README's
  one-liner, with its address pointed at a fake release served over TLS, and checks that the guide,
  the site's home page and the script's header carry the same line. It names the platform from `uname -s` and `uname -m` (Darwin or Linux; x86_64/amd64 or
  arm64/aarch64; on a Mac, a shell under Rosetta gets the arm64 build) and stops on any other before
  it downloads anything; it needs `curl`, `tar` and `sha256sum` or `shasum`, and names the one
  missing. The release is `JACKSTAY_VERSION` or the newest one (where GitHub's
  `releases/latest` redirects); it downloads the platform's archive and `checksums.txt` with
  `--proto =https --proto-redir =https --tlsv1.2`, and goes on only when the archive's own line in
  `checksums.txt`, matched by its whole name, names the SHA-256 it computes. By default, older
  releases remain installable with an explicit integrity-only warning. `JACKSTAY_SHA256` adds a
  separately recorded digest requirement; `JACKSTAY_PROVENANCE=attestation` requires `gh` to verify
  the archive against the repository, release workflow and version tag. Both checks apply when
  both are set. A failed configured trust check stops before unpacking. It unpacks into a
  `mktemp -d` folder it removes on exit, runs
  `jst install --prefix "${JACKSTAY_PREFIX:-$HOME/.local}" --binary <that jst>` and prints the next
  steps ([W-078](../decisions/W-078-setup-after-install.md)). It asks which `jst` the inherited
  `PATH` runs (`command -v jst`, its folder resolved with `pwd -P` and compared with the prefix's
  `bin` resolved the same way), not how `PATH` is spelt: when none, or one in another folder (an
  older install first on `PATH`, which it names), it prints the line that puts the prefix's `bin`
  first (named as resolved, the folder the check compared, as one single-quoted word) and the
  profile it goes in, when `SHELL` names a POSIX shell (zsh `~/.zshrc`; bash `~/.bash_profile` on
  macOS, `~/.bashrc` elsewhere; sh, dash, ksh `~/.profile`); for any other shell (fish, csh) it names
  the folder to put first and prints no line it cannot test; then `jst doctor` and `jst adopt --plan`. It only
  prints them: nothing outside the prefix is written, and the tests check that the home holds
  nothing else afterwards, with a real `jst` installed. A relative `JACKSTAY_PREFIX` is made absolute
  first, as `jst install` makes it, and `jst install` runs with `JACKSTAY_TELEMETRY=off`, so its own
  journal entry is not written under `~/.jackstay`. It never uses `sudo`. Everything runs from one function,
  `main`, the script's last line, so a download cut short runs nothing: cut before that line, it
  defines functions only; the line itself runs the whole. Exit: 0 installed, 1 nothing was
  installed, else the status of the `jst install` that stopped. `JACKSTAY_RELEASES` names another
  releases page (the tests' fake one, served on this machine). `just check` runs shellcheck on it,
  and `internal/installscript` runs it, under `sh` and under `dash` where the machine has it,
  against a fake release: an install of the newest release and of the one asked for, an archive
  whose hash does not match or that has no line of its own (never run), a release without the
  platform's archive, an unsupported system or processor and a missing tool (nothing downloaded).
- **The documentation site** ([W-076](../decisions/W-076-quick-install-and-docs-site.md)) is built
  with Hugo and the Hextra theme from `site/`, published to GitHub Pages by
  `.github/workflows/pages.yml`. It carries only what a user needs
  ([W-077](../decisions/W-077-docs-site-for-users-only.md)): the home page, which opens with the
  one-line install; the guide, its getting-started page first and its pages in the order of
  `docs/guide/README.md`, with the integrations (Claude Code, Codex) as a group of their own,
  `guide/integrations`; and one page pointing to the design, the decision records and
  CONTRIBUTING on GitHub, the guide's last page (`site/content/guide/more-on-github.md`). The design and the decision records
  are not published. The guide's pages are not copied: `site/hugo.yaml` mounts `docs/` (and
  `CONTRIBUTING.md`) as assets, and one content adapter (`site/content/_content.gotmpl`, with
  `site/layouts/_partials/jackstay/pages-from-docs.html`) adds a page for each Markdown file of
  `docs/guide`. A file's first line `# Title` is its title; the guide's `README.md` is its
  section's page and orders its sidebar. The links stay as the repository writes them: the link
  render hook (`site/layouts/_markup/render-link.html`) turns a relative link to a guide file into
  a link to its page, checking the heading a `#fragment` names; a link to any other file the site
  mounts (a design document, a decision record, `docs/profiles`, `CONTRIBUTING.md`) into a link to
  the file on GitHub, checking a `#fragment` against the headings of that Markdown file as GitHub
  names them (the text without its links' and code's Markdown, through `anchorize`); and stops the
  build on a link that leads nowhere. The site's own pages (the home page, the integrations
  group's page, the page pointing to GitHub) write their links as the repository does too. What
  the theme and those pages write besides
  (menus, the sidebar, the home page's buttons and cards) is checked on the built site:
  `cmd/jst-sitelinks` (`internal/sitelinks`, not installed) reads every page the build wrote as a
  browser does (parsed into elements by golang.org/x/net/html, SVG and MathML included: the `href`,
  `src` and `id` of elements, never text, comments or scripts that look like them), resolves each link as a browser would from that page, and names each one to the
  site's host that leaves its base path (`/jackstay/`), leads to no file the build wrote, or names a
  `#fragment` that is no id of the page it leads to.
  Search is Hextra's FlexSearch, vendored in `site/assets/lib/flexsearch-<version>/` (the file the
  npm registry's integrity hash names; `site/assets/lib/README.md` says where from, its hash and
  licence) instead of fetched from a CDN at each build; dark mode follows the system, with a toggle. `install.sh` is served as `/install.sh`.

  Hugo is run pinned from its module, `go run github.com/gohugoio/hugo@<version>` (the standard
  edition: Hextra needs no Sass), as GoReleaser is: one version in the justfile, used by
  `just site`, `just site-serve`, `just docs` and the workflow, with nothing to install. The theme
  is a Hugo module pinned by `site/go.mod` and checked against `site/go.sum`, fetched through the Go
  module proxy: a version and a hash, where a vendored copy would be a second copy of the theme to
  review on each update. `just docs` builds the site into a temporary folder with
  `--panicOnWarning` (raw HTML in a document, which the site would drop, is a warning), at the Pages
  address, and runs `cmd/jst-sitelinks` on it, after the document checks; `just site` runs it on
  the minified build it writes into `.build/site`, at the address that build was made for, so what
  the workflow publishes is the build that was checked. `pages.yml` runs on a push to `main` that changes `docs/guide/`, `site/`,
  `CONTRIBUTING.md`, `install.sh`, the justfile or itself, and by hand (a change to the design or
  the decision records does not change the site); only in `zima-oss/jackstay`. Its build job
  (`contents: read`, `pages: read`) takes the site's address from `actions/configure-pages`, runs
  `just site` at that address (building and checking) and hands `.build/site` to
  `actions/upload-pages-artifact`; its deploy job
  (`pages: write`, `id-token: write`, the `github-pages` environment) runs `actions/deploy-pages`.
  Every action is pinned by its full commit hash; one deployment runs at a time and is not
  cancelled.
- **Runners** (a project's `card-line` and `release` workflows, written by `jst adopt`) download
  their platform's archive of the version the project pins: `gh release download <version> -R
  <repository> -p 'jackstay_*_linux_amd64.tar.gz' -p checksums.txt`, and check it against
  `checksums.txt`.

## 7. Install, uninstall, doctor

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

A version folder is named `jackstay-<version>-<commit>-<seconds>-<pid>` (`+` in a commit becomes
`-`; `-2`, `-3`… when the name is taken). It is a **folder of jackstay's** only when its files say
so, never by its name alone: a `VERSION`, an executable regular `jst`, and `jackstay` and
`jst-guard` links to `jst`. It is made as `<name>.partial` and renamed whole, and never written
again, so its own time is the time it was made.

A **launcher** (`P/bin/jst`, `P/bin/jackstay`, `P/bin/jst-guard`) is `fixed` (the link above,
exactly that text), `absent`, or `foreign` (anything else). `P/lib/jackstay` is absent, a link to a
folder of jackstay's, a link elsewhere, or not a link. A `foreign` launcher, a link elsewhere or a
`P/lib/jackstay` that is not a link is never touched: install refuses with nothing changed and
says what is there; uninstall keeps it and names it.

`layout.Read(P)` is the one reader of a prefix: it gives a `State` (the launchers' kinds, what
`lib/jackstay` names, the live folder, every folder of jackstay's, leftover `.partial` names).
Install, uninstall and doctor all decide from that value; none reads the prefix its own way.

### 7.2 `jst install`

```
jst install [--prefix DIR] [--checkout DIR | --binary FILE]
```

**Where the binary comes from.**
- From a checkout: `--checkout DIR`, else the nearest folder at or above the working folder whose
  `go.mod` names `github.com/zima-oss/jackstay`. It is built into a temporary folder with
  `go build -trimpath -ldflags '-s -w -X github.com/zima-oss/jackstay/internal/version.Commit=<commit>'
  ./cmd/jst` and `CGO_ENABLED=0`, under a 10-minute deadline. Install resolves the full commit
  first. With clean build inputs, it extracts that commit's Git archive into a private source
  folder and builds there. Edits and commits made during the build cannot change those sources.
  The binary records the abbreviated SHA. If `git status --porcelain` lists changes to build inputs
  (`cmd`, `internal`, `go.mod`, `go.sum`, tests excluded), the trial build instead uses the working
  checkout and appends `+changes` to the SHA. `SOURCE` is the checkout.
- From a file (`--binary FILE`; `SOURCE` is `binary FILE`) or, with neither flag and no checkout
  around, the running binary itself (`os.Executable`, links resolved, so a downloaded release's
  `./jst install` installs itself; `SOURCE` is `release`).

The binary's own `jst version` must print `jackstay <version> (<commit>)`; that text is the folder's
`VERSION`.

**A `go install` build** records no commit, but it records the module version. Built at a version
tag, `jst version` says `jackstay 0.5.11 (v0.5.11)`: the tag stands for the one commit it names (tags
are never moved, and Go's checksum database ties the module's content to the tag). Built at a
branch or a commit (`@main`, `@<hash>`), it names the commit from the pseudo-version. Install takes
either. A build named by its tag and one named by a hash are, to install, two commits of one
version, which the account's prefix refuses (below: no binary says which commit the tag is); to
change one for the other, a newer version is installed, or the old one uninstalled first.

**What is refused** (exit 2, nothing changed):
- a given or running binary whose commit is `unknown` or `+changes`;
- a checkout's `+changes` build into the account's own prefix
  ([W-020](../decisions/W-020-version-tags.md): an installed version names one commit, and only a
  commit can be tagged); into another `--prefix` it is taken (tests, trials);
- the install lock held ("another install or uninstall is running"), taken without waiting;
- a `foreign` launcher or a `lib/jackstay` that is not jackstay's (section 7.1);
- **going back**: a version lower than the live one (versions compare as `MAJOR.MINOR.PATCH`); a
  version only moves forward (there is no rollback);
  the same version from another commit, into the account's prefix (W-020);
- a signing or a proof that fails (sections 7.3, 7.4); the signing setting unreadable; a build that
  fails.

**The steps.** Every change is one `rename(2)` of a name made beside it (`<name>.partial`, made
fresh), so each name is always the old thing or the new thing, and every decision is read from
the prefix as it is (`layout.Read`), so nothing is remembered between runs and a stop at any step
is finished by running install again.

1. Read the signing setting (section 7.4), get the binary, read its version.
2. Take the install lock; `layout.Read`; the refusals above.
3. When `lib/jackstay` already names a folder of the same `VERSION` text, probe the live
   launchers. If the proof fails, prepare and prove a replacement beside the current executable
   before renaming it into place. Apply the same-version signature rules in section 7.4. For a
   different version, make the new folder whole (`MakeFolder`), sign it when an identity is
   configured, and prove it before activation. A preparation failure leaves the live files intact.
4. **Activate** (`layout.Activation.Activate`, skipped when the version is already live and every
   launcher is `fixed`): `lib/jackstay` is replaced by the new folder in one rename, then each
   launcher that is not `fixed` is replaced, one rename each, by its fixed link. On a fresh prefix
   `lib/jackstay` comes first, so no launcher ever names nothing. Every launcher runs whatever
   `lib/jackstay` names, so the switch between two versions is that one rename: every moment
   leaves all three launchers on one version.
5. **Prune** (section 7.5).

Output: one line naming the version, the launchers, the folder and the version before ("already
installed" when it was), then each path removed. Exit 0 installed or already installed; 2 refused,
nothing changed; 1 when a rename failed half-way (the message names the step and that running
install again finishes it).

### 7.3 Proving a folder (before any rename that would make it live)

Each probe runs through `supervise.Capture` (a process group of its own, stopped whole at the
deadline, output read under a bound) with a 30 s deadline, `/` as the working folder, and
`JACKSTAY_HOME` a fresh temporary folder (removed after) with every other `JACKSTAY_*` variable
dropped, so a probe never writes the account's state:

- `FOLDER/jst version` and `FOLDER/jackstay version` each print `jackstay <VERSION's text>` and
  exit 0;
- `FOLDER/jst-guard` given `{"tool_name":"Bash","cwd":"/","session_id":"jackstay-prove","tool_input":{"command":"git reset --hard"}}`
  on standard input answers `permissionDecision: deny` (read as JSON, not searched as text).

The build and every git question go through the same runner (`gitcli` for git, under a 30 s
deadline).

### 7.4 Signing at install ([W-070](../decisions/W-070-sign-jst-at-install.md))

So that a firewall's rule (LuLu) follows `jst` across installs, `jst install` signs the binary of a new
version folder with this machine's code-signing identity and a stable identifier, between making the
folder (`MakeFolder`) and proving it (section 7.3), so before any rename makes it live:

1. `codesign --sign <identity> --identifier jst --options runtime --timestamp --force <folder>/jst`
   (`--timestamp`: a secure timestamp from Apple, which a Developer ID signature needs, so the install
   reaches Apple's timestamp server, [W-075](../decisions/W-075-release-through-public-ci.md));
2. `codesign --verify --strict <folder>/jst`;
3. `codesign --display --verbose=2 <folder>/jst` reads back the identifier and the first authority,
   which must be the identity.

Any step failing refuses the install (exit 2): the new folder is removed and nothing live changes,
and the message names the fix: put the identity back in the keychain, or name another identity in
the setting, or remove the setting to install without signing.

**A binary that keeps its signature.** Before step 1, the binary's signature is read
(`codesign --display`, then `codesign --verify --strict`). A binary signed by a certificate (not
ad-hoc) with the identifier `jst` whose signature verifies, as a release archive's is (signed with a
Developer ID and notarised, section 6), is installed as it is, whatever identity the setting names:
signing it again would change its code directory, and Gatekeeper would no longer find its
notarisation. Only an unsigned or ad-hoc signed binary (a local `go build`), or one with another
identifier, is signed.

`jackstay` and `jst-guard` are links to `jst` in the folder, so the proof of section 7.3, which runs
`jst`, `jackstay` and the guard, runs the signed binary through each link.

**The version already live.** Install runs the launcher and guard probes in section 7.3, then
keeps a working binary when its required signature checks pass. With an identity configured,
an unsigned, ad-hoc, earlier-identifier or invalid signature calls for a replacement. On macOS,
a working ad-hoc binary can also be replaced by an incoming binary with a verified certificate
signature even when no identity is configured.

For a repair or signature upgrade, install copies the incoming executable to
`<folder>/resign.partial/` under the prefix lock, creates the two launcher links and applies any
required signing there. An incoming certificate signature carried through an upgrade is verified
again on the copy. The staged launchers must pass the probes before install renames the staged
executable over `<folder>/jst`. If preparation fails, the executable stays in place and install
returns 2.

**The identity setting is a file of this machine's jackstay home**, `~/.jackstay/.all/codesign-identity`
(under `JACKSTAY_HOME` when that is set): one line, the identity's name
exactly as `security find-identity -v -p codesigning` prints it. It is not an environment
variable, because the processes that run `jst install` (a terminal, a Claude Code session in the agent
sandbox, a hook) do not share one environment, while every one of them reads jackstay's home, which
the sandbox already lets jackstay read and write. It is not in `jackstay.json` either: the identity
belongs to the machine, not to a project. Without the file, and on Linux always, nothing is signed:
the binary keeps the Go linker's ad-hoc signature.

`jst doctor` (section 7.7), on macOS: the live binary's identifier and authority as facts; with an
identity configured, a problem `codesign-identity-invalid` when the keychain does not list it as
valid (missing, expired, revoked) and `binary-not-signed` when the live binary does not keep its
signature by install's own rule (above, the same function: an install made before the setting, an
earlier identifier, or a signature that does not verify strictly), which `jst install --binary` with
a binary whose `jst version` prints exactly what the live one prints mends (above: a tagged
`go install` build and a checkout's build of the same commit print different commits), as an
install of a newer version does.

Not covered: development builds (`.build/jst`, the gate's `go run` tools). Release archives are
signed and notarised by the release workflow, not here (section 6). The guard's answer time is unchanged by the signature
(measured).

### 7.5 What is kept and what is pruned

After the activation, install keeps two folders of jackstay's and removes every other one, with
every leftover `.partial` name:
- the live folder;
- **the version before**: the folder that was live when this install began, so a run that started
  under it ends with it; on an install of the version already live, the newest folder (by its own
  time) whose `VERSION` text differs from the live one's.

Prune reads only the prefix, so a stop after the activation is finished by the next install. A
folder that cannot be removed is a warning, not a failure.

### 7.6 `jst uninstall`

```
jst uninstall [--prefix DIR]
```

Under the install lock: each `fixed` launcher is removed, then `lib/jackstay` when it is a link to
a folder of jackstay's, then every folder of jackstay's and every leftover `.partial` name. A
`foreign` launcher and a `lib/jackstay` that is not jackstay's are kept and named. The lock file
stays (removing it after its release would let two installs each lock a file of their own). The
launchers go first, so a hook that runs meanwhile gets the shell's 127 (Claude Code does not block
on it), never a half-removed folder. `~/.jackstay` is left and named, with the warning that projects
whose hooks or recipes name the launchers now run without the guard. Exit 0 (also when nothing was
there: "nothing (it was not installed here)"), 1 when something could not be removed, 2 for usage,
a held lock or a prefix that cannot be read.

### 7.7 `jst doctor`

```
jst doctor [--json] [--prefix DIR]
jst doctor --expect NAME | --forget NAME
```

The prefix is `--prefix` when given, else the one the running binary is installed under
(`os.Executable` resolved is `P/lib/jackstay-*/jst`), else `~/.local`. Every check is a function of
its inputs (`doctor.Inputs`: the layout, the guard's spans from the journal, the marks, the expected
list, the settings file, the clock, and the programs it asks), so tests give a fixed clock and
fixtures ([W-013](../decisions/W-013-quality-testing-declarative.md)). Each finding has a **kind**
(a fixed identifier) and a sentence; problems make exit 1. Every problem also names its **fix**
([W-078](../decisions/W-078-setup-after-install.md)): the command to run, or what to change and
where. A broken install's fix is the whole one-liner, to run in sh, bash or zsh, with
`JACKSTAY_PREFIX='P'` given to the `sh` that runs the script, so the install goes to the prefix the
hooks name; for a wrong live binary, then `jst doctor --prefix 'P'`, and a problem still reported
there is a bug to report (doctor never advises an uninstall: it cannot know which install a later
report is about, and an uninstall removes the version before too); `sandbox-blocks-state`:
the settings file and the `allowWrite` entry to add, or, when a `denyWrite` entry stops it, changing
that entry so it still protects what else it is for, or moving `JACKSTAY_HOME` (never removing a
deny, which may protect more than jackstay's state); `guard-never-called`: `jst adopt --apply`, the
trust dialog, one shell command. `TestEveryProblemNamesItsFix` reads the package's source: every
call that reports a problem passes a fix, and every kind is listed in the test.

| check | finding |
|-------|---------|
| the prefix can be read | `layout-unreadable` |
| every launcher `fixed`, `lib/jackstay` a folder of jackstay's | `launcher-missing`, `launcher-not-fixed` (a `foreign` launcher), `link-missing`, `folder-incomplete` |
| leftover `.partial` names; the install lock held | notes `partial-left`, `install-running` |
| the installed `jst-guard` refuses `git reset --hard` (the probe of section 7.3, through `P/bin/jst-guard`, with a home of its own so it leaves no mark in the one it inspects) | `guard-does-not-refuse` |
| `P/bin/jst version` equals the live folder's `VERSION` | `version-mismatch` |
| the checkout in `SOURCE` (when it is one) has no change to the build inputs since the installed commit; a checkout that moved only in other files is a fact, not a problem | `checkout-moved` |
| on macOS: the signing setting, the identity in the keychain, the live binary's signature (section 7.4) | `codesign-setting-unreadable`, `codesign-identity-invalid`, `codesign-uninspected`, `binary-not-signed` |
| the guard's command spans of the last 7 days in every project's journal ([observability.md](observability.md)) | `failed-open` and `config-error` per project; ignored tools counted as a fact |
| each project's `guard-seen` mark and the expected list | a project quiet for more than 3 days: note `guard-quiet`; an expected project never heard: `expected-never-called`; no mark and no expected project: `guard-never-called`; a list that cannot be read: `expected-unreadable` |
| `~/.claude/settings.json`'s `sandbox`: enabled and `~/.jackstay` not writable by its `filesystem` lists | `sandbox-blocks-state` |
| the lock's folder (`flock.Path`) can be made and written | `lock-unwritable` |
| the project around the working folder is up to date with jackstay (`adopt.Check`, [adopt.md](adopt.md) section 10) | `adoption` (problems and notes) |

`--json` prints `{"facts": {…}, "problems": [{"kind", "text", "fix"}], "notes": [{"kind", "text"}]}`; the
facts are `version`, `prefix`, `layout` (each launcher's kind, `lib_jackstay`, `live`), `checkout`,
`checkout_build_inputs`, `signature` (`identity`, `identifier`, `authority`), `guard_last_7_days`,
`guard_last_call_hours_ago`, `expected_projects`, `lock` and `adoption`, each when it applies.
Without `--json` the notes, the facts and each problem are printed as lines, each problem followed
by a line `  fix: …`, or "nothing needs the owner".

**The sandbox check** reads Claude Code's `sandbox.enabled` and `sandbox.filesystem.allowWrite` /
`denyWrite` of the account's `~/.claude/settings.json` only (a project's own settings may still
allow it; the sentence says so). An entry is a path, `~/…` expanded from the account, or a pattern;
a pattern's part before its first `*?[` is resolved as a real path and the rest matched per path
element with `path.Match`, `**` standing for any number of elements. `denyWrite` covering
`~/.jackstay` or a folder inside it wins.

**`--expect NAME` / `--forget NAME`** change `~/.jackstay/.all/expected-projects` (one marker name
per line, sorted) under `flock` on `expected-projects.lock` (waiting up to 30 s) and replace it by
an atomic write (temporary file in the same folder, `Sync`, `Rename`). The name is kept as its
marker (`homedir.MarkerName`); `--forget` also drops the name as given and the project's
`guard-seen`. A blank name is refused (exit 2); a list that cannot be changed is exit 1. The list's
reader is one function (`internal/expected`); a list that exists and cannot be read whole is an
error, never an empty list.

## 8. GitHub and branches: `jst checks`, the `github` section, `jst tidy`, `jst start`, `jst sync`, `jst owner`

jackstay talks to GitHub through go-github and its GraphQL endpoint, in one client
(`internal/githubapi`) that `jst checks`, `jst land`, `jst tidy`, `jst lint pr` and `jst release` share: one token
path, one retry rule ([W-036](../decisions/W-036-go-native-not-python-mimicry.md),
[W-050](../decisions/W-050-go-part3-design-github-account-all-steps-here.md)). It never runs the
`gh` CLI except to ask it for a token. git pushes, fetches and deletes remote branches.

### 8.1 The `github` section: which account

A project says which GitHub account jackstay acts as, so that with several accounts signed in to
`gh` jackstay never depends on, or changes, the one `gh` has active.

```json
"github": {
  "account": "example-bot",
  "remote": "origin",
  "repository": "example-org/example",
  "wait": {"timeout_seconds": 540, "interval_seconds": 20, "log_lines": 80}
}
```

| key | required | meaning |
|---|---|---|
| `account` | yes, in the section | the GitHub login jackstay acts as on this project |
| `remote` | no | the remote that names the repository and that `sync`, `start`, `tidy` fetch (the rule is in 8.2) |
| `repository` | no | `owner/name`, when it is not to be read from the remote's URL |
| `wait` | no | defaults for `jst checks wait`, `jst checks run-wait` and `jst land` (`--timeout`, `--interval`, `--lines`); the flags still win |

Checked like every section (section 3.2): `account` a GitHub login (letters, digits, single
hyphens, at most 39); `repository` `owner/name`; `remote` a remote's name; the `wait` numbers above
0, `log_lines` a whole number. Only the commands that read it are stopped by it.

**Two ways to a token**, chosen once per call and never mixed:

- **Inside GitHub Actions** (`GITHUB_ACTIONS=true`): `GH_TOKEN` only (a workflow's own or App
  token). `account` is not read, `GET /user` is not asked (an installation token cannot answer
  it), and a `gh` signed in on the runner is never used. No `GH_TOKEN` there: exit 2.
- **Anywhere else**: the effective account — `JACKSTAY_GITHUB_ACCOUNT` when set (a one-off for one
  call), else `github.account` — and its token from `gh auth token --hostname github.com --user
  <account>`, which answers for a signed-in account without switching `gh`'s active one. Then
  `GET /user` once per call: a login other than the effective account is exit 2 (a token `gh`
  stored under a stale name, a renamed login). An account not signed in to `gh` is exit 2 naming
  `gh auth login --hostname github.com`; a keychain or TLS failure is told apart from it and named.
  A `GH_TOKEN` (and `GITHUB_TOKEN`, `GH_HOST`, the enterprise variables) in the environment is
  ignored, and said once on standard error: it could belong to any account.

**Required only where GitHub is asked.** A project without `github.account` gets no GitHub call
outside Actions: `jst checks` and `jst land` exit 2 ("set github.account in jackstay.json"); `jst tidy` does not try
`pr-merged` and says why (branches only GitHub could show finished are kept); `jst lint pr --pr N`
exits 2. What needs no GitHub keeps working without it: `jst lint pr --body-file`/`--event`,
`jst release`'s offline dry runs, `sync`, `start`, `tidy --no-github`.

**The account is the API's, not git's.** The guarantee covers jackstay's GitHub API calls only.
`jst land`'s push and the remote branch delete go through git, whose credentials are set apart (the
remote URL's user, a credential helper — `gh`'s helper answers with the *active* account — or an
SSH key) and are not verified by jackstay. Before a push or deletion, `jst land` checks that every
effective push URL of the selected remote (including `pushurl` and URL rewrites) names the GitHub
repository of the pull request. The deletion also requires GitHub to confirm the merged head's
repository, branch and SHA. jackstay never rewrites a remote or git's settings.

**The repository**: `github.repository`, else the URL of the remote (8.2), in https, userinfo
(`https://user@github.com/…`) and scp forms, read as configured (`remote.<name>.url`), not as
`url.*.insteadOf` rewrites it. A host other than github.com is refused (exit 2); so is a
GitHub-like URL that does not parse. A pull request's URL given to a command names its repository
and wins over the checkout. `JACKSTAY_GITHUB_API_URL` points the client at a GitHub served on this
machine for tests; anything but a loopback http address is refused, so a token never leaves for
it.

**Reads and writes.** Only reads (REST GETs and read-only GraphQL queries) are retried, on 5xx and
network errors: four calls in all, waiting about 1, 2 and 4 s with ±50 % jitter, inside the call's
deadline. A write (merge, create, edit, a release) is never retried; an answer lost after a write
is settled by reading the state again (did it merge, does the pull request exist). A primary rate
limit waits until its reset when that is inside the deadline, else exit 1 naming the time; a
secondary limit honours `Retry-After`.

**Checks** are one GraphQL query of the head commit's `statusCheckRollup`, paged to the end: the
newest run of each check name in each check suite (so a re-run does not leave a false red) and
commit statuses of other apps. Each workflow run is a suite of its own, so a run a `concurrency`
group cancelled for a newer run is still listed. A cancelled check is superseded only after a
bounded repository run listing for the checked commit identifies the cancelled run and a newer run
with the same workflow ID and commit (and the same event and branch when known). The newer run's
matching job result is judged; if that job has not appeared, the check stays pending. A missing or
unrelated run leaves the cancellation red.
The pull request identity is checked again after the run lookup and before landing.
Buckets: SUCCESS → pass; SKIPPED,
NEUTRAL → skipping; FAILURE, ERROR, TIMED_OUT, ACTION_REQUIRED, STARTUP_FAILURE → fail; CANCELLED →
cancel (settled, failing, and named as a cancellation); STALE, PENDING, EXPECTED and anything else
→ pending. No checks registered is waiting, not a pass. The head is compared after the rows are
read.

**Finding a pull request by its tip** is GraphQL `search`. REST traps are written into the code and
tested: the `head` filter is `owner:branch`; "merged" is closed with `merged_at`; `mergeable` is
null while GitHub computes it (waited for, as `UNKNOWN`).

**The failed step's log**: the run's failed jobs and their failed steps from the API; each failed
job's log cut by the step's `started_at`..`completed_at` against the log's timestamped lines; when
it cannot be cut, the job log's last lines, saying so.

### 8.2 The remote and the default branch

`jst sync`, `jst start`, `jst tidy`, `jst checks` and `jst land` share one rule for the remote a project's work goes
to: `github.remote` when it is set (one that names no remote is an error); else `origin`; else the
remote the local `main` (then `master`) tracks; else the only remote. Several remotes and none the
rule picks is an error naming them. A project's remote need not be called `origin`.

The default branch is the remote's as fetched (`refs/remotes/<remote>/HEAD`, else
`<remote>/main`, `<remote>/master`), else the local `main`, else `master`. `jst land` takes the base
from GitHub's own default branch, never from what the checkout fetched.

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

The guard cannot tell finished work from unfinished in the text of a command, so it keeps refusing
`git branch -D`, `git worktree remove --force` and `rm -r` of a worktree; its refusals name
`jst tidy`, which asks git, GitHub and the process table first
([W-006](../decisions/W-006-tidy-and-github-tools.md)).

    jst tidy [--fix] [--branch NAME]... [--worktree PATH]... [--no-fetch] [--no-github] [--json] [DIR]

Reporting is the default; `--fix` removes. It fetches the remote first (8.2). `--branch` and
`--worktree` narrow it to those items (each with its worktree or branch). GitHub is asked first: an
open pull request keeps a branch whatever git says.

| A branch is finished when | It is kept when |
|---|---|
| every commit is on the default branch (`merged`): the remote's, or the local branch of the same name — merged into a local `main` that a session pushes later counts | it is the default branch, or is checked out in a worktree that is kept |
| merging it into the default branch (either of the two) changes no file and its commits are not work taken back (`adds-nothing`, the guard's test) | a pull request from it is open (`pr-open`) |
| GitHub lists a merged pull request from it whose head is its tip or holds its tip (`pr-merged`) | it has commits after its merged pull request's head |
| no pull request comes from a branch of its name, and GitHub's search by its tip finds a merged one whose head is or holds the tip (`pr-merged`: a branch fetched under another name, or renamed before the push) | a pull request that search finds is open |
| | none of the above can be shown (`own-work`: an unpushed branch with work of its own; a commit GitHub has not indexed yet) |

A worktree is removed when its branch is finished (a detached one: its commit is on the default
branch or held by a branch or tag) and:

- `git status` shows nothing modified, staged or untracked, and no file is marked skip-worktree or
  assume-unchanged (git hides their edits from `status`);
- every ignored path is regenerable (the guard's built-in names and `guard.regenerable`) or agents'
  runtime state (`.cc-writes`, `.DS_Store`, and `.omc/` and `.omx/`'s `state`, `logs` and
  `sessions`; `.omc/plans` and `.omc/handoffs` are work). A `guard.regenerable` entry that would
  match nearly any name (`?*`), an `.env`, or one with a slash is not applied, as in the guard;
- it is not locked and not the main checkout;
- no process has its working folder in it, `jst tidy` itself included (a session does not remove
  the worktree it runs in), and the view of processes is complete (section 5.2).

`--fix` then runs `git worktree remove <path>` without `--force` (git checks once more) and
`git update-ref -d refs/heads/<name> <tip>`. Each item is judged again just before it is removed,
without fetching, and the branch is deleted only at the tip that was judged (git refuses
otherwise): another session may commit, write an ignored file or start working there between the
report and `--fix`. An item called off that way makes `--fix` end 1. Records of the asked-about
worktrees whose folder is still gone are pruned individually. A sweep leaves missing worktree
records in place and exits 1, since the worktree might have moved. A scoped fix also exits 1 if
any named record cannot be verified or pruned. Remote branches are not touched.

| Failure | What happens |
|---|---|
| no account, no token, `--no-github`, or GitHub failing | `pr-merged` is not tried and the report says so; a branch only GitHub could show finished is kept |
| the search by tip is refused or fails | the branch is kept and the report says so |
| GitHub fails for one branch, or answers in another shape | that branch is kept and the report says so |
| the fetch fails | judged on the remote refs as they were; said in the report |
| the process table cannot be read, or `lsof`'s view is partial (inside the agent sandbox it always is) | every worktree is kept, with the reason; a few processes `lsof` did not name are named in it |
| `jackstay.json` cannot be used | noted; `guard.regenerable` is not applied |
| a removal fails (git refuses) | said; exit 1; the rest goes on |

Exit 0: nothing finished is left; 1: something finished is listed and not removed, or a removal
failed or was called off; 2: the request was wrong (not a repository, a branch or worktree that
does not exist). `--json` gives the report with each verdict's and reason's identifier.

What it cannot see: work that is only in a stash (stashes are the repository's, not a
worktree's, and `jst tidy` does not touch them); a pull request merged on a host other than
GitHub; a process that keeps a file open in a worktree without its working folder there; a new
ignored file or a process starting in the worktree during the one `git worktree remove` after the
last look (git itself refuses modified and untracked files there).

### 8.5 `jst sync` and `jst start`

    jst sync [--no-fetch] [--push] [DIR]
    jst start NAME [--from REF] [--path DIR] [--no-fetch]

They replace the fetches, merges, rebases and `git worktree add` calls sessions write by hand
([W-007](../decisions/W-007-tools-from-the-usage-study.md)).

**`jst sync` merges only** ([W-019](../decisions/W-019-repeated-findings-mean-redesign.md)):
`--rebase` is refused (exit 2). A rebase replays every commit of the branch, so a commit that adds
an ignored file (`add -f`) and a later one that stops tracking it can write over or delete a
private file that neither end's tree has; the class is removed rather than guarded.

It fetches the remote (8.2) and refuses (exit 2): a merge, rebase, cherry-pick or revert already
under way; uncommitted or untracked work; files marked assume-unchanged or skip-worktree (their
edits do not show and a merge may write over them; a sparse checkout's marks are not edits); a
detached HEAD. Then: on the default branch a fast-forward only; on another branch a merge, whose
message is the one `git merge <ref>` writes. A conflict ends 1 with the files named and the merge
left to resolve or abort.

**The ignored file.** git refuses where it has a refusal, and one check covers where it has none.
`--no-overwrite-ignore` stops a fast-forward that would write over an ignored file, but a
three-way merge (ort) writes over it without a word — also at a path neither end has, when the
default branch renamed a folder this branch added a file to. So the fast-forward and the merge run
with that flag, and before both one check compares HEAD with the merge's own result, made in
memory (`git merge-tree --write-tree HEAD <default>`, its tree written also when the merge has
conflicts): each path the result adds that is untracked here, holds untracked files, or has a file
or link on its way where a folder must be, stops the sync (exit 1, named). Names are read as bytes
from `-z` lists, nothing trimmed (a name may begin with a space or a tab); an answer git did not
mean stops it too. HEAD and the default branch are read once, as commits, at the start.

`--push` pushes exactly this branch to its own upstream (`git push <remote> HEAD:<its branch>`),
or with `-u` under its own name the first time; never a bare `git push`, which `push.default` and
`pushRemote` may send elsewhere or with other branches. It is decided before anything is merged.

**`jst start NAME`** fetches and makes the branch on the default branch as fetched (not a stale
local `main`; when the remote has no default branch git can name, it says so and asks for
`--from`), or on `--from REF`, with no upstream yet, in a worktree at
`.claude/worktrees/<name>` when git ignores that folder, else at `<main checkout>-worktrees/<name>`
(`--path` chooses; a `/` in the branch name is `-` in the folder's). A name git does not accept, a
branch or folder that is already there, or a `REF` that is not a commit is refused (exit 2).

Exit for both: 0 done or nothing to do; 1 a conflict, git refused; 2 the request.

### 8.6 `jst owner`

    jst owner [--session S|all] [--kind typed|queued|widget|declined] [--grep RE] [--since D] [--max-chars N]
    jst owner --stats [--session S]

Reads what the person running the sessions actually said in a Claude Code session's transcript,
in the three shapes a message arrives in, where the obvious extractor misses two
([W-006](../decisions/W-006-tidy-and-github-tools.md)):

- a typed message;
- a queued one (typed while the assistant worked). When it is delivered it is written a second
  time as typed; a typed copy within 5 s after a queued one with the same words is shown once;
  further apart they are two messages;
- an `AskUserQuestion` answer, in both spellings the tool writes (`The user answered:`, `Your
  questions have been answered:`); a declined question is its own kind.

`--session` is a substring of a session file's name, or `all`; without it, the newest session.
`--grep` is a case-insensitive RE2 expression; `--since` an ISO time prefix. `--stats` says where a
session's tool calls went instead. The transcript folder is Claude Code's projects folder
(`CLAUDE_CONFIG_DIR`'s, else the account's) and the checkout's real path with every character that
is not an ASCII letter or digit replaced by `-` (measured: `/`, `_` and `.` all become `-`, which
covers `.claude/worktrees/*` sessions).

Exit 0 read (filters that match nothing are still 0, and say so); 1 nothing to read (no transcript
folder, no session file, an empty one, or a file that cannot be read); 2 a bad request; 5 the
transcript format or location
changed (a transcript with no message from the person at all is a changed format, not a quiet
session); 6 a widget answer in a spelling `jst owner` does not know.

## 9. Receipts and the baseline: `jst test -- COMMAND`, `jst receipts`, `jst baseline`

`jst test <scope>` (scopes, the `swift-test` judge, a timeout that ends the whole process tree, the
gate with its step marks, leftovers, `jst test last`) is specified in [testing.md](testing.md) and
builds on what follows. `jst test -- COMMAND` is the raw form: any command, judged, recorded and
keyed by the tree.

### 9.1 `jst test -- COMMAND`

    jst test [--no-reuse] [--lock | --no-lock] [--project NAME] [--wait SECONDS] -- COMMAND [ARG ...]

It reads the `lock` and `receipts` sections (a broken `test` section does not stop it). It runs the
command at once: no lock is taken unless the project turned the lock on or the call says `--lock`
(section 5.4). With or without the lock the command runs in a process group of its own given the
terminal; a request to stop `jst test` is passed on to the whole group, and what the command left
running in its group is stopped after it ends (TERM, 30 s, KILL; section 5.2). The run is recorded:
its start, its end, its seconds, its outcome and its key. That record is all a project without a
`receipts` section gets.

After a green run whose tests were counted, `jst test` stores a **receipt** keyed by the tree.
Where a project has opted a command in, a receipt for the same key is printed and nothing is run.
No project need turn reuse on: a command listed with `"reuse": false` is keyed, timed and counted,
and a repeat is logged as `would-hit`, which measures what reuse would save before anyone decides
to use it.

With the lock on, the lookup is made before the lock (a hit does not wait in line) and again under
it (another checkout may have just tested the same tree). `--no-lock` is for a project that has the
lock on and a command that takes it itself for its heavy part: the same receipt logic, no lock held
by `jst test`.

### 9.2 The key

A SHA-256 over:

- the project's name, the command's words, the folder it runs in relative to the checkout, and
  jackstay's version;
- each input file's path, kind (file, link, missing), full permission bits and content. Inputs are
  the tracked files under `receipts.inputs` and the untracked files that the checkout's own
  committed `.gitignore` files do not ignore, less `receipts.exclude`, plus `receipts.extra_files`.
  A rule in `.git/info/exclude` or in a person's global ignore file hides nothing: a build tool may
  compile such a file all the same. The working tree is read, not the index, so an uncommitted edit
  changes the key and a commit of the same content does not. A link is its target's name and, when
  that is a file, its content. An `exclude` pattern is for the whole path from the root: `*.md` is
  a file at the root, `**/*.md` one anywhere, `docs/**` everything under `docs`;
- what each `receipts.toolchain` command prints (none by default; a command with `"reuse": true`
  needs the list written out, e.g. `[["swift", "--version"]]`), the system's version and build, the
  machine's kind, and every environment variable whose name matches `receipts.env` (names or
  patterns such as `APP_*`). `MAKEFLAGS`, `MFLAGS` and `MAKEOVERRIDES` are always in the key,
  whatever `receipts.env` says (they carry `make X=1` to an inner make), less the job server's
  address;
- the commits that `receipts.refs` name and the merge bases of HEAD with `receipts.merge_bases` (a
  command's own entry may add to both): for a command whose work depends on a base, such as "test
  what changed since `origin/main`";
- the input lists, the evidence and the veto patterns themselves;
- HEAD's commit, only when `receipts.include_head` is true.

No key (and so no receipt, no reuse and no `would-hit`) when:

- there is no `jackstay.json`, it names no `receipts.inputs`, an input is not a plain path inside
  the checkout, names no file, or the inputs less the excluded are no file at all;
- a folder (a submodule, a nested repository) or a link to a folder is among the inputs: what is
  behind it would not be read;
- a `.gitignore` is untracked, differs from HEAD, or is hidden from git by assume-unchanged or
  skip-worktree: only committed ignore rules may hide a file from the key. An untracked
  `.gitignore` inside a folder that a committed rule already ignores is the one exception;
- a ref cannot be resolved, a toolchain command fails (60 s each), or git fails (120 s each).

`jst receipts key -- COMMAND` prints the key a command would have here, what went into it, and
whether a receipt would be reused; nothing is run.

**What the key does not hold**, and the adopting project must judge, most likely to bite first:

1. environment variables that are not listed — a test skipped by an environment check, a variable
   that `make` turns into a make variable;
2. files the committed `.gitignore` ignores and the build still reads: the build folder and the
   fetched dependencies in it, edited package checkouts, a stale build — and a source file an
   ignore rule hides when the build compiles every file of a target, ignored or not. A
   `.gitignore` outside `receipts.inputs` is not in the key either: a rule committed there later
   hides a file without changing the key, so an old receipt may still be used. List the
   `.gitignore` files in `receipts.extra_files` when that matters;
3. anything outside the checkout: a package by path, the SDK (list e.g. `xcrun --show-sdk-version`
   under `toolchain`), a running daemon, the keychain, the network, the clock, the display and
   whether it is awake;
4. an empty untracked folder: git lists files, so a test that asks whether a folder exists is not
   a candidate;
5. a flaky test: a receipt records one pass.

Tests that depend on 2–4 are not candidates for reuse.

### 9.3 Outcomes, evidence and the veto

The last `jst test:` line on standard error names exactly one outcome:

| Outcome | Meaning | Exit |
|---|---|---|
| `passed` | the command ran now and ended 0, its output was read to its end, its tests were **counted** (above zero) and no veto line was seen. Nothing else is `passed` | 0 |
| `exited-0` | the command ended 0 and nothing counted its tests: it has no `evidence` pattern (or its judge is `exit`). The line reads `outcome=exited-0 (no test count: not proof that tests ran)`. Never a receipt, never counted as a pass | 0 |
| `reused` | **nothing was run**; a receipt for this key exists and reuse is allowed | 0 |
| `failed` | the command ran and ended non-zero | its status |
| `failed` | the command ended 0 and its output held a line its `veto` pattern names | 1 |
| `nothing-run` | the command ended 0 but its output showed no test: not a pass, no receipt | 76 |
| `unjudged` | the command ended 0 but its output could not be judged: it could not be read to its end, what it left could not be confirmed gone, or the evidence group matched something that is no number. Not a pass, no receipt | 77 |
| `passed-on-retry` | a structured report shows every test passed, but at least one only after its runner retried it (Surefire's `flakyFailure`/`flakyError`, a Jest assertion with more than one invocation). Not a clean pass, no receipt; a gate step with it fails the gate | 78 |
| `stopped` | `jst test` was asked to stop (SIGTERM, SIGINT, SIGHUP, SIGQUIT) after the command had started. Logged with the signal; no receipt | it ends by that signal |
| `stopped-waiting` | the same while it waited for the lock. Nothing was run | it ends by that signal |
| `gave-up` | only with the lock on: it was not had within `--wait`. Nothing was run | 75 |
| `not-started` | the lock or `jackstay.json` could not be used (an `evidence` pattern without a group is one such error): nothing was run | 2 |
| — | the command cannot start | 127 |
| `timed-out` | `jst test <scope>` only ([testing.md](testing.md) section 3) | 124 |
| `resumed`, `refused` | `jst test gate --resume` only ([testing.md](testing.md) section 5) | 0, 2 |

**An agent reads the last `jst test:` line**, not the status and not an earlier line: a command may
itself end with 2, 75, 76, 77 or 78, and then the line says `failed` with that status. When a line of
the record could not be written, a `WARNING` line says so on standard error just before the outcome
line, every time; it never turns a pass into a failure.

**Counted** is the command's `evidence` pattern (from its `receipts.commands` entry), which must
hold one group for a number: the numbers are added up over every matching line of both streams and
the sum must be above zero (`Executed ([0-9]+) tests?` for XCTest). It is a floor, not a proof, and
not the number of tests: a test tool may print its total once per enclosing suite, and a step that
ran no test is hidden by another that ran some. A `veto` pattern names a line that means failure
whatever the status says (a failure printed by a pipeline that lost its status); it stands without
an evidence pattern too. Escape sequences (colours) are taken out of a line before either pattern
is tried. For `jst test <scope>` the judges `swift-test`, `steps` and `exit` and the order in which
verdicts win are [testing.md](testing.md) section 2; `steps` is this evidence pattern.

**The output is read to its end, or the run is not a pass.** When the command has ended and its
output is still open (a process it left behind holds the pipe: `server &`), what is left of its
process group is stopped (section 5.2), so that the last line, also one without a newline, is read
and tried against the patterns: a veto line behind such a leftover is therefore seen. If the output
still cannot be read to its end within 10 s, the outcome is `unjudged`.

**What a run says.** Every run's output is kept in `~/.jackstay/<project>/outputs/<run_id>.log`
(the newest 30 per project by run id; the output of a run with no end in the record is kept, since
it may still be going, unless it is a week old) and named in the run's record (`output`); when it
cannot be kept, or a write to it fails, the run says so and goes on. A run that did not pass prints,
before its outcome line (which stays last), up to 40 lines of its output that say what went wrong
(`error:`, `failed`, `FAIL`, `N failures`, `fatal`, the veto's line), and every run says where the
whole output is. `jst test last` says it again, running nothing ([testing.md](testing.md)
section 7).

A reuse prints the receipt on standard error — the command, when it passed, how long it took, the
tests counted, the worktree, commit and branch it ran in, the receipt's path, and how to run it
anyway (`--no-reuse`) — and nothing on standard output, so it cannot be mistaken for a run's
output.

### 9.4 When a receipt is written, removed and used

**Written** (receipt schema 3: `schema`, `key`, `outcome`, `project`, `command`, `seconds`,
`evidence_count`, `started`/`started_epoch`, `ended`/`ended_epoch`, `material`, `jackstay`,
`run_id`, `worktree`, `head`, `branch`) only when all hold: status 0; the output read to its end;
no veto line; evidence above zero (so never for `exited-0`); a key could be made; the key computed
after the run equals the key computed before it; no input file was rewritten meanwhile, even to the
same content (each file's identity and times are compared); and no other run of the same key failed
after this one started. A rewritten input forbids the receipt because jackstay cannot tell it from
an edit made and undone under the tests. Why a pass stored none is in its record
(`not_stored_kind`: `no-key`, `tree-changed`, `input-rewritten`, `failed-since`, `write-failed`,
`no-evidence`).

**Removed** before the key's command runs again, so a run that is stopped or fails leaves none. A
run that does not pass also leaves a failure note beside it (`<key>.failed.<when>-<process>`, one
file per failure, never rewritten, so a slow writer cannot put an older time over a newer one): a
pass that started before the newest of them stores nothing, and a receipt whose run started before
it is never used.

**Pruned**: whoever writes a receipt or a note removes the project's receipts and notes older than
`receipts.max_age_hours` and than 24 hours (the larger of the two). They can no longer change
anything: a receipt that old is never used.

**Used** only when all hold:

- the command is listed in `receipts.commands` by its words exactly (`argv`) with `"reuse": true`
  and a usable `evidence` pattern (`argv_prefix`, the longest that matches, is for `"reuse": false`
  entries only);
- the project writes out `receipts.env` and `receipts.toolchain` (an empty list is a statement too);
- neither `--no-reuse` nor `JACKSTAY_NO_REUSE` is given;
- the branch is not in `receipts.no_reuse_branches` (default `main`, `master`), and HEAD is not the
  commit such a branch points at, here or at any remote's tip of that branch (a detached HEAD at
  `origin/main` is `main`, also where the local `main` is behind);
- the receipt is at most `receipts.max_age_hours` old (default 72; one dated up to five
  minutes ahead of the clock still counts), its file holds this key and `passed`, its schema is this jackstay's, and no failure note
  is newer than its run's start;
- the reuse could be written to the record (every reuse is counted, or it does not happen).

So reuse is opt-in per project and per command, and on `main` the command always runs: the whole
run after a batch of merges stays.

`jst receipts list [--project NAME]` lists the stored receipts (project, end, seconds, command,
head, key).

### 9.5 The run record and `jst receipts stats`

`~/.jackstay/.all/runs.jsonl`, one JSON object per line, written before any telemetry span
describes it (observability.md section 9). Every line has `at`, `epoch` (when the line was written),
`event`, `project`, `command`, `key`, `version`, `worktree`, `head`, `branch`, and `inside` (the
outer run's id) for a run nested in a `jst test` of the same project, which every count skips. A
command that is started writes two lines joined by one `run_id` (`<nanoseconds hex>-<pid>`):

- `start`, just before the command starts: `run_id`, `pid`, `started_epoch`, `locked`;
- `run`, when it has ended: `run_id`, `pid`, `locked`, `outcome`, `started_epoch` and `ended_epoch`
  (the command's own start and end, not the tool's), `seconds`, `evidence`, `receipt_removed`,
  `output`, and what belongs to the outcome (`evidence_count`, `stored` or `not_stored` and
  `not_stored_kind` for `passed` and `exited-0`; `status`, and `veto_line` after a veto, for
  `failed`; `why` for `unjudged`; `signal` for `stopped`). A scope's run adds its fields
  ([testing.md](testing.md) section 7).

A `start` with no `run` of the same `run_id` is a run that died without a word (SIGKILL, a crash,
the power) or is still going; `jst receipts stats` and `jst baseline` report these as
`unfinished`. `gave-up` and `stopped-waiting` are `run` lines without a `run_id`, with
`waited_seconds`: nothing was started.

Beside these: every `reused` (`saved_seconds`, the seconds its receipt's run took; `lookup`;
`receipt_of`), and every `would-hit` (`reason`, `saved_seconds`, `receipt`, `forced`). A
`would-hit` is a listed command that ran although the same key had passed before. It is counted
from the record, not from a stored receipt: the record's last `passed` run of the key counted
tests, is within the age limit, and no run of the key has failed or run nothing since. So a project
whose test step rewrites its own sources, and therefore never gets a receipt, is still measured.

`jst receipts stats [--project NAME] [--json]` adds them up per project: runs, reuses and seconds
saved (`passed` is counted runs only; `exited-0` has a column of its own); a run a person forced
with `--no-reuse` or `JACKSTAY_NO_REUSE` is counted apart, not as a saving missed.

| What goes wrong | What happens |
|---|---|
| No key can be made | The command runs; no receipt; the last lines say why. |
| The receipt file is damaged, old, of another key or of another schema | It is not used; the command runs. |
| The tree changed, or an input was rewritten, during the run | `passed`, no receipt, said so. |
| The receipt cannot be written | `passed`, said so; nothing is reused later. |
| The record cannot be written | Said on standard error just before the outcome line, every time. The outcome stands; the run is not counted; a reuse does not happen (the command runs). |
| A run of a tree that has a receipt fails or is stopped | The receipt was removed before the run; a failure leaves its note. |
| The output holds a veto line and the status is 0 | `failed`, exit 1; no receipt. |
| The command ended 0 and its output cannot be read to its end, or the evidence group matched a non-number | `unjudged`, exit 77; no receipt. |
| The command has no evidence pattern and ended 0 | `exited-0`, exit 0; no receipt; not a pass in any count. |
| `jst test` is stopped (Ctrl-C, `kill`) | The stop reaches the command's whole group, with the lock or without it; a command that ignores it is killed after 30 s; `stopped` is logged with the signal; no receipt. |
| `jst test` is killed outright (`kill -9`, the power) | A `start` line with no `run`: counted as `unfinished`. |
| `jackstay.json` cannot be used: also `"reuse": true` with `argv_prefix`, or an `evidence` pattern without a group | Status 2, `not-started`, before anything runs; `jst check` says the same. |

Every failure falls towards running the command. The one way to a false green is an input the key
does not hold (the list in 9.2).

### 9.6 `jst baseline`

    jst baseline --repo DIR --since YYYY-MM-DD --until YYYY-MM-DD [--project NAME] [--config FILE]
                 [--kind NAME=REGEX ...] [--gate NAME ...] [--merge-unit pr|commit] [--branch B]
                 [--transcripts GLOB ...] [--also NAME=DIR ...] [--projects-dir DIR]
                 [--jackstay-log FILE] [--examples FILE] [--json]

The measurement of one project over a span of UTC days, from Claude Code transcripts, git and
jackstay's run record. It reads, and changes nothing. What to count comes from the project's
`baseline` section (or `--config`): `kinds`, each a name and an RE2 pattern over one simple
command, `gate` marking a kind that stands before a merge; `merge_unit`; `branch`; `transcripts`
(the session folders, by name). The flags replace what they name. It reports:

- **test runs by kind**: every Bash call of the span read as a shell reads it (a word in a `grep`,
  an `echo` or a commit message starts nothing) and given the first kind one of its simple commands
  matches; attempts, refused (a hook), denied (another permission layer), started, and the
  transcript wall time of the started ones (a background call ends at its notification; a call
  answered in under five seconds is not timed); the same per UTC day;
- **merges**: subjects on the branch (first parent) ending in `(#N)`, or the branch's commits;
- **overlap**: per gate kind, how many timed runs shared five seconds of wall time with another
  gate run of the project or of one named with `--also`, and the medians;
- **destructive commands**: every Bash command of the span put through today's guard, split by what
  became of the call; git's answers of that day cannot be asked now, so the questions to git
  answer the way that finds nothing. `--examples FILE` writes each command the guard would refuse
  (it may hold private text);
- **jackstay's run record** (`jackstay_log`): runs that executed, by outcome, by kind and by scope,
  their time alone, reuses, `would-hit`, `gave-up`, `unfinished`, overlap with other recorded runs,
  and the lock evidence below.

**Whether simultaneous runs cause a problem** ([W-005](../decisions/W-005-lock-off-by-default.md))
is read from the run record, over three situations a recorded run can be in: `other` (another
project's recorded run shared at least 5 seconds with it), `same` (only another run of the same
project did), `alone` (no other recorded run was going). For each (`split`): `runs`; `not_well`,
the runs that ended `failed`, `unjudged` or `stopped`; and `then_passed_unchanged`, those whose key
passed again in a run that began after the failure ended and ended within two hours of it, and that
did not itself report its tree changing — a failure that was not the tree's.

`lock_evidence.shown` is true only when all hold, else `why_not` names each that failed:

- the span is at least 7 days (`span-under-7-days`);
- it holds at least 50 recorded runs of the project (`under-50-runs`), on at least 5 days
  (`runs-on-under-5-days`);
- at least 20 went alone (`under-20-alone`): without them there is nothing to compare with;
- at least 5 of the failures that then passed unchanged happened beside another run, `other` and
  `same` together (`under-5-repassed-beside`);
- their rate per run is at least 3 times that of runs that went alone, and above zero
  (`rate-not-3-times-alone`).

`shown` says that failures and simultaneous runs go together, not that one causes the other. It is
a proposal, never a switch.

What it cannot see: Codex sessions; runs started in a terminal outside a session; from transcripts
alone, pass or fail behind `| tail`; a project without `jst test` in its runs (its runs are not in
the record, so a run beside only such a run counts as `alone`); a tree whose key moved between a
failure and a pass (only the same key is "unchanged"); a run with no key, which can never count as
"then passed"; a `start` with no `run`, which is `unfinished`, not in the split.

## 10. Versions, checks and practices ([W-037](../decisions/W-037-current-go-and-dependencies.md))

Each was the newest release at its source when it was pinned (go.dev/dl, the Go module proxy's
release times, the actions' release pages):

| what | version | released |
|------|---------|----------|
| Go (`go.mod`: `go 1.27.0`, `toolchain go1.27.4`) | 1.27.4 | latest stable on go.dev/dl when pinned |
| mvdan.cc/sh/v3 (the guard's parser) | v3.14.1 | 2026-09-06 |
| golang.org/x/sys (`flock`, the process table on darwin) | v0.50.0 | 2026-11-06 |
| golang.org/x/sync (`errgroup`) | v0.25.0 | 2026-10-15 |
| github.com/google/go-github/v93 (`internal/githubapi`) | v93.0.0 | 2026-10-15 |
| go.opentelemetry.io/otel, `/trace`, `/sdk` (`internal/telemetry`) | v1.47.0 | 2026-10-02 |
| go.opentelemetry.io/proto/otlp, with google.golang.org/protobuf as it requires | v1.11.1, v1.36.12 | ([observability.md](observability.md) section 1) |
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

A tool is run pinned (`go run module/cmd@version`), never `@latest`; updating one is a commit that
changes its line here and in the justfile (and `release.yml`). Each action of a workflow is
pinned by its full commit hash, its version in a comment.

**Checks** are `just` recipes ([W-035](../decisions/W-035-justfile-not-makefile.md)): `check`
(gofmt over `cmd` and `internal`, `go vet`, golangci-lint v2 with errcheck, govet, ineffassign,
staticcheck, unused, revive, gosec, varnamelen, and gofmt/goimports as formatters:
`.golangci.yml`), `go` (`tests/go.sh`: `go vet`, every test with `-race`, and a build),
`integration` (the tests that need real git or processes, `//go:build integration`, with `-race`),
`security` (govulncheck, gosec at high severity and medium confidence, `.claude/` left out) and
`docs` (relative links lead somewhere, every decision is listed, and the site builds with no warning
and no broken link: section 6). `check` also runs shellcheck on `install.sh`. **The gate**
([W-040](../decisions/W-040-python-frozen-tests-by-change-readable-names.md)): `just gate`
(`jackstay.json`'s `gate` scope) runs `check`, `go`, `integration`, `security` and `docs` in that
order, stops at the first that fails, and prints `Ran N tests`. Outside the gate: `terminal` (the
tests that need a terminal, which the agent sandbox refuses; run before an install) and
`guard-latency` (the hook's wall time; timing is no pass or fail on a shared machine). The justfile
sets `GOCACHE`, `GOMODCACHE`, `GOLANGCI_LINT_CACHE` and `HUGO_CACHEDIR` for every recipe. `main` gets finished steps,
one gate run each, not every edit ([W-040](../decisions/W-040-python-frozen-tests-by-change-readable-names.md)).

**The workflow** `.github/workflows/gate.yml` runs `just gate` on GitHub's `ubuntu-latest` for every
pull request, every push to `main` and by hand: Go from `go.mod`'s toolchain line, read-only
permissions, the checkout's credentials not kept, a 30-minute limit, a pull request's older run
cancelled. It is the gate `main` requires. The Vitest producer check runs separately. There is no macOS job
([W-083](../decisions/W-083-no-macos-job-in-ci.md)): the macOS-only files (`*_darwin.go`) are
checked by the maintainers' `just gate` on a Mac before an install and a release.

**Practices**, and why:
- `cmd/<binary>` + `internal/<concern>` — the layout of section 1.
- The version and commit injected at build (`-X …/internal/version.Commit`), with the build
  info's VCS revision as the fallback — so a plain `go build` still names its commit (`+changes`
  when the tree had uncommitted changes, `unknown` when nothing is recorded).
- No logging library: what a command did is recorded as OpenTelemetry spans in the project's
  journal ([observability.md](observability.md)).
- Strict JSON decoding (unknown keys, a key twice, trailing data, oversize refused) —
  `internal/strictjson` on `encoding/json/v2` (stable in Go 1.27, which refuses repeated members
  and trailing data by default; `RejectUnknownMembers(true)` is set). `internal/config` keeps its
  own token walk because `jackstay.json`'s types are checked against the table, not a Go struct.
- Interfaces declared where they are used (a git or GitHub runner) and faked by hand in tests; the
  standard `testing` package with table-driven `t.Run`, `t.TempDir`, `t.Setenv`; no assertion
  library.
- For the process commands: shutdown in reverse order of what was started (`supervise.Closer`, a
  LIFO closer passed as an instance, never global), signals and the stop deadline as section
  5.2 has them, each phase under its own timeout, atomic writes (`os.CreateTemp` in the same
  folder, `Sync`, `Rename`, the temporary removed on every failure path), `//go:build integration`
  for tests that need real git or processes, as a scope of their own.
- Retries of GitHub reads (and any outside call that may fail for a moment): exponential backoff
  with jitter on `math/rand/v2`, bounded by the context's deadline; no retry library. Writes are
  never retried.
- No goroutine's error is lost: concurrent work runs under `errgroup` or reports on a channel that
  is read to the end; a goroutine never logs an error and returns.
- Shipped templates and texts (the AGENTS block, workflow and record templates) are
  `//go:embed`ded, not read from the checkout at run time.
- Tests clear every inherited `JACKSTAY_*` variable first (`internal/testrepo`) and work in
  `t.TempDir()`, so the developer's own `JACKSTAY_HOME` and settings never reach a test; no test
  depends on the time (W-013).
- Not taken: configuration libraries that accept unknown keys (`jackstay.json` must refuse them),
  RFC 7807 errors (jackstay maps typed errors to exit codes instead), `os.Exit` below `main`,
  `@latest` tools, unpinned CI.

## 11. Not in this spec

Ideas kept for later are not specified here, and nothing of them is built until it becomes a spec.
Among them: a mail hook, not built. jackstay keeps no merge queue of its own: `jst land` uses the
host's ([land.md](land.md)).
