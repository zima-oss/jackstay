# `jst adopt`: one command brings a project to jackstay's reference; profiles carry an organisation's rules

Status: built. It follows [W-058](../decisions/W-058-projects-adopt-through-jst-adopt.md) (`jst adopt
--plan | --apply`, idempotent, one short prompt, the AGENTS block rewritten and printed from
`jackstay.json`, existing workflows classified), [W-059](../decisions/W-059-runner-agnostic-core-and-profiles.md)
(jackstay's core runner-agnostic; an organisation's rules are a profile kept outside jackstay; an example
profile in jackstay's docs), [W-013](../decisions/W-013-quality-testing-declarative.md) (declarative,
nothing named after a project), [W-037](../decisions/W-037-current-go-and-dependencies.md)
(actions pinned by full hash), [W-056](../decisions/W-056-observability-otel-traces-local.md) (`jst log`,
`jst stats`).

## 1. What it removes

Without it, a project learns jackstay from a block of text copied by hand into its `AGENTS.md` and
fitted there, and from workflow files copied by hand and fitted
(measured). Every jackstay change to a tool then means a
prose instruction to each project, and several agents turn one instruction into different
results. Only a tool is idempotent (W-058).

`jst adopt` computes, from the project's `jackstay.json`, the running jackstay and an optional profile, what
the project's jackstay-owned files must hold; `--plan` prints the difference and the conflicts it must
not decide; `--apply` writes the difference when no conflict is left. A second `--apply` writes
nothing. `jst doctor` reports a project that has drifted.

## 2. The command

```
jst adopt [--plan] [--settings-only] [--profile PATH] [--json] [DIR]
jst adopt --apply [--accept ID]... [--pin] [--settings-only] [--profile PATH] [DIR]
jst adopt --block [DIR]           # the AGENTS block this project gets, printed; nothing written
```

- `DIR` (default `.`): any folder of the project; the project is the nearest `jackstay.json` above it,
  else the git work tree's top (a project with no `jackstay.json` yet gets one, section 3.1).
- `--plan` is the default. It reads, never writes.
- `--profile PATH`: the profile (section 6). Without it, the profile the project's `jackstay.json`
  names in `adopt.profile`, if any. Given, `--apply` records it there (section 3.1).
- `--accept ID` (repeatable, only with `--apply`): let `jst adopt` overwrite the one thing the
  conflict `ID` protects (section 5). Accepting is for one run; nothing is remembered.
- `--pin`: set `adopt.pin` (the jst release every jackstay-written workflow downloads) to this jackstay's
  version (section 7.3). Without it the pin is never moved.
- `--settings-only` (with `--plan` or `--apply`): `jackstay.json` alone, its changes and its
  conflicts; the other files' are planned again by the apply after it merges, and an `--accept` of
  one of theirs is refused here ([W-085](../decisions/W-085-adopt-lands-settings-and-files-apart.md)).
- `--json`: the plan as one object `{"project", "profile", "changes": [...], "conflicts": [...],
  "exceptions": [...], "notes": [...]}`; each change `{"id", "path", "what"}`, each conflict
  `{"id", "path", "what", "settle"}`.

Exit status:

| status | `--plan` | `--apply` |
|---|---|---|
| 0 | nothing to change, no conflict: adopted | done: every change written (or none was needed) |
| 1 | conflicts (changes may also be pending) | conflicts: **nothing written** |
| 2 | usage; `jackstay.json` or the profile unreadable or invalid | the same |
| 3 | changes pending, no conflict | — |

`--apply` is all or nothing in what it decides: with one unsettled conflict it writes no file.
Before the first write jackstay compares the project inputs with the contents read during planning.
It checks each target again just before writing. A mismatch stops the apply and names the file.
Writes are individually atomic. The local delivery record under
`<jackstay home>/.all/delivery/<worktree digest>/adopt.json` holds only paths and fingerprints,
with the phase and outcome of the latest attempt. After a stop, rerun the same `--apply` options.
A new plan resumes the previous operation when all inputs and remaining outputs still match
and the recorded completed effects are visible. If the state has changed, adoption plans a new
operation and applies its usual conflict checks.

## 3. What `jst adopt` owns

