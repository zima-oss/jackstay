# `jst discard`, advice that is a way out, and the gate on a clean copy (W-052)

Status: built. Decisions: [W-052](../decisions/W-052-guard-dead-end-and-clean-copy-gate.md);
[W-053](../decisions/W-053-guard-rules-configured-per-project.md) (section 9, the guard's rules set per project).

## 0. Why

- **Advice is a way out** (W-052): a refusal names a command the guard lets through, or a step after
  which the refused command passes (`git tag archive/<name> <name>` before `git branch -D`). Advice
  that leaves the answer unchanged is a dead end. `jst discard` (section 1) is the way out for the
  rules that refuse throwing uncommitted changes away.
- **A gate on a clean copy is jackstay's** (section 3), so no session leaves hand-made worktrees and
  logs in the temporary folder.
- **The copy is a clone sharing the repository's objects, not a worktree**: inside Claude Code's
  sandbox `git worktree add` fails (`.git/worktrees` is read-only), and the clone registers nothing.

## 1. `jst discard <path>…`

**What it does.** Returns each named path to its committed state (HEAD) and keeps everything that
throws away in jackstay's trash (`docs/design/guard.md` section 10, W-042), restorable. It decides
**from the disk and HEAD**, not from the index alone (an index-only reading misses a file recreated
after `git rm --cached` and leaves a staged addition on disk):

1. **The paths to put back**: every path at or under a named path that git lists as changed in the
   working copy against the index (`git diff --name-only -z --no-renames`) or in the index against
   HEAD (`git diff --cached --name-only -z --no-renames HEAD`; the empty tree when there is no
   commit), both sides of a rename included (`--no-renames`). Untracked paths that are not in
   either list are not touched (they are not a change git can put back: the summary names them and
   says `jst trash <path>`); ignored files are not looked at.
2. **For each path, before anything is written**:
   - what is on disk there (`Lstat`, whatever the index says: a file, a link, or nothing) is
     **moved** into the trash (`trash.Move`, the same rename or checked copy as `jst trash`) unless
     it is byte for byte HEAD's version: same kind, same executable bit, and the raw bytes of
     `git cat-file blob HEAD:<path>` (no filters: a clean filter may hide what differs) equal to the file's bytes, or a link's target equal to HEAD's link. A path HEAD does
     not have keeps anything on disk. When in doubt it keeps: an extra entry costs nothing;
   - the index's version, when it differs from HEAD's and from what was on disk, is written into a
     second entry (`trash.Keep`: an entry made from bytes and a mode, kind `staged`).
3. **Then the path is written back**: present in HEAD, `git restore --source=HEAD --staged
   --worktree -- <path>`; absent from HEAD, `git rm --cached --quiet --ignore-unmatch -- <path>`
   (what was on disk is already in the trash, so the path is gone as at HEAD). The source commit
   is the HEAD that was read during planning. Before changing a path, discard checks that HEAD,
   the path's index entry and the working file still match the plan. It checks again after keeping
   an entry, before writing the path back. If the file changes as it moves, discard keeps the
   moved version and stops before changing the index. The backup stays held until discard has
   recorded what replaced it, or the user restores it.

The checks narrow the interval in which a concurrent write can be missed. Git and editors do
not share a lock with discard, so concurrent writes between the last check and Git's update
remain possible.

So a staged deletion with the file made again on disk keeps that file; a staged addition (or a
rename's new name) leaves nothing behind and keeps the file; a file with staged and unstaged edits
keeps both versions; a file deleted on disk is written back with nothing to keep.

Every git call runs in the repository's top folder with literal pathspecs
(`--literal-pathspecs`), no hooks (`-c core.hooksPath=/dev/null`), under the shared git runner
(section 4).

**Refused** (nothing changed, exit 2): outside a git work tree; no path; a path outside the work
tree (after `..` and links); a path in a merge conflict (unmerged: "finish the merge, or
`git checkout --ours|--theirs <path>`"); a submodule (a gitlink: "this is another repository");
a path git reports nothing for and that is not untracked (`nothing uncommitted in <path>`,
exit 0). The paths are all checked before any is changed: one refused path means nothing is done.

**Output**: one line per kept item, naming its entry and the `jst trash restore` that brings it
back, then one summary line. Exit 0 every path done; 1 one failed midway (the line says which and
in what state: after the move, `jst trash restore ENTRY` puts the file back); 2 refused before
anything was done.

**Restore.** An entry records, beside `jst trash`'s fields, `via: "discard"` and `replaced`: the
kind (file, link, none), executable bit and SHA-256 of what discard prepared from HEAD, taken
before installing it. `jst trash restore ENTRY` replaces an occupied destination only when it
still matches that identity, else refuses ("is there already … use --to PATH"). It exchanges a
checked copy with the destination, verifies the displaced file, and puts a concurrent change back
where possible, keeping recovered versions otherwise. An empty destination is installed with a
no-replacement rename (a checked copy staged beside it across volumes); a destination that appears
meanwhile is left alone; a failed rename keeps the current file and the entry. A `staged` entry is
restored as a working file, not staged (the line says so).

**Package.** `internal/discard` (`jst discard`, a verb of its own because the guard's advice names
it); `internal/trash` holds `Keep` and the restore rule.

## 2. Advice that is a way out (the guard)

**The rule.** Every command a row's `Instead` names passes the guard in the situation the row
refused, and a step to take "first" or "before" is followed by a command that then passes. The
advice is each row's `Instead` in `internal/guard/rules.go` (gist: [guard.md](guard.md) section 0).
The rows that refuse dropping uncommitted changes (`git-checkout-path`, `git-restore-worktree`,
`git-checkout-index-force`, `git-reset-hard`, `git-read-tree-reset`) name `jst discard <path>`. The
stash is never advised for dropping changes (every worktree shares it and a drop is refused);
`git-checkout-force` and `git-switch-discard` advise `git stash push -u`, then `git switch`. Only the
advice changed: the rows still refuse the raw destructive form.

**The test** (`internal/guard/advice_test.go`, real git) builds a fixture in the refused situation
(an edited and a staged file, an untracked file, a merged branch `done`, a commit held only by a
branch, a bare `origin`, a stash entry, a folder with an ignored file). For every row and every
finding the corpus produces (so composed advice is covered), each back-quoted snippet of `Instead`
is a command or a listed fragment (anything else fails the test); placeholders are filled from the
fixture, and `guard.Decide` must find nothing. A row whose advice says "first" or "before" must have
a scenario: the row refuses, the advised step runs for real (`jst discard` through the package),
then the command it leads to must pass. Scenarios: `git-checkout-path` (also for a folder with an
ignored file), `git-read-tree-reset`, `git-branch-reset`, `git-branch-force-delete`,
`git-clean-force`, `find-delete`.

## 3. `jst test <scope> --commit REV`: the scope on a clean copy

**Flag**: `--commit REV` (any scope; meant for `gate`). `REV` is resolved in the checkout
(`rev-parse --verify REV^{commit}`), so `--commit HEAD` is "this commit, clean"; `jackstay.json` is
read from the copy.

1. Under `<jackstay home>/<project>/clean-copies/<short commit>-<8 random hex>/` (never the system
   temporary folder) jackstay writes `owner` (`jst test <pid> <start-micros>`, the owner words of
   `internal/pinned`) and makes the copy in `<repository folder name>/`: `git init` with the
   source's object format, the repository's absolute object folder in `objects/info/alternates`,
   `git fetch --no-tags --update-head-ok` of its branches, remote-tracking branches and tags, and
   `git checkout --detach <commit>`, hooks off. The copy has **the repository's own refs**
   (`origin/main` is the repository's, not its local `main`) and its remotes' URLs. Nothing is
   written in the repository.
2. **The project's name** is `--project`, else the live checkout's (its normal default included),
   else the nearest `jackstay.json` at REV from the matching subfolder. It is written to the copy's
   `.git/jackstay-project` (`config.CopyMarker`, read first by `config.ProjectName`), so nested
   commands share it. The scope's settings are validated in the committed tree.
3. The scope runs with the copy as its checkout (the same subfolder), so receipts,
   durations and the lock are the project's own; its run lines' `worktree` is the copy and `head`
   the commit.
4. The copy folder is removed on every way out: after the last line, on a timeout, and on a stop
   (SIGINT, SIGTERM, SIGHUP: removed before jackstay ends by the signal), also during its making.
5. A kill cannot be caught: the next `jst test --commit` of the project first removes every copy
   whose `owner` is gone (an unreadable owner counts as alive).

Every command runs afresh, even with a matching receipt. `--commit` is exit 2 (`not-started`) with
an invalid commit, or a copy that cannot be made (Git's words included). The copy has no
submodules and no ignored files: a step that needs them builds them. `internal/cleancopy` makes and
removes it; `internal/pinned` holds what it shares with review's pinned trees (owner words,
whether an owner is alive, the random name).

## 4. One git runner

`jst discard` and the clean copy run git through `internal/gitcli`: its own process group ended at
the deadline, `LC_ALL=C`, the variables that point git elsewhere or change pathspecs or diffs
removed (`GIT_DIR`, `GIT_INDEX_FILE`, `GIT_CONFIG`, `GIT_*_PATHSPECS`, `GIT_EXTERNAL_DIFF`; config
and transport settings such as `GIT_CONFIG_PARAMETERS` and `GIT_SSH_COMMAND` kept for the sandbox's
proxy), hooks and fsmonitor off (`-c core.hooksPath=/dev/null`).

## 5. The AGENTS block

The AGENTS block ([adopt.md](adopt.md) section 8) names `jst discard` for dropping changes and
`jst test gate --commit HEAD` for a gate on a clean copy.

## 6. Tests

- `internal/discard` (real git): edited, staged-and-edited (two entries), new staged, renamed,
  deleted-and-recreated (also ignored), mode-only, link, deleted, folder and untracked paths;
  refused conflicted, submodule and outside paths with nothing changed; restores over HEAD's
  version (replaces) and over a changed file, mode or link (refused); a failure after the move.
- `internal/trash`: `Keep` and the replace rule. `internal/guard`: `advice_test.go` (section 2).
- `internal/testing`: `--commit` sees no checkout edit; resolves `origin/main`, local `main` and
  the commit as the checkout does; keeps the project's name (with and without `project`, a linked
  worktree, `--project`, a nested run); checks out a commit no ref holds; removes the copy after a
  pass, a failure, a timeout and a stop; sweeps a dead owner's copy and keeps a live one; leaves nothing in `TMPDIR`.

## 7. Not done here

A `guard.deny` row's advice is the project's own text and is not walked.

## 9. The guard's rules set per project: `guard.rules` (W-053)

**Why.** Most refusals are `zsh-glob`, a misread pattern that destroys nothing; the rest are the
destructive rules. A project sets how strict each rule is.

**The section.** `jackstay.json`'s `guard` has `rules`, beside `regenerable` and `deny`:

```json
"guard": {
  "regenerable": ["build-cache"],
  "rules": {
    "zsh-glob":     {"level": "refuse"},
    "rm-recursive": {"paths": ["Generated/**", "~/Library/Caches/myapp/**"]},
    "git-push-force": {"level": "warn"}
  }
}
```

- A key is a built-in row's id ([guard.md](guard.md) section 0: `git-reset-hard` … `zsh-glob`). Its value takes
  `level` — `refuse`, `warn` or `off` — and, for a rule about paths, `paths`. An unknown id, an
  unknown key, an unknown level, or `paths` on a rule that is not about paths is a problem `jackstay
  check` reports, and the guard then applies the built-in rules only (`config-unusable`).
- **Defaults**: `zsh-glob` warns; every other row refuses. A row not named keeps its default.
- **Not configurable**: the parser's own refusals (a line it cannot read, a line over the budget,
  an unreadable tool call) are not rows: what the guard cannot read it cannot judge. Nor are the
  rows that say a command **cannot be judged** — `git-config-alias`, `git-config-worktree`,
  `env-split-string`, `card-push-unreadable-config`; naming one in `guard.rules` is a problem. The
  project's `guard.deny` rules are already the project's own.
- **Every row takes a level** ([W-055](../decisions/W-055-every-guard-rule-configurable-now.md)),
  so a loosened row must never hide another. That holds **by construction**: the git judges (`gitjudge.go`'s
  `gitChecks`, `push.go`) are **one check per row**. For each git subcommand a list of checks `{row, check}`; `check(job)`
  reads only the parsed invocation and git's answers, and returns its own row's finding or none
  (the reporting API takes no row id: the list supplies it, so a check cannot report another row).
  The job runs every check of its subcommand; none sees whether another fired. Where one row
  is judged on several subcommands (`git-branch-reset`: `branch -f`/`-C`, `checkout -B`,
  `switch -C`/`--force-create`; `git-checkout-path`: `checkout`'s path forms), it is one check
  listed under each. The judges' shared helpers (`loses`, `sided`, `takesSide`, `movedBranch`'s
  questions, `forced`) stay shared: they compute facts, not verdicts.
  The levels are then applied to the complete list of findings in one place (`applyLevels`,
  called by `Decide`): `off` drops the row's findings, `warn` turns them into warnings.
  - The judges for `rm`, `find` and `zsh-glob` stand alone (one row each).
- **Tests of the split**: (1) every configurable row has a check, and a corpus run reports, per
  check, only its own row (by the API, and asserted); (2) **pairs**: a table of commands where two
  rows meet, each with both findings expected from an independently written expectation —
  `git switch --discard-changes -C topic main` (`git-switch-discard`, `git-branch-reset`),
  `git checkout -f -B topic main` (`git-checkout-force`, `git-branch-reset`), `git checkout -B topic
  main -- file` (`git-branch-reset`, `git-checkout-path`), `git push --force origin :main`
  (`git-push-force`, `git-push-delete-protected`), `git push --force --prune` (`git-push-force`,
  `git-push-prune`), `git reset --hard` + a Card push on one line; then the same command with one
  of the two `off` keeps the other; (3) the property, now meaningful because checks are
  independent: for every row R and every corpus and fuzzed line, findings with R `off` are the
  findings with every row on, less R's.
- **Path exceptions are for the deleting rules only**: `rm-recursive` and `find-delete` take
  `paths` (any other row naming `paths` is a problem). They are matched by the resolution those
  rules already use for `guard.regenerable` — `disposable`: every place the command may run in,
  `cd` and links followed, the lexical and the real path both inside; a target it cannot know is
  never matched. **The written path is the real path** (W-019: `..` through a link, a link the
  line makes, a link above the pattern's folder, a final `/.` entering a link, a pattern character
  the shell expands — each can make the modeled path differ from the one deleted): a target is eligible only when it holds no
  pattern character (`*?[`), no `..`, no link the command line makes lies at or above it, and its
  real path — every link on disk followed, the target's own last part included — is the path as
  written; and **the whole line is that one command** — a plain `rm …` or `find …`, nothing before
  or after it, no wrapper, no substitution, not in the background — run in the session's own folder
  (`cd -P`, `set -P`, a link made earlier on the line or in a subshell can each make the modeled
  folder or path differ from the real one; a line with nothing else on it leaves nothing to model).
  A target that fails is judged as without the exception (refused unless disposable). A pattern
  adds to what is disposable **for that rule only**. `find` with `-L`, `-follow` or `-H` is never
  excepted (it follows links below its start, which the start's resolution does not see). The git rows that overwrite paths
  (`git-checkout-path`, `git-restore-worktree`, …) take a level only: they judge pathspecs relative
  to the folders git runs in, and an exception there would need git's own path resolution
  (cumulative `-C`, magic pathspecs, links) — `jst discard` is the way to drop changes instead.
  - **Patterns.** Relative to the project's top (jackstay.json's folder), or beginning `~/`; `*` matches
    within a name, `**` across folders. The part before the first wildcard (the pattern's fixed
    folder) must lie **strictly below the project's top**, or below `~/` and neither the project's
    top nor above it; it may not be, or be above, a temporary root or the jackstay home, and may not be
    inside `.git`. Any other absolute pattern is a problem. So `Generated/**` and
    `~/Library/Caches/myapp/**` pass; `*`, `**`, `./*`, `~/*`, `~/proj/*` (the top itself) and
    `/repo/*` are problems.
- **What each level does.** `refuse`: the command is refused. `off`: the row finds nothing. `warn`: the row's
  finding becomes a warning — the command runs and the agent is told what the row says
  (`[<id>] <why>. Instead: <instead>.`). In the hook (see below for the field) and in `jst guard
  check` (exit 0, the warning on stderr). The guard's span records it with the outcome `warned`
  and `rules: [<id>]` (the journal), so `jst stats guard` counts warnings as it counts refusals.
- **Where it lives in the code.** The levels in one place, after the reading: `applyLevels` filters
  the complete findings by level. The path exceptions in `disposable`, given the rule it judges for.
  The split: `gitjudge.go` and `push.go` hold the per-subcommand check lists.
- **`-C` is cumulative**: git applies relative `-C`s one after another (`git -C a -C b` runs in
  `a/b`), and so does the guard's reading, with a corpus case. The ids, which take `paths`, and the
  defaults are data in `internal/config`
  (`config.GuardRules`), so `jst check` validates the section without importing the guard; a guard
  test pins that list to the table (same ids, same path rules).
- **Telling the agent.** A warning reaches the agent as `additionalContext` and the person as
  `systemMessage`, with no `permissionDecision`, so a warning never grants a permission
  ([guard.md](guard.md), the hook contract). An `allow` reason would reach only the debug log, and
  a `systemMessage` alone never reaches the agent.

**Tests**: config — every problem above refused (a gate row named), a good section accepted;
guard — the split's tests above; each level for a git row, `rm-recursive`, `find-delete` and
`zsh-glob` (default warn, `refuse` set); `paths` for `rm-recursive` and
`find-delete` (every target matched passes; one outside refuses; an unknown target refuses; after
`cd src` the top's `Generated/**` does not match `src/Generated`; `Generated/link/../work`, a link on disk on
the way or above the pattern's folder, `find Generated/hop/. -delete`, `rm -r Generated/*/work` and
`ln -s hop/../work Generated/link && rm -r Generated/link/data` refuse; `find -L` refuses); `git -C a -C b`; the hook's output for a warning and its log event;
the table/config list test.
