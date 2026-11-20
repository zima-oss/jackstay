# The guard, discard and trash

The guard stops an agent's shell command before it destroys work that cannot be got back:
uncommitted edits, branches whose commits are nowhere else, folders. Each refusal names a command
that does the job safely, often one of jackstay's own: `jst discard` drops uncommitted changes but
keeps a copy, and `jst trash` is a delete you can undo.

The guard runs as a hook in [Claude Code](claude-code.md#the-guard-hook) (written by `jst adopt`)
or [Codex](codex.md#the-guard-as-a-codex-hook). Without a hook, ask it about any command yourself.

## Ask the guard about a command

```sh
jst guard check -- 'git push --force origin main'
jst guard check -- 'rm -rf build'
jst guard check --cwd ../other-repo -- 'git branch -D old-feature'
```

`check` judges the command as the hook would, in the folder `--cwd` names (default: here). It
exits 0 when the command is allowed (a warning, if any, on standard error) and 2 when it is refused
(the reason on standard error). `jst-guard`, installed beside `jst`, is the hook's name for
`jst guard`. Every flag and exit status: `jst guard --help`.

### What it refuses

The line is parsed as a shell parses it, so text inside quotes, a commit message or a `grep`
pattern is data, while `$(…)`, `bash -c '…'`, `xargs`, `find -exec` and the like are looked into.
The built-in rules, by id:

| Rule | Refuses | Suggests |
|---|---|---|
| `git-reset-hard` | `git reset --hard` | `jst discard <path>`; `git reset --keep <commit>` |
| `git-checkout-path`, `git-restore-worktree` | `git checkout -- <path>`, `git checkout .`, `git restore <path>` over uncommitted changes | `jst discard <path>`; `git restore --staged` |
| `git-checkout-force`, `git-switch-discard` | `git checkout -f`, `git switch --discard-changes` | `git stash push -u`, then `git switch` |
| `git-clean-force` | `git clean -f` | `git clean -n`, then `jst trash <path>` |
| `git-rm-force` | `git rm -f` | `git rm`; `git rm --cached` |
| `git-push-force` | `git push --force`, `-f`, `+refspec`, `--mirror` | `--force-with-lease --force-if-includes` |
| `git-push-prune`, `git-push-delete-protected` | `git push --prune`; deleting `main` or `master` on the remote | `--prune --dry-run`; one `--delete` |
| `git-branch-force-delete` | `git branch -D` of a branch whose commits are nowhere else | `jst tidy --branch <name>`; `git branch -d` |
| `git-branch-reset` | `git checkout -B`, `git switch -C`, `git branch -f` over a branch that would lose commits | `git tag archive/<name> <name>` first |
| `git-stash-drop`, `git-stash-clear` | `git stash drop`, `git stash clear` | `git stash apply`; `git stash branch` |
| `git-worktree-force` | `git worktree remove --force` of a worktree with changes | `jst tidy --worktree <path>` |
| `git-update-ref-delete`, `git-update-ref-move` | `git update-ref -d`, moving a ref by hand | `git branch -d`; `git tag -d` |
| `git-filter-branch`, `git-filter-repo` | history rewrites | the owner runs them in their own terminal |
| `git-read-tree-reset`, `git-checkout-index-force` | `git read-tree -u --reset`, `git checkout-index -f` | `jst discard <path>` |
| `git-config-alias`, `git-config-worktree` | git configuration on the line the guard cannot read (aliases, includes, config files), moving the work tree | the git command written out |
| `cards-push`, `card-push-unreadable-config` | a push to the cards branch without valid `Card:` lines ([records](records.md)) | `jst lint range` |
| `rm-recursive` | `rm -r` of anything but temporary and regenerable folders | `jst trash <path>` |
| `find-delete` | `find … -delete` | `find … -print`, then `jst trash` |
| `env-split-string` | `env -S STRING` it cannot split | the command without `env -S` |
| `zsh-glob` | in zsh, an unquoted pattern that matches no file (`--include=*.go`): zsh would stop the command | the same command with the word quoted |

What is let through matters as much: `git branch -D` of a branch that adds nothing to the default
branch (after a squash merge), `git checkout -- <path>` where nothing is uncommitted, `rm -r` inside
the session's scratch folder or a folder `mktemp` made in the same command, and `rm -r` of
regenerable folders: `.build`, `DerivedData`, `node_modules`, `__pycache__`, `.pytest_cache`,
`.mypy_cache`, `.ruff_cache`, `.tox`, `.venv`, `venv`, `htmlcov`, `*.egg-info`, plus your project's
`guard.regenerable`.

A path known only when the command runs (`rm -rf "$DIR"` with `DIR` read from a file) is refused:
the guard does not guess. A line it cannot parse is refused with the advice to write it plainly.
Every refusal ends by saying that the owner can run the exact command in their own terminal, and
that an agent must not rephrase it to get around the guard.

### Levels, and your own rules

Every rule refuses by default, except `zsh-glob`, which warns. A project sets levels in
`jackstay.json`:

```json
"guard": {
  "regenerable": ["build-cache"],
  "rules": {
    "zsh-glob":       {"level": "refuse"},
    "git-push-force": {"level": "warn"},
    "rm-recursive":   {"paths": ["Generated/**", "~/Library/Caches/myapp/**"]}
  },
  "deny": [
    {"id": "no-prod-deploy", "command": "^deploy .*--env prod", "why": "production deploys go through CI",
     "instead": "open a pull request; CI deploys on merge"}
  ]
}
```

- `level`: `refuse`, `warn` (the command runs, and the agent is told what the rule says) or `off`.
- `paths` (only `rm-recursive` and `find-delete`): folders below the project's top or below `~/`
  where a recursive delete is allowed. They apply only when the line is that one `rm` or `find`
  command and the path is written as its real path.
- `regenerable`: extra folder names that may always be deleted.
- `deny`: your own rules, with RE2 patterns (`command`, an optional `env` and `unless`), shown as
  `project:<id>`.
- `git-config-alias`, `git-config-worktree`, `env-split-string` and `card-push-unreadable-config`
  say a command cannot be judged; they take no level.

Check the section with `jst check`. A `guard` section that cannot be used turns off your rules and
levels (the built-in rules stay on), and every answer says so.

### What it cannot see

A command inside a script, a `justfile` recipe or a `Makefile`; a deletion by other means
(`python -c`, `rsync --delete`, `mv`); tools other than the agent's shell tool; your own terminal.
It fails open on a fault of its own code (the command runs, and you are told), because a guard that
blocked on its own bug would stop every session at once. `jst doctor` counts those failures, which
should be zero.

The full table, with every allowance: [the guard's design](../design/guard.md).

## Drop uncommitted changes: `jst discard`

`jst discard` puts each path (a file or a folder) back to HEAD. Whatever that throws away, the file
on disk or a staged version that differs from both, is first kept in jackstay's trash:

```sh
jst discard src/parser.go docs/draft.md
```

```text
jst discard: src/parser.go: what was on disk kept as 20261117-224808-1 (`jst trash restore 20261117-224808-1`)
jst discard: 2 path(s) back to HEAD; 1 kept in jackstay's trash for 7 days (`jst trash list`)
```

An untracked file is left alone (`jst trash PATH` moves it). Every path is checked before anything
changes: with one bad path (outside a work tree, in a merge conflict, a submodule) nothing is done.
Every flag and exit status: `jst discard --help`.

## Delete undoably: `jst trash`

```sh
jst trash build/old-output
jst trash list
jst trash restore 20261117-224808-2
jst trash restore 20261117-224808-2 --to /tmp/old-output
jst trash -- list
```

The last two put an entry back somewhere else, and move a path named `list` into the trash. Entries
live in `~/.jackstay/.trash/`, which Claude Code's sandbox can write once `~/.jackstay` is allowed
([Claude Code](claude-code.md#the-sandbox)); the system's Trash cannot be written from inside it.
Each entry is kept for the `trash.keep_days` (default 7) of the project it came from; once older,
the next `jst trash` or `jst discard` removes it, and `jst trash prune` removes it now. Every flag
and exit status: `jst trash --help`.

## What can go wrong

- **A refusal for a command you meant**: read its `Instead`. If you mean it, run it in your
  own terminal. To loosen a rule for a whole project, set its level in `guard.rules`.
- **A `zsh-glob` warning on a pattern that will match later** (a folder that is empty now): quote
  the word; the warning is about the shell, not about danger.
- **`jst trash restore` refused**: something is already at the path; use `--to PATH`.
- **The trash is per machine**: an entry is kept `trash.keep_days`, then gone.

The design: [guard.md](../design/guard.md), [discard-and-clean-gate.md](../design/discard-and-clean-gate.md).