Four places, and inside each only what is marked as jackstay's. Everything else in the project is the
project's: `jst adopt` reads it to find conflicts and never changes it.

### 3.1 `jackstay.json`

- **Created** with `{"jackstay_schema": 2, "project": "<the folder's name>"}` when the project has none;
  a jackstay of 0.6.0 or later writes `{"$schema": "<its schema on its tag>", "jackstay_schema": 3, …}`.
- **`jackstay_schema` moved to 3** when the file would carry a key that needs 3 (`config.Schema3Keys`).
  A key the apply adds is refused instead while a jackstay before 0.6.0 still reads the file: the pin
  before the apply, the pin after it, or the release a managed workflow on disk downloads, read from
  its `JACKSTAY_VERSION` and from its own template's `@@jackstay_version@@` lines (the settings land
  before the workflows, W-085); a workflow whose release cannot be told counts as old until it is
  rewritten for the pin. That jackstay would refuse the file.
- **`jackstay_schema` moved from 1 to 2** when the file would carry a section that needs 2 (`config`'s
  `Stage1Sections`, which gains `adopt`). Schema 1 to 2 is a number only (2 reads every schema-1
  file the same way).
- **The profile's defaults** (section 6, `jackstay_json.defaults`) added where the project has no value:
  a missing section is added whole; inside an existing object only missing keys are added,
  recursively; a value the project has (a list, a string, a different number) is never replaced.
- **The `adopt` section**, new in the table of keys:

  ```json
  "adopt": {
    "pin": "v0.5.0",
    "profile": "../org-profile.json",
    "exceptions": {
      "file:Makefile:present": "the move to a justfile is card 130, after the 1.0 release"
    }
  }
  ```

  `pin` — the jst release the workflows download (`v0.5.0`), section 7.3; written by the first
  `--apply` that writes a workflow using it, moved only by `--pin`.
  `profile` — the profile's path as given to `--profile`, relative to the project's root unless it
  starts with `/` or `~/`; written by `--apply`, read by later runs and by `jst doctor`.
  `exceptions` — conflicts the project keeps on purpose, by ID, each with its reason (section 5);
  written by the project's maintainers, never by `jst adopt`. The section is written only when there is
  a pin, a profile or an exception, so a project adopted without any has no `adopt` key.

- Rewritten whole, keys in their existing order and new keys after them, two spaces of indent,
  one newline at the end — only when something changes. The text is read and written through one
  ordered JSON model (`internal/adopt/jsondoc`), so a run that changes nothing leaves the file
  byte for byte.

### 3.2 The AGENTS block

The block (section 8) lives in `AGENTS.md` between two markers:

```
<!-- jackstay-adopt: begin agents-block sha256:3f1c9a0b2d4e — written by `jst adopt` from jackstay.json and the profile; change those, not this text -->
...
<!-- jackstay-adopt: end agents-block -->
```

The hash is the first 12 hex digits of the SHA-256 of the text between the markers as written. A
block whose text no longer matches its hash was edited by hand: a conflict (section 5), never
overwritten silently.

Where it goes:

- `AGENTS.md` at the project's root; created when missing. If `CLAUDE.md` is missing too, it is
  created holding `@AGENTS.md` (Claude Code reads `CLAUDE.md`). If `CLAUDE.md` exists and names
  neither `AGENTS.md` nor the block, a note says so; it is not changed.
- In place of the markers when they are there; else appended at the end, after one blank line.
  A block fitted by hand, without the markers, is not recognised; remove it when adopting.
- A file with two begin markers, an end before a begin or a begin with no end: a conflict
  `agents:markers`, settled only by hand.

### 3.3 The guard hook in `.claude/settings.json`

The reference is one `PreToolUse` entry with matcher `Bash` whose command runs `jst-guard`:

```json
{"matcher": "Bash", "hooks": [{"type": "command", "command": "\"$HOME/.local/bin/jst-guard\"", "timeout": 10}]}
```

- Satisfied by any `PreToolUse` hook with matcher `Bash` whose command's last path element,
  quotes removed, is `jst-guard`.
- Missing: added (the file and the folder created when missing), every other key and hook kept in
  order. The same ordered model as `jackstay.json`. The command names the guard launcher of the
  install `jst` runs from (`P/bin/jst-guard` for `P/lib/jackstay-*/jst`), else of `~/.local`; it is
  written `"$HOME/…"` when the launcher is under the account's home, so the file names no account,
  and as a quoted absolute path otherwise.
