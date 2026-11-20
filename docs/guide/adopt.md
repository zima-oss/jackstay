# Adopting a project

A project adopts jackstay by having a `jackstay.json` at the root of its repository, committed. One
command, `jst adopt`, writes it and everything else jackstay owns in a project, and tells you what
it will not decide for you.

## `jst adopt`

```sh
jst adopt [--plan] [--settings-only] [--profile PATH] [--profile-sha256 DIGEST] [--json] [DIR]
jst adopt --apply [--accept ID]... [--pin] [--settings-only] [--profile PATH] [--profile-sha256 DIGEST] [DIR]
jst adopt --block [--profile PATH] [--profile-sha256 DIGEST] [DIR]
```

`jst adopt` brings the project around `DIR` (default: here) to jackstay's reference. It owns four
places and nothing else:

| Place | What jackstay writes |
|---|---|
| `jackstay.json` | created with `jackstay_schema` and `project` (and, from jackstay 0.6.0, an editor schema association) when missing; a profile's defaults added where the project has no value; never a value you set |
| `AGENTS.md` | the tools block, between two `jackstay-adopt` markers; `CLAUDE.md` holding `@AGENTS.md` when there is no `CLAUDE.md` |
| `.claude/settings.json` | the guard hook (`PreToolUse`, matcher `Bash`, `jst-guard`), every other key kept |
| `.github/workflows/` | `card-line.yml` when the project has `cards` or `changes`; `release.yml` when it has `release` |

- `--plan` (the default) prints the changes and the **conflicts**, and writes nothing.
- `--apply` writes the changes. With any conflict left, it writes nothing at all.
- `--accept ID` lets `--apply` overwrite the one thing a conflict protects, for this run only.
- `--profile PATH` uses an organisation's profile (below); `--apply` records it in
  `jackstay.json` so later runs need no flag.
- `--profile-sha256 DIGEST` pins the exact portable profile bundle and records the digest for later runs.
- `--pin` moves `adopt.pin`, the jackstay release the workflows download, to the running version.
  Without it the pin never moves.

- `--settings-only` plans or writes `jackstay.json` alone. `jst lint pr` (rule D0) wants a change
  of `jackstay.json` in a pull request of its own, so a change of it and other files lands in two
  (below).
- `--block` prints the AGENTS block this project gets.
- Exit: 0 adopted (nothing to change, or everything written); 1 conflicts left; 2 usage, or
  `jackstay.json` or the profile cannot be read; 3 (`--plan`) changes pending, no conflict.

The generated `card-line.yml` and `release.yml` download the pinned Linux archive and check
`checksums.txt`. For releases with build attestations, set the repository variable
`JACKSTAY_PROVENANCE=attestation`; the workflow then requires a GitHub attestation for the pinned
tag, source repository and jackstay release workflow before unpacking. For older releases, an
independently recorded archive digest can be set as `JACKSTAY_LINUX_AMD64_SHA256` (64 lowercase
hex digits). Update that digest when moving the pin. When both variables are set, both checks must
pass. With neither variable, the workflow warns that the checksum is an integrity check only.

A second `--apply` leaves project files unchanged. If an apply stops partway through, rerun it
with the same options. jackstay checks the previous attempt against the files now on disk and
applies the remaining changes. `jst doctor` reports drift after a newer jackstay is installed.

### Example: a fresh repository

```sh
cd my-service
jst adopt
```

```
jst adopt: project my-service (/home/you/my-service), no profile
changes (4):
  agents-block                 AGENTS.md: new, with the block
  claude-md                    CLAUDE.md: new: @AGENTS.md (Claude Code reads CLAUDE.md)
  hook:guard                   .claude/settings.json: add the guard hook (PreToolUse, Bash, jst-guard)
  jackstay.json                jackstay.json: new: $schema, jackstay_schema, project
conflicts (0)
```

```sh
jst adopt --apply
jst adopt              # exit 0: nothing left
git add jackstay.json AGENTS.md CLAUDE.md .claude/settings.json
git commit -m "Adopt jackstay"
```

Each time you add a section to `jackstay.json`, run `jst adopt` again: the AGENTS block lists only
the tools the project has enabled, and a workflow is written when its section appears.

### The questions

Some of how jackstay behaves in a project is the project's choice, so adopt asks rather than
assumes. With `--apply` on a terminal it asks each question `jackstay.json` does not answer yet,
recommended answer first, and offers this machine's answer from `jst setup` when there is one:

