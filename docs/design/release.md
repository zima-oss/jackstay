# The release features: typed entries, the next version, the release pull request

Status: built.

Rulings it follows: [W-013](../decisions/W-013-quality-testing-declarative.md) (declarative,
nothing named after a project; no time-dependent tests),
[W-017](../decisions/W-017-review-fixes-and-threat-model.md) (the guard is against accidents),
[W-020](../decisions/W-020-version-tags.md) (one version, one commit, a tag never moved),
[W-026](../decisions/W-026-release-scope-now-in-parallel.md) (the scope: types, audience, version
rules, app versions, `jst release prepare`, the "change is documented" check),
[W-029](../decisions/W-029-queue-self-merge-configurable-go-last.md) (who may merge the release pull
request is a project setting), [W-034](../decisions/W-034-release-pr-is-a-request-app-commits.md)
(the release pull request is a request; the writing App makes the release commit),
[W-047](../decisions/W-047-release-redesign-fixed-settings-no-one-off-imports.md) (`jackstay.json`
is fixed settings; prepare is a preview; no one-off imports). Background:
Knope (measured),
release features (measured). A sentence that rests
on none of them says "inferred". How the Go build is laid out is
[implementation.md](implementation.md)'s; section 13 says what this one needs of it.

## What it removes

| Without it | With it |
|---|---|
| A change file says only `major`, `minor` or `patch` (as Knope's do); an untyped `changes/<slug>.md` ([implementation.md](implementation.md) section 4.3) says nothing at all | An entry has a type (breaking, feature, fix, deprecated, security, note) and an audience (consumer or internal); a breaking entry carries a migration note |
| The version is written by hand, and often never bumped or tagged | The next version is computed from the entries; an override needs a recorded reason; 1.0 only by override |
| A "documented change" check and a release pull request depend on a separately installed bot (Knope) | `jst lint pr` requires an entry or `Changes: none (<reason>)`; a workflow on the project's runners keeps the release pull request on `jackstay/release` |
| A tag and a GitHub release are made by hand, if at all | Landing the release pull request releases exactly what it asked for: the workflow commits the version, tags it and publishes the notes; a tag is never moved |

What it does not do ([W-026](../decisions/W-026-release-scope-now-in-parallel.md)): an API-breakage check against the last tag; the
next-version preview in a pull request's check; prereleases; assets on a GitHub release; building
or uploading an app. jackstay's own versions stay under W-020 (by hand, `internal/version/version.go`) until
jackstay adopts this itself, which is not part of this spec.

## How a release happens, in short

1. Sessions land changes, each with an entry (`changes/<slug>.md`, typed).
2. After each landing on `main`, a workflow (GitHub Actions) keeps one
   pull request open on `jackstay/release`. It changes one file, the **request**: the next version and the
   exact list of entries it releases. Its description shows the release as it will be written.
3. The project's owner (or, where the project allows, an agent) merges it. That is the decision to release.
4. The landing is a push to `main`, so the workflow runs again. If the request still agrees exactly
   with what `main` holds, the workflow commits the prepared release to `main` (version files,
   changelog, entries removed) with the writing App, tags that commit and publishes the GitHub
   release. If it does not agree (something landed between the request and its merge), nothing is
   released, and the pull request is refreshed for another look.

jackstay keeps no state of its own: what was released, and what is waiting, is read from git each time.

## 1. The project file

Two parts of `jackstay.json`: the `changes` section ([implementation.md](implementation.md) section 4.3), extended, and a new `release` section. A project
may have `changes` without `release` (entries and the documented-change check, no versions), never
`release` without `changes`. `jackstay_schema` stays 2: the new keys are optional, and a jackstay that does not
know them refuses the file as a whole (an unknown key is an error, [implementation.md](implementation.md) section 3.2), so the
new jackstay is installed locally and the workflows' pin moved before a project commits them (below,
"One reader").

```json
"changes": {
  "dir": "changes",
  "into": "CHANGELOG.md",
  "heading": "## {version} — {date}",
  "newest": "first",
  "internal_into": "docs/internal-changes.md"
},
"release": {
  "merge": "owner",
  "branch": "jackstay/release",
  "base": "main",
  "tag": "v{version}",
  "first": "0.1.0",
  "files": [
    {"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}
  ],
  "build": {"path": "Config/Version.xcconfig", "match": "^CURRENT_PROJECT_VERSION = (?P<build>[0-9]+)$"}
}
```