- The plan does not ask whether the launcher exists (a checkout or CI may have nothing installed);
  `jst doctor`'s `adoption` check does: a guard hook whose command does not run an executable
  `jst-guard` (`$HOME`, `${HOME}` and `~` read as home) is a problem, since Claude Code would run
  every command unguarded.
- A `settings.json` that is not a JSON object: a conflict `hook:settings-unreadable`.
- The install `jst` runs from (an install's own launcher leading to it, `layout.RunningPrefix`) has
  no executable `bin/jst-guard` (an install that stopped part way): a conflict
  `hook:no-guard-launcher`, settled by running the original install command again with the same
  options (its `--prefix`, `--checkout` or `--binary`; the conflict names the prefix, quoted); no
  hook is written that cannot run.

### 3.4 Workflows

Each workflow template (section 7) whose condition holds is written to
`.github/workflows/<name>.yml`, with a first line naming it as jackstay's:

```
# jackstay-adopt: card-line sha256:9b0e3d17a6c2 — written by `jst adopt`; change jackstay.json or the profile, not this file
```

The hash covers everything after the line. A marked file whose hash holds is
jackstay's: rewritten when the rendering differs. A file of that name that is not marked (the project's
own, or a copy made by hand) or whose hash fails (edited by hand) is a conflict.

**The inventory**: every file in `.github/workflows/` whose first line is a
`jackstay-adopt:` header is jackstay's, whatever its name. A marked file that the current templates and
conditions do not want — its section removed, its template dropped or renamed in the profile, the
project moved to another profile — is a change, **removed**, when its hash holds, and the conflict
`workflow:<name>:edited` when it does not. So a workflow that publishes or deploys cannot outlive
the template that wrote it while the plan says the project is adopted. Unmarked workflows of
other names are the project's: listed as notes (section 7.2), never read further. A header that
cannot be parsed is `workflow:<name>:edited` too.

## 4. Order of work and what `--plan` prints

`jst adopt` reads `jackstay.json` (strictly, with `config.Parse`; an invalid file is exit 2 and
`jst check`'s message), the profile, the AGENTS files, `.claude/settings.json`, `.github/workflows/`
and the paths the profile names. Then, in this order and from one value:

1. **The prospective `jackstay.json`**: the project's file, the profile's defaults merged (section
   3.1), `jackstay_schema` moved when needed.
2. **Judged whole**, by the same checks as `jst check` (the table of keys and every section's
   meaning checks). A prospective file that fails — a default that cannot sit beside a value the
   project has, like `procs.max_minutes` 240 under an existing `default_minutes` 300 — is the
   conflict `jackstay.json:invalid` with `jst check`'s words, settled by changing the project's value
   (a value the project sets is never replaced, so the default then stays out); it cannot be
   excepted. Nothing is written while it stands.
3. **Everything else is computed from that one prospective file**: the block's items, the
   workflows' conditions and values, the profile's required keys and scopes. So a default that
   enables an item (say `lock.enabled`) is in the block the same `--apply` writes, and a second
   run finds nothing.

**Order**: keys `jst adopt` adds to an object go after its existing keys, in
lexical order; within each group of the plan, entries are sorted by ID; profile workflows and
values are taken in lexical order of their names. Nothing depends on a Go map's order. A test
plans the same input bytes twenty times and compares.

Text output, in this order, each line led by its ID:

```
jst adopt: project storefront (/home/…/storefront), profile ../org-profile.json
changes (4):
  jackstay.json:jackstay_schema        jackstay.json: jackstay_schema 1 -> 2
  jackstay.json:github           jackstay.json: add github (from the profile)
  agents-block             AGENTS.md: append the block (34 lines)
  workflow:card-line       .github/workflows/card-line.yml: new
conflicts (2) — nothing is written until each is settled:
  file:knope.toml:present  the profile forbids knope.toml: changelog entries and releases are
                           `jst changes` and jackstay's release workflow
  file:Makefile:present    the profile forbids Makefile: recipes live in a justfile
exceptions (0)
notes (1):
  .github/workflows/pr-checks.yml is the project's own; jackstay leaves it alone
```

For every `:edited` conflict the plan also prints the text that would be
lost, so what matters can be moved out before accepting.

## 5. Conflicts

A conflict is a difference `jst adopt` must not decide. Each has a stable ID, used in `--accept`
and in `adopt.exceptions`:

| ID | when | settled by |
|---|---|---|
| `agents:edited` | the marked block's hash fails | moving the edit out (to the project's text or the profile), then `--accept` |
| `agents:markers` | the markers are malformed | the file fixed by hand |
| `workflow:<name>:unmarked` | `.github/workflows/<name>.yml` exists and is not jackstay's | `--accept` (jackstay's template replaces it), or an exception (the project keeps its own; the template is not written) |
| `workflow:<name>:edited` | a marked workflow's hash fails | `--accept`, or an exception |
| `file:<path>:missing` | the profile requires the path | creating it, or an exception |
| `file:<path>:present` | the profile forbids the path | the migration the profile names (`instead`), or an exception |
| `jackstay.json:invalid` | the prospective `jackstay.json` (section 4) fails `jst check` | changing the project's value; cannot be excepted |
| `jackstay.json:<key>:missing` | the profile requires a key (dotted path) the project lacks and the profile gives no default for | adding it, or an exception |
| `scope:<name>:missing` | the profile requires a `test` scope | declaring it, or an exception |
| `scope:<name>:runner` | a scope's command (`run[0]`, or each step's) is not one the profile runs scopes with | changing it, or an exception |
| `hook:settings-unreadable` | `.claude/settings.json` is not a JSON object | the file fixed by hand |
| `hook:no-guard-launcher` | the install `jst` runs from has no executable `bin/jst-guard` | the original install command again, with the same options |