| Question | Answers, recommended first | Written to |
|---|---|---|
| `reviewer`: who runs `jst review` | `codex`, `claude-code`, `codex,claude-code` | `review.use` |
| `chat`: the agent chat in this repository | `off`, `on` | `chat.enabled` (needs a pin of 0.6.0 or later; not asked before) |

A value the project already sets is never asked again (an empty `review.use` counts: each run then
names its reviewer). Without a terminal adopt writes no answer,
and the plan's `choices` say what applies and how to choose; `--answer ID=VALUE` answers without a
question and is written even over the project's value:

```sh
jst adopt --apply --answer reviewer=codex,claude-code
```

Two more of the owner's questions wait for keys this jackstay does not read yet: how far the agent
may land a change on its own (`jst setup` already keeps a machine answer), and which test scopes
CI runs.

### Conflicts

A conflict is a difference `jst adopt` must not decide. Each has a stable ID:

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

An exception keeps a conflict on purpose, with a reason, in `jackstay.json`:

```json
"adopt": {
  "exceptions": {
    "file:Makefile:present": "moving to a justfile after the 2.0 release"
  }
}
```

Never edit inside the `jackstay-adopt` markers: change `jackstay.json` or the profile instead.

## `jackstay.json`

The nearest `jackstay.json` at or above the folder a command runs in is the project's file. It is
read strictly: one JSON object, no key twice, an unknown key is an error. Every section is optional;
a command is stopped only by a broken section it reads.

```sh
jst check            # is the whole file usable? exit 0, or 2 naming every problem
jst patterns         # the patterns that are not RE2 (Go's dialect: no lookaround, no backreferences)
```

Put `jst check` in your project's own gate.

### Versions of the file

`jackstay_schema` says which jackstay a file was written for, so an older jackstay refuses a
newer file with "upgrade jackstay" rather than half-reading it:

| `jackstay_schema` | Needed for |
|---|---|
| 1 | `guard`, `test`, `receipts`, `lock`, `baseline`, `trash`, `telemetry` |
| 2 | `cards`, `ids`, `records`, `changes`, `procs`, `release`, `github`, `review`, `adopt` |
| 3 (jackstay 0.6.0 and later) | `$schema`, `chat`, `queue`, `brief`, `status`, `review.pool`, `adopt.profile_sha256`, `test.selection`, and a scope's, step's or receipts command's `selector`, `report`, `reports`, `empty_exit`, `xcresult_bundles` (and a receipts command's `judge`) |

A project whose workflows download a pinned jackstay (`adopt.pin`) must keep to what that release
reads. **Do not add `$schema` or another schema-3 key while the pin is older than 0.6.0**: the
pinned jackstay would refuse the whole file. `jst adopt` keeps to this: it writes `$schema` and
schema 3 only for jackstay 0.6.0 or later, and refuses to add a schema-3 key while the project is
pinned to an older release. When you move the pin, land the workflows that download it before a
change that adds schema-3 keys: until then, CI still runs the older release.

