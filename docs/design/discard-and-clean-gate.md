# `jst discard`, advice that is a way out, and the gate on a clean copy (W-052)

Status: built. Decisions: [W-052](../decisions/W-052-guard-dead-end-and-clean-copy-gate.md);
[W-053](../decisions/W-053-guard-rules-configured-per-project.md) (section 9, the guard's rules set per project).

## 0. Why

- **Advice is a way out** (W-052). A refusal names a command the guard lets through that does what
  was meant, or a step after which the refused command passes (`git tag archive/<name> <name>`
  before `git branch -D`: the tag then holds the commits). Advice whose step leaves the guard's
  answer unchanged is a dead end, and the session has to stop and ask. `jst discard` (section 1) is the way out for
  the rules that refuse throwing uncommitted changes away (`git-restore-worktree`,
  `git-checkout-path`, `git-checkout-index-force`, `git-reset-hard`, `git-read-tree-reset`).
- **A gate on a clean copy is jackstay's** (section 3), so a session never makes one by hand and
  leaves registered worktrees and logs in the system's temporary folder behind.
- **The copy is a clone, not a worktree.** Inside Claude Code's sandbox `git worktree add` fails
  (the sandbox keeps `.git/worktrees` read-only except the session's own worktree), and a session
  may run its gate there. A clone that shares the repository's objects
  (`objects/info/alternates`) works in the sandbox, is quick and small, and registers nothing in the
  repository.

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
   (what was on disk is already in the trash, so the path is gone as at HEAD).

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

**Output**, one line per kept item and one summary:

```
jst discard: src/a.go: your edits kept as 20261117-101500-1 (jst trash restore 20261117-101500-1)
jst discard: src/a.go: the staged version kept as 20261117-101500-2 (jst trash restore 20261117-101500-2 --to <path>)
jst discard: 2 paths back to HEAD; 2 kept in jackstay's trash for trash.keep_days (7) days
```

Exit 0 when every path was done; 1 when one failed midway (the line says which and what state it
is in: a failure after the move leaves the file missing from the working copy and its copy in the
trash, so `jst trash restore ENTRY` puts it back); 2 when refused before anything was done.

**Restore.** An entry records, besides `jst trash`'s fields, `via: "discard"` and `replaced`: what
`jst discard` wrote back — its kind (file, link, none), its executable bit, and the SHA-256 of its
content or of the link's target (read with `Lstat`/`Readlink`, never followed). `jst trash restore
ENTRY` puts the item back as now; and when the target exists, it replaces it **only** when the
target's kind, executable bit and SHA-256 all equal `replaced` (nothing changed since the discard,
and what is replaced is HEAD's version, which git still has). Otherwise it refuses ("is
there already … use --to PATH"). A `staged` entry is restored as a working file, not staged again
(the restore line says so).

**Package.** `internal/discard`, registered as `jst discard`; `internal/trash` holds `Keep` and the
restore rule above. `jst discard` is a
command of its own, not `jst trash --discard`, because the guard's advice names it and an agent
looks for the verb it means.

## 2. Advice that is a way out (the guard)

**The rule.** No row's advice leads to a refusal: every command a row's `Instead` names passes the
guard in the situation the row refused, and a step the advice says to take "first" or "before" is
followed by a command that then passes (the refused one, or one it names).

**The rows' advice** (`internal/guard/rules.go`; [guard.md](guard.md) section 4 states the rule):

| row | `Instead` |
|-----|---------------|
| `git-checkout-path` | `` `jst discard <path>` `` puts the path back to HEAD and keeps your changes in jackstay's trash (`jst trash restore <entry>` brings them back); to take files from another commit, `jst discard <path>` first, then `git checkout <commit> -- <path>` (a folder must also hold no untracked or ignored file: `git status --ignored <path>` lists them, `jst trash <path>` moves them); to change branch, `git switch <branch>` |
| `git-restore-worktree` | `` `git restore --staged <path>` `` only unstages; to drop the edits, `jst discard <path>`: it keeps them in jackstay's trash (`jst trash restore <entry>` brings them back) |
| `git-checkout-index-force` | `` `jst discard <path>` `` puts the files back from HEAD and keeps the changes in jackstay's trash; `git checkout-index --prefix=<scratch>/` writes a copy elsewhere |
| `git-reset-hard` | `` `jst discard <path>` `` drops uncommitted changes and keeps them in jackstay's trash; `git reset --keep <commit>` moves the branch and refuses to lose local changes |
| `git-read-tree-reset` | `` `git read-tree -u -m <tree>` `` refuses to lose local changes; drop them first with `jst discard <path>`, then `git read-tree -u -m <tree>` |

The stash is not advised for dropping changes: it is shared by every worktree and a drop is
refused, so a session cannot clean up after it. `git-checkout-force` and `git-switch-discard` advise
"`git stash push -u`, then `git switch <branch>`" (the switch passes; the stash keeps the work).

**Rows still refuse the raw destructive form**: only the advice is a way out; what the judges
refuse is unchanged. The dynamic advice the judges compose (`rm`'s, `zsh-glob`'s, the merge note of
`git-checkout-path`) is checked by the same test (below).

**The test** (`internal/guard/advice_test.go`, an integration test: real git):

1. A fixture repository in the situation the rows refuse: a tracked file edited and another
   staged, an untracked file, a branch `done` merged into `main`, a commit `archived` held only by
   a branch, a remote `origin` (a bare clone), a stash entry.
2. **Every command an advice names passes.** For every row of `table`, and for every finding the
   corpus (`tests/guard/corpus.jsonl`) produces (so the judges' composed texts are covered), each
   back-quoted snippet of `Instead` is classified: a command (its first word is a program the guard
   reads or `jst`) or a fragment (an option, a `Card:` line, a placeholder — an explicit list in the
   test; a snippet that is neither fails the test, so a new row cannot slip by). Placeholders are
   filled from the fixture (`<path>` the edited file, `<branch>`/`<name>` `done`, `<commit>` HEAD,
   `<tree>` `HEAD^{tree}`, `<remote>` origin, `<entry>` an id, `<scratch>` the session scratch, `…`
   `.`), and `guard.Decide` in the fixture must find nothing. The fixture also has a folder with an
   ignored file in it, for the folder form of `git-checkout-path`'s scenario.
3. **Every "first" leads somewhere.** A row whose `Instead` says "first" or "before" must have a
   scenario in the test: the fixture where the row refuses (checked: `Decide` refuses it), the
   advised step run for real, then the command the advice leads to (the refused one, or the one it
   names after "then") — `Decide` must now find nothing. A row with "first"/"before" and no
   scenario fails the test. Scenarios: `git-checkout-path` (discard, then `git checkout HEAD~1 --
   <path>`; and for a folder holding an ignored file: discard, `jst trash` the ignored file, then the
   checkout), `git-read-tree-reset` (discard, then `read-tree -u -m`), `git-branch-reset` and
   `git-branch-force-delete` (the tag, then the branch command), `git-clean-force` and `find-delete`
   (look, then `jst trash`).
4. The test runs `jst discard` itself through the package (not the binary) so step 3 uses the real
   discard.

## 3. `jst test <scope> --commit REV`: the scope on a clean copy

**Flag**: `--commit REV` (any scope; meant for `gate`). `REV` is resolved in the checkout
(`rev-parse --verify REV^{commit}`), so `--commit HEAD` is "this commit, clean". The scope runs as
the commit defines it: jackstay.json is read from the copy.

**The copy** — a **clone sharing the repository's objects**, not a worktree (section 0: a worktree
cannot be made inside the sandbox, and a registered worktree is what hand-made gates leave
behind):

1. Under `<jackstay home>/<project>/clean-copies/<short commit>-<8 random hex>/` (never the system temp
   folder), jackstay writes `owner` (`jst test <pid> <start-micros>`, the same owner words and liveness
   check as `jst review`'s lock reason) and makes the copy in `<repository folder name>/` inside it:
   `git init --quiet`, the repository's object folder as an absolute path (`git rev-parse
   --path-format=absolute --git-path objects`) written to `objects/info/alternates`, then `git fetch --quiet --no-tags --update-head-ok <top>
   '+refs/heads/*:refs/heads/*' '+refs/remotes/*:refs/remotes/*' '+refs/tags/*:refs/tags/*'` and
   `git checkout --quiet --detach <commit>`, hooks off. The copy has **the repository's own refs**
   — its branches, its remote-tracking branches (`origin/main` is the repository's `origin/main`,
   not its local `main`), its tags — and each of its remotes' URLs
   (`remote.<name>.url`, `remote.<name>.fetch`): the same `for-each-ref` as the repository, a
   clean status, a small `.git`. Nothing is written in the repository.
2. **The project's identity.** A name derived again in the copy — from its folder, from
   jackstay.json — would differ from the checkout's whenever the checkout is a linked worktree, has
   no `project` key, or was called with `--project`. So jackstay writes the outer
   call's **effective** project name (jackstay.json's `project`, `--project`, or the name derived in the
   checkout) into the copy's `.git/jackstay-project`, and `config.ProjectName` reads that file **first**
   when the folder's `.git` is a folder holding it. Every `jst` command a step runs inside the copy
   (a nested `jst test`, `jst receipts`, `jst lock`) therefore has the outer call's name, by one rule,
   with no variable to pass through the step's environment. Only jackstay writes the file, and only in a
   copy it made; it is inside `.git`, so the tree stays clean.
3. The scope runs with the copy as its checkout: `root` and the working folder are the copy (the
   same subfolder when jackstay was called below the top), so receipts, marks, bars, durations and the
   lock are the project's own. The run lines' `worktree` is the copy (under `clean-copies/`) and
   their `head` the commit.
4. The copy folder (the copy and `owner`) is removed on every way out: after the scope's last line
   (passed or failed), on a timeout (the scope ends through its normal path), and on a stop
   (SIGINT, SIGTERM, SIGHUP: the folder is removed before jackstay ends itself by that signal — `endBy`
   is wrapped so the removal comes first). A stop that comes while the copy is made ends the call
   with the folder removed.
5. A kill (SIGKILL, a crash, a reboot) cannot be caught: the next `jst test --commit` of the project
   first removes every copy folder whose `owner` names a process that is gone (the same rule as
   `review.RemoveStaleTrees`: an owner that cannot be read counts as alive).

**Refused** (`not-started`, 2): `--commit` with `--resume` (the two are not combined: a gate on a
clean copy of REV is a whole run, and resume is for the checkout a session works in); a `REV` that is not a commit; the copy cannot be made (the line says git's
words).

**Limits:** submodules are not checked out in the copy;
the copy has no ignored files (no `.build`, no `node_modules`): a step that needs them builds
them, which is the point of a clean copy.

**Package.** What `jst review` and the copy share is in `internal/pinned`: the owner words
(`OwnWords(prefix)`, `Alive(prefix, words)`) and the random name; `jst review`'s tree code calls
them. The rest differs by kind (a locked worktree, a folder with an `owner` file), so each keeps
its own sweep. `jst review` keeps its locked worktrees (Codex runs outside the sandbox, so they can
be made). The copy is made and removed by `internal/cleancopy` which `internal/testing/cleancopy.go` calls; the marker's name is `config.CopyMarker`,
and `config.CopyProject` (called first by `config.ProjectName`) reads it.

## 4. One git runner

`jst discard` and the clean copy run git through `internal/gitcli`, which is already "the one place
jackstay runs git" (its own process group ended at the deadline, `LC_ALL=C`, and the variables that point git at another repository or change how it reads a pathspec or prints a diff removed — `GIT_DIR`, `GIT_INDEX_FILE`, `GIT_CONFIG`, `GIT_*_PATHSPECS`, `GIT_EXTERNAL_DIFF` and the like; configuration such as `GIT_CONFIG_PARAMETERS` and transport settings such as `GIT_SSH_COMMAND` are kept, since an agent sandbox's proxy needs them), with
hooks and fsmonitor turned off on the line (`-c core.hooksPath=/dev/null`). `jst review` keeps its
runner (the process model, [implementation.md](implementation.md) section 5.2), which does the same through `internal/supervise`.

## 5. The AGENTS block

The AGENTS block (`internal/adopt/block`) says:

- under the guard: "to drop uncommitted changes, `jst discard <path>…` (they are kept in jackstay's
  trash: `jst trash restore <entry>`), never `git restore`/`git checkout --`, which the guard
  refuses";
- under tests: "`jst test gate --commit HEAD` runs the gate on a clean copy of a commit, made and
  removed by jackstay — never by hand with `git worktree add` or `mktemp`".

## 6. Tests

- `internal/discard`: an edited file, a staged and edited file (two entries), a new staged file
  (gone from disk and index, kept), a staged rename (both names), a staged deletion with the file
  made again on disk (kept) and the same with an ignored file, a mode-only change, a link, a file
  deleted in the working copy, a folder with several changes, an untracked file (named, not
  touched), a conflicted path and a submodule (refused, nothing changed), a path outside the tree,
  outside a work tree, a restore over HEAD's version (replaces), over a changed file, a changed mode and a link
  (refused), a failure after the move (the entry restores it). Integration (real git).
- `internal/trash`: `Keep`, the replace rule.
- `internal/guard`: `advice_test.go` (section 2: every advised command is judged by the guard and must pass).
- `internal/testing`: `--commit` runs the scope in a copy at that commit (a file edited in the
  checkout is not seen; `origin/main`, local `main` and the commit at three different tips resolve
  in the copy as in the checkout; the same name inside the copy as in the checkout for a
  project with a `project` key, one without it, a linked worktree of another folder name, and a
  call with `--project`, for a nested `jst test` too; a commit reachable from no ref checks out
  through the alternate; a lossy clean filter case for discard), removes it after a pass, a failure, a timeout and a stop (a fake signal
  through the existing `listen`/`endBy` seams), sweeps a copy whose owner is gone and leaves a
  live one, refuses `--resume`, leaves nothing in `TMPDIR`.

## 7. Not done here

- A jackstay.json `guard.deny` row's advice is the project's own text and is not walked.
- Making `git reset --hard` pass on a clean tree (an exception the row lacks) — the advice no
  longer needs it.

## 9. The guard's rules set per project: `guard.rules` (W-053)

**Why.** Most refusals in practice are `zsh-glob`, a misread pattern that destroys nothing
(measured); the rest are the
destructive rules. A project needs to set how strict each rule is.

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

- A key is a built-in row's id (section 2's table: `git-reset-hard` … `zsh-glob`). Its value takes
  `level` — `refuse`, `warn` or `off` — and, for a rule about paths, `paths`. An unknown id, an
  unknown key, an unknown level, or `paths` on a rule that is not about paths is a problem `jackstay
  check` reports, and the guard then applies the built-in rules only, as it does for any unusable
  `guard` section (`config-unusable` warning).
- **Defaults**: `zsh-glob` warns; every other row refuses. A row not named keeps its default.
- **Not configurable**: the parser's own refusals (a line it cannot read, a line over the budget,
  an unreadable tool call) are not rows: what the guard cannot read it cannot judge. Nor are the
  rows that say a command **cannot be judged** — `git-config-alias`, `git-config-worktree`,
  `env-split-string`, `card-push-unreadable-config`; naming one in `guard.rules` is a problem. The
  project's `guard.deny` rules are already the project's own.
- **Every row takes a level** ([W-055](../decisions/W-055-every-guard-rule-configurable-now.md)).
  A git judge that stopped at its first row would let a loosened row hide another (a
  `--discard-changes` taken over `-C`), and no property over the corpus can see a hidden row, since
  it is in neither list. The class is removed **by construction**: the git judges (`gitjudge.go`'s
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
- **Telling the agent.** The hook answers a warned command with `hookSpecificOutput:
  {hookEventName: "PreToolUse", additionalContext: "<the warning>"}` and **no**
  `permissionDecision`: Claude Code's hooks reference (code.claude.com/docs/en/hooks, read
  2026-11-17) says `additionalContext` passes the text into Claude's context on PreToolUse, while
  an `allow` reason goes to the debug log only and `systemMessage` is shown to the user only. With
  no decision the call goes through the user's own permission flow unchanged: a warning never
  grants a permission. The guard's other warnings (`config-unusable`, a NUL byte, …) use the same
  field (a `systemMessage` alone would never reach the agent), with `systemMessage` kept for the
  user. A refusal is unchanged (`deny` with its reason).
- The `zsh-glob` warning by default needs no jackstay.json change.
- The AGENTS block says: a guard warning is the guard's word that the command may not do what it
  says — read it; a project's own rule levels are in its jackstay.json.

**Tests**: config — every problem above refused (a gate row named), a good section accepted;
guard — the split's tests above; each level for a git row, `rm-recursive`, `find-delete` and
`zsh-glob` (default warn, `refuse` set); `paths` for `rm-recursive` and
`find-delete` (every target matched passes; one outside refuses; an unknown target refuses; after
`cd src` the top's `Generated/**` does not match `src/Generated`; `Generated/link/../work`, a link on disk on
the way or above the pattern's folder, `find Generated/hop/. -delete`, `rm -r Generated/*/work` and
`ln -s hop/../work Generated/link && rm -r Generated/link/data` refuse; `find -L` refuses); `git -C a -C b`; the hook's output for a warning and its log event;
the table/config list test.