Rules:

- `--accept` takes only the IDs whose settlement says so; any other is exit 2. An `--accept` for
  a conflict that is not there is exit 2 too (a typo must not pass as a decision).
- An exception needs a non-empty reason. It turns its conflict into an entry under `exceptions`
  in the plan, and the conflict no longer blocks `--apply` or `jst doctor`. An exception that
  matches no conflict is a note ("stale exception"), never an error: the project may remove it.
- The malformed-file conflicts (`agents:markers`, `hook:settings-unreadable`) and `jackstay.json:invalid`
  cannot be excepted.
- `jst adopt` decides no migration: Knope, a Makefile, review adapters, a
  missing gate scope are each a conflict with the profile's words on what replaces them. The
  project does the migration (a change of its own, a pull request where it is large) or records an
  exception with the reason.

## 6. Profiles

A profile is one JSON file — data, never code (W-059) — read strictly like `jackstay.json` (unknown keys
refused). Files it names are relative to its own folder and must stay inside it. Every key is
optional but `jackstay_profile` and `name`.

```json
{
  "jackstay_profile": 1,
  "name": "example",
  "about": "what this profile is for, one line",
  "files": {
    "required": [{"path": "justfile", "why": "the recipes the scopes run"}],
    "forbidden": [{"path": "Makefile", "why": "one runner per project", "instead": "a justfile"}]
  },
  "scopes": {
    "required": ["gate"],
    "run_with": ["just"]
  },
  "jackstay_json": {
    "required": ["github.account"],
    "defaults": {"procs": {"default_minutes": 30, "max_minutes": 240}}
  },
  "workflows": {
    "card-line": {"file": "workflows/card-line.yml", "when": ["cards", "changes"]}
  },
  "values": {"runner": "self-hosted"},
  "agents": {
    "rules": ["`just gate` is the gate; `jst test gate` runs it."],
    "leave_out": ["owner"]
  }
}
```

