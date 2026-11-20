# Adopting a project

You get jackstay's files into a project with one command, `jst adopt`, and commit them. It writes
`jackstay.json`, the block in `AGENTS.md` that tells agents which `jst` commands to use, the guard
hook, and the workflows a project's sections need, and it tells you what it will not decide for
you.

## Adopt a project

```sh
cd my-service
jst adopt
```

```text
jst adopt: project my-service (/home/you/my-service), no profile
changes (4):
  agents-block                 AGENTS.md: new, with the block
  claude-md                    CLAUDE.md: new: @AGENTS.md (Claude Code reads CLAUDE.md)
  hook:guard                   .claude/settings.json: add the guard hook (PreToolUse, Bash, jst-guard)
  jackstay.json                jackstay.json: new: $schema, jackstay_schema, project
conflicts (0)
```

`jst adopt` alone (or `--plan`) prints the changes and the conflicts and writes nothing. Write them,
check that nothing is left, and commit:

```sh
jst adopt --apply
jst adopt
git add jackstay.json AGENTS.md CLAUDE.md .claude/settings.json
git commit -m "Adopt jackstay"
```

From jackstay 0.6.0, `jst adopt` also writes the `land` section, which says who merges a change
(see [branches and GitHub](branches-and-github.md#the-land-section)). Before it does, `--apply` asks
one question at the terminal:

```text
Who merges this project's changes (land.autonomy)?
  1) The agent merges when the required checks pass; `jackstay.json` and the listed paths need a person (recommended)
  2) The agent prepares; a person merges.
Answer [1]:
```

Without a terminal, give the answer as `--autonomy merge-when-green` or `--autonomy prepare`;
`--apply` without it exits 2. An answer the file already has is kept: adopt never changes it. Answer
once per machine with `jst setup` and adopt offers that answer first.

`jst adopt` owns four places and changes nothing else:

| Place | What jackstay writes |
|---|---|
| `jackstay.json` | created with `jackstay_schema` and `project` (and, from jackstay 0.6.0, an editor schema association) when missing; a profile's defaults added where the project has no value; never a value you set |
| `AGENTS.md` | the tools block, between two `jackstay-adopt` markers; `CLAUDE.md` holding `@AGENTS.md` when there is no `CLAUDE.md` |
| `.claude/settings.json` | the guard hook (`PreToolUse`, matcher `Bash`, `jst-guard`), every other key kept |
| `.github/workflows/` | `card-line.yml` when the project has `cards` or `changes`; `release.yml` when it has `release` |

Run `jst adopt --apply` again each time you add a section to `jackstay.json`: the block then tells
agents about the new tools, and a workflow is written when its section appears. A second
`--apply` with nothing new leaves the files unchanged. If an apply stops partway, rerun it with the
same options: it checks the previous attempt against the files on disk and applies the rest.

Every flag and exit status: `jst adopt --help`.

## The AGENTS block

The block lists, one line per task, the `jst` commands this project has enabled: run tests, wait,
background, branches, discard, the guard, GitHub, review, cards, numbers, changelog, releases, what
happened, healthy here. It names only commands the installed jackstay has and sections the
project's `jackstay.json` has. Claude Code reads it through the `CLAUDE.md` that holds
`@AGENTS.md`.

```sh
jst adopt --block
```

prints the block this project gets. Never edit inside the `jackstay-adopt` markers: `jst adopt`
finds a hand edit by its hash and stops with the conflict `agents:edited`. To change the block,
change `jackstay.json` or the profile: a profile adds lines (`agents.rules`) or leaves items out
(`agents.leave_out`).

## Conflicts

A conflict is a difference `jst adopt` must not decide. With any conflict left, `--apply` writes
nothing at all. Each conflict has a stable ID:

| ID | When | Settled by |
|---|---|---|
| `agents:edited` | the text between the markers was edited by hand | move your lines outside the markers, then `--accept agents:edited` |
| `agents:markers` | the markers are malformed | fixing the file by hand |
| `workflow:<name>:unmarked` | a workflow of that name exists and is not jackstay's | `--accept`, or an exception |
| `workflow:<name>:edited` | a jackstay workflow was edited | `--accept`, or an exception |
| `file:<path>:missing`, `file:<path>:present` | the profile requires or forbids a path | the change the profile names, or an exception |
| `jackstay.json:<key>:missing` | the profile requires a key | adding it, or an exception |
| `scope:<name>:missing`, `scope:<name>:runner` | the profile requires a test scope, or how scopes are run | changing the scope, or an exception |
| `jackstay.json:invalid` | the file with the profile's defaults would not pass `jst check` | changing your value |
| `hook:settings-unreadable` | `.claude/settings.json` is not a JSON object | fixing the file by hand |

`--accept ID` lets `--apply` overwrite the one thing that conflict protects, for this run only. An
exception keeps a conflict on purpose, with a reason, in `jackstay.json`:

```json
"adopt": {
  "exceptions": {
    "file:Makefile:present": "moving to a justfile after the 2.0 release"
  }
}
```

## Bring a project up to date

After you install a newer jackstay, `jst doctor` reports a project that has drifted from it. The
workflows download the jackstay release pinned in `adopt.pin`; `--pin` moves the pin to the running
version, and without it the pin never moves.

A pull request that changes `jackstay.json` changes nothing else (rule D0 of `jst lint pr`), so
moving the pin takes two pull requests:

1. `jst adopt --apply --pin --settings-only`: `jackstay.json` alone, merged by the owner;
2. then, on the merged base, `jst adopt --apply`: the workflows for the new pin, merged as usual.

Between the two, `jst adopt --plan` lists the files still to write.

The upgrade from 0.5.x to 0.6.0 takes three pull requests, each merged by a person, and the notes
of `jst adopt --plan` name them and the one due:

1. the pin: `jst adopt --apply --pin --settings-only`; `jackstay.json` stays schema 2, which 0.6.0
   reads;
2. the workflows that download 0.6.0, with `card-line.yml`, which runs `jst lint pr` for the
   coming `land` section: `jst adopt --apply` on the merged base;
3. schema 3 with the `land` section: `jst adopt --apply` on the merged base, which asks who merges
   (`--settings-only` keeps it to `jackstay.json`; the AGENTS block follows in the next apply).

Each plan holds only the step that is due: a later step is planned once the earlier one has merged.
Step 3 also waits for every workflow you keep (your own, or one kept by an exception) that
downloads jackstay to name a `JACKSTAY_VERSION` of 0.6.0 or later; until then the plan has a
`blocker:` note naming the file. Record a profile's digest (`--profile-sha256`) in step 3 or after
the upgrade: step 1 refuses it, since it needs schema 3.

The generated `card-line.yml` and `release.yml` download the pinned Linux archive and check
`checksums.txt`. For releases with build attestations, set the repository variable
`JACKSTAY_PROVENANCE=attestation`; the workflow then requires a GitHub attestation for the pinned
tag, source repository and jackstay release workflow before unpacking. For older releases, an
independently recorded archive digest can be set as `JACKSTAY_LINUX_AMD64_SHA256` (64 lowercase
hex digits). Update that digest when moving the pin. When both variables are set, both checks must
pass. With neither variable, the workflow warns that the checksum is an integrity check only.

A session can do the update with this prompt:

> Bring this project up to date with jackstay. Run `jst adopt --plan` (with `--profile <path>` the
> first time, if there is one). Settle each conflict it lists: do the migration as the project's
> own work, or add an exception with its reason to `jackstay.json`'s `adopt.exceptions`. Look at
> each workflow the plan lists as the project's own: keep it, or remove from it what jackstay's
> workflows now do. Before
> `--accept …:edited`, move the project's own lines out of the text the plan shows will be
> replaced; never edit inside the `jackstay-adopt` markers. If the pin is to move (`--pin`), or the
> change against the base would touch `jackstay.json` and other files (adopt's writes or your own
> exceptions), land two pull requests (rule D0): first
> `jst adopt --apply --settings-only` (with `--pin`, `--profile` and the accepted IDs of
> `jackstay.json`'s conflicts), `jackstay.json` alone, for the owner to merge; then, on the merged
> base, `jst adopt --apply` with the other accepted IDs. Otherwise one `jst adopt --apply` with the
> accepted IDs. Then `jst adopt --plan` must exit 0. Run `jst doctor` and the project's gate, and
> list in each pull request each conflict and how it was settled.

## `jackstay.json`

The nearest `jackstay.json` at or above the folder a command runs in is the project's file. It is
read strictly: one JSON object, no key twice, an unknown key is an error, so a misspelt safety
setting is never silently off. Every section is optional; a command is stopped only by a broken
section it reads.

```sh
jst check
jst patterns
```

`jst check` names every problem in the file (exit 2) and warns about the test section; put it in
your project's own gate. `jst patterns` lists the patterns that are not RE2, Go's dialect (no
lookaround, no backreferences).

### Versions of the file

`jackstay_schema` says which jackstay a file was written for, so an older jackstay refuses a
newer file with "upgrade jackstay" rather than half-reading it:

| `jackstay_schema` | Needed for |
|---|---|
| 1 | `guard`, `test`, `receipts`, `lock`, `baseline`, `trash`, `telemetry` |
| 2 | `cards`, `ids`, `records`, `changes`, `procs`, `release`, `github`, `review`, `adopt` |
| 3 (jackstay 0.6.0 and later) | `$schema`, `land`, `chat`, `brief`, `status`, `review.pool`, `adopt.profile_sha256`, `test.selection`, and a scope's, step's or receipts command's `selector`, `report`, `reports`, `empty_exit`, `xcresult_bundles` (and a receipts command's `judge`) |

A project whose workflows download a pinned jackstay (`adopt.pin`) must keep to what that release
reads. **Do not add `$schema` or another schema-3 key while the pin is older than 0.6.0**: the
pinned jackstay would refuse the whole file. `jst adopt` keeps to this: it writes `$schema` and
schema 3 only for jackstay 0.6.0 or later, and refuses to add a schema-3 key while the project is
pinned to an older release. When you move the pin, land the workflows that download it before a
change that adds schema-3 keys: until then, CI still runs the older release.

From 0.6.0, `$schema` names the [JSON Schema](https://github.com/zima-oss/jackstay/tree/main/schemas)
of one release, at its tag, for example
`https://raw.githubusercontent.com/zima-oss/jackstay/v0.6.0/schemas/jackstay.schema.json`, so your
editor completes keys and flags wrong types and values. Move the tag in the URL when you move the
pin. The schema cannot express rules about paths, other fields or Go regular expressions:
`jst check` remains the authoritative check.

| Section | What it configures | Guide page |
|---|---|---|
| `$schema`, `jackstay_schema` (required), `project` | the editor schema, the file's version (1 to 3, see above) and the project's name | here |
| `guard` | `regenerable` folder names, your own `deny` rules, each rule's level | [guard](guard.md) |
| `test` | scopes, steps, judges, timeouts, leftovers | [tests](tests.md) |
| `receipts` | what goes into a tree's key, which commands may reuse a receipt | [tests](tests.md#receipts) |
| `lock` | the machine-wide lock for heavy runs (off by default) | [tests](tests.md#the-machine-wide-lock-jst-lock) |
| `baseline` | the kinds of test run `jst baseline` counts | [tests](tests.md#measuring-jst-baseline) |
| `procs` | `jst bg`'s default and maximum deadline | [background](background.md) |
| `trash` | how long `jst trash` keeps entries | [guard](guard.md#delete-undoably-jst-trash) |
| `github` | the account jackstay acts as, the remote, wait defaults | [branches and GitHub](branches-and-github.md) |
| `land` | who merges (`autonomy`), the merge method, extra required checks, paths only a person lands | [branches and GitHub](branches-and-github.md#the-land-section) |
| `review` | reviewers, brief, severities, round limit; present (even `{}`), the AGENTS block tells agents about `jst review` | [reviews](review.md) |
| `cards`, `ids`, `records` | the `Card:` line, number series, one-file records | [records](records.md) |
| `changes`, `release` | changelog entries, versions and the release workflow | [releases](releases.md) |
| `adopt` | the workflows' pin, the profile, kept exceptions | here |
| `brief` | `max_age_hours`: how long a saved brief stays fresh (default 168, 1 to 720) | `jst brief save --help` |
| `status` | `repos`: the repositories the overview reads when none are named | `jst workspace board --help` |
| `chat` | session chat | [session chat](session-chat.md) |
| `telemetry` | the journal's retention and how command text is recorded | [observability](observability.md) |

### A real example

This is close to jackstay's own `jackstay.json`: a Go project whose checks are `just` recipes.

```json
{
  "jackstay_schema": 2,
  "project": "jackstay",
  "receipts": {
    "inputs": ["."],
    "exclude": [".claude/**", ".idea/**"],
    "toolchain": [["git", "--version"], ["go", "version"]],
    "env": ["JACKSTAY_*", "GO*"]
  },
  "test": {
    "judge": "steps",
    "timeout": 1800,
    "scopes": {
      "gate": {
        "run": ["just", "gate"],
        "when": "before a finished step is pushed, and before an install",
        "evidence": "Ran ([0-9]+) tests?",
        "veto": "^FAILED"
      },
      "x-go": {
        "run": ["just", "go"],
        "when": "after any change to cmd/, internal/ or go.mod",
        "evidence": "Ran ([0-9]+) tests?",
        "veto": "^FAILED"
      },
      "x-docs": {
        "run": ["just", "docs"],
        "when": "after a change to documents",
        "judge": "exit"
      }
    }
  },
  "ids": {
    "W": {"files": "docs/decisions/W-{n}-*.md", "width": 3}
  },
  "procs": {"default_minutes": 30, "max_minutes": 240}
}
```

### Keep policy and local credentials separate

Configure project policy in `jackstay.json` so linked worktrees can share it. Keep machine-specific
authority and transient identities in the relevant launcher, credential store or invocation.

| Capability | Project configuration | Local or per-run input |
|---|---|---|
| Session chat | `chat.enabled` and bounded message, pending, session and retention settings | MCP host registration, session label, optional reconnect token |
| Local merge queue | `queue.enabled` and `queue.base_ref` | Head to enqueue, local repository, candidate and evidence |
| Structured tests | `test.scopes`, judge, report names, selector and duration settings, path selection | Test ID, revision or base to test |
| Review | Reviewer commands, options, selected reviewers and limits | Local provider authentication and optional invocation override |
| Installation and adoption | Profile and pin when a project needs them | Installer arguments, local permissions and external trust credentials |

The editor schema describes keys, types, ranges and selected choices. Runtime checking still
validates Git refs, RE2 syntax, file relationships and other rules that use local state.

## Profiles

A profile carries an organisation's rules, so that many projects adopt jackstay the same way. It is
one JSON file of data, kept outside jackstay (in a repository of your organisation, say), passed
once with `--profile`.

| Key | What it does |
|---|---|
| `files.required`, `files.forbidden` | paths that must or must not exist (with what replaces a forbidden one) |
| `scopes.required`, `scopes.run_with` | test scopes every project declares, and the commands scopes may start with |
| `jackstay_json.required`, `jackstay_json.defaults` | keys a project must set; sections and keys added where missing |
| `workflows.<name>` | the organisation's own workflow templates, replacing jackstay's of the same name |
| `values` | names its templates use as `@@name@@` |
| `agents.rules`, `agents.leave_out` | lines added to the AGENTS block; block items left out |

The example profile, with every capability: [docs/profiles/example/profile.json](../profiles/example/profile.json),
with its own [card-line workflow](../profiles/example/workflows/card-line.yml), which runs on
self-hosted runners.

```sh
jst adopt --profile ../org-profile/profile.json            # the plan with the profile
jst adopt --apply --profile ../org-profile/profile.json    # recorded in jackstay.json's adopt.profile
jst adopt                                                   # later runs need no flag
```

For a reviewed organisation profile, adopt a portable bundle pinned by its SHA-256. Get the
expected digest through a trusted channel, not from where you downloaded the bundle, and adopt in
two stages, settings first:

```sh
jst profile verify --sha256 "$expected" policy.bundle.json
jst adopt --plan --profile policy.bundle.json --profile-sha256 "$expected"
jst adopt --apply --settings-only --profile policy.bundle.json --profile-sha256 "$expected"
jst adopt --apply
```

The settings stage records `adopt.profile` and `adopt.profile_sha256`; later runs reread the same
bundle and refuse a changed digest, a loose profile or a missing bundle. Keep the bundle at the
recorded path in linked worktrees too. When the project's pull request rules require it, land the
settings stage before applying the other files. Your own settings outside the profile's defaults
stay in `jackstay.json`.

To upgrade or roll back, keep each released bundle at its own path (`policy-v1.bundle.json`,
`policy-v2.bundle.json`) and run the same verify and two stages with that bundle and its digest:

```sh
jst profile verify --sha256 "$v2_digest" policy-v2.bundle.json
jst adopt --apply --settings-only --profile policy-v2.bundle.json --profile-sha256 "$v2_digest"
jst adopt --apply
```

A profile is refused as a whole (exit 2) when a key is unknown, a path leaves its folder, a
workflow's `uses:` is not pinned to a full commit hash, a template uses an undefined `@@name@@`, or a
default would not pass `jst check`.

## What can go wrong

- **`jst check` exit 2 after an edit**: the message names the key.
- **A newer schema**: a file written for a newer jackstay is refused with the advice to upgrade.
- **The workflows download a release you have not published**: they download `adopt.pin` from
  jackstay's public releases. Move the pin after installing a new version
  ([Bring a project up to date](#bring-a-project-up-to-date)).
- **A block fitted by hand without markers** is not recognised: remove it, and let `--apply` append
  the generated one.

The design: [adopt.md](../design/adopt.md), [the AGENTS block](../design/adopt.md#8-the-agents-block).
