# Branches and GitHub

Five commands replace the fetches, merges, worktree adds, clean-ups, CI polling loops and merges
that sessions otherwise write by hand: `jst start`, `jst sync`, `jst tidy`, `jst checks` and
`jst land`.

All four use one remote: `github.remote` from `jackstay.json`, else `origin`, else the remote your
local `main` (then `master`) tracks, else the only remote. The default branch is that remote's as
fetched (`<remote>/HEAD`, else `main`, `master`).

## Start a task: `jst start`

```sh
jst start fix-parser
jst start fix-parser-2 --path ../fix-parser-2
cd ../fix-parser-2
```

`jst start` fetches, then makes the branch on the default branch as fetched (not a stale local
`main`), in a worktree of its own, and prints its folder: `.claude/worktrees/NAME` when git ignores
that folder, else `<checkout>-worktrees/NAME` beside the checkout. `--path` chooses the folder;
`--from REF` starts from another commit. Every flag and exit status: `jst start --help`.

## Bring a branch up to date: `jst sync`

```sh
jst sync
jst sync --push
```

`jst sync` fast-forwards the default branch, and merges the default branch into any other. It
never rebases (`--rebase` is refused). Before merging it checks, in memory, that the merge would
not write over an untracked or ignored file. `--push` then pushes exactly this branch to its own
upstream.

It stops with exit 1 on a conflict, naming the files: resolve or abort the merge yourself. It
refuses (exit 2) uncommitted work, a detached HEAD, or a merge already under way. Every flag:
`jst sync --help`.

## Remove what is finished: `jst tidy`

```sh
jst tidy
jst tidy --fix --branch fix-parser
jst tidy --no-github
```

The first reports the branches and worktrees whose work is done. The second removes only that
branch and its worktree. The third asks git alone; branches only GitHub could show finished are
kept.

- A branch is finished when its commits are on the default branch, when merging it would change
  nothing, or when GitHub lists a merged pull request at its tip. It is kept when a pull request
  from it is open, or when nothing shows it finished.
- A worktree is removed only with nothing modified, staged or untracked, nothing ignored but
  regenerable folders, not locked, and no process working in it.
- `--fix` judges each item again right before removing it, runs `git worktree remove` without
  `--force`, and deletes the branch only at the tip it judged. Remote branches are never touched.
- Inside an agent sandbox `jst tidy` cannot see every process, so it keeps every worktree and says
  why: run `--fix` outside the sandbox.

`jst tidy` exits 1 while something finished is listed and not removed. Every flag: `jst tidy --help`.

## `jst checks`

```text
jst checks wait [PR|URL] [--timeout S] [--interval S] [--lines N] [--any-head]
jst checks status [PR|URL]
jst checks run-wait WORKFLOW [--ref BRANCH|SHA] [--repo URL] [--timeout S] [--interval S] [--appear S]
```

- `wait` waits until every check on a pull request's current head settles (default: this
  branch's), and prints the failing step's log of each red check if time remains. Its timeout
  includes GitHub requests and retries. `--timeout 0` makes one look, with up to 10 seconds for
  its requests. A cancelled Actions check waits for a newer run's matching job only when GitHub
  confirms that the run has the same workflow and commit; otherwise it remains cancelled.
- `status` is one look.
- `run-wait` waits for one workflow run on a branch or commit.

Exit codes are a contract, because these run inside loops: 0 fine; 1 not fine (the reason
printed); 2 a bad request (also: no account configured, a wrong login); 3 still pending at the
deadline; 4 the head moved and the check results no longer apply; 5 GitHub answered in a shape
jackstay does not know.

```sh
jst checks wait --timeout 1200
jst checks run-wait release --ref main
```

## `jst land`

```text
jst land [--change N|URL] [--title T] [--body-file F] [--card ID]... [--reason TEXT]
         [--prepare | --confirm] [--plan] [--timeout S] [--interval S] [--lines N]
jst land status [--change N|URL]
jst land find (--head BRANCH | --search TEXT) [--repo URL] [--state STATE] [--wait S]
```

`jst land` pushes the branch, opens the change request (a pull request on GitHub), waits for the
checks the base branch's rules require, and merges at the head that was checked. It prints the plan
first; `--plan` stops there. It never switches, pulls or rebases your checkout; `jst tidy` removes
the local branch afterwards.

```sh
jst land --title 'Fix the parser' --plan
jst land --title 'Fix the parser' --body-file pr-body.md
jst land status
```

### The `land` section

`jst land` needs a `land` section in `jackstay.json` (`jackstay_schema: 3`); without one it exits 2
and names `jst adopt`, which writes the section in full:

```json
"land": {
  "host": "github",
  "autonomy": "merge-when-green",
  "method": "squash",
  "checks": [],
  "human_paths": []
}
```

`autonomy` says who merges:

| `autonomy` | Who merges |
|---|---|
| `merge-when-green` | the agent, when the required checks pass and the change touches no human path |
| `prepare` | a person, always; the agent pushes, opens the change request and waits for the checks |

The human paths are `jackstay.json`, `.github/workflows/**`, `.claude/settings.json` and the globs
in `human_paths`. `method` is `squash`, `merge` or `rebase`; a project whose Card line is in the
description (`cards.unit` `pr`) cannot use `rebase`, which lands no description. `checks` names
checks required in addition to the base's rules. `--prepare` stops before the merge for one run,
whatever `autonomy` says. `autonomy` and `checks` are read from the base's `jackstay.json`, never
the change's.

`jst adopt` asks who merges before it writes the section, the recommended answer first (at a
terminal; elsewhere `--apply` needs `--autonomy merge-when-green|prepare`), and never changes an
`autonomy` the file has. `jst setup` asks the same once per machine and keeps the answer adopt
offers first.

