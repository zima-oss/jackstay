# The release features: typed entries, the next version, the release pull request

Status: built. How to use it is the [releases guide](../guide/releases.md).

Rulings it follows: [W-013](../decisions/W-013-quality-testing-declarative.md) (declarative;
no time-dependent tests), [W-017](../decisions/W-017-review-fixes-and-threat-model.md) (the guard
is against accidents), [W-020](../decisions/W-020-version-tags.md) (one version, one commit, a tag
never moved), [W-026](../decisions/W-026-release-scope-now-in-parallel.md) (the scope),
[W-029](../decisions/W-029-queue-self-merge-configurable-go-last.md) (who merges the release pull
request is a project setting; its merge queue is not built), [W-034](../decisions/W-034-release-pr-is-a-request-app-commits.md)
(the release pull request is a request; the writing App makes the release commit),
[W-047](../decisions/W-047-release-redesign-fixed-settings-no-one-off-imports.md) (`jackstay.json`
is fixed settings; prepare is a preview; no one-off imports). A sentence that rests on none of them
says "inferred". Section 13 says what this spec needs of the Go build
([implementation.md](implementation.md)).

## What it removes

An entry has a type and an audience, and a breaking one carries a migration note. The next version
is computed from the entries (an override needs a recorded reason; 1.0 only by override).
`jst lint pr` requires an entry or `Changes: none (<reason>)`. Landing the release pull request
releases exactly what it asked for: the workflow commits the version, tags it and publishes the
notes, and a tag is never moved. No separately installed bot is needed.

Not done ([W-026](../decisions/W-026-release-scope-now-in-parallel.md)): an API-breakage check;
the next version previewed in a pull request's check; prereleases; release assets; building or
uploading an app. jackstay's own versions stay under W-020 (by hand, `internal/version/version.go`).

## How a release happens, in short

1. Sessions land changes, each with a typed entry (`changes/<slug>.md`).
2. After each landing on `main`, a workflow keeps one pull request open on `jackstay/release`. It
   changes one file, the **request**: the next version and the exact list of entries it releases.
   Its description shows the release as it will be written.
3. The owner (or, where the project allows, an agent) merges it: the decision to release.
4. The landing is a push to `main`, so the workflow runs again. If the request still agrees with
   what `main` holds, the workflow commits the prepared release (version files, changelog, entries
   removed) with the writing App, tags that commit and publishes the GitHub release. Otherwise
   nothing is released and the pull request is refreshed.

jackstay keeps no state of its own: what was released and what waits is read from git each time.

## 1. The project file

Two parts of `jackstay.json`: the `changes` section ([implementation.md](implementation.md)
section 4.3), extended, and the `release` section. `changes` may come without `release` (entries
and the documented-change check, no versions), never the reverse. `jackstay_schema` stays 2; a
jackstay that does not know the keys refuses the whole file, so the new jackstay is installed and
the workflows' pin moved before a project commits them.

| Key | Meaning | Default |
|---|---|---|
| `changes.internal_into` | where internal entries are collected; without it they go only into the release commit's message and the release pull request's description (section 5) | none |
| `release.merge` | who merges the release pull request: `owner` (only the owner; the pull request is a draft, section 6) or `agents` (any session, `jst land`) — W-029 | `owner` |
| `release.branch` | the release pull request's head branch | `jackstay/release` |
| `release.base` | the branch releases are made from and land on | `cards.branch`, else `main` |
| `release.tag` | the tag's name; holds `{version}` once | `v{version}` |
| `release.first` | the version the version files hold when the section is added; tags at or below it are history jackstay does not read | required |
| `release.files` | the version files: each `{"path", "match"}`, a pattern with one named group `version` | required, at least one |
| `release.build` | an app's build number: `{"path", "match"}` with one named group `build`, a decimal integer | none |

Patterns are RE2 (`(?P<name>…)` groups), in multi-line mode. `jst check` refuses:

