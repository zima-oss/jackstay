# Branches and GitHub

Four commands replace the fetches, merges, worktree adds, clean-ups and CI polling loops that
sessions otherwise write by hand: `jst start`, `jst sync`, `jst tidy` and `jst gh`.

## Which remote, which default branch

They share one rule: the remote is `github.remote` from `jackstay.json`, else `origin`, else the
remote your local `main` (then `master`) tracks, else the only remote. The default branch is that
remote's as fetched (`<remote>/HEAD`, else `main`, `master`).

## `jst start`

```sh
jst start NAME [--from REF] [--path DIR] [--no-fetch]
```

Fetches, then makes branch `NAME` on the default branch as fetched (not a stale local `main`), in a
worktree of its own: `.claude/worktrees/NAME` when git ignores that folder, else a folder beside the
checkout, `<checkout>-worktrees/NAME`. `--path` chooses the folder; `--from REF` starts elsewhere.

```sh
jst start fix-parser                        # prints the folder it made
jst start fix-parser-2 --path ../fix-parser-2   # or choose the folder
cd ../fix-parser-2
```

Exit: 0 made; 1 git refused; 2 a taken name or folder, or a `REF` that is not a commit.

## `jst sync`

```sh
jst sync [--no-fetch] [--push] [DIR]
```

Brings this branch up to date with the default branch: a fast-forward on the default branch, a
merge on any other. Never a rebase (`--rebase` is refused): a rebase replays every commit and can
write over files git ignores. Before merging it checks, in memory, that the merge would not write
over an untracked or ignored file.

```sh
jst sync
jst sync --push      # then push exactly this branch to its own upstream
```

Exit: 0 done or nothing to do; 1 a conflict (the files are named; resolve or abort the merge
yourself), or git refused; 2 uncommitted work, a detached HEAD, or a merge already under way.

## `jst tidy`

```sh
jst tidy [--fix] [--branch NAME]... [--worktree PATH]... [--no-fetch] [--no-github] [--json] [DIR]
```

Reports the branches and worktrees whose work is done; `--fix` removes them. A branch is finished
when its commits are on the default branch, when merging it would change nothing, or when GitHub
lists a merged pull request at its tip. It is kept when a pull request from it is open, or when
nothing shows it finished. A worktree is removed only with nothing modified, staged or untracked,
nothing ignored but regenerable folders, not locked, and no process working in it.

```sh
jst tidy                             # the report
jst tidy --fix --branch fix-parser   # remove only that branch and its worktree
jst tidy --no-github                 # git alone; branches only GitHub could show finished are kept
```

`--fix` judges each item again just before removing it, runs `git worktree remove` without
`--force`, and deletes the branch only at the tip it judged. Remote branches are never touched.

Exit: 0 nothing finished is left; 1 something finished is listed and not removed, or a removal
failed; 2 the request is wrong.

Inside an agent sandbox `jst tidy` cannot see every process, so it keeps every worktree and says
why; run `--fix` outside the sandbox.

## `jst gh`

```sh
jst gh ci-wait [PR|URL] [--timeout S] [--interval S] [--lines N] [--any-head]
jst gh ci-status [PR|URL]
jst gh run-wait WORKFLOW [--ref BRANCH|SHA] [--repo URL] [--timeout S] [--interval S] [--appear S]
jst gh pr-find [--head BRANCH] [--search TEXT] [--repo URL] [--wait S] [--state open|closed|merged|all]
jst gh pr-status
jst gh pr-land [--title T] [--body-file F] [--card ID]... [--reason TEXT] [--no-push] [--no-create]
               [--no-wait] [--no-merge] [--dry-run] [--timeout S] [--interval S] [--lines N]
```

- `ci-wait` waits until every check on a pull request's current head settles (default: this
  branch's), and prints the failing step's log of each red check. `ci-status` is the same, once.
- `run-wait` waits for one workflow run on a branch or commit.
- `pr-find` finds one pull request by head branch or search text; none found is exit 1.
- `pr-status` says where this branch and its pull request stand.
- `pr-land` pushes, opens the pull request, waits for its checks, squash-merges the head that was
  checked and deletes the remote branch. The checks it waits for are the ones the base branch's
  rules (rulesets and branch protection) require; another check that fails still stops it, one
  still pending does not. When the rules require none, it waits for every check. A run cancelled
  by its `concurrency` group for a newer run is superseded: the newer run is judged. The plan is
  printed first; `--dry-run` stops there. It
  never switches, pulls or deletes anything in your checkout; `jst tidy` removes the local branch
  afterwards.

Exit codes are a contract, because these run inside loops: 0 fine; 1 not fine (the reason
printed); 2 a bad request (also: no account configured, a wrong login); 3 still pending at the
deadline; 4 the head moved while waiting; 5 GitHub answered in a shape jackstay does not know.

```sh
jst gh pr-land --title 'Fix the parser' --dry-run
jst gh pr-land --title 'Fix the parser' --body-file pr-body.md
jst gh ci-wait --timeout 1200
jst gh run-wait release --ref main
```

Defaults: `--timeout` 1800 s, `--interval` 20 s, `--lines` 25, or the `github.wait` values.

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

The token comes from `gh auth token --user <account>`, so you need the [GitHub CLI](https://cli.github.com/)
installed and that account signed in (`gh auth login --hostname github.com`). `JACKSTAY_GITHUB_ACCOUNT`
overrides the account for one call. A `GH_TOKEN` in your environment is ignored outside Actions
(it could belong to any account); inside Actions, `GH_TOKEN` is the only token used. Only
github.com is supported.

The account covers jackstay's API calls only. `pr-land`'s push and the remote branch delete go
through git, with git's own credentials.

Without `github.account`: `jst gh` exits 2, `jst tidy` does not ask GitHub, and `jst lint pr --pr`
exits 2. Everything else keeps working.

## What can go wrong

| What you see | Why |
|---|---|
| `jst gh`: set github.account in jackstay.json | No account configured. |
| Exit 2: the login is not the account | `gh` holds a token under a stale name; sign in again. |
| `pr-land` refuses before pushing | You are on the default branch, the tree is dirty, or a new pull request has no `--title`. In a project with `cards`, the description has no valid `Card:` line ([records](records.md)). |
| `pr-land` exit 3 after merging | A merge queue or auto-merge holds the pull request; the branch is kept. |
| `ci-wait` exit 4 | Someone pushed while you waited. Wait again, or use `--any-head`. |
| `jst sync` exit 1 naming an ignored file | The merge would write over it. Move the file, then sync. |

The design: [implementation.md, section 8](../design/implementation.md#8-github-and-branches-jst-gh-the-github-section-jst-tidy-jst-start-jst-sync-jst-owner).