### Ready for a person

When the agent may not merge, `jst land` still pushes, opens the change request and waits for the
checks, then ends with `jst land: ready for a person` (exit 6). A person then either merges on the
host's page, or runs `jst land --confirm` at a terminal and types the change's number; `jst land`
judges the policy and the checks again before it merges. `--confirm` is refused without a terminal.

### Which checks count

A check the base's rules require, or one listed in `land.checks`, must pass or be skipped; a failed
or cancelled one stops the landing. Any other check is reported and never waited for: a red
optional check does not block. When the rules require no check and `land.checks` is empty, CI is
not a gate for that base, so `jst land` stops ready for a person. When the rules cannot be read, the
checks in `land.checks` are required; with none listed, it stops ready for a person.

Where the base requires a merge queue, the agent does not queue the change: the host's queue merges
later, on a base jackstay has not judged, so `jst land` stops ready for a person. A person queues it
on the host's page, or with `jst land --confirm`, which puts it in the queue at the head that was
checked and waits for it to merge.

### The last line

`jst land` ends with `jst land: STATE` on standard error:

| State | Exit | When |
|---|---|---|
| `landed` | 0 | merged, or merged by the host's queue within the wait |
| `planned` | 0 | `--plan` |
| `refused` | 1 | a required check failed or was cancelled; a draft; a conflict; an operation the host does not offer |
| `invalid` | 2 | usage, configuration, no account, no `land` section |
| `pending` | 3 | checks or the queue still pending at the deadline |
| `moved` | 4 | the head, the base or the test merge moved: run it again |
| `unexpected` | 5 | a host answer jackstay does not expect |
| `ready for a person` | 6 | a person merges (above) |

Only `landed` exits 0 with the change merged, so `jst land && jst tidy` never runs on an unmerged
change. Defaults: `--timeout` 1800 s, `--interval` 20 s, `--lines` 25, or the `github.wait` values.

### The guard

The guard refuses `gh pr merge` (rule `gh-pr-merge`) and `glab mr merge` (rule `glab-mr-merge`)
and names `jst land`: a merge on the host from an agent's shell would skip the project's landing
policy. Like every guard rule, they can be set in `jackstay.json` ([the guard](guard.md)).

### The account

Outside GitHub Actions, jackstay acts as the account `jackstay.json` names, so that with several
accounts signed in to the GitHub CLI it never depends on, or changes, the active one:

```json
"github": {
  "account": "your-login",
  "remote": "origin",
  "repository": "your-org/your-repo",
  "wait": {"timeout_seconds": 900, "interval_seconds": 20, "log_lines": 80}
}
```

- The token comes from `gh auth token --user <account>`: install the
  [GitHub CLI](https://cli.github.com/) and sign that account in
  (`gh auth login --hostname github.com`). Only github.com is supported.
- `JACKSTAY_GITHUB_ACCOUNT` overrides the account for one call.
- Outside Actions a `GH_TOKEN` in your environment is ignored, since it could belong to any
  account; inside Actions, `GH_TOKEN` is the only token used.
- Without `github.account`, `jst checks`, `jst land` and `jst lint pr --pr` exit 2 and `jst tidy`
  does not ask GitHub. Everything else keeps working.

The account covers jackstay's API calls only. `jst land`'s push and the remote branch delete go
through git, with git's own credentials. Every effective push URL of the selected remote must
name the GitHub repository of the pull request, including URLs set by `pushurl` or URL rewrites.
After merging, `jst land` skips deletion if it cannot verify the destination or if another open
pull request in the repository still uses the branch.

Without `github.account`: `jst checks` and `jst land` exit 2, `jst tidy` does not ask GitHub, and
`jst lint pr --pr` exits 2. Everything else keeps working.

## Upgrading from 0.5.x

jackstay 0.6.0 removes the commands below with no alias. A removed command is unknown: its error
names the replacement and runs nothing.

| Removed (after `jst`) | In 0.6.0 |
|---|---|
| `gh pr-land` | `jst land` (`--pr` is `--change`, `--dry-run` is `--plan`, `--no-merge` is `--prepare`) |
| `gh pr-status`, `gh pr-find` | `jst land status`, `jst land find` |
| `gh ci-wait`, `gh ci-status`, `gh run-wait` | `jst checks wait`, `jst checks status`, `jst checks run-wait` (same flags) |
| `queue …` and the `queue` key (never released) | none: `jst land` uses the host's merge queue |
| `receipts attest-ci`, `receipts verify-ci` (never released) | none: provenance stays in release and install |

One behaviour changes: a check the base's rules do not require never blocks `jst land`. In 0.5.11
any failing check stopped `pr-land`.

## What can go wrong

| What you see | Why |
|---|---|
| `jst checks`: set github.account in jackstay.json | No account configured. |
| Exit 2: the login is not the account | `gh` holds a token under a stale name; sign in again. |
| `jst land` refuses before pushing | You are on the default branch, the tree is dirty, or a new change request has no `--title`. Where the base asks for a `Card:` line, the description has none or fails its rules on the merge with the base ([records](records.md)). |
| `jst land: ready for a person` | The change touches a human path, the project is `prepare`, or the base requires no check. A person merges it on the host or with `jst land --confirm`. |
| `jst land: pending` after `--confirm` queued it | The host's merge queue holds the change; the branch is kept. |
| `jst checks wait` exit 4 | Someone pushed while you waited. Wait again, or use `--any-head`. |
| `jst sync` exit 1 naming an ignored file | The merge would write over it. Move the file, then sync. |

The design: [landing](../design/land.md) and
[implementation.md, section 8](../design/implementation.md#8-github-and-branches-jst-checks-the-github-section-jst-tidy-jst-start-jst-sync-jst-owner).
