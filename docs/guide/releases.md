# Changelogs and releases

Parallel branches stop conflicting in `CHANGELOG.md`: each changelog entry is a file of its own,
written in the commit that makes the change, and jackstay collects them at release time. With a
`release` section it goes further: typed entries compute the next version, a workflow keeps a
release pull request open, and merging that pull request makes the release.

## Write a changelog entry: `jst changes`

```json
"changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}"}
```

- `new` writes `changes/<slug>.md` and prints its path. Edit the file to add a body.
- `check` validates every entry and names each problem (exit 1).
- `view` prints the next section as it would be written; nothing is written.
- `collect` (only in a project **without** `release`) writes the section into the changelog and
  removes the entries, once per release, in the release commit.

```sh
jst changes new --slug faster-start --title 'Starts faster'
jst changes check
git add changes/faster-start.md && git commit -m 'Start faster'
```

The entry lands in the same commit as its change. At release time:

```sh
jst changes view --version 1.4.0
jst changes collect --version 1.4.0 --dry-run
jst changes collect --version 1.4.0
```

`collect` needs tracked entry files and a clean changelog and entries folder. If it is
interrupted, rerun the same version in the same worktree: it resumes from its saved plan and
removes only the unchanged tracked entries listed there. The worktree's Git directory keeps the
latest attempt and its outcome. An existing version without a matching unfinished plan leaves the
entries in place. Every flag: `jst changes --help`.

### Typed entries

An entry may carry a type and an audience; in a project with `release` it must:

| Type | Version bump |
|---|---|
| `breaking` (needs `--migration`) | major (minor before 1.0) |
| `feature`, `deprecated` | minor |
| `fix`, `security` | patch |
| `note` | none |

```text
jst changes new --slug widget-builder --type breaking --audience consumer \
  --title 'WidgetForm takes its rows as a builder' \
  --migration 'Replace WidgetForm(rows:) with WidgetForm { … }'
```

The audience is `consumer` (goes into the changelog) or `internal`.

## Release: `jst release`

```json
"release": {
  "merge": "owner",
  "base": "main",
  "tag": "v{version}",
  "first": "0.1.0",
  "files": [{"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]
}
```

How a release happens:

1. Changes land with typed entries.
2. After each landing on the base, the release workflow (written by `jst adopt`) keeps one pull
   request open on `jackstay/release`. It changes one file, the request: the next version and the
   exact entries it releases.
3. The owner merges it (or, with `"merge": "agents"`, a session may).
4. The workflow runs again, commits the version files and the changelog, tags that commit and
   publishes the GitHub release. If something landed in between, nothing is released and the pull
   request is refreshed.

Before 1.0, a breaking change bumps the minor version. 1.0 comes only by override.

```sh
jst release prepare
jst release notes 1.3.0 --format text
jst release override --version 1.0.0 --reason 'the interface is stable'
```

`prepare` shows the next release as the workflow would make it; `notes` prints a release's notes
(`markdown`, `text`, `testflight` or `sparkle-html`); `override` writes a version chosen by hand, as
a file for a pull request; `jst release yank` marks a release yanked, as an entry. Every flag: `jst release --help`.

`jst release pr` and `jst release publish` are the workflow's own steps. They write to GitHub only
inside GitHub Actions with `GH_TOKEN` set; anywhere else they are dry runs. A dry run can fetch the remote base into a temporary Git repository and leaves your
checkout's refs and objects unchanged. The commands use the project's Git remote
(`github.remote` when set), and the fetched branch must agree with its release base.
A tag is never moved.

### What the release workflow needs

The workflow pushes and opens pull requests with a **GitHub App's** token, because events made with
the workflow's own `GITHUB_TOKEN` start no further workflows. You create the App once:

- repository permissions: contents write, pull requests write, metadata read; no webhook;
- installed only on the repositories that release with jackstay;
- the variable `JACKSTAY_RELEASE_APP_CLIENT_ID` and the secret `JACKSTAY_RELEASE_APP_PRIVATE_KEY`,
  visible to those repositories.

The workflow downloads the jackstay release pinned in `adopt.pin`.

## `jst lint pr`

One check of a pull request: its `Card:` lines ([records](records.md)), its changelog entry, and the
tree's rules. In a project with `changes`, every pull request adds or edits an entry, or says why
not on a line of its description:

```text
Changes: none (refactoring with no visible effect)
```

A pull request that changes `jackstay.json` changes nothing else. In a project with `release`,
version files and the changelog change only in release commits.

```sh
jst lint pr --body-file pr-body.md --base origin/main --head HEAD
jst lint pr --pr 42
jst lint pr --event "$GITHUB_EVENT_PATH"
```

The first checks by hand, before you open the pull request; the second reads it from GitHub (it
needs `github.account`); the third is what the `card-line` workflow runs. It exits 1 with each
problem named. Every flag and exit status: `jst lint pr --help`.

## What can go wrong

- **An invalid entry**: `jst changes check` names it and the fix (a missing title, an unknown type,
  a breaking entry without `## Migration`).
- **`collect` in a project with `release`**: refused; the release workflow writes the changelog.
- **No release pull request**: only `note` entries have landed; they bump nothing.
- **The version files say A, the last release is B**: a version was changed outside a release
  commit. Revert it.

The design: [release.md](../design/release.md).