| key | capability | conflict it makes |
|---|---|---|
| `files.required[]` | a path (file or folder) that must exist | `file:<path>:missing` |
| `files.forbidden[]` | a path that must not exist, with what replaces it | `file:<path>:present` |
| `scopes.required` | `test` scopes the project must declare | `scope:<name>:missing` |
| `scopes.run_with` | the commands a scope may start with (`run[0]` of the scope, or of each step) — how scopes are run | `scope:<name>:runner` |
| `jackstay_json.required` | dotted keys the project's `jackstay.json` must have | `jackstay.json:<key>:missing` |
| `jackstay_json.defaults` | sections and keys added where missing (section 3.1); each must pass `jst check`'s table | — |
| `workflows.<name>` | a workflow template of the profile's: `file`, `when` (sections of `jackstay.json`, any present; absent means always). A name jackstay also embeds replaces jackstay's template | the workflow conflicts |
| `values` | names the profile's templates use as `@@name@@` (section 7.3) | — |
| `agents.rules` | lines added at the end of the AGENTS block, under "This project's organisation" | — |
| `agents.leave_out` | IDs of block items (section 8) the organisation does not use | — |

A profile is invalid (exit 2, nothing planned) when: a key is unknown; a path leaves its folder;
a template has a `uses:` not pinned by a full hash (section 7.1); a template uses a `@@name@@`
neither jackstay nor `values` defines; a default does not pass the `jackstay.json` table; `leave_out` names
no item.

jackstay's core never names a runner, an organisation, a runner group or an App: they are in a
profile's data. The example profile with every capability is in
[docs/profiles/example/](../profiles/example/profile.json); an organisation keeps its own profile outside jackstay.

### Portable bundles

`jst profile export PROFILE` writes bundle format 1 with a canonical JSON profile, the exact
template bytes it references and a SHA-256 of that payload. It prints the digest of the exact
bundle file separately. `jst profile verify --sha256 DIGEST BUNDLE` compares that digest against
the expected value from the recipient and checks the bundle with the same validator as
`jst adopt --profile`.

Adoption reads the templates inside the bundle. The existing `adopt.profile` path identifies the
bundle, and the existing `adopt.pin` value controls the workflow binary version. Move between
retained versions by verifying a new bundle and previewing that path on the settings-only
stage, followed by the managed files stage. See [the profile guide](../profiles/README.md#distribute-a-profile-offline).

## 7. Workflow templates

### 7.1 Embedded in jackstay

jackstay embeds (Go `embed`) two templates, general enough for anyone (W-059): GitHub's own runners
(`ubuntu-latest`), jackstay downloaded from its public release (no App), no container.

| name | condition (`jackstay.json` has) | does |
|---|---|---|
| `card-line` | `cards` or `changes` | the `Card:` line, the changelog entry and the tree's rules on every pull request event, including `edited` (the description is what is checked) |
| `release` | `release` | `jst release publish`, then `jst release pr` on every push to the base; the writing App's variable and secret keep their names (`JACKSTAY_RELEASE_APP_CLIENT_ID`, `JACKSTAY_RELEASE_APP_PRIVATE_KEY`) |

Every `uses:` in a template — embedded or a profile's — must be `owner/repo[/path]@<40 hex>`
followed by a `# vX.Y.Z` comment, a local `./path`, or `docker://…@sha256:<64 hex>`; anything else
makes the template invalid (W-037). A test checks the embedded ones; `jst adopt` checks a
profile's on every run.

Kept for the fork guard: both templates run only for branches of the same repository
(`if: github.event.pull_request.head.repo.full_name == github.repository` on `card-line`), so a
self-hosted runner a profile substitutes never runs a fork's code.

### 7.2 The project's own workflows

`jst adopt` does not judge what a project's own workflow does: an overlap check — a template's
pattern matched against the other files — would have to be right for every state of the folder
before and after an apply, under renames, accepts and exceptions (W-019: a place that keeps failing
is redesigned, not patched). The plan instead lists, as notes, every workflow jackstay does not own
(unmarked, or marked and kept by an exception), and the checklist of section 11 asks for each to
be classified: keep it, or remove from it what jackstay's templates now do.

### 7.3 Values

A template's text holds `@@name@@` where a value goes. jackstay defines:

| name | value |
|---|---|
| `jackstay_version` | `adopt.pin` from the prospective `jackstay.json`; when it has none, `v` + this jackstay's version, which the same `--apply` writes to `adopt.pin` |
| `release_base` | `release.base` from `jackstay.json`, else `cards.branch`, else `main` |
| `jackstay_repository` | where jackstay's releases are downloaded from (`zima-oss/jackstay`; jackstay's own home, one constant in `internal/adopt/templates`) |

A profile adds its own through `values`.

