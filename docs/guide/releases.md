# Changelogs and releases

A shared `CHANGELOG.md` is where parallel branches conflict. jackstay keeps each changelog entry as
a file of its own, written in the commit that makes the change, and collects them at release time.
With a `release` section it goes further: typed entries compute the next version, a workflow keeps
a release pull request open, and landing that pull request makes the release.

## Changelog entries: `jst changes`

```sh
jst changes new --slug SLUG [--type TYPE] [--audience AUDIENCE] [--title TEXT] [--migration TEXT] [--card ID]
jst changes check [--json]
jst changes view [--version V] [--date D] [--audience AUDIENCE]
jst changes collect --version V [--date D] [--dry-run]
```

```json
"changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}"}
```

- `new` writes `changes/<slug>.md` and prints its path. Edit the file to add a body.
- `check` validates every entry: exit 0 all valid; 1 problems, each named; 2 `jackstay.json`
  cannot be used.
- `view` prints the next section as it would be written; nothing is written.
- `collect` (only in a project **without** `release`) writes the section into the changelog and
  removes the entries, once per release, in the release commit.

```sh
jst changes new --slug faster-start --title 'Starts faster'
jst changes check
git add changes/faster-start.md && git commit -m 'Start faster'   # the entry lands with its change
jst changes view --version 1.4.0
jst changes collect --version 1.4.0 --dry-run
jst changes collect --version 1.4.0
```

`collect` refuses while `CHANGELOG.md` or the entries folder has uncommitted changes.

### Typed entries

An entry may carry a type and an audience; in a project with `release` it must:

| Type | Version bump |
|---|---|
| `breaking` (needs `--migration`) | major (minor before 1.0) |
| `feature`, `deprecated` | minor |
| `fix`, `security` | patch |
| `note` | none |

```sh
jst changes new --slug widget-builder --type breaking --audience consumer \
  --title 'WidgetForm takes its rows as a builder' \
  --migration 'Replace WidgetForm(rows:) with WidgetForm { … }'
```

The audience is `consumer` (goes into the changelog) or `internal`.

## Releases: `jst release`

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
jst release prepare [--json]                        # the next release as the workflow would make it
jst release notes [VERSION] [--format FORMAT] [--audience AUDIENCE]   # markdown, text, testflight, sparkle-html
jst release override --version V --reason TEXT      # a version by hand, as a file for a pull request
jst release yank X.Y.Z --reason TEXT                # mark a release yanked, as an entry
jst release pr [--dry-run]                          # the workflow's step
jst release publish [--dry-run]                     # the workflow's step
```

```sh
jst release prepare
jst release notes 1.3.0 --format text
jst release override --version 1.0.0 --reason 'the API is stable'
```

`pr` and `publish` write to GitHub only inside GitHub Actions with `GH_TOKEN` set; anywhere else they
are dry runs, so nothing run locally races the workflow. A tag is never moved.

### What the release workflow needs

The workflow pushes and opens pull requests with a **GitHub App's** token, because events made with
the workflow's own `GITHUB_TOKEN` start no further workflows. You create the App once:

- repository permissions: contents write, pull requests write, metadata read; no webhook;
- installed only on the repositories that release with jackstay;
- the variable `JACKSTAY_RELEASE_APP_CLIENT_ID` and the secret `JACKSTAY_RELEASE_APP_PRIVATE_KEY`,
  visible to those repositories.

The workflow downloads the jackstay release pinned in `adopt.pin`.

## `jst lint pr`

```sh
jst lint pr (--event FILE | --pr N | --body-file F) [--rev REV] [--base B] [--head H] [--head-branch NAME]
```

One check of a pull request: its `Card:` lines ([records](records.md)), its changelog entry, and the
tree's rules. In a project with `changes`, every pull request adds or edits an entry, or says why
not on a line of its description:

```
Changes: none (refactoring with no visible effect)
```

A pull request that changes `jackstay.json` changes nothing else. In a project with `release`,
version files and the changelog change only in release commits.

```sh
jst lint pr --body-file pr-body.md --base origin/main --head HEAD    # by hand, before opening
jst lint pr --pr 42                                                  # read from GitHub (needs github.account)
jst lint pr --event "$GITHUB_EVENT_PATH"                             # in the card-line workflow
```

Exit: 0 no problem; 1 problems, each named; 2 the request or `jackstay.json` cannot be used;
5 an event file that is not what GitHub documents.

## What can go wrong

- **An invalid entry**: `jst changes check` names it and the fix (a missing title, an unknown type,
  a breaking entry without `## Migration`).
- **`collect` in a project with `release`**: refused; the release workflow writes the changelog.
- **No release pull request**: only `note` entries have landed; they bump nothing.
- **The version files say A, the last release is B**: a version was changed outside a release
  commit. Revert it.

The design: [release.md](../design/release.md).