| Key | Meaning | Default |
|---|---|---|
| `changes.internal_into` | where internal entries are collected; without it they go only into the release commit's message and the release pull request's description (section 5) | none |
| `release.merge` | who merges the release pull request: `owner` (only the project's owner; the pull request is a draft, section 6) or `agents` (any session, `jst gh pr-land`, later the queue) — W-029 | `owner` |
| `release.branch` | the release pull request's head branch | `jackstay/release` |
| `release.base` | the branch releases are made from and land on | `cards.branch`, else `main` |
| `release.tag` | the tag's name; holds `{version}` once | `v{version}` |
| `release.first` | the version the version files hold when the `release` section is added: the last release before jackstay kept them; tags at or below it are history jackstay does not read | required |
| `release.files` | the version files: each a path and a pattern with one named group `version` | required, at least one |
| `release.build` | an app's build number: a path and a pattern with one named group `build`, a decimal integer | none |

Patterns are RE2 (Go's `regexp`; `(?P<name>…)` is the group syntax), applied
with multi-line mode on (`^` and `$` at line ends). `jst check` refuses: a pattern that does not
compile, one without exactly one group of the right name; a path outside the repository (links
followed and refused when they lead out); **any path named twice** among
`release.files`, `release.build`, `changes.into` and `changes.internal_into`, or any of them inside
`changes.dir` (so no two rewrites ever touch one file, and no output is an entry); `jackstay.json` itself,
or any file under `.github/`, named as any of them (a release commit changes no configuration and no
workflow); `release.tag`
without exactly one `{version}`; a `release.merge` other than the two words; `release.branch` equal
to `release.base`; `release.first` that is not a version; and `release` without `changes`. Names
are checked as git will use them, before anything is prepared or pushed: `release.branch` and
`release.base` as `refs/heads/<name>`, and `release.tag` expanded with `release.first` as
`refs/tags/<name>`, each by git's ref-name rules (`git check-ref-format`); a name git would refuse
makes the file unusable.

**One reader.** The
rules of this section (RE2 patterns, paths and links, outputs that read back their raised values,
ref names, typed templates) are one Go validator, which `jst check` runs, as does every command of this
spec before anything else; `jst changes check` prints it in full. Tested with a malformed template, a
pattern that is not RE2, and a pattern that cannot read its raised result.

## 2. Entries

### The file

An entry is one file, `<changes.dir>/<slug>.md`, written in the commit that makes the change ([implementation.md](implementation.md)
section 4.3). From this spec on it has a front matter of one JSON object between `---` lines — the
shape records have ([implementation.md](implementation.md) section 4.3) — then a title line, then an optional body:

```markdown
---
{"type": "breaking", "audience": "consumer", "card": "WDG-130"}
---
# `WidgetForm` takes its rows as a builder

What changed, for the changelog (optional).

## Migration

Replace `WidgetForm(rows: [...])` with `WidgetForm { ... }`; the rows keep their order.
```

| Key | Values | Required |
|---|---|---|
| `type` | `breaking`, `feature`, `fix`, `deprecated`, `security`, `note` | yes |
| `audience` | `consumer` (goes into `changes.into`) or `internal` (section 5) | yes |
| `card` | a card ID matching `cards.id` | no |
| `yanks` | a released version this note marks yanked (section 8); only with `type` `note` | no |

The title is the first non-empty line after the front matter, a single line starting `# `, not
empty. A `breaking` entry must have a `## Migration` heading followed by at least one non-empty line
before the next heading or the end. Any other key in the front matter, a front matter that is not
one JSON object, a type or audience outside the lists, a missing title, or a breaking entry
without its migration note makes the entry **invalid**.

In a project with `release`, an entry, the override and the request must be tracked **regular
files** (git mode `100644` or `100755`): a symbolic link is invalid there, since a request binds an
entry by its blob, and a link's blob is its target's name, not the text released. The same holds for
the **outputs**: every file of `release.files` and `release.build` must be a tracked regular file,
and `changes.into` and `changes.internal_into` a tracked regular file or absent (prepare creates it);
no folder on any of these paths, nor `changes.dir`, may be a symbolic link. So prepare only ever
writes the named file itself, two names cannot reach one file, and a release commit changes exactly
the paths its proof names. `jst check` and prepare refuse otherwise. Which files are
entries is unchanged from the untyped entries of [implementation.md](implementation.md) section 4.3: `<slug>.md` with a slug of lower-case letters,
digits and `-`; another `.md` beside them (a `README.md`) is not an entry and is named on standard
error. Two files in `changes.dir` are jackstay's own and are not entries: `_release.json`,
the override (section 3), and `_release-request.json`, the request (section 3).

**Untyped entries** ([implementation.md](implementation.md) section 4.3). A file with no front matter is an untyped entry,
valid in a project without a `release` section: `jst changes view` and `collect` render it in one
list under the heading. In a project with `release` it is invalid, named with the fix (add the
front matter).

### Commands

    jst changes new --slug SLUG [--type T --audience A --title TEXT [--migration TEXT]] [--card ID]
    jst changes check [--json]                   every entry, the override and the request, valid or each failure named
    jst changes view [--version V] [--date D] [--audience consumer|internal|all]
    jst changes collect --version V [--date D] [--dry-run]   for projects without `release`

In a project **without** `release`, `new` is the command of [implementation.md](implementation.md) section 4.3 (the
template, or a one-line entry), and `--type`, `--audience`, `--title` are optional: given, they make a typed entry. In a
project **with** `release`, `--type`, `--audience` and `--title` are required, `--migration` is
required with `--type breaking` (it becomes the `## Migration` section), and `new` writes a complete,
valid entry or nothing: the result is checked before it is written. The body is added by editing the
file afterwards. With `changes.template` in a project **with** `release`, the template must begin
with a front matter of one JSON object: `new` fills `{card}` and `{slug}` in the text as it does without `release`,
then sets `type`, `audience` and `card` in that object (the command's values win over the
template's), replaces the template's first `# ` line with the title (or adds one after the front
matter), and appends the `## Migration` section for a breaking entry; a template without that front
matter is refused by `jst check`. Without `release`, the template is used exactly as [implementation.md](implementation.md) section 4.3 uses it.

`view` keeps its flags ([implementation.md](implementation.md) section 4.3): `--version` and `--date` fill the heading, and
the date defaults to today's date — a command for reading, run by hand. In a project with `release`,
`--version` defaults to the computed next version (section 3). `collect` in a project with `release`
refuses and names `jst release prepare`, so that one path writes versions.

### Order

A section is rendered by type, in this order: **Breaking changes**, **Security**, **Deprecated**,
**Features**, **Fixes**, **Notes**; a type with no entry has no heading. Within a type, entries are
in the byte order of their file names. The order never depends on the file system's listing or on
dates (the research's trap: fragments sorted implicitly). Each entry is rendered as `- **<title>**`
followed by its body indented under it, the migration note last under a `Migration:` lead-in; a
`card` is appended to the title as ` (<card>)`. Untyped entries come after all typed ones, under no
type heading. The same renderer makes the changelog, the release message,
the release pull request's description, the GitHub release's notes and the exports (section 4).

## 3. Versions, the request, release commits — read from git

Versions are `MAJOR.MINOR.PATCH`, three decimal integers without leading zeros; no prerelease or
build suffix (prereleases are not in this spec). Every term below is a function of the repository's
commits alone. **Tags are not read as state**: they and GitHub's releases publish what the commits
already say (section 8), so a missing, late or stray tag never changes what jackstay computes.

### The last release

The **last release** at a commit X is the version of the newest **release commit** (below) on X's
first-parent line; with none, it is `release.first`. The version files at X must hold it (exit 2
otherwise: "the version files say A, the last release is B"); only release commits change those
values (D3, section 7).

jackstay exits 2, with the reason, and never assumes a version (actions/checkout #1471,
dunamai #63) when the clone is shallow (`git rev-parse --is-shallow-repository`): the first-parent
line cannot be walked. Tags play no part in this, so a clone without tags computes the same answer
as one with them.

### The next version

Each type has a bump: `breaking` major, `feature` and `deprecated` minor, `fix` and `security` patch,
`note` none. Internal entries count like consumer ones (a change is a change, whoever reads about
it). The bump is the highest among the entries at X. Before 1.0 (the last release's major is 0), a
major bump becomes a minor one (Knope and release-please; W-026): 0.6.2 with a breaking entry gives
0.7.0, with a feature 0.7.0, with a fix 0.6.3. From 1.0 on, semver: 1.4.2 → 2.0.0, 1.5.0, 1.4.3.
Nothing computed ever reaches 1.0.0 from 0.x: **1.0 only by override**. Entries whose bump is none
(only notes) make no release: no release pull request is open (section 6) until a bumping entry or an
override arrives.

### The override

    jst release override --version V --reason TEXT

writes `<changes.dir>/_release.json`, `{"version": "V", "reason": "TEXT"}`, committed in a pull
request like an entry (and counting as one for the documented-change check). The next release uses
V instead of the computed version, records the reason (section 5) and removes the file. V must be
greater than the last release; it may be lower than the computed version (a breaking change judged a
minor one after all) or higher (1.0.0); the reason is required and not empty.
There is one fixed name, so there is never a second override. `jst changes check` validates it.

### The request

The **request** at X, `request(X)`, is what X would release: one JSON object

```json
{"version": "0.2.0", "entries": {"activity-timeline.md": "<blob id>", "…": "…"}, "override": "<blob id or null>", "made_on": "<commit id>"}
```

— the next version, every entry file in `changes.dir` at X with its git blob id, the override's blob
id, and `made_on`, the commit the request was written on (X). It exists only when X has a bumping
entry or an override. `made_on` is the request's **identity**, not part of what it releases: two
requests written on different commits always differ as files, so a request written again is a new
approval even when it would release the same entries. The **request file**
`<changes.dir>/_release-request.json` holds a request, written by the release pull request (section
6) and by nothing else (D3, section 7).

Two comparisons, used apart:

- A request file **matches** X when its entries and override are exactly those at X, blob for blob.
  No rule is recomputed: this is what proves old releases (below), so a later jackstay never unproves one.
- A request file **agrees** with X when it matches X and its version is the next version computed at
  X (above): what making a release and the release pull request's check use, with the jackstay that runs.
So a release holds exactly the entries the merged pull request listed, byte for byte — never one that
landed after it was made, and never a changed one.

**A request is good once, from its landing.** The **landing** of the request file at X, `L(X)`, is
the newest commit on X's first-parent line, at or before X, that added or changed the request file
(`git log --first-parent -1 -- <request file>`); every request written by the workflow differs from
the one before it (`made_on`), so its merge is always a landing. A request file at X is
**releasable** when it agrees with X **and** matches `L(X)`: it was fresh when it was merged, and the
base holds exactly its entries now. A request that did not match when it landed (something landed
between its writing and its merge) is never releasable, whatever the base does later — reverting the
late change does not bring it back; only a newly merged request does (W-034: a mismatch is refreshed
and merged again), and the workflow's next request differs from it by `made_on` even when its entries
are the same. One that agreed
when it landed and was then disturbed (a late entry, later reverted, while no run released it) is
releasable again once the base holds exactly its entries: it releases exactly what was approved, no
more. Both are functions of the commits, so coalesced or failed runs change nothing.

### The prepared tree

**P(X)** is what `jst release prepare` (section 5) writes on a clean checkout of X: the version and
build files rewritten, the sections rendered into the changelogs with `{date}` = **X's committer date
in UTC** (`YYYY-MM-DD`; not the clock, W-013), every entry, the override and the request file
removed. It reads the configuration from X's own `jackstay.json`. It is a pure function of X's tree, X's
committer date and the last release at X — itself a function of X's first-parent history, never of
later commits or of tags — so two runs of one jackstay give the same bytes. It is computed only to make a
release and to check a release request (D4), never to prove an old release (below).

### Release commits

A **release commit** is a commit R on the base's first-parent line such that:

- its message ends with the paragraph `Jackstay-Release: X.Y.Z`;
- its first parent R^ holds a request file asking for X.Y.Z that **matches** R^ and matches its
  landing `L(R^)`, and X.Y.Z is greater than the last release at R^ — no bump is recomputed, so the
  proof is the same under any later jackstay;
- R's diff against R^ removes exactly those entries, the override and the request file, changes the
  version values (and the build value, by one) to X.Y.Z's, changes `changes.into` and
  `changes.internal_into`, and touches nothing else.

Everything about R — its version files and their patterns, the changelogs, `changes.dir`,
`release.tag` — is read from **R^'s `jackstay.json`** (R itself changes no `jackstay.json`), so a later change of
paths or of the tag's format neither unproves an old release nor renames its tag. This proof is about
**what** R changed, not about the bytes jackstay renders: it does not run prepare
again, recompute the bump or re-render a changelog, so a later jackstay that renders or computes
differently never makes an earlier release fail its proof. Exact bytes are checked once, when R is
made: the workflow writes R as P(R^) with the jackstay it runs (section 8). Only the workflow makes a
release commit, and it pushes it to the base itself — there is no squash or edit between jackstay writing
the message and the commit landing, so the trailer is read from exactly what jackstay wrote. The trailer
finds the candidates (`git log --first-parent --grep`); the proof above decides, oldest first. A
candidate that is not proven is reported by `publish` (section 8) and is not a release.

The entries of the release X.Y.Z are the entry files R removed: those of R^, which are exactly the
request's.

## 4. Version files and apps

prepare replaces, in each file of `release.files`, the text of the `version` group with the new
version. Each pattern must match **exactly once** in its file and the match must hold the last
release's version; otherwise exit 2, naming the file and the count. A missing file is exit 2. The
rest of the file is kept byte for byte. Since no path is named twice (section 1), each file is
rewritten once, from its own bytes.

**Apps.** For an app the version files hold the marketing version (`CFBundleShortVersionString`, or
`MARKETING_VERSION` in an `.xcconfig` or a project file, by the project's pattern). Such a file holds
other settings too, so a pull request may change it freely except for the captured value (D3,
section 7). The **build number** (`CFBundleVersion`) is apart: macOS requires it to be unique and
increasing across all versions, not reset per version (Apple TN2420). With `release.build`,
prepare sets it to the value in the file plus one, at each release; only a release changes that value
(D3), so it only grows. A project whose build stamps its own build number (a build time, say)
declares no `release.build`, and the number is then the project's responsibility, not jackstay's.

**Notes for TestFlight and Sparkle**, exported from the entries, never stored separately:

    jst release notes [VERSION] [--format markdown|text|testflight|sparkle-html] [--audience consumer|internal|all]

The entries of a released VERSION are those its release commit removed (section 3). Without VERSION,
the entries in the working tree (what the next release would hold). `markdown` is the section as
prepare writes it; `text` the same without markup; `testflight` is `text` of consumer entries,
titles and bodies, which must fit App Store Connect's "What to Test" limit of 4,000 characters
(inferred from App Store Connect's documented field length; checked at build) — if it does not,
titles only, and if that does not fit either, exit 1 with the length, never a cut text;
`sparkle-html` is an HTML fragment (`<h2>` per type, `<ul>` of titles and bodies, HTML-escaped) for
an appcast item's description. `testflight` and `sparkle-html` are for consumers and take consumer
entries only: `--audience` other than `consumer` with them is refused (exit 2); `markdown` and `text`
take any audience. The `sparkle-html` format is kept minimal.

## 5. `jst release prepare`

    jst release prepare [--json]

**A preview that writes nothing** (W-047). It computes P(HEAD) — section 3's prepared tree — and
prints it: the last release, each entry with its bump, the bump, the version, the override's reason
if any, the files a release would change, and the consumer section. `--json` prints the same as one
object. It makes no commit, writes no file, calls no GitHub: a release is asked for by merging a
request and made by the workflow (section 8), which computes the same P from the same commit. There
is nothing to undo.

It requires what every local release command requires first (section 3): the settings valid, the
history proven, the version files holding the last release; then every entry and the override
valid, and at least one bumping entry or an override.

**All bytes first.** P computes every output's new bytes in memory and checks them: in each new
version file the pattern matches exactly once and captures the new version, and in the build file
exactly once capturing the new build number. A pattern that would not read its own result
(`^(?P<version>0\.1\.0)$` after 0.1.0 becomes 0.2.0) is exit 2, naming the file; `jst check` also
tries every pattern on its file with the version's three parts raised by one, so such a pattern is
refused before any release.

**Where internal entries go.** Into `changes.internal_into` when the project declares it; always
into the release commit's message and the release pull request's description, under "Internal", so
that git keeps them even without the file. Never into `changes.into`, the GitHub release's notes,
or the TestFlight and Sparkle exports.

## 6. The workflow and the release pull request (W-034)

### The workflow

Each project has `.github/workflows/release.yml`, written by `jst adopt` from the embedded template
(`internal/adopt/templates/files/release.yml`):

```yaml
on:
  push:
    branches: [main]      # release.base; a squash merge, a queue landing and the App's release commit are pushes
  workflow_dispatch: {}   # rerun by hand: everything is derived from git, so a rerun is safe
concurrency:
  group: jackstay-release
  cancel-in-progress: false
permissions:
  contents: read          # GITHUB_TOKEN reads only; every write uses the writing App's token
```

Its steps: make a token of the **writing App** for this repository (`actions/create-github-app-token`
with `vars.JACKSTAY_RELEASE_APP_CLIENT_ID` and `secrets.JACKSTAY_RELEASE_APP_PRIVATE_KEY`, section 10);
download the `jst` binary of the pinned version from jackstay's public GitHub release with the
workflow's own `GITHUB_TOKEN` and check it against the release's `checksums.txt`
(`JACKSTAY_VERSION`, a tag of jackstay, moved by a commit in the project, as `card-line`'s pin is;
section 13); check out the repository with full
history (`fetch-depth: 0`) and the writing App's token, then `git fetch --no-tags origin
'refs/tags/*:refs/tags/*'` (without force: a local tag that differs fails the step); then two steps,
both with the writing App's token as `GH_TOKEN` (the `gh` CLI reads it from there; the checkout's
credential is git's only):

    jst release publish     # section 8: make, tag and publish what a landed request asks for
    jst release pr          # below: create, refresh or close the release pull request

The second runs even when the first fails (`if: always()`), and the run is red if either fails.
Each reads the base's tip as fetched at that moment, not the commit that started the run: a run
that starts late does the newest thing, and a run that finds nothing to do ends 0. Runs are serial
(`concurrency`); GitHub keeps only the newest pending run of a group, which is enough because each
run derives everything from git (inferred from GitHub's documented concurrency behaviour). The
release commit the App pushes starts one more run, which finds nothing to do. `jst release pr` and
`publish` write to GitHub only inside GitHub Actions
(`GITHUB_ACTIONS=true`) and with `GH_TOKEN` set; elsewhere they run as `--dry-run`, so nothing run
locally races the workflow.

Why the App and not `GITHUB_TOKEN`: events made with `GITHUB_TOKEN` start no workflow, except that
a pull request it opens, reopens or pushes to gets runs that wait for a person's approval; its `edited`
events and its pushes to a branch start nothing (GitHub's documentation of the token). The release
pull request's checks would wait for a click, `card-line` would not re-run on its description, and
the release commit would start no run on `main` (the changesets trap). Pushes and pull
requests made with an App's installation token start them as a person's would.

### `jst release pr`

    jst release pr [--dry-run]

Let `B` be the base's tip, fetched again after `publish`.

1. **Nothing to ask for** (`request(B)` does not exist, or `B`'s request file is already releasable
   at `B` — `publish` failed to release it and will try again): if an open pull request has head
   `release.branch`, it is closed with a comment saying why, and its branch deleted under a lease on
   the head it read; end 0. A request file left on the base that is not releasable (it landed late,
   and only notes remain) is harmless: a request that did not agree at its landing never releases
   (section 3), and it stays until a release pull request replaces it.
2. **One commit on `B`** that writes `request(B)` into the request file and changes nothing else;
   message `Request release X.Y.Z` and a paragraph `Card: none (release request X.Y.Z)`.
3. **Find the pull request**: the open ones whose head is `release.branch`, whatever their base.
   One whose base is not `release.base` (the setting changed) is closed with a comment saying so.
   So is any open pull request opened by the writing App whose head is another branch (the setting
   `release.branch` changed): it is closed with a comment, and its branch deleted under a lease on the
   head read — the App opens pull requests for nothing else, so its author identifies them.
   In an `owner` project, the one on `release.base`, if it is not a draft, is turned into a draft
   before the branch moves.
4. **Push** the commit to `release.branch` with `--force-with-lease` on the branch's value on the
   remote as read just before (`git ls-remote`), or on its absence — read from the remote, never from
   a pull request's head: a merged or closed pull request may leave the branch behind or not,
   depending on the repository's "delete head branches" setting. The branch is the workflow's and is rebuilt on every run; a commit added to it
   by hand is overwritten (said in the description). What a release says is changed through
   entries.
5. **Open or update.** None open (never opened, merged, or **closed without merging**): open a new
   one — a draft in an `owner` project, a ready one in an `agents` project. One open: update it. The
   title is `Release X.Y.Z`. The description is the release as it will be written: the consumer
   section, the internal section, the override's reason, the list of entry files, then
   `Card: none (release request X.Y.Z)`.
6. **Read it back**: its head must be the commit pushed in step 4, its base `release.base`, its state
   open, and, in an `owner` project, a draft. If any is not so (it was merged, closed, pushed to or
   marked ready meanwhile), the run starts again from the fetch, at most three times, then fails red
   naming what kept changing.

**What is released is exactly the landed request** — never an entry it does not list, never a
changed one. What the person merging it *read* can differ from it in one window, which is documented, not closed:
GitHub cannot bind a merge to the head a person looked at. The description is written in the same
run as the request, from the same entries; between steps 3 and 6 (the draft, the push, the
description; a few API calls, inferred) a person may mark the pull request ready and merge a head
whose description is not updated yet. The released notes are then the landed request's, not the old
description's; the GitHub release shows them. The date in the description is a preview: the heading's
date is the committer date of the commit the release is made on (section 3), which is later.

**Landing a request that no longer agrees is harmless**: `publish` releases nothing and this
command opens a fresh pull request (section 8). So the release pull request needs no
"up to date" rule: whichever head lands, nothing is released that the request did not list.

What stops a session from merging it in an `owner` project is, at W-017's level, against accidents
only — sessions act through the owner's `gh` login, so GitHub cannot tell them apart: the draft;
the AGENTS block `jst adopt` writes (releases are made by the release workflow's pull request); and
the merge queue, which would never take it in an `owner` project (the queue is not built). A guard
rule refusing, inside a session, `gh pr merge` and `gh pr ready` of a pull request whose head is
`release.branch` in an `owner` project is designed, not built. A real barrier would be the queue's
separate design.

## 7. The checks in `jst lint pr`

The one `Card:` check (`jst lint pr`, [implementation.md](implementation.md) section 4.1), which the `card-line` workflow and
`jst gh pr-land` call, and the merge queue's cut-off would call (the queue is not built), gains the
rules below. They need inputs the `Card:` description rules did not, so the command's
inputs are made explicit and every caller passes them:

| Input | `card-line` (`--event`) | `pr-land` (`--pr N`) | the queue's cut-off | by hand (`--body-file F`) |
|---|---|---|---|---|
| description | `pull_request.body` | GitHub's, read at that moment | GitHub's, read at the cut-off | the file |
| head branch | `pull_request.head.ref` | GitHub's | GitHub's | the current branch |
| base commit `b` | `pull_request.base.sha` | `release.base` (else `cards.branch`) as just fetched | the batch's `B` | `release.base` (else `cards.branch`), local |
| head commit `h` | `pull_request.head.sha` | the head it saw green | the pull request's head at the cut-off | `HEAD` |
| tree judged by the tree's rules | the merge `card-line` checked out | the in-memory merge | the candidate's tree | the working tree |

Flags `--base`, `--head` and `--head-branch` set them by hand (and override what the event gives).
A head in another repository (a fork) is judged by the rules that need no head commit, and D1–D4
are red: the projects jackstay serves take no forks, and `card-line` runs only on branches of the
repository itself.

**The queue's stack copies** (the queue is not built). A pull request whose head branch is under `jackstay-queue/` and
whose description has the line `Jackstay-Queue-Of: #n` is judged as `#n`: `jst lint pr` reads pull request
`n` from GitHub and uses **its** head branch as the head-branch input — for D4, D1's exemptions and
the owner-only rules alike — while everything judged stays the copy's own: its diff against its own
base (`b` is the copy's base, the stack member before it, so the diff is this member's change alone),
its description, its tree. `#n` gives only the branch identity; it must exist in the same repository,
otherwise the copy is red. That the copy carries `#n`'s change as it was at the cut-off is the queue's
to guarantee (it made the copy from the head it recorded), not the check's: a copy's tree
holds the earlier members' changes too, and `#n` may have moved after the cut-off.
`Jackstay-Queue-Of` on a branch outside `jackstay-queue/` is refused, and so is a `jackstay-queue/` branch without it.
For this `card-line.yml` (`internal/adopt/templates/files/card-line.yml`) grants its `GITHUB_TOKEN`
`pull-requests: read` beside `contents: read`, and passes it as `GH_TOKEN` to the step that
downloads jackstay and to the `jst lint pr` step.

**The configuration read** for these rules is `jackstay.json` at `b`, never the pull request's: a pull
request cannot loosen the rules it is judged by. **`jackstay.json` is fixed settings** (W-047): a pull
request that changes it changes nothing else (D0), and it is the project owner's to merge, as the
queue's policy is. So no rule ever has to choose between two configurations: a settings pull request
carries no code, entry or version change to judge, and every other pull request runs under the
base's settings.

| Rule | Applies to | Refused when |
|---|---|---|
| **D0, settings alone** | every pull request | the diff `merge-base(b, h)..h` changes `jackstay.json` and any other path; a pull request that changes only `jackstay.json` is checked to leave valid release settings (section 1, `ValidateAt`) |
| **D1, documented change** | every pull request of a project with `changes` (at `b`), except release requests (D4) and heads in `cards.exempt_branches` | the diff `merge-base(b, h)..h` adds or modifies no entry and no override, and the description has no line `Changes: none (<reason>)` with a reason; or it has both |
| **D2, valid entries** | every pull request of a project with `changes` | an entry or override added or modified in that diff is invalid (section 2) |
| **D3, versions only by release** | every pull request of a project with `release` that is not a release request (D4) | by the base's settings: the request file, `changes.into` or `changes.internal_into` changed in the diff; at `h`, a version file's pattern not matching once or not holding the last release at `b`, or the build file not holding the base's build number; and in the tree the check judges (the merge of `b` and `h` that `card-line` checks out, the in-memory merge for `--pr`, the queue's candidate) the same values, the base's changelogs byte for byte, and every output a regular file |
| **D4, a release request** | the pull request whose head branch is `release.branch`, and a queue copy of it (above) | the diff `merge-base(b, h)..h` changes any path but the request file (W-034: the release pull request changes one file); or the request file at `h` is not a valid request (a regular file holding a JSON object of section 3's shape, with a version that parses). Everything about freshness — a version not greater than the last release at `b`, entries that differ from `b`'s, in short anything by which it does not agree with `b`: the base moved since the workflow wrote it, a release was made meanwhile, or an earlier member of a queue batch added an entry — D4 says and **does not fail**: a request that does not agree releases nothing (section 8) |

`Changes: none` is a line in the description, never a label: a label races the check, and a
description edit re-runs `card-line` (it listens for `edited`; the "skip" label trap). The
reason is free text and not judged. `jst lint tree` gains D2 over every entry in the tree.
Which rules apply to whom, in one table:

| Head branch (after a queue copy is resolved to its original) | The description rules (`Card:` lines) | the tree rules | D1 | D2 | D3 | D4 |
|---|---|---|---|---|---|---|
| `release.branch` (listed in `cards.exempt_branches` or not) | yes, unless listed; the description carries a valid `Card: none` line either way | yes | no | yes | no | yes |
| in `cards.exempt_branches` (a bot's branch) | no | yes | no | yes | yes | no |
| any other | yes | yes | yes | yes | yes | no |

D4 follows the release branch, and so does every owner-only rule (the draft, the guard, the queue's
exclusion): a pull request on any other branch that changes the request file is refused by D3, so a
request cannot be landed around the `owner` setting by copying it to another branch; the queue's
copies are judged as their original. Exit codes are `jst lint`'s (0 fine, 1 a rule broken, each
named; 2 unusable request or `jackstay.json`).

D3 is what keeps release values to release commits in pull requests. A version value pushed to the
base some other way (a direct push) is found by section 3 at the next run of any command — "the
version files say A, the last release is B", exit 2 — and repaired by hand (a revert), which jackstay
names. D4's freshness is advice: landing a request that no longer agrees releases nothing (section 6); its
one-file and shape rules are not.

**The boundary.** These checks see pull requests. A request file pushed straight to the base, which
agrees with it, is released like a merged one: nothing in git tells the two apart, and asking GitHub
which pull request brought a commit would not cover the queue's pushes. This is W-017's level: the
guard's push rule refusing, inside a session, a push to the base that changes the request file is
designed, not built; a person or a process outside a session is not stopped.

## 8. Publishing: `jst release publish`

    jst release publish [--dry-run]

Run by the workflow after each landing on the base. Let `T` be the base's tip as fetched.

1. **Finish what was released.** Every release commit on `T`'s first-parent line (section 3: its
   trailer, its first parent's matching request, what it changed — proven from the commits, not from any
   tag), oldest first, whatever tags exist: the tag
   `release.tag` for its version — absent → created on it, annotated ("Release X.Y.Z"), pushed with
   no force (git refuses to overwrite an existing tag without one); present on it → kept; present on
   another commit → **never moved**: exit 1, naming both commits, for a person to decide (W-020, W-026).
   Then its GitHub release (W-047): absent → created once, **published**, on that tag, after the
   remote tag has been read back (`git ls-remote`, peeled) pointing at the release commit; present →
   left as it is (a draft found there is reported, for a person to publish or delete). jackstay never
   edits a release afterwards, except a yank's title (below). "Latest" is GitHub's own rule
   (`make_latest: legacy`). A commit whose message has the
   trailer but which is not a release commit is reported, exit 1, and left alone; so is a tag matching
   `release.tag` with a version above `release.first` that is not on a release commit. This is what
   finishes a run that stopped after pushing the commit or after the tag, with the published state
   read from GitHub each time, so a tag that exists without its release is published again,
   without moving the tag. Tags are listed from the remote (`git ls-remote --tags`), so a
   checkout that lacks one never tries to make it twice.
2. **Release what was asked.** If `T` holds a request file that is releasable at `T` (section 3: it
   agrees with `T` and with its landing): make the commit R on
   `T` with tree P(T) (section 3) and the release message —

       Release X.Y.Z

       <the consumer section>

       Internal:
       <the internal section, if any>

       Override: <reason>                         (only with an override)

       Card: none (release X.Y.Z)

       Jackstay-Release: X.Y.Z

   — then check R as section 3 proves a release commit (its trailer, `T`'s matching request, what it
   changed, and each pattern reading the new values), so the workflow never pushes a commit it could
   not later recognise; a failure is exit 2 and nothing is pushed. Then push it with the writing
   App's token: `git push --force-with-lease=refs/heads/<base>:<T>
   origin <R>:refs/heads/<base>`, a fast-forward accepted only if the base is still `T`. Refused (the
   base moved): fetch again and start over from step 1, at most three times, then red. Pushed: tag
   R and publish its release as in step 1. A request file that is not releasable at `T` is reported
   (what differs: the version, entries added, removed or changed) and nothing is released; the run
   goes on to step 3 — `jst release pr` refreshes the pull request next.
3. **Yanked** (below), reconciled. Always reached, whatever steps 1 and 2 found; the exit code is
   the worst of the three.

The notes of a GitHub release are the consumer entries R removed, rendered as in section 2, the
override's reason, and a link to the compare view from the previous release's tag (none before the
first). 

**Yanked.** A tag is never moved and a release never deleted (Keep a Changelog; GitHub's immutable
releases refuse moved tags). A bad release is marked by a pull request:

    jst release yank X.Y.Z --reason TEXT

writes the entry `<changes.dir>/yank-X-Y-Z.md` (`type` note, `audience` consumer, `yanks` X.Y.Z, the
reason as its body), refusing if X.Y.Z is not a release or the file exists with other content; it
changes nothing on GitHub. Once that entry lands, the workflow does the rest, serialised with
publishing in its concurrency group. In step 3, for every yanked version — a `yanks` entry at `T`, or one removed by a release commit —
whose release's tag is on its proven commit, the GitHub release's title becomes `X.Y.Z (yanked)` with
the reason at the top of its notes, unless it already is; nothing else is edited. "Latest" stays
GitHub's rule, so a yanked latest release can stay "latest" until the next one (its title says so;
the changelog's ` [YANKED]` too).
The next release collects the entry: ` [YANKED]` is added to that version's heading in
`changes.into` and the note is listed. Every step checks whether it is already done, so a run that
stops half-way is finished by the next.

## 9. With the merge queue (not built)

The merge queue is not built (an idea kept for later; W-029). What its design asks of
this spec, and where it is met:

| The queue's design asks | Here |
|---|---|
| the workflow runs on `push` to the base (queue landings are pushes) | section 6, `on: push` |
| the release pull request is created or updated by branch, so a closed one is replaced | section 6, step 5 |
| publication is bound to the landing of the release pull request, not to any commit that changes version files | sections 3 and 8: a release happens only when a request file, which only the release pull request writes (D3), has landed and agrees with the base; the release commit is then the workflow's own |
| a version file changed by any other pull request is refused (`jst lint pr`) | section 7, D3 |
| a tag that exists without its release is published again without moving the tag | section 8, step 1 |
| in `agents` projects the release pull request lands in a batch of its own, as one commit | not needed: the release pull request changes one file, and the release commit is made after it lands, on whatever the base then is; the queue may land it with others |
| the release branch's description is written by the workflow, so it is exempt from the description rules | section 7: listed or not, D4 applies; it carries a valid `Card: none` line |
| the "change is documented" check is part of the one `Card:` check | section 7 |

**What this means for the queue's design:**

- The setting is `release.merge` and `release.branch` in the `release` section, and a change to that
  section is owner-only policy, read from the tip.
- A release is not "the landing of the release pull request, judged by its tree": the release
  pull request lands a request; the **writing App then pushes one commit to the base**, the release
  commit, made from the base's tip with a lease on it (section 8). For the queue this is an outside
  push, like a release bot's: its watch tests the new tip. The release commit changes only release
  values, changelogs and removes entries; a ruleset that restricts pushes to the base must let the
  writing App through (the queue's real enforcement design).
- D4 is advice, not a guard: a request that no longer agrees releases nothing, so the queue may take
  the release pull request in any batch in an `agents` project.

**A hold** (not built, like the queue) is not how the release pull request waits for the
owner: the workflow, which runs on GitHub's runners and has no session state, sets the draft itself,
and in an `owner` project the pull request stays a draft until the owner marks it ready.

## 10. The writing App

A second GitHub App, beside a read-only one a project may already use, made by the organisation's owner:

- owned by the organisation; no webhook, no events;
- repository permissions: **contents write** (the release branch, the release commit on the base,
  tags, releases), **pull requests write** (open, update, close, draft), metadata read; nothing else
  — no `workflows` permission;
- installed only on the repositories that release with jackstay;
- its settings are organisation settings: the variable `JACKSTAY_RELEASE_APP_CLIENT_ID` and the
  secret `JACKSTAY_RELEASE_APP_PRIVATE_KEY`, visible to those repositories only (any workflow in a
  repository that sees the secret can make a token that writes to all of them);
- its private key goes straight into the organisation secret: it is never printed, saved or seen by a
  session.

The queue, if built, does not use this App to push its landings.

**Unproven, settled before a project adopts, by a test on a scratch repository with the App
installed:** which of the App's writes GitHub refuses without the `workflows` permission when the
commits involved have workflow files that differ from the default branch's. GitHub refuses an App's
push that "creates or updates" a workflow, and documents that a release whose target commit has
workflow files unlike the default branch's needs that permission too (GitHub's releases API); whether
a branch moved onto a base that changed workflows, or a tag on an older commit, counts is not
documented. The test makes the base change a workflow and then tries every write the App does: the
release branch's push onto that base; the release commit's push; a tag and a GitHub release on the
new tip; a tag and a GitHub release on a commit from before the workflow change (the recovery of
section 8, step 1); and a release's edit (yank). The release commit itself never changes a workflow.
If any write is refused, the `workflows` permission is reconsidered with the result — the recovery
path is the one most likely to need it.

## 11. Migrations

**Untyped `changes`.** Kept as it is in a project without `release` ([implementation.md](implementation.md) section 4.3):
untyped entries, `jst changes new`, `view` with `--version` and `--date`, `collect`, which files are entries
(section 2). A project moves to typed entries by adding front matter to its entries and, with it,
the `release` section.

**From another tool** (Knope's change files, say). A one-off conversion is not part of jackstay
(W-047): the project converts its files once with a throwaway script — Knope's
`<package>: major | minor | patch` becomes `{"type": "breaking" | "feature" | "fix", "audience":
"consumer"}`, the title and body kept (a `major` needs a `## Migration` section) — then checks the
result with `jst changes check` and removes the old tool's folder.

## 12. Failure behaviour

| What goes wrong | What happens |
|---|---|
| Shallow clone | Exit 2; never a guessed version (section 3) |
| A tag on a commit that is not a release commit, or a trailer on a commit that is not proven | Reported by `publish`, exit 1, left alone; nothing else is affected (tags are not state) |
| A version value changed outside a release (a direct push) | Exit 2 in every command, "the version files say A, the last release is B"; a revert by hand |
| A version-file pattern matches zero or several times | Exit 2, the file and the count |
| An invalid entry | `jst changes check`, prepare and the PR check name the file and the rule; prepare changes nothing |
| The release pull request lands after the base moved | Its request no longer agrees: nothing released; a new pull request opened from the new base |
| The workflow stops after pushing the release commit, or after the tag | The next run tags and publishes it (section 8, step 1) |
| The base moves while the release commit is pushed | The lease refuses; fetched again, at most three times |
| The pull request changes while the workflow updates it | Read back after writing; the run starts again, at most three times, then red |
| The release pull request was closed by hand | The next run opens a new one |
| The tag exists on another commit | Never moved: red, both commits named, a person decides |
| A GitHub release exists as a draft | Reported by `publish`, exit 1, left for a person to publish or delete (section 8, step 1) |
| Two runs at once | Not possible: one concurrency group; every push is leased; the commands write only inside Actions |
| The writing App's token cannot be made | The run is red at its first step; nothing written |
| A yank stops half-way | The next run finishes it |
| A session merges the release pull request in an `owner` project | Not prevented beyond the draft and the AGENTS block (section 6); it releases exactly what the request listed |

## 13. What the Go build needs

What this spec needs of the Go foundation ([implementation.md](implementation.md)):

- `jst changes …`, `jst release …` and `jst lint pr` / `jst lint tree` as commands of the one binary.
- `jackstay.json` read by the loader with the same strictness (an unknown key is an error), including
  the `cards` section ([implementation.md](implementation.md) section 4.1), since `jst lint pr` needs it.
- linux/amd64 and linux/arm64 builds published as release assets of jackstay
  (`jackstay_<version>_linux_<arch>.tar.gz`) with `checksums.txt`, so that `card-line.yml` and
  `release.yml` download the binary instead of building jackstay.
- The formats here are contracts defined by this spec: the entry's front matter, the override and
  the request are JSON objects read strictly (an unknown key is an error, a duplicate key is an
  error), and jackstay writes the request with its keys in a fixed order (`version`, `entries` with
  its keys sorted by byte, `override`, `made_on`), one line, ending with a newline — so a request is
  the same bytes whichever jackstay writes it. Patterns are RE2 (section 1).

### The Go design (W-036)

`jst changes` and the `Card:` check are designed from [implementation.md](implementation.md)
sections 4.1 and 4.3 and this one. How a command joins the binary — registration, the shared
`Card:` rules — is the Go foundation's contract, not this spec's. **[implementation.md](implementation.md)
is the one authority**; where they differ, this section follows it.

| Package | What it holds |
|---|---|
| `internal/changes` | the entry model (front matter, title, migration, type, audience), validation, the renderer (one for the changelog, the release message, the description, the notes and the exports), `new`, `check`, `view`, the untyped `collect` |
| `internal/release` | versions and bumps, the request, the last release and the proof of release commits (section 3), the preview prepare (section 5), `override`, `notes`, `yank`, and the workflow's `pr` and `publish` (sections 6, 8) — the only code that writes to GitHub, kept in files of their own; the rest are pure functions over a repository read through an interface, so most tests need no GitHub |
| `internal/lint` | `jst lint pr` and `lint tree`: D1–D4 and the queue's copies (section 7), its inputs as one struct filled by `--event`, `--pr` or the flags; the `Card:` rules come from `internal/cards`, never a second copy |
| `internal/config` | the loader (`config.Load`, strict, RE2), with the `changes` and `release` keys |

**Interfaces where they are used** ([W-037](../decisions/W-037-current-go-and-dependencies.md)):
`internal/release` and `internal/lint` each declare the small interfaces they need — a git reader
and writer (rev-parse, first-parent log, trees and blobs with modes, diff, merge-base,
check-ref-format, ls-remote, a leased push, annotated tags) and a GitHub client (pull requests: find,
open as a draft or ready, update, close, draft and ready again; releases: list, create, edit; a pull
request's head branch) — with hand-written fakes in their tests. The real adapters are one each:
git through its command line, and GitHub through REST plus GraphQL for the two things REST cannot do
(turning a pull request into a draft and back: `convertPullRequestToDraft`,
`markPullRequestReadyForReview`). Commands are registered in each package's `init()`
(`cli.Register(cli.Command{Name, Run})`). Framework code uses readable names, no single-letter or
cryptic identifiers (the gate's linter refuses them outside tests).

**Libraries.** Standard library first (`encoding/json` with a strict reader that refuses unknown and
repeated keys, `regexp` for RE2, `os/exec`, `net/http`), and two choices beyond it:

- **git through its command line**, not a Go reimplementation such as `go-git`: the push with a
  lease, `check-ref-format`, the merge base and the exact meaning of "first parent" are git's own, git
  is on every machine and runner already, and a second implementation of git would be a second reader
  of the same history (inferred).
- **GitHub through its REST API with `github.com/google/go-github`** (the major version is
  `go.mod`'s), not the `gh` command: a static binary then needs nothing installed beside it, and the
  client is tested against `net/http/httptest` instead of a scripted `gh` on `PATH`. The token is
  `GH_TOKEN` (the writing App's in the workflow).

**Tests.** No test depends on the clock or on the network (W-013): dates come from commits made in
temporary repositories with fixed `GIT_AUTHOR_DATE` / `GIT_COMMITTER_DATE`, and `jst changes view`
is always given `--date`; GitHub is replaced by a hand-written fake of the interface each package
declares, and the GitHub adapter itself is tested against `net/http/httptest` with GitHub's answers
recorded from its documentation — no call to GitHub; the remote is a local bare repository. Made in
local repositories: tags, shallow and tag-less clones; every landing shape of the request (squash,
rebase, merge commit, a push); a request that lands after the base moved (an entry added, removed,
changed; the version raised); a stop after each push; a tag on a non-release commit; a draft
release; yanks. The tests that need real git are `//go:build integration`, a test scope of their
own, part of the gate. Section 10's permission question and section 4's TestFlight limit are checked
once by hand.

## 14. What it cannot see

- Whether an entry's type is right: a breaking change filed as a fix bumps the wrong number (an
  API-breakage check that would catch it is out of scope, W-026).
- Whether a `Changes: none` reason is true.
- A request merged by a session in an `owner` project (section 6): it releases what it listed.
- A release made by hand outside jackstay (a tag pushed by a person on a commit jackstay did not make): it is
  reported, never published by jackstay, and changes nothing jackstay computes.
- A request file pushed straight to the base (section 7, "The boundary").
- Anything about the app's binary: the build number in the built app is the project's build's
  (section 4).

## 15. Adoption

A project adopts from its own session: `jst adopt` writes `release.yml` and `card-line.yml` from
`internal/adopt/templates/files/` and the AGENTS block's `changes` and `release` items, which name
`jst changes new` and say releases come from the release workflow's pull request
([adopt.md](adopt.md)). Prerequisites: a jackstay release that the workflows can download
(section 13); the writing App made and installed (section 10); the permission test of section 10.
The project then converts any old change files (section 11) and adds `changes` and `release` to
`jackstay.json` in a pull request of their own (D0).

## Details

Where the sections above are silent:

- Entries carry JSON front matter (the records' shape, [implementation.md](implementation.md)
  section 4.3), not YAML: one parser, strict.
- `jst changes new` without `release` writes an untyped entry ([implementation.md](implementation.md)
  section 4.3); with `--type` there it writes a typed one.
- An entry's text is read with carriage returns made plain; an untyped entry that is only white space
  is left out of a section.
- `jst lint pr --body-file` judges the current branch against `release.base` (else `cards.branch`, else
  `main`) as the local repository has it; `--pr N` asks GitHub as the project's `github.account`
  ([implementation.md](implementation.md) section 8.1; `GH_TOKEN` inside GitHub Actions only).
- The release commit and the request commit are made with git's plumbing in a temporary index, never
  in a checkout; their author is the writing App (`JACKSTAY_RELEASE_AUTHOR`, the action's `app-slug`).
- GitHub reads (pull requests, releases) are retried after a server error, a rate limit or a network
  failure, with exponential backoff and jitter within the run's deadline; writes are not retried
  (a created pull request or release must not be made twice), the next run finishes them.
- A tag is never moved, never forced, never deleted by jackstay; a GitHub release is created once,
  published, and only a yank's title is edited after; "latest" is GitHub's own rule (W-047).
- `release.merge` and `release.branch` live in the `release` section; the queue reads them there and
  treats the section as owner-only policy.