- a pattern that does not compile, or lacks exactly one group of the right name;
- a path outside the repository (links followed);
- **any path named twice** among `release.files`, `release.build`, `changes.into` and
  `changes.internal_into`, or any of them inside `changes.dir`, so no two rewrites touch one file;
- `jackstay.json` or a file under `.github/` named as any of them (a release commit changes no
  configuration and no workflow);
- `release.tag` without exactly one `{version}`; another `release.merge`; `release.branch` equal to
  `release.base`; a `release.first` that is not a version; `release` without `changes`;
- a `release.branch` or `release.base` git refuses as `refs/heads/<name>`, or a `release.tag`
  expanded with `release.first` it refuses as `refs/tags/<name>` (`git check-ref-format`).

**One reader.** These rules (patterns, paths and links, outputs that read back their raised values,
ref names, typed templates) are one Go validator. `jst check` and every command of this spec run it
first; `jst changes check` prints it in full.

## 2. Entries

### The file

An entry is `<changes.dir>/<slug>.md`, written in the commit that makes the change: a front matter
of one JSON object between `---` lines (the records' shape, read strictly), a title line, then an
optional body:

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

The title is the first non-empty line after the front matter: one line starting `# `, not empty.
A `breaking` entry needs a `## Migration` heading followed by a non-empty line before the next
heading. Any other key, a front matter that is not one JSON object, a type or audience outside the
lists, a missing title, or a breaking entry without migration makes the entry **invalid**. Text is
read with carriage returns made plain.

In a project with `release`, entries, the override and the request must be tracked **regular
files** (mode `100644` or `100755`): a request binds an entry by its blob, and a link's blob is its
target's name. So must every file of `release.files` and `release.build`; `changes.into` and
`changes.internal_into` are tracked regular files or absent. No folder on these paths, nor
`changes.dir`, may be a link. So a release commit changes exactly the paths its proof names.

Entries are `<slug>.md` files with a slug of lower-case letters, digits and `-`; another `.md` (a
`README.md`) is named on standard error. `_release.json` (the override) and `_release-request.json`
(the request) are jackstay's own (section 3). A file with no front matter is an **untyped entry**,
valid only without `release` ([implementation.md](implementation.md) section 4.3); with `release`
it is invalid, named with the fix. An untyped entry of only white space is left out.

### Commands

`jst changes new` writes an entry; `check` validates every entry, the override and the request;
`view` renders the next section; `collect` is for projects without `release`. Flags:
`jst changes --help`.

- **Without** `release`, `new` is implementation.md section 4.3's; `--type`, `--audience` and
  `--title` make a typed entry.
- **With** `release`, `--type`, `--audience` and `--title` are required, and `--migration` with
  `--type breaking`. `new` writes a complete, valid entry or nothing. A `changes.template` must
  begin with a JSON front matter: `new` fills `{card}` and `{slug}`, sets `type`, `audience` and
  `card` (the command's values win), replaces the first `# ` line with the title, and appends the
  migration for a breaking entry.
- `view`'s `--date` defaults to today (a command for reading); with `release`, `--version` defaults
  to the computed next version. `collect` with `release` refuses and names `jst release prepare`.

### Order

A section is rendered by type: **Breaking changes**, **Security**, **Deprecated**, **Features**,
**Fixes**, **Notes**; a type with no entry has no heading. Within a type, entries are in the byte
order of their file names, never the file system's or by date. Each entry is `- **<title>**` with
its body indented under it, the migration last after `Migration:`, and a `card` appended as
` (<card>)`. Untyped entries come last, under no heading. One renderer makes the changelog, the
release message, the release pull request's description, the GitHub release's notes and the
exports (section 4).

## 3. Versions, the request, release commits — read from git

Versions are `MAJOR.MINOR.PATCH`, decimal without leading zeros, no suffix. Every term below is a
function of the commits alone. **Tags are not read as state**: they and GitHub's releases publish
what the commits say (section 8), so a missing, late or stray tag changes nothing jackstay computes.

### The last release

The **last release** at commit X is the version of the newest **release commit** (below) on X's
first-parent line, else `release.first`. The version files at X must hold it (exit 2: "the version
files say A, the last release is B"); only release commits change them (D3, section 7). A shallow
clone (`git rev-parse --is-shallow-repository`) is exit 2, never a guessed version; a clone without
tags computes the same answer.

### The next version

Bumps: `breaking` major; `feature` and `deprecated` minor; `fix` and `security` patch; `note` none.
Internal entries count too. The bump is the highest among the entries at X. Before 1.0 a major bump
becomes minor (0.6.2 gives 0.7.0 for a breaking or a feature, 0.6.3 for a fix); from 1.0 on,
semver. **1.0 only by override.** Entries that bump nothing make no release and open no release
pull request.

### The override

`jst release override --version V --reason TEXT` writes `<changes.dir>/_release.json`,
`{"version": "V", "reason": "TEXT"}`, committed like an entry (and counting as one for D1). The next
release uses V, records the reason (section 5) and removes the file. V must be above the last
release (it may be below or above the computed version); the reason is not empty. One fixed name,
so never a second override.

### The request

The **request** at X, `request(X)`, is what X would release:

```json
{"version": "0.2.0", "entries": {"activity-timeline.md": "<blob id>", "…": "…"}, "override": "<blob id or null>", "made_on": "<commit id>"}
```

It exists only when X has a bumping entry or an override. `made_on` is its **identity**, not part of
what it releases: a request written again is a new approval even for the same entries. The
**request file** `<changes.dir>/_release-request.json` is written by the release pull request
(section 6) and nothing else (D3). Two comparisons:

- it **matches** X when its entries and override are exactly X's, blob for blob (nothing
  recomputed, so a later jackstay never unproves an old release);
- it **agrees** with X when it matches and its version is the next version computed at X.

**A request is good once, from its landing.** `L(X)` is the newest commit on X's first-parent line,
at or before X, that changed the request file. A request file at X is **releasable** when it
agrees with X **and** matches `L(X)`. A request that did not match when it landed is never
releasable, even if the base later reverts the late change; only a newly merged request is (W-034).
One that matched at its landing and was disturbed meanwhile is releasable again once the base holds
exactly its entries. Coalesced or failed runs change nothing.

### The prepared tree

**P(X)** is what `jst release prepare` writes on a clean checkout of X: version and build files
rewritten, the sections rendered with `{date}` = **X's committer date in UTC** (not the clock,
W-013), every entry, the override and the request removed, all read from X's `jackstay.json`. It
is a pure function of X's tree, committer date and last release, so one jackstay gives the same
bytes every time. It is computed to make a release and to check a request, never to prove an old one.

### Release commits

A **release commit** is a commit R on the base's first-parent line such that:

- its message ends with the paragraph `Jackstay-Release: X.Y.Z`;
- R^ holds a request file for X.Y.Z that **matches** R^ and `L(R^)`, and X.Y.Z is above the last
  release at R^;
- R's diff against R^ removes exactly those entries, the override and the request file, changes the
  version values (and the build value, by one) to X.Y.Z's, changes `changes.into` and
  `changes.internal_into`, and touches nothing else.

Everything about R is read from **R^'s `jackstay.json`**, so a later change of paths or tag format
neither unproves an old release nor renames its tag. The proof checks **what** R changed, not the
bytes rendered, so a later jackstay never fails an earlier release; exact bytes are checked once,
when the workflow writes R as P(R^) (section 8). Only the workflow makes and pushes a release
commit, so its trailer is exactly what jackstay wrote. The trailer finds candidates (`git log
--first-parent --grep`); the proof decides, oldest first; an unproven candidate is reported by
`publish` and is not a release. The release's entries are those R removed.

## 4. Version files and apps

prepare replaces the `version` group's text in each `release.files` file. Each pattern must match
**exactly once** and hold the last release, else exit 2 naming the file and the count; a missing
file is exit 2. The rest of the file is kept byte for byte.

**Apps.** The version files hold the marketing version (`CFBundleShortVersionString`,
`MARKETING_VERSION`); a pull request may change the rest of such a file, never the captured value
(D3). The **build number** (`CFBundleVersion`) must grow across all versions (Apple TN2420): with
`release.build`, each release sets it to the file's value plus one, and only a release changes it.
A project whose build stamps its own number declares no `release.build`.

**Notes for TestFlight and Sparkle**: `jst release notes [VERSION]` exports them from the entries,
never stored apart (flags: `jst release --help`). A released VERSION's entries are those its release
commit removed; without VERSION, the working tree's. `markdown` is the section as prepare writes it;
`text` the same without markup; `testflight` is `text` of consumer entries, which must fit App
Store Connect's 4,000 characters (inferred) — else titles only, else exit 1 with the length, never a
cut text; `sparkle-html` is an HTML fragment (`<h2>` per type, `<ul>` of titles and bodies,
escaped). `testflight` and `sparkle-html` take consumer entries only (another `--audience` is exit
2).

## 5. `jst release prepare`

**A preview that writes nothing** (W-047). It computes P(HEAD) and prints the last release, each
entry with its bump, the bump, the version, the override's reason, the files a release would change,
and the consumer section (`--json`: one object). No commit, no file, no GitHub call. It first
requires the settings valid, the history proven, the version files holding the last release, every
entry and the override valid, and a bumping entry or an override.

**All bytes first.** P computes every output in memory and checks that each new version file's
pattern matches once and captures the new version, and the build file the new number. A pattern
that would not read its own result (`^(?P<version>0\.1\.0)$`) is exit 2; `jst check` also tries
every pattern on its file with the version's parts raised by one.

**Internal entries** go into `changes.internal_into` when declared, and always into the release
commit's message and the release pull request's description, under "Internal". Never into
`changes.into`, the GitHub release's notes or the exports.

## 6. The workflow and the release pull request (W-034)

### The workflow

`jst adopt` writes `.github/workflows/release.yml` from `internal/adopt/templates/files/release.yml`:

```yaml
on:
  push:
    branches: [main]      # release.base; a squash merge, a host's merge queue and the App's release commit push
  workflow_dispatch: {}   # rerun by hand: everything is derived from git, so a rerun is safe
concurrency:
  group: jackstay-release
  cancel-in-progress: false
permissions:
  contents: read          # GITHUB_TOKEN reads only; every write uses the writing App's token
```

Its steps: make a token of the **writing App** (`actions/create-github-app-token` with
`vars.JACKSTAY_RELEASE_APP_CLIENT_ID` and `secrets.JACKSTAY_RELEASE_APP_PRIVATE_KEY`, section 10);
download the pinned `jst` (`JACKSTAY_VERSION`) with `GITHUB_TOKEN` and check it against
`checksums.txt`; check out with full history and the App's token, then `git fetch --no-tags origin
'refs/tags/*:refs/tags/*'` without force; then, with the App's token as `GH_TOKEN`,
`jst release publish` (section 8) and `jst release pr` (below). The second runs even when the first
fails (`if: always()`); the run is red if either fails.

Each reads the base's tip as fetched at that moment, so a late run does the newest thing and a run
with nothing to do ends 0. Runs are serial; GitHub keeps only the newest pending run, which is
enough because each derives everything from git (inferred). `pr` and `publish` write to GitHub only
inside Actions (`GITHUB_ACTIONS=true`) with `GH_TOKEN`; elsewhere they are `--dry-run`, which builds
its candidates in a temporary repository and leaves the checkout alone. They select the remote by
[implementation.md, section 8](implementation.md#8-github-and-branches-jst-checks-the-github-section-jst-tidy-jst-start-jst-sync-jst-owner)'s
rule; a live run verifies the fetch and push URLs against `GITHUB_REPOSITORY`, and the fetched
base's settings must name that branch as `release.base`.

**Why an App:** events made with `GITHUB_TOKEN` start no workflow (a pull request it opens waits
for a person's approval; its edits and pushes start nothing). The release pull request's checks
would wait, `card-line` would not re-run on its description, and the release commit would start no
run on `main`. An App's installation token starts them as a person's would.

### `jst release pr`

Let `B` be the base's tip, fetched again after `publish`.

1. **Nothing to ask for** (no `request(B)`, or `B`'s request file already releasable at `B`): an
   open pull request with head `release.branch` is closed with a comment and its branch deleted
   under a lease; end 0. A request file left on the base that is not releasable is harmless.
2. **One commit on `B`** writes `request(B)` into the request file and nothing else; message
   `Request release X.Y.Z` and a paragraph `Card: none (release request X.Y.Z)`.
3. **Find the pull request**: open ones with head `release.branch`. One whose base is not
   `release.base` is closed with a comment, and so is any open pull request by the writing App on
   another head (the setting changed), its branch deleted under a lease. In an `owner` project the
   one on `release.base` is turned into a draft before the branch moves.
4. **Push** with `--force-with-lease` on the branch's value on the remote as read just before (`git
   ls-remote`), or on its absence. The branch is the workflow's: a commit added by hand is
   overwritten (the description says so).
5. **Open or update.** None open: open one (a draft in an `owner` project). Title `Release X.Y.Z`;
   the description is the consumer section, the internal section, the override's reason, the entry
   files, then `Card: none (release request X.Y.Z)`.
6. **Read it back**: head the commit pushed, base `release.base`, open, and a draft in an `owner`
   project. Otherwise the run starts again from the fetch, at most three times, then fails red
   naming what kept changing.

**What is released is exactly the landed request.** What a person *read* can differ in one
documented window: between steps 3 and 6 a person may mark the pull request ready and merge a head
whose description is not updated yet; the released notes are then the landed request's. The date in
the description is a preview. **A request that no longer agrees is harmless**: `publish` releases
nothing and a fresh pull request follows, so no "up to date" rule is needed.

In an `owner` project only accidents are stopped (W-017; sessions act through the owner's login):
the draft, the AGENTS block, and the guard, which from 0.6.0 refuses `gh pr merge` in a session
(rule `gh-pr-merge`, naming `jst land`). A guard rule refusing `gh pr ready` of the release branch
is designed, not built.

## 7. The checks in `jst lint pr`

The one `Card:` check (`jst lint pr`, [implementation.md](implementation.md) section 4.1), which
`card-line` calls, gains the rules below. `jst land` shares two of them: it judges the `Card:` line
and D1 on the exact description it sends ([land.md](land.md) section 3.1). The inputs of
`jst lint pr` are explicit:

| Input | `card-line` (`--event`) | `--pr N` | by hand (`--body-file F`) |
|---|---|---|---|
| description | `pull_request.body` | GitHub's, read at that moment | the file |
| head branch | `pull_request.head.ref` | GitHub's | the current branch |
| base commit `b` | `pull_request.base.sha` | the pull request's base commit, as GitHub reports it | `release.base` (else `cards.branch`, else `main`), local |
| head commit `h` | `pull_request.head.sha` | the pull request's head commit | `HEAD` |
| tree judged by the tree's rules | the merge `card-line` checked out | the in-memory merge of `h` into `b` | the working tree |

`--base`, `--head` and `--head-branch` set them by hand. A head in a fork is judged by the rules
that need no head commit, and D1–D4 are red. `--pr N` asks GitHub as `github.account`
([implementation.md](implementation.md) section 8.1).

**Queue copies.** jackstay keeps no merge queue of its own ([land.md](land.md)), but `jst lint pr`
still resolves a queue's stack copy, as below. A pull request whose head branch is under `jackstay-queue/` and
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

**The configuration read** is `jackstay.json` at `b`, never the pull request's, so a pull request
cannot loosen its own check. **`jackstay.json` is fixed settings** (W-047): a pull request that
changes it changes nothing else (D0) and is the owner's to merge, so no rule ever chooses between
two configurations.

| Rule | Applies to | Refused when |
|---|---|---|
| **D0, settings alone** | every pull request | the diff `merge-base(b, h)..h` changes `jackstay.json` and any other path; a pull request that changes only `jackstay.json` is checked to leave valid release settings (section 1, `ValidateAt`) |
| **D1, documented change** | every pull request of a project with `changes` (at `b`), except release requests (D4) and heads in `cards.exempt_branches` | the diff `merge-base(b, h)..h` adds or modifies no entry and no override, and the description has no line `Changes: none (<reason>)` with a reason; or it has both |
| **D2, valid entries** | every pull request of a project with `changes` | an entry or override added or modified in that diff is invalid (section 2) |
| **D3, versions only by release** | every pull request of a project with `release` that is not a release request (D4) | by the base's settings: the request file, `changes.into` or `changes.internal_into` changed in the diff; at `h`, a version file's pattern not matching once or not holding the last release at `b`, or the build file not holding the base's build number; and in the tree the check judges the same values, the base's changelogs byte for byte, and every output a regular file |
| **D4, a release request** | the pull request whose head branch is `release.branch`, and a queue copy of it | the diff changes any path but the request file (W-034), or the request file at `h` is not a valid request (a regular file holding a JSON object of section 3's shape with a version that parses). Not agreeing with `b` (the base moved, a release was made, a batch member added an entry) is said and **does not fail**: such a request releases nothing (section 8) |

`Changes: none` is a line in the description, never a label (a label races the check; an edit
re-runs `card-line`). The reason is not judged. `jst lint tree` applies D2 to every entry.

| Head branch (a queue copy resolved to its original) | `Card:` lines | tree rules | D1 | D2 | D3 | D4 |
|---|---|---|---|---|---|---|
| `release.branch` (in `cards.exempt_branches` or not) | yes, unless listed; it carries a valid `Card: none` either way | yes | no | yes | no | yes |
| in `cards.exempt_branches` (a bot's) | no | yes | no | yes | yes | no |
| any other | yes | yes | yes | yes | yes | no |

D4 and every owner-only rule follow the release branch: a request file changed on any other branch
is refused by D3, so the `owner` setting cannot be bypassed by copying. Exit codes are `jst lint`'s
(0 fine, 1 a rule broken, each named; 2 an unusable request or `jackstay.json`).

**The boundary.** These checks see pull requests. A version value pushed straight to the base is
found at the next command ("the version files say A, the last release is B", exit 2) and repaired
by a revert. A request file pushed straight to the base, agreeing with it, is released like a merged
one (W-017; a guard rule against such a push is designed, not built).

## 8. Publishing: `jst release publish`

Run by the workflow after each landing. Let `T` be the base's tip as fetched.

1. **Finish what was released.** For every release commit on `T`'s first-parent line, proven from
   the commits (section 3), oldest first: the tag `release.tag` absent is created on it, annotated
   ("Release X.Y.Z"), pushed without force; present on it, kept; present on another commit, **never
   moved**: exit 1 naming both, for a person (W-020, W-026). Then its GitHub release (W-047): absent,
   created once and **published** after the remote tag is read back (`git ls-remote`, peeled)
   pointing at the commit; present, left alone (a draft is reported for a person). "Latest" is
   GitHub's rule (`make_latest: legacy`). A trailer on an unproven commit, or a `release.tag`-shaped
   tag above `release.first` on no release commit, is reported, exit 1, and left alone. Tags are
   listed from the remote. So a run stopped after the commit or the tag is finished by the next.
2. **Release what was asked.** If `T`'s request file is releasable at `T`, make R on `T` with tree
   P(T) and the message

       Release X.Y.Z

       <the consumer section>

       Internal:
       <the internal section, if any>

       Override: <reason>                         (only with an override)

       Card: none (release X.Y.Z)

       Jackstay-Release: X.Y.Z

   then prove R as section 3 does (a failure is exit 2, nothing pushed), and push it with the App's
   token: `git push --force-with-lease=refs/heads/<base>:<T> <remote> <R>:refs/heads/<base>`. A stale
   lease fetches again and starts over from step 1, at most three times; a server policy refusal is
   an error, not retried. Then tag and publish as in step 1. A request file not releasable at `T` is
   reported (what differs) and nothing is released.
3. **Yanked** (below), reconciled. Always reached; the exit code is the worst of the three.

The release commit and the request commit are made with git's plumbing in a temporary index, never
a checkout; their author is the writing App (`JACKSTAY_RELEASE_AUTHOR`). A GitHub release's notes are
the consumer entries R removed, rendered as in section 2, the override's reason, and a compare link
from the previous tag. jackstay never edits a release afterwards, except a yank's title.

**Yanked.** A tag is never moved and a release never deleted. `jst release yank X.Y.Z --reason TEXT`
writes `<changes.dir>/yank-X-Y-Z.md` (`type` note, `audience` consumer, `yanks` X.Y.Z, the reason as
body), refusing an unknown version or a different existing file, and changes nothing on GitHub.
Once it lands, step 3 gives every yanked version whose tag is on its proven commit the title
`X.Y.Z (yanked)` and the reason atop its notes. The next release collects the entry: ` [YANKED]` is
added to that version's heading in `changes.into`. Every step checks whether it is done, so a run
stopped half-way is finished by the next.

## 9. With a merge queue

jackstay keeps no merge queue of its own; the local queue this section once fitted to is removed
in 0.6.0, and a base that requires the host's native merge queue is landed through it by
`jst land` ([land.md](land.md); [W-092](../decisions/W-092-thin-land-and-where-tests-run.md)).
The release flow needs nothing of a queue: the workflow runs on every `push` to the base (a
queue's landing is one, section 6), and a release happens only when the request file, which only
the release pull request writes (D3), has landed and agrees with the base (sections 3 and 8). A
ruleset that restricts pushes to the base must let the writing App push the release commit.

## 10. The writing App

A second GitHub App, made by the organisation's owner:

- owned by the organisation; no webhook, no events;
- repository permissions: **contents write** (the release branch, the release commit, tags,
  releases), **pull requests write**, metadata read; nothing else — no `workflows` permission;
- installed only on the repositories that release with jackstay;
- the variable `JACKSTAY_RELEASE_APP_CLIENT_ID` and secret `JACKSTAY_RELEASE_APP_PRIVATE_KEY` are
  organisation settings visible to those repositories only. Any workflow in a repository that
  sees the secret can make a token that writes to all of them;
- its private key goes straight into the secret, never seen by a session.

**Unproven, settled before a project adopts** by a test on a scratch repository: which of the App's
writes GitHub refuses without `workflows` when the commits involved have workflow files unlike the
default branch's. The test changes a workflow on the base and tries every write: the release
branch's push, the release commit's push, a tag and release on the new tip and on an older commit
(section 8, step 1), and a yank's edit. If one is refused, the permission is reconsidered.

## 11. Migrations

A project without `release` keeps untyped entries ([implementation.md](implementation.md)
section 4.3) and moves to typed ones by adding front matter and the `release` section. Another
tool's change files (Knope's, say) are converted once by a throwaway script, not by jackstay
(W-047): `major | minor | patch` becomes `{"type": "breaking" | "feature" | "fix", "audience":
"consumer"}` (a `major` needs a `## Migration`), checked with `jst changes check`.

## 12. Failure behaviour

Each failure is stated where it happens; in short:

| What goes wrong | What happens |
|---|---|
| Shallow clone; a version value changed outside a release; a pattern matching zero or several times | exit 2, named (sections 3, 4) |
| A tag on a non-release commit, an unproven trailer, a tag on another commit, a draft release | reported by `publish`, exit 1, left for a person (section 8) |
| The release pull request lands after the base moved | nothing released; a new pull request (sections 3, 6) |
| The workflow stops after a push or the tag; a yank stops half-way | the next run finishes it |
| The base or the pull request changes while the workflow writes | lease or read-back; again at most three times, then red |
| The writing App's token cannot be made | red at the first step; nothing written |
| A session merges the release pull request in an `owner` project | not prevented beyond the draft and the AGENTS block; it releases what the request listed |

## 13. What the Go build needs

- `jst changes …`, `jst release …`, `jst lint pr` and `jst lint tree` as commands of the one binary,
  with `jackstay.json` read strictly, including `cards` ([implementation.md](implementation.md)
  section 4.1).
- linux/amd64 and linux/arm64 archives with `checksums.txt` on jackstay's releases, which
  `card-line.yml` and `release.yml` download.
- The entry's front matter, the override and the request are JSON read strictly. The request is
  written with keys in a fixed order (`version`, `entries` sorted by byte, `override`, `made_on`),
  on one line ending with a newline, so it is the same bytes whichever jackstay writes it.

### The Go design (W-036)

[implementation.md](implementation.md) is the one authority for how a command joins the binary.

| Package | What it holds |
|---|---|
| `internal/changes` | the entry model, validation, the one renderer, `new`, `check`, `view`, the untyped `collect` |
| `internal/release` | versions and bumps, the request, the last release and the proof (section 3), prepare (section 5), `override`, `notes`, `yank`, and the workflow's `pr` and `publish` (sections 6, 8) — the only code that writes to GitHub, in files of their own; the rest are pure functions over a repository interface |
| `internal/lint` | `jst lint pr` and `lint tree`: D1–D4 and queue copies (section 7), its inputs as one struct; the `Card:` rules from `internal/cards` |
| `internal/config` | the `changes` and `release` keys |

`internal/release` and `internal/lint` declare small interfaces for git (rev-parse, first-parent
log, trees and blobs with modes, diff, merge-base, check-ref-format, ls-remote, a leased push,
annotated tags) and GitHub (pull requests: find, open as draft or ready, update, close, draft and
ready again; releases: list, create, edit), faked by hand in tests. GitHub is REST through
`go-github`, plus GraphQL for `convertPullRequestToDraft` and `markPullRequestReadyForReview`, so
the static binary needs no `gh`; git is its command line, not `go-git`, because the leased push,
`check-ref-format` and "first parent" are git's own (inferred).

**Tests** (W-013): dates come from commits with fixed `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE`, and
`view` is given `--date`; GitHub is a hand-written fake, the adapter tested against
`net/http/httptest`; the remote is a local bare repository. Covered: tags, shallow and tag-less
clones; every landing shape of the request (squash, rebase, merge commit, push); a request landing
after the base moved; a stop after each push; a tag on a non-release commit; a draft release;
yanks. Real-git tests are `//go:build integration`, in the gate. Section 10's permission question
and section 4's TestFlight limit are checked once by hand.

## 14. What it cannot see

- Whether an entry's type is right (no API-breakage check, W-026), or a `Changes: none` reason true.
- A request merged by a session in an `owner` project, or pushed straight to the base (sections 6, 7).
- A release made by hand outside jackstay: reported, never published, changes nothing computed.
- The build number inside the built app (section 4).

## 15. Adoption

A project adopts from its own session: `jst adopt` writes `release.yml`, `card-line.yml` and the
AGENTS block's `changes` and `release` items ([adopt.md](adopt.md)). Prerequisites: a downloadable
jackstay release (section 13), the writing App installed and its permission test passed (section
10). The project then converts old change files (section 11) and adds `changes` and `release` in a
pull request of their own (D0).