**The pin is data in `jackstay.json`, never read back from a generated file**: reading it from the
workflow would make the version depend on the file's state, so a template could hide it and
accepting a repaired file would move it. Every rendering is a function of the prospective `jackstay.json`, the profile
and jackstay's templates alone: a newer jackstay does not make every project drift, `--accept` of an edited
workflow renders it with the same pin, and a project moves its pin on purpose (`--pin`), in a pull
request of its own.

**A change of `jackstay.json` and other files lands in two pull requests**, settings first: rule D0
of `jst lint pr` lands `jackstay.json` alone. `jst adopt --apply --pin --settings-only` writes it
(the owner merges it); then `jst adopt --apply` on the merged base writes the workflows from it, an
ordinary pull request. Between the two, `jst adopt --plan` on the base lists exactly the files left.
An `--apply` that writes both says so.

## 8. The AGENTS block

Short and led by the task an agent has in hand, one line per task, the details left to `jst <command> --help` (section 9). It is printed
from `jackstay.json`: an item appears only when what it needs is there, so the block never names a tool
the project has not enabled. An item also appears only when every command it names is in this
jackstay's command table, so a block is never ahead of the binary that prints it.

The text, for a project with every section (`internal/adopt/block`, golden-tested):

```markdown
## jst: the tools for working on this project

`jst` is installed on this machine. Reach for it before a hand-written shell loop:
`jst <command> --help` gives a command's details, `jst help` lists every command.

- **Run tests**: `jst test <scope>`; `jst test list` shows the scopes and when each is required.
  Only `passed` is a pass — read the last `jst test:` line. After a failure, `jst test last`, not
  a rerun. Before a pull request, `jst test gate` (`--commit HEAD`: on a clean copy jackstay makes and
  removes). No scope fits: `jst test -- COMMAND`.
- **Share a counted pass** with `jst receipts export KEY`. Verify it with `jst receipts verify`
  using the expected digest, revision and key from a trusted source.
- **Wait** for a process, a file or a log line: `jst wait … --timeout S`, never a `sleep` loop.
- **Run something in the background**: `jst bg --for MIN --name N -- COMMAND`, never `&` or
  `nohup`; `jst procs` lists them, `jst procs stop NAME` stops yours.
- **A heavy command** others on this machine must not overlap: `jst lock -- COMMAND`.
- **Branches**: `jst start NAME` (a branch and its worktree from the fetched default branch),
  `jst sync` (merges the default branch in; never rebases), `jst tidy` (what is finished;
  `--fix --branch NAME` removes only what you made).
- **Drop uncommitted changes**: `jst discard PATH…` — what it drops goes to jackstay's trash
  (`jst trash restore`); never `git restore` or `git checkout -- PATH`.
- **The guard** refuses destructive commands and names what to run instead: run that, never a
  workaround. A line `jst guard let this command run, with a warning` means read it before going
  on. This project sets the guard's rules in `jackstay.json` (`guard`).
- **GitHub**: `jst gh ci-wait` / `ci-status` (a pull request's checks), `run-wait`, `pr-status`,
  `pr-land` (push, open, wait, squash-merge the head that was checked). jackstay acts as the account in
  `jackstay.json` (`github.account`).
- **Review a change**: `jst review BASE..REV --task NAME --reviewer NAME` runs a cold review.
  Pick `codex` or `claude-code`, or use the project's `review.use`.
  `jst review check BASE..REV --task NAME` checks the saved round.
- **Commits to the default branch** carry `Card: <ID>` or `Card: none (why)`; check with
  `jst lint range origin/main..HEAD` before pushing; `jst cards ID` shows what landed per card.
- **Numbers** come from `jst id next SERIES`, never by hand; `jst new SERIES --slug S` makes a
  record, `jst view SERIES` prints their table.
- **A user-facing change** gets a changelog entry: `jst changes new --slug S`; `jst changes check`.
- **Releases** are made by the release workflow's pull request, never a tag by hand;
  `jst release prepare` shows the next one.
- **What happened**: `jst log --session ID` (or `--trace ID`) and `jst stats REPORT` — after a
  surprising failure, before asking.
- **Follow work across tools** with `jst timeline`, the local PR, test, review and background history.
- **Which agents are here**: `jst inventory` reports this repository's worktrees and observed sessions.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Share an adoption profile** with `jst profile export`. Verify its digest before adopting the bundle.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
```

