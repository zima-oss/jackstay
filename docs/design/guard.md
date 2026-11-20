# The guard

The guard is jackstay's `PreToolUse` hook (`jst guard`, run as `jst-guard`): it reads each Bash
call an agent makes and refuses the destructive ones, naming what to run instead. How to live with
it is the [guard guide](../guide/guard.md). Per-project levels and path exceptions (`guard.rules`)
and the rule that advice never leads to a refusal are
[discard-and-clean-gate.md](discard-and-clean-gate.md) sections 2 and 9. Rulings:
[W-007](../decisions/W-007-tools-from-the-usage-study.md) (zsh's patterns),
[W-017](../decisions/W-017-review-fixes-and-threat-model.md) (the guard is against accidents, not
a security boundary), [W-019](../decisions/W-019-repeated-findings-mean-redesign.md) (a finding
that repeats means a redesign), [W-036](../decisions/W-036-go-native-not-python-mimicry.md) (the
guard reads the line with a shell parser and refuses what it cannot read).

## 0. What the guard does

### The hook contract

A Claude Code `PreToolUse` hook on the Bash tool (the contract of the hooks reference at
code.claude.com/docs/en/hooks): the tool call arrives as JSON on standard input
(`tool_name`, `tool_input.command`, `cwd`, `scratchpad_dir`, `session_id`, `agent_type`); a refusal
is `{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny",
"permissionDecisionReason": "…"}}` on standard output with exit 0, and the reason goes to the
agent. A warning that lets the command run goes to the person as `systemMessage` and to the agent
as `additionalContext`, with no `permissionDecision`, so the call goes through the person's own
permission flow unchanged. The projects' settings name the hook as `"$HOME/.local/bin/jst-guard"`
with a timeout of 10 s; `jst-guard` is `jst guard` by argv[0] (`implementation.md` section 7.1). The hook also
fires for subagents' Bash calls and in worktrees. A call whose `tool_name` is not `Bash` is not
checked and is recorded as `ignored_tool`. Every call that carries a `session_id` touches
`~/.jackstay/<project>/guard-seen`; a payload piped in by hand has none and leaves no mark, so a
probe does not make `jst doctor` call a hook alive. The hook sees only the `Bash` tool: a shell
command run by another tool (Claude Code's Monitor) is not checked. `jst guard check [--cwd DIR] --
COMMAND` judges one command the same way from a terminal: exit 0 allowed, 2 refused, the reason on
standard error; 64 when its own words are wrong (no `--` before COMMAND, or more than one COMMAND),
and so is `jst guard` given any other word.

Each decision is recorded on the guard's command span in the project's journal
([observability.md](observability.md) section 2): the outcome (`allowed`, `refused`, `warned`,
`failed_open`, `ignored_tool`), the rule ids, the warning ids, and why the project file could not
be used when it could not.

### What it refuses, and what it says to do instead

Each row is a rule id of the table (section 4). "Safe" lists what the rule lets through; the last
column is the gist of its `Instead`.

| Rule | Refused | Safe | The refusal names |
|---|---|---|---|
| `git-reset-hard` | `git reset --hard` | `--soft`, `--mixed`, `--keep`, `--merge`, a path reset | `jst discard <path>` (the edits kept in jackstay's trash); `git reset --keep <commit>` |
| `git-checkout-path` | `git checkout -- <path>`, `git checkout <tree> <path>`, `git checkout .`, `-p`, `--pathspec-from-file`, `--ignore-skip-worktree-bits` | `-b`, `--detach`, `--orphan`; a single name that is not a path on disk; a name that is also a path when a local branch of that name exists (asked of git); a path under which git reports nothing uncommitted (and, when another commit's files are written, nothing untracked or ignored): there is nothing to lose; taking a side, below | `jst discard <path>`; to take files from another commit, `jst discard` first, then `git checkout <commit> -- <path>`; to change branch, `git switch` |
| `git-checkout-force` | `git checkout -f` / `--force` | without `-f` | `git stash push -u`, then `git switch <branch>` |
| `git-switch-discard` | `git switch --discard-changes` / `-f` | plain `git switch` | `git stash push -u`, then `git switch <branch>` |
| `git-restore-worktree` | `git restore` unless only `--staged` | `--staged` without `--worktree`; a path under which git reports nothing uncommitted, as for `checkout` (not when a path there is marked skip-worktree or assume-unchanged, or is a submodule; `--ignore-skip-worktree-bits` is always refused); taking a side, below | `git restore --staged <path>`; `jst discard <path>` |
| `git-clean-force` | `git clean -f`, or a bare `git clean` with `-c clean.requireForce=false` | with `-n` / `--dry-run` | `git clean -n`, then `jst trash <path>`; or `git stash push -u` |
| `git-rm-force` | `git rm -f` | without `-f` (git itself refuses to lose changes); `--cached`; `-n` | `git rm`; `git rm --cached` |
| `git-push-force` | `git push --force`, `-f`, `+refspec`, `--mirror` | `--force-with-lease` (without `--force` beside it) | `--force-with-lease --force-if-includes` with a plain refspec |
| `git-push-prune` | `git push --prune` | with `--dry-run` | `--prune --dry-run`; `git push <remote> --delete <name>` for one branch |
| `git-push-delete-protected` | `git push --delete` / `:branch` of `main` or `master` | any other branch | nothing: `main` and `master` are not deleted from a session |
| `git-branch-force-delete` | `git branch -D` (or `-d --force`) | when another branch, tag or remote ref still holds the branch's commits; when the branch adds nothing to the default branch (below); `-D -r` | `jst tidy --branch <name>` (section 8.4 of `implementation.md`); `git branch -d`; `git fetch` first after a squash merge; `git tag archive/<name> <name>` first |
| `git-branch-reset` | `git checkout -B`, `git switch -C`, `git branch -f`, `-M`/`-C` over an existing branch, `git update-ref refs/heads/<name>` | when the branch does not exist yet, or another ref holds its commits | `git tag archive/<name> <name>` first; `-b` / `-c` |
| `git-stash-drop`, `git-stash-clear` | `git stash drop`, `git stash clear` | `apply`, `pop`, `list`, `branch` | `git stash apply`; `git stash branch <name>` |
| `git-worktree-force` | `git worktree remove --force` | when git reports nothing uncommitted and nothing untracked in that worktree | `jst tidy --worktree <path>`; `git worktree remove <path>` without `--force` |
| `git-update-ref-delete` | `git update-ref -d` / `--delete` / `--stdin` | other `update-ref` forms | `git branch -d`; `git tag -d` |
| `git-update-ref-move` | `git update-ref` of a ref other than a branch, a remote-tracking ref or a new tag | a branch (judged by `git-branch-reset`), `refs/remotes/…`, a tag that does not exist yet | `git branch -f` (refused when that loses commits); `git tag -f` |
| `git-filter-branch`, `git-filter-repo` | `git filter-branch`, `git filter-repo` (also `git-filter-repo`) | `filter-repo --analyze` / `--dry-run` only | the owner runs the rewrite in their own terminal |
| `git-read-tree-reset` | `git read-tree -u --reset` | `git read-tree -u -m` | `jst discard <path>`, then `git read-tree -u -m <tree>` |
| `git-checkout-index-force` | `git checkout-index -f` | with every `--prefix` in a disposable folder | `jst discard <path>`; `--prefix=<scratch>/` |
| `git-config-alias` | git configuration given on the line or in the environment that the guard cannot read: an alias, an include, a config file (section 5) | other keys (`core.quotePath`, `color.ui`) | the git command itself, written out |
| `git-config-worktree` | `git config core.worktree …`, `core.bare`, or a key not known | reading and unsetting | clone into the folder to work in |
| `cards-push` | a `git push` that adds commits to `cards.branch` without valid `Card:` lines (`implementation.md` section 4.1) | commits that carry them | reword them; `jst lint range origin/<branch>..HEAD` |
| `card-push-unreadable-config` | a `git push` that may move a remote branch while the project declares `cards` and the section cannot be used | | mend the `cards` section (`jst check`) |
| `rm-recursive` | `rm -r` / `-R` / `--recursive` of anything outside temporary and regenerable folders | what is under the session's own scratch folder; a folder `mktemp` made in the same command; a path with a regenerable component. Not elsewhere under `/tmp`, `/var/tmp` or `$TMPDIR`: they are every session's, and a git checkout there (also the session's scratch, when its work tree is elsewhere) is another's | `jst trash <path>` (section 10) |
| `find-delete` | `find … -delete` | when it starts in a folder that could be removed whole, without `-L`/`-H`/`-follow`; `find -name <regenerable> -delete` (below) | `find … -print` first, then `jst trash` |
| `env-split-string` | `env -S STRING` it cannot split, or with `${VAR}` not known (section 2a) | a STRING it splits | the command itself, without `env -S` |
| `zsh-glob` | in zsh (the account's shell, or `zsh -c`), an unquoted `*`, `?` or `[` in an option (`--include=*.swift`), in the pattern a program is given (`find`'s `-name`/`-path`…, `grep`'s `--include`/`--exclude`/`--exclude-dir`, `rg`'s `-g`/`--glob`), or in a word that matches no file where the command runs | quoted or escaped; behind `noglob`; inside `[[ ]]`; in `bash -c`/`sh -c`; glob qualifiers (`*(N)`); brace expansion (`{a,b}/*.x`) unless one of the patterns it makes matches no file; anything after `setopt`/`unsetopt`/`emulate`/`set` in the line; a word with a variable in it; a `**` pattern; where the folder is not known | the same command with the word quoted, given in the refusal |
| `project:<id>` | a project's `guard.deny` rules | the rule's `unless` | the rule's `instead` |

Every row's level is `refuse` by default and can be set per project to `warn` or `off` in
`guard.rules`, with path exceptions for `rm-recursive` and `find-delete`
([discard-and-clean-gate.md](discard-and-clean-gate.md) section 9); `zsh-glob` is `warn` by
default, and `git-config-alias`, `git-config-worktree`, `env-split-string` and
`card-push-unreadable-config` cannot be changed. zsh aborts an unquoted pattern that matches
nothing (`no matches found`), so a `zsh-glob` refusal ends "quote the pattern and run the command
again", not "ask the owner".

- **Taking a side** (`--ours`, `--theirs`, `-m`, `--merge` of `git checkout` / `git restore`) is
  allowed only while a merge with conflicts is under way and every edited path under the pathspec
  is in conflict (on any other path the same words throw edits away). When git cannot say, or a
  path is known only at run time, it is refused.
- **A branch that adds nothing** may be deleted with `-D`: merging it into the default branch
  (`origin/HEAD`, else `origin/main`, `origin/master`, else `main`, `master`; never the branch
  itself) would change no file, as after a squash merge. A branch whose files equal the merge
  base's (work done and undone) is "no". Git answers with `git merge-tree --write-tree` in memory,
  its objects in a throwaway folder; a conflict, a missing ref or an older git is "no". `jst tidy`
  uses the same test.
- **A throwaway checkout** (under a temporary folder of the session's own that the command went
  to: a `mktemp` clone, a repository in the scratch folder; never the session's own folder) is
  exempt from the `reset`, `checkout`, `switch`, `restore`, `clean`, `rm`, `read-tree` and
  `checkout-index` rules. Its branches, stash and pushes are not.
- **Regenerable names**: `.build`, `DerivedData`, `node_modules`, `__pycache__`, `.pytest_cache`,
  `.mypy_cache`, `.ruff_cache`, `.tox`, `.venv`, `venv`, `htmlcov`, `*.egg-info`, and the
  project's `guard.regenerable` (one folder name each, counted only inside its checkout; `.swiftpm`
  is not built in). The plain spellings of "everything" make `jackstay.json` unusable
  (`implementation.md` section 3.2); another entry that matches every name, or holds a slash, is
  passed over and said (`regenerable-ignored`).
- **`find … -name <regenerable> -exec rm -rf {} +`** (or `-delete`) is allowed when every `-name`
  is a regenerable name and the expression has no `-o`, `!` or parentheses. Any other recursive
  `rm` that `find -exec` or `xargs` runs is refused: its path cannot be named.
- **Where a path really leads** decides: links on disk are followed, and so are links the command
  itself made with `ln -s`. A temporary folder itself, or anything above one, is never disposable.

A path known only when the command runs (`rm -rf "$DIR"` with `DIR` read from a file, a relative
path after `cd "$X"`, a brace list, an unquoted word a shell would split) is refused, not guessed.
One form passes where the place is unknown: a regenerable name written as the **last** name of a
quoted path that is not entered (`"$ROOT/.build"`), which `rm` removes without going through a
link; `.build/`, `.build/.` and `.build/*` need a known place. Git's abbreviated long options
(`--har`) count as the option (section 5). Every refusal but `zsh-glob` ends by saying the owner can
run the exact command in their own terminal and the command must not be rephrased around the
guard.

### How a command line is read

As a shell reads it, not as text: the line is parsed into a tree and walked (sections 2 and 3).
Arguments that are data (a `grep` pattern, a commit message, an `echo`, a comment, a here-document
body) are not judged; what a shell would run is (`$(…)`, `bash -c`, `eval`, `trap`, `xargs`,
`find -exec`, `git submodule foreach`, a script piped to a shell). Known variables are put in
(`F=--hard; git reset $F` is `git reset --hard`); a dashed word with an unknown expansion (`-$F`)
is read as the dangerous option. Only what holds on every path is known, and unknown is never
disposable. zsh's `=rm`, GNU tools under Homebrew names (`grm`, `gfind`) and `busybox`/`toybox
<applet>` are read as the tool. The one place where data can be refused is the fallback scan of a
script piped from a producer the guard cannot model (section 2, warning `producer-scanned`).

### Failure behaviour

Input the guard cannot read is refused; evidence it cannot get (git does not answer) makes the
rule that needed it refuse; only a fault of its own code fails open, because a guard that blocked
on its own bug would stop every session at once. Every outcome is the table of section 7; beside
it, an unusable `jackstay.json` turns off the project's `deny` rules, regenerable names and levels
(`config-unusable` on every answer), one bad entry is passed over and said (`regenerable-ignored`,
`project-rule-unreadable`, `guard-exception-not-applied`), and a binary that cannot start ends 126
or 127, which Claude Code shows and runs the command (`jst doctor` reports it, `implementation.md`
section 7.7). `failed_open` must stay at zero; `jst doctor` reports each project's 7-day count.

### What it cannot see

A command inside a script file, a recipe or a git alias from a config file; what a sourced file
defines; a command or subcommand that is itself an unknown variable (`$CMD -rf x`); an unknown
whole-word variable among options (taken as a name); deletion by other means (`python -c`,
`rsync --delete`, `mv`); `$(…)` inside an unquoted here-document; a file that changes between the
guard's question to git and the command; a destructive form not in the table; tools other than
Bash; a project whose hooks are not trusted yet; the owner's own terminal.

It lowers the rate of accidents and is not a security boundary
([W-017](../decisions/W-017-review-fixes-and-threat-model.md)): the cheap bypasses (`env -S`,
`git -c alias.*`, `-c clean.requireForce=false`, GNU and busybox names, `git submodule foreach`,
`$(echo <constant>)`) are closed; options through `"$@"`, function parameters, arrays,
`printf -v` or `read`, a partial or escaped constant `echo`, a config-file alias the session's
environment names, git settings that run commands (`core.fsmonitor`, `core.hooksPath`,
`core.sshCommand`, `diff.external`, `filter.*`) given by `-c` or `GIT_CONFIG_COUNT`, and records
forged in `~/.jackstay` are known limits.

### Codex

Codex CLI (0.162.0, checked by running the hook) has the same `PreToolUse` hook with matcher `Bash`,
input and `deny` answer, so the same `jst-guard` serves it from `.codex/hooks.json`. Codex loads it
only when the project's `.codex/` layer is trusted **and** a person approved the hook in `/hooks`;
the approval is tied to the hook's text (`trusted_hash` in `~/.codex/config.toml`), so an edit needs
approval again. Until then Codex runs unguarded and silent (`codex exec` too), which `jst doctor`
notices as an expected project never heard from. In a linked worktree Codex reads the **main
checkout's** `.codex/hooks.json`, so it is guarded only while the main checkout's branch carries it.
Codex's experimental exec-policy rules do not see inside `bash -lc "…"` and are not used. Setup:
the [Codex guide](../guide/codex.md).

## 1. Shape

`internal/guard`: `hook.go` (the hook and `jst guard check`, fail-open), `parse.go` (`Decide`, the
parse), `walk.go`, `state.go` and `words.go` (the reader, section 3), `invoke.go` (wrappers,
inner scripts, producers), `gitopts.go` (section 5), `gitjudge.go` and `push.go` (the git rows,
the Card push rule), `rules.go` (the table; `rm`, `find`, `ln`, zsh), `project.go` (`guard.deny`,
regenerable names, the fallback scan), `paths.go` and `gitq.go` (section 6), `telemetry.go`, and
`export.go` (`Words`, for `jst baseline`). Beside it: `internal/cards` (the `Card:` rules, shared
with `jst lint`), `internal/procs` (the session note, `implementation.md` section 5.1),
`internal/trash` (section 10).

## 2. Reading a line: a parser, and refusing what it cannot read

- **mvdan.cc/sh/v3/syntax** (the version: `implementation.md` section 10) parses the line into a
  tree, which the guard walks; it has no lexer of its own.
- **A NUL byte** (which the parser drops) makes the line read twice, with every NUL removed and
  with each a space (warning `nul-byte`); it is refused when either reading is.
- A here-document left open runs to the end of the text, as bash reads it (with a warning): the
  parser is given the delimiter at the end. A line that does not parse as written is tried once
  more with `((` read as two subshells (`((cmd) && (x))`, which bash takes so when it is not
  arithmetic).
- The parser takes `f() { …; } && f a` as a function whose body is `{ …; } && f a`; bash and zsh
  define `f` and then run `f a`, so the walker reads it so.
- The line is parsed as **bash**; if that fails, as **zsh** (the parser's experimental zsh
  variant: `${(f)…}` flags, `for x (…)`). **If both fail, the command is refused** ("it could not
  be read as shell; write it plainly, one command per line") — not passed, and not searched for
  words ([W-036](../decisions/W-036-go-native-not-python-mimicry.md),
  [W-039](../decisions/W-039-unparsable-command-refused.md)).
- **zsh's own checks**, which no parser does because they are about the shell at run time, stay
  jackstay's code, on the tree's literal parts with their source positions: an unquoted pattern zsh
  would abort on (`--include=*.swift`, `find -name *.x`, a pattern that matches nothing where
  the command runs: W-007), `=rm`, `noglob`, `nocorrect`, `setopt`/`set -f` turning the check
  off (and `set` given a word only known when it runs, which may be `-f`). A pattern is matched
  as zsh matches it: `*` not against a hidden name, `*/` against folders only, a path with `..`
  walked as written (through a folder that is gone it leads nowhere). Which shell executes an
  invocation decides whether they apply (below).
- **Where a script comes from, which shell reads it, and what state flows** — one table, the
  contract of the walker (section 3):

  | source | read as | state in | state back |
  |--------|---------|----------|------------|
  | `eval TEXT` (text known) | this shell | all of it | all of it |
  | `eval` of text not known | — | — | nothing known after it (variables, folder) |
  | `source FILE` / `. FILE` | not read (a file) | — | nothing known after it |
  | `source <(PRODUCER)`, `. <(…)` | this shell, when PRODUCER is a constant `echo`/`printf`/`cat <<` | all | all |
  | `$(…)`, `` `…` ``, `<(…)`, `>(…)` | a subshell | all | nothing |
  | `( … )`, a pipeline's part other than the last, `&`, `coproc` | a subshell | all | nothing |
  | a pipeline's last part | this shell in zsh, a subshell in bash: either | all | the join of the state before the pipeline and the part's end (`printf src \| read D` makes D unknown) |
  | `trap TEXT SIG` | this shell, when the signal comes: TEXT is judged with the **join of every state from the `trap` to the end of the line** (it may fire at any point after it, EXIT at the end), never only the state at registration | that join | what TEXT writes (variables, the folder) is unknown from the `trap` on |
  | `bash -c`, `sh -c`, `dash -c`, `ksh -c`, `zsh -c`, `script -c`, `watch`, `parallel CMD` | a new shell of the named kind | exported variables only (the ones exported on every path known, the rest unknown); the command's own `A=1` prefixes | nothing |
  | a here-document or here-string fed to a shell, `echo`/`printf`/`cat <<` piped to a shell or to `parallel` without a command (each line it reads is a command), `bash <(PRODUCER)` | a new shell | as for `bash -c` | nothing |
  | `xargs CMD`, `find -exec CMD`, `git submodule foreach TEXT` | CMD as an invocation (TEXT as a new shell) | as for `bash -c` | nothing |
  | `env -S STRING` | env's split-string language, section 2a | as for a wrapper | nothing |

  A quoted here-document (`<<'EOF'`) is the body as written; an unquoted one is expanded in the
  parent (variables put in when known; a `$(…)` inside it is a subshell of the parent).
- **A producer the guard cannot model** (`curl … | sh`, `zsh <(sed … <<< '…')`, `… | parallel`,
  `bash -c "$X"` with X unknown) may turn any text into anything, so the **fallback scan** applies:
  every row's destructive form (its `Words`, section 4) is searched as words in the **whole raw
  text of the tool call**, since what the producer reads may come from anywhere earlier in the
  line. A form found refuses the receiving shell with that row's id and the warning
  `producer-scanned`. A text that parses is also judged as a shell line, and `guard.deny` patterns
  are matched against the whole raw text too. This is the one place where data can be refused.
- **Wrappers that run another process** (`sudo`, `env`, `nohup`, `timeout`, `xargs`, …) give
  nothing back: `sudo cd x` or `env A=1 cd x` changes neither the folder nor a variable. Only the
  shell's own (`command`, `builtin`, `exec`, `noglob`, `nocorrect`, `time`) act in this shell. Nesting is bounded at 8 levels; deeper is refused.
- **zsh's rules apply per invocation, to the shell that executes it**: the session's shell for the
  line (zsh when `$SHELL` is zsh), `zsh -c` children always, `bash -c`/`sh -c` children never; the
  parser variant that accepted the text does not decide it.

### 2a. Wrappers and their argument grammars

Each wrapper the guard sees through (`sudo`, `doas`, `env`, `nohup`, `nice`, `timeout`,
`gtimeout`, `caffeinate`, `taskpolicy`, `stdbuf`, `time`, `command`, `builtin`, `exec`,
`noglob`, `nocorrect`, `xcrun`, `arch`, `script`, `watch`, `parallel`, `xargs`, `busybox`,
`toybox`, GNU `g`-names) has a declared grammar: which options take a value (attached or
separate), which positional it consumes (`timeout`'s duration), and which option hands over
text. `env -uSX cmd` consumes `SX` as `-u`'s value and finds no `-S`; `env -S STRING`,
`-iS…`, `--split-string=…` take STRING in **env's own split-string language** (whitespace, `\_`
as a space, single and double quotes, `\c`, `${VAR}` expanded by env itself): split by that
language into an invocation and judged as one; a STRING it cannot split, or one with `${…}`
whose value is not known, is refused (`env-split-string`).

## 3. The reader: what is known on every path

What section 0 says is known, as one walker over the tree:

- **State**: the folders the command may be in (a set of up to 6, or unknown), variables (a text,
  one of up to 8 texts, or unknown), what is exported (and on every path), links the command
  made, functions defined, and `set -e` (only in a shell process of its own).
- Every command leaves two states, the one where it **succeeded** and the one where it **failed**,
  both after its own effects (`read D` makes D unknown in both). `a && b || c` reads `b` from
  `a`'s succeeded state and `c` from the join of `a`'s failed and `b`'s failed states; `if C;
  then T; elif C2; then T2; else E; fi` reads `T` from `C`'s succeeded state, `C2` from `C`'s
  failed one, `E` from the last condition's failed state, and joins the branches' ends (a
  missing `else` contributes the last condition's failed state); `while C; do B; done` reads `B`
  from `C`'s succeeded state and leaves the loop in `C`'s failed one (`until` the other way
  round); `case` reads each branch from the state after its word's expansion and joins
  (`D=.build; if read D; then rm -rf "$D"; fi` reads the `rm` with D unknown); loops are below;
  a subshell, a pipeline's parts but the last, `&` and `$(…)` give nothing back;
  `eval` and `source` share the state; after an unread `source` or `eval` nothing is known.
- `cd X && …`, `cd X || exit`, `if cd X` make X certain; `cd X; …` leaves both places.
- Word values are read by jackstay's own reader (`words.go`), which keeps what `expand` does not:
  which segment stood in quotes, which characters are an unquoted pattern, one reading per value a
  variable may hold. Command substitution is not run: its value is known only for a constant
  `echo`/`printf`/`cat <<` (W-017), and `$(mktemp …)` stands for a fresh temporary folder.
- **What makes a variable unknown**: `read`, `mapfile`/`readarray`, `getopts`, `printf -v`,
  `unset` (then not set), `+=`, arithmetic (`(( ))`, `$(( ))`, `let`, `for (( ))`), `local`/
  `declare` without a value inside a function (and the caller's value back at its return), a
  variable set on only some paths, and anything after an unread `eval`/`source`.
- **Loops**: the body is read from the state at the loop's top; then again from the join of that
  and the body's end, until nothing changes; if it still changes after 6 passes, every variable
  the body writes and the folder are made unknown **and the body is read once more from that
  widened state**, so a command reached only on a later round is judged with what it could hold.
- **`set -e`** holds only in a shell process of its own (`bash -c 'set -e; …'`, `bash -e`), not
  on the tool's own line (which runs inside `eval … &&`), and never inside a condition (`if`,
  `while`, `&&`/`||` left side, `!`).
- **Bounds**, each refusing (`too-long`) rather than reading on, so no input can hold the hook:
  the command text at most 1 MiB (checked before parsing; the parser's work is linear in it);
  nesting of scripts at most 8 levels (`too-deep`); at most 20,000 simple commands read per call,
  loop passes and function calls included; a word's value at most 64 readings (more is
  unknown) and at most 64 KiB each (longer is unknown); the whole reading under a deadline of
  2 s. Git's questions (section 6) have their own budget: each at most 1 s, all of a call at most
  3 s; a question past its budget is unanswered (the rule needing it refuses, section 7). The
  limits are constants in `walk.go`, each with a test that reaches it.

## 4. Rules as data

Each built-in rule is a row of one table; the walker hands each invocation (program, git
subcommand, parsed options, operands, where it runs) to the rows that name its program:

```go
type Rule struct {
    ID      string     // "git-reset-hard"
    Program string     // "git reset", "rm", "find", …
    Words   [][]string // the destructive form as words, for the fallback scan (section 2)
    Unless  []string   // a word that makes the scanned form safe ("--dry-run")
    Why     string
    Instead string
}
```

When a row applies and what makes a command safe are named checks, one per row: for git, a map
from each subcommand to its rows' checks over the parsed record (`gitjudge.go`, `push.go`: the
throwaway checkout, nothing uncommitted under the paths, the branch held elsewhere, adds nothing
to the default branch, a dry run, only staged, in conflict); for `rm`, `find`, `ln`, `env -S` and
zsh's patterns, `rules.go`. The table has the 29 rows of section 0. The project's `guard.deny`
rules (environment, command and `unless` patterns, RE2) apply after the built-in ones, as
`project:<id>`. A refusal names each row's `Why` and `Instead`. **No row's advice leads to a
refusal** ([W-052](../decisions/W-052-guard-dead-end-and-clean-copy-gate.md); the test:
[discard-and-clean-gate.md](discard-and-clean-gate.md) section 2). Levels and path exceptions:
the same document, section 9.

## 5. One model of git's options

`gitopts.go` describes git's global options and, per subcommand the guard judges, its options:
long name, short letter, whether it takes a value, its negation. One parser applies it: long
options taken by any unambiguous prefix as git takes them (`--har`), short clusters (`-fdx`), a
value attached or separate, `--` ending options, `-c key=val` and `--config-env` as config
given on the line, `-C dir` as where it runs. The result is a record (flags with their last
setting, values, operands, the config given, the place) that rules query. A word that starts
with `-` and holds an unknown expansion is an unknown option, read as the dangerous one.

**What else changes what git does, and where.** The record also takes the environment the
invocation runs with (its own prefixes, `env A=…`, what the line exported, the session's):
- `GIT_DIR`, `GIT_WORK_TREE`, `GIT_COMMON_DIR`, `--git-dir`, `--work-tree`, `-c core.worktree`,
  `-c core.bare`: the repository is elsewhere — the exceptions that need a known checkout (a
  throwaway clone, "nothing uncommitted") do not apply, and the Card push rule refuses unless the
  named repository's project is known;
- configuration given as **entries** — `-c key=val`, `--config-env key=VAR`,
  `GIT_CONFIG_PARAMETERS`, `GIT_CONFIG_COUNT` with `GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n`: read
  entry by entry. Refused (`git-config-alias`): a count that is not a known decimal number (an
  empty `GIT_CONFIG_COUNT=` is git's "none" and is read as 0); a count with any of its
  `KEY_n`/`VALUE_n` (n below the count) not set or not known; a key not known; a key that defines
  an alias (`alias.*`, any case) or includes a file (`include.path`, `includeIf.*.path`);
  `core.worktree`, `core.bare` (the repository moves: as above). `clean.requireForce=false` makes
  a bare `git clean` forced; keys that steer a push (`remote.*`, `push.*`, `branch.*`, `url.*`)
  make the Card push rule refuse. Any other key changes nothing the guard reads and passes, its
  value known or not (`core.quotePath`, `color.ui`, `-c "http.extraHeader=$H"`);
- configuration given as **files** — `GIT_CONFIG`, `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_SYSTEM`: a
  file the guard does not read may hold any alias or include, so a value other than `/dev/null`
  refuses (`git-config-alias`), as an include does — when the line sets it (its own prefixes,
  `env`, an export); the session's own environment is not judged so, since a session that always
  had one would have every git command refused; `GIT_CONFIG_NOSYSTEM` and
  `GIT_CONFIG_GLOBAL=/dev/null` take files away and pass.
The questions to git (section 6) are asked of the effective repository, never of the folder
alone.

## 6. Paths and the questions to git

- `paths.go` decides what may be deleted, by the rules of section 0: strictly inside the
  session's own temporary folder or a regenerable folder, links followed, a temporary folder
  itself or above it never.
- `gitq.go` asks the questions the rows need (another ref holds a branch; it exists; a conflicted
  merge is under way; what is uncommitted under paths; a branch adds nothing; what a push adds to
  `cards.branch` and whether its `Card:` lines are valid), each `git --no-optional-locks` with a
  timeout, once per call. An unanswered question counts as the unsafe answer.

## 7. The hook

`encoding/json` in, `encoding/json` out, exit 0 always (only 2 would block, and the hook never
uses it). Every outcome, in order:

| situation | answer on stdout | logged | exit |
|-----------|------------------|--------|------|
| standard input is not a JSON object | `systemMessage` "failed and did not check" | `failed_open` | 0 |
| standard input over 4 MiB | deny (`unreadable-call`) | `refused` | 0 |
| a call with a session id | (goes on) | `guard-seen` touched | — |
| not the Bash tool | nothing | `ignored_tool` | 0 |
| `tool_input` present but not readable, or `command` not a string | deny (`unreadable-call`) | `refused` | 0 |
| no command, or only spaces | nothing | — | 0 |
| a background call not through `jst bg` | (goes on) | the journal event `jackstay.bg.requested_outside` | — |
| jackstay.json unusable | built-in rules only; `systemMessage` says so — **except the Card push rule**, which follows the next row | `jackstay.guard.config_error` on the span | 0 |
| the project **declares** `cards` but the section cannot be used (a broken section, or a jackstay.json that does not parse while its text holds a `"cards"` member name, or cannot be read at all) | the Card push rule stays on and refuses every `git push` that could move a remote branch (it cannot judge them: `card-push-unreadable-config`); nothing else changes | `refused` when it refuses | 0 |
| findings | deny with the reasons | `refused` | 0 |
| warnings only, or findings of rules set to `warn` | `systemMessage` to the person, `additionalContext` to the agent | `warned` | 0 |
| the line cannot be parsed (section 2), is nested too deep or over the budget | deny (`unparsable`, `too-deep`, `too-long`) | `refused` | 0 |
| git cannot answer a question a rule needs (a timeout, not a repository) | that rule refuses, as when the answer is unsafe | `refused` | 0 |
| an internal fault: a panic of the guard's own code, or no answer within 5 s | `systemMessage` "jst guard failed and did NOT check this command (…)", also on standard error | `failed_open`, with the fault and its stack | 0 |

The answer is written **once**, before any log or mark, so a failed write changes nothing.
**The whole hook is bounded**: standard input up to 4 MiB (more is `unreadable-call`),
`jackstay.json` up to 1 MiB (more is unusable), the reading and git as in sections 3 and 6, and
one 5 s deadline to the answer (past it: fail open, `internal-error`). The span and marks then run
under the rest of an 8 s total, at which the process exits, abandoning a stalled write. The
settings give the hook 10 s. A test stalls each bound (a sleeping git, a FIFO as `jackstay.json`, a
blocking log folder) and checks the answer comes in time.

Refusals and warnings carry stable identifiers beside their prose: the rule ids, and the warnings
`config-unusable`, `regenerable-ignored`, `project-rule-unreadable`, `guard-exception-not-applied`,
`nul-byte`, `producer-scanned`, `too-deep`, and `internal-error` in a fail-open message. The
guard's span carries them ([observability.md](observability.md)); `jst stats` and `jst doctor`
read them there.

## 8. How it is judged

- `tests/guard/corpus.jsonl` holds **versioned semantic cases**, each with everything the decision
  depends on (the line, folder, shell, `TMPDIR`, scratch folder, `jackstay.json` sections, the
  fixture and the answers to git's questions) and the expected verdict and rule ids. **These cases
  are the authority for the guard**; a decision changed on purpose changes its case in the same
  commit.
- `fixtures_test.go` runs the git rows against real repositories; `bounds_test.go` and
  `hook_test.go` reach every bound of sections 3 and 7; `advice_test.go` walks every row's advice;
  `levels_test.go` the levels of `guard.rules`.
- `just guard-latency` measures the hook's wall time (about a millisecond a call) outside the gate;
  a change that moves it records its numbers.

## 10. `jst trash`: the undoable way to delete (W-042)

The guard's advice for `rm -r` must be something a session can do, and macOS's `trash` fails in
the sandbox (`afpAccessDenied`), so the advice is `jst trash PATH`; the guard knows `jst trash` as
harmless. Use: the [guard guide](../guide/guard.md#jst-trash).

- `jst trash PATH…` moves each item into `<jackstay home>/.trash/<time>-<n>/` beside `entry.json`
  (`path` absolute, `moved_at`, `project`, `session`, `kind`). On the same volume the move is a
  `rename`; otherwise a copy checked by sizes and modes before the original is removed, never after
  a failed copy. The jackstay home and a folder holding it are refused.
- `jst trash restore ENTRY [--to PATH]` puts an item back, refusing when something is there; the
  entry then goes.
- An entry records its project and the `trash.keep_days` of the item itself (default 7, W-043): a
  folder's read from inside it, anything else's from its folder; an older entry without them takes
  its project's or the default. Older entries are removed by the next `jst trash` or `jst discard`
  of any project (before they move anything) or by `jst trash prune`. An entry is held while its
  move is under way, and pruning never removes a held entry. A move that stops part way leaves its
  entry held, its checked copy perhaps the only whole one, and the error names it.