From 0.6.0, `$schema` names the [JSON Schema](https://github.com/zima-oss/jackstay/tree/main/schemas)
of one release, on its tag, for example
`https://raw.githubusercontent.com/zima-oss/jackstay/v0.6.0/schemas/jackstay.schema.json`. It
describes the keys, types and selected value choices accepted by `jst check`, is generated from
jackstay's runtime configuration table, and is checked for drift in tests. Editors that support
JSON Schema use it for completion and early diagnostics. When you move the pin, move the tag in
the URL with it. The schema cannot express every rule involving paths, other fields or Go regular
expressions; `jst check` remains the authoritative validator.

| Section | What it configures | Guide page |
|---|---|---|
| `$schema`, `jackstay_schema` (required), `project` | the editor schema, the file's version (1 to 3, see above) and the project's name | here |
| `guard` | `regenerable` folder names, your own `deny` rules, each rule's level | [guard](guard.md) |
| `test` | scopes, steps, judges, timeouts, leftovers | [tests](tests.md) |
| `receipts` | what goes into a tree's key, which commands may reuse a receipt | [tests](tests.md#receipts) |
| `lock` | the machine-wide lock for heavy runs (off by default) | [tests](tests.md#the-machine-wide-lock-jst-lock) |
| `baseline` | the kinds of test run `jst baseline` counts | [tests](tests.md#measuring-jst-baseline) |
| `procs` | `jst bg`'s default and maximum deadline | [background](background.md) |
| `trash` | how long `jst trash` keeps entries | [guard](guard.md#jst-trash) |
| `github` | the account jackstay acts as, the remote, wait defaults | [branches and GitHub](branches-and-github.md) |
| `review` | reviewers, brief, severities, round limit; present (even `{}`), the AGENTS block tells agents about `jst review` | [reviews](review.md) |
| `cards`, `ids`, `records` | the `Card:` line, number series, one-file records | [records](records.md) |
| `changes`, `release` | changelog entries, versions and the release workflow | [releases](releases.md) |
| `adopt` | the workflows' pin, the profile, kept exceptions | here |
| `brief` | `max_age_hours`: how long a saved brief stays fresh (default 168, 1 to 720) | `jst brief save --help` |
| `status` | `repos`: the repositories the overview reads when none are named | `jst workspace board --help` |
| `chat`, `queue` | session chat and the local merge queue | [session chat](session-chat.md) |
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

For a reviewed organization profile, export a portable bundle and obtain its SHA-256
through a trusted channel independent of the bundle download. Verify the exact bytes before
adopting them:

```sh
jst profile verify --sha256 "$expected" policy.bundle.json
jst adopt --plan --profile policy.bundle.json --profile-sha256 "$expected"
jst adopt --apply --settings-only --profile policy.bundle.json --profile-sha256 "$expected"
jst adopt --apply
```

The settings stage records `adopt.profile` and `adopt.profile_sha256`. Later plans and
applies reread the same artifact and reject a changed digest, a loose profile or a missing
bundle. To upgrade, independently verify the new bundle and give its expected digest to
`--profile-sha256` on the first settings stage. The following files stage reads the pinned
bundle again. Keep the bundle accessible at the recorded profile path in linked worktrees.

Keep each released bundle at its own path so that the previous version remains available for
rollback. For example, after adopting `policy-v1.bundle.json`, upgrade and then roll back with
the same two stages:

```sh
jst profile verify --sha256 "$v2_digest" policy-v2.bundle.json
jst adopt --apply --settings-only --profile policy-v2.bundle.json --profile-sha256 "$v2_digest"
jst adopt --apply

jst profile verify --sha256 "$v1_digest" policy-v1.bundle.json
jst adopt --apply --settings-only --profile policy-v1.bundle.json --profile-sha256 "$v1_digest"
jst adopt --apply
```

Review and land the `jackstay.json` settings stage before applying the remaining files when the
project's pull request rules require that separation. Local settings outside the profile's
defaults remain in `jackstay.json` through both changes.

A profile is refused as a whole (exit 2) when a key is unknown, a path leaves its folder, a
workflow's `uses:` is not pinned to a full commit hash, a template uses an undefined `@@name@@`, or a
default would not pass `jst check`.

## Bringing a project up to date, for an agent

When jackstay changes, a project's session can adopt the new version with this prompt:

> Bring this project up to date with jackstay. Run `jst adopt --plan` (with `--profile <path>` the
> first time, if there is one). Settle each conflict it lists: do the migration as the project's
> own work, or add an exception with its reason to `jackstay.json`'s `adopt.exceptions`. Before
> `--accept …:edited`, move the project's own lines out of the text the plan shows will be
> replaced; never edit inside the `jackstay-adopt` markers. If the pin is to move (`--pin`), or the
> change against the base would touch `jackstay.json` and other files (adopt's writes or your own
> exceptions), land two pull requests (rule D0): first
> `jst adopt --apply --settings-only` (with `--pin`, `--profile` and the accepted IDs of
> `jackstay.json`'s conflicts), `jackstay.json` alone, for the owner to merge; then, on the merged
> base, `jst adopt --apply` with the other accepted IDs. Otherwise one `jst adopt --apply` with the
> accepted IDs. Then `jst adopt --plan` must exit 0. Run `jst doctor` and the project's gate, and
> list in each pull request each conflict and how it was settled.

## What can go wrong

- **`jst check` exit 2 after an edit**: the message names the key. An unknown key is an error, not
  ignored: a misspelt safety setting must not be silently off.
- **A newer schema**: a file written for a newer jackstay is refused with the advice to upgrade.
- **The workflows download a release you have not published**: they download `adopt.pin` from
  jackstay's public releases. Move the pin after installing a new version, in two pull requests:
  1. `jst adopt --apply --pin --settings-only`: `jackstay.json` alone, merged by the owner;
  2. then, on the merged base, `jst adopt --apply`: the workflows for the new pin, merged as usual.

  Between the two, `jst adopt --plan` lists the files still to write.
- **A block fitted by hand without markers** is not recognised: remove it, and let `--apply` append
  the generated one.

The design: [adopt.md](../design/adopt.md).

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