Item IDs, what each needs, and what changes with the section:

| ID | needs | variation |
|---|---|---|
| `test` | always | with no `test.scopes`, only `jst test -- COMMAND`; `gate` sentence only with a `gate` scope |
| `wait`, `bg`, `branches`, `discard`, `guard`, `doctor` | always | the guard's last sentence only with a `guard` section |
| `lock` | `lock.enabled` true | |
| `github` | `github.account` | |
| `review` | `review` (a section, even `{}`: every key has a default, W-051) | |
| `cards` | `cards` | the branch from `cards.branch`, else `origin/main` |
| `ids` | `ids`; the `jst new`/`jst view` half with `records` | |
| `changes` | `changes` | |
| `release` | `release` | |
| `log` | the commands `log` and `stats` in the table | |
| `evidence` | `receipts` | |
| `inventory`, `timeline`, `owner` | always | |
| `profiles` | `adopt` | |

Then the profile's `agents.rules`, under `### This project's organisation`, one bullet each.

The full text of each item is the code's; this section is its approved shape. A change to an item
is a change to the golden file and to this section in the same commit.

`docs/adoption/agents-block.md` says the block is printed by `jst adopt --block` and points to the
items. When a tool is added or its use changes, the item in `internal/adopt/block` and its golden
file change in the same commit; after an install, `jst doctor` in a project reports the drift and
`jst adopt --apply` there settles it (the checklist of section 11).

## 9. `jst <command> --help`

The block's promise needs every command to answer `--help` the same way, and before it runs: a
command that took `--help` for an argument could act on it (read it as a series name, or move a
file named `--help` into the trash).

So `cli.Main` answers `--help` (or `-h`) given as the only word after a command's name, before the
command runs:

- `cli.Command` has `Usage string`: the usage line(s); every option the command accepts, one line
  each saying what it does; the exit statuses when they are more than 0, 1 and 2; one example of
  the common use. `jst X --help` prints `Summary` and `Usage`, exit 0.
- A word that is only the first word of two-word commands (`jst changes --help`, `jst lint --help`)
  prints those commands with their summaries.
- `jst help X` is the same as `jst X --help`.
- A test walks the table: every command has a `Usage` with a usage line, and `--help` runs nothing
  (the command's `Run` is never called). Every command's `--help` output is a committed golden
  file (`internal/cli/testdata/help.txt`), so a review reads all help text in one place and a
  change to it shows in the diff.

## 10. `jst doctor` reports drift

`jst doctor`, run inside a project (a `jackstay.json` at or above the current folder), adds one check,
`adoption`: it runs the plan (the profile from `adopt.profile`) and reports

- a **problem** (doctor's exit 1) for each unsettled conflict and when changes are pending: "this
  project is not up to date with jackstay N.N.N: run `jst adopt --plan`";
- a **problem** when `adopt.profile` names a file that is missing or invalid;
- a **note** for each exception, with its reason.

Outside a project the check is skipped. `jst doctor --json` carries the summary under
`facts.adoption` and each finding with the kind `adoption`.

## 11. Bringing a project up to date: the checklist

A checklist for whoever brings a project up to date with jackstay after an upgrade, a person or an
agent; the same for every project and every jackstay version:

> Bring this project up to date with jackstay.
>
> 1. Run `jst adopt --plan` (the first time, with `--profile <path>` if your organisation has one; give
>    the same `--profile` to the first `--apply` too — it is recorded only then).
> 2. Settle each conflict it lists, and look at each workflow it lists as the project's own:
>    keep it, or remove from it what jackstay's workflows now do. For a migration (`file:…:present`, `scope:…`), do it as the
>    project's own work with its card, or add an exception to `jackstay.json` (`adopt.exceptions`) with
>    the reason and the task that will do it; ask the project's maintainer where the choice is theirs.
>    Before `--accept …:edited`, move this project's own lines out
>    of the text the plan shows will be replaced. Never edit inside the `jackstay-adopt` markers.
> 3. Run `jst adopt --apply` with the `--accept` IDs you decided on (and the `--profile` of step
>    1); then `jst adopt --plan` must print nothing to change, name the profile, and exit 0.
> 4. Run `jst doctor`: no problem for this project. Run the project's gate.
> 5. Open a pull request with the result, listing each conflict and how it was settled. When its
>    diff against the base changes `jackstay.json` and other files (adopt's writes, or your own
>    exceptions), rule D0 wants two: `jackstay.json` alone first (`jst adopt --apply
>    --settings-only`, with `--pin` and `jackstay.json`'s `--accept` IDs), merged by the owner; then,
>    on the merged base, steps 3 to 5 again for the other files.
>
> Done means `jst adopt --plan` exits 0 and `jst doctor` reports no adoption problem — not your
> word.

## 12. Packages

| package | holds |
|---|---|
| `internal/adopt` | the command, the plan (wanted state vs files), conflicts and exceptions, apply (atomic writes), the text and JSON output, the doctor entry `adopt.Check` |
| `internal/adopt/jsondoc` | an ordered JSON model: parse with key order, set a key, write with two-space indent; byte-identical when unchanged |
| `internal/adopt/block` | the AGENTS block's items, conditions, markers |
| `internal/adopt/profile` | the profile's strict reading and validation |
| `internal/adopt/templates` | the embedded templates, `@@name@@` rendering, the pin check, the marked header |
| `internal/cli` | `Usage`, `--help` before `Run`, `jst help X` |
| every command package | its `Usage` text |
| `internal/config` | the `adopt` section in the table; `adopt` in `Stage1Sections` |
| `internal/doctor` | the `adoption` check |
| `docs/profiles/` | the example profile, its README, its example template |

## 13. Test plan

- **Golden: apply twice → no diff.** For four fixture projects (empty folder with git; a schema-1
  project with a block fitted by hand; a project with every section; the example profile
  applied), `--apply` with the needed `--accept`s, then a second `--apply` writes nothing (every
  file's bytes and modification time unchanged) and `--plan` exits 0.
- **jackstay adopts itself first**: in `cmd/jst` (so the real command table is used), the plan of
  jackstay's own checkout has no change and no conflict. When a command is added or a block item
  changes, the test fails until `jst adopt --apply` is run in jackstay's checkout and its
  `AGENTS.md` is committed with the change.
- **Ownership**: a marked block or workflow edited by one byte → `:edited`; an unmarked
  `card-line.yml` (a copy made by hand) → `:unmarked`; `--accept` replaces; an exception
  keeps it and `--apply` succeeds; a stale exception is a note; `--accept` of an absent ID is
  exit 2.
- **Markers**: malformed markers refused.
- **`jackstay.json`**: schema 1 → 2 only when needed; defaults never replace a value; key order and
  bytes kept when nothing changes; `adopt` written only with a profile or exception; a default
  enabling an item (`lock.enabled`) lands with its block line in one `--apply`; a default that
  breaks a meaning check beside the project's value (`procs`) is `jackstay.json:invalid`; the same input
  planned twenty times gives the same bytes.
- **Hook**: present in the reference form → nothing; missing → added, other hooks kept;
  not an object → conflict.
- **Profiles**: every key of the example profile exercised; each invalidity of section 6 refused;
  a path outside the profile's folder refused; an unpinned `uses:` refused.
- **Templates**: every embedded `uses:` pinned (40 hex + version comment); every `@@name@@`
  defined; the pin comes from `adopt.pin` only: written by the first apply, moved only by
  `--pin`, unchanged by `--accept workflow:<name>:edited` under a newer jackstay; a template renamed in
  the profile — the old marked file removed, the new one written, a second apply clean; a marked
  workflow whose template left the profile is removed (hash holds) or a conflict (edited), and
  kept by an exception it is listed among the project's own workflows.
- **Block**: a golden file per variation of section 8 (no sections; every section; no scopes;
  `leave_out`); an item naming a command missing from the table is left out.
- **`--help`**: the table walk of section 9; `jst trash --help` moves nothing.
- **Doctor**: drift and a missing profile are problems; exceptions are notes; outside a project
  skipped.

## 14. What it does not do

- It changes only the project it runs in: each project is brought up to date in its own repository
  (section 11).
- It has no Codex-specific integration (an `AGENTS.md` hook for Codex); Claude Code and Codex
  integrations are separate.
