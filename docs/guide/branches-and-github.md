# Branches and GitHub

Four commands do what sessions otherwise write by hand as fetches, merges, worktree adds, clean-ups
and CI polling loops: `jst start` begins a task in its own worktree, `jst sync` brings it up to
date, `jst tidy` removes what is finished, and `jst gh` waits on GitHub.

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

## Wait on GitHub: `jst gh`

```sh
jst gh ci-wait --timeout 1200
jst gh run-wait release --ref main
```

- `ci-wait` waits until every check on a pull request's current head settles (default: this
  branch's), and prints the failing step's log of each red check if time remains. Its timeout
  includes GitHub requests and retries. `--timeout 0` makes one look, with up to 10 seconds for
  its requests. A cancelled Actions check waits for a newer run's matching job only when GitHub
  confirms that the run has the same workflow and commit; otherwise it remains cancelled.
  `ci-status` is one look.
- `run-wait` waits for one workflow run on a branch or commit.
- `pr-find` finds one pull request by head branch or search text; none found is exit 1.
- `pr-status` says where this branch and its pull request stand, including the pull request's base.
- When a branch has multiple open pull requests, select one by number or URL. For `pr-status` and
  `pr-land`, use `--pr PR|URL` to select an open pull request from this branch and repository.

Defaults: `--timeout` 1800 s, `--interval` 20 s, `--lines` 25, or the `github.wait` values.

The exit codes are a contract, so a loop can act on them: 0 fine; 1 not fine (the reason printed);
2 a bad request (also: no account configured, a wrong login); 3 still pending at the deadline;
4 the pull request changed and the check results no longer apply; 5 GitHub answered in a shape
jackstay does not know. Every flag: `jst gh --help`.

### Land a pull request: `jst gh pr-land`

```sh
jst gh pr-land --title 'Fix the parser' --dry-run
jst gh pr-land --title 'Fix the parser' --body-file pr-body.md
```

- `pr-land` pushes, opens the pull request, waits for its checks, squash-merges the head that was
  checked and deletes the remote branch. The checks it waits for are the ones the base branch's
  rules (rulesets and branch protection) require; another check that fails still stops it, one
  still pending does not. When the rules require none, it waits for every check. A change in the
  head, reported base commit or selected test merge commit after the checks stops the merge.
  The plan is printed first; `--dry-run` stops there. It
  never switches, pulls or deletes anything in your checkout; `jst tidy` removes the local branch
  afterwards.

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
- Without `github.account`, `jst gh` and `jst lint pr --pr` exit 2 and `jst tidy` does not ask
  GitHub. Everything else keeps working.

The account covers jackstay's API calls only. `pr-land`'s push and the remote branch delete go
through git, with git's own credentials. Every effective push URL of the selected remote must
name the GitHub repository of the pull request, including URLs set by `pushurl` or URL rewrites.
After merging, `pr-land` skips deletion if it cannot verify the destination or if another open
pull request in the repository still uses the branch.

## What can go wrong

| What you see | Why |
|---|---|
| `jst gh`: set github.account in jackstay.json | No account configured. |
| Exit 2: the login is not the account | `gh` holds a token under a stale name; sign in again. |
| `pr-land` refuses before pushing | You are on the default branch, the tree is dirty, or a new pull request has no `--title`. In a project with `cards`, the description of a non-exempt branch has no valid `Card:` line ([records](records.md)). |
| `pr-land` exit 3 after merging | A merge queue or auto-merge holds the pull request; the branch is kept. |
| `ci-wait` exit 4 | Someone pushed while you waited. Wait again, or use `--any-head`. |
| `jst sync` exit 1 naming an ignored file | The merge would write over it. Move the file, then sync. |

The design: [implementation.md, section 8](../design/implementation.md#8-github-and-branches-jst-gh-the-github-section-jst-tidy-jst-start-jst-sync-jst-owner).
