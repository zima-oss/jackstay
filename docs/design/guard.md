# The guard

The guard is jackstay's `PreToolUse` hook (`jst guard`, run as `jst-guard`): it reads each Bash
call an agent makes and refuses the destructive ones, naming what to run instead. This spec covers
what it refuses and why (section 0), how it reads a command line (sections 2 and 3), its rules and
git's options (sections 4 to 6), the hook's answers and bounds (section 7), how it is tested
(section 8), and `jst trash`, the undoable delete its advice names (section 10). Rulings it follows:
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
| `gh-pr-merge`, `glab-mr-merge` | `gh pr merge`, `glab mr merge`: it merges the change on the host outside `jst land`, past the project's landing policy (its required checks and the paths only a person lands; [land.md](land.md) section 3.3) | `--help`, `-h`, `help` | `jst land`; a change for a person stops at `jst land: ready for a person` |
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
`card-push-unreadable-config` cannot be changed. zsh stops a command with an unquoted pattern that
matches nothing with `no matches found`, and the command (most often a `grep`) never runs
(measured). A `zsh-glob` refusal ends
"quote the pattern and run the command again", not "ask the owner".

- **Taking a side** (`--ours`, `--theirs`, `-m`, `--merge` of `git checkout` / `git restore`) is
  allowed only when a merge with conflicts is under way and every edited path under the pathspec
  is in conflict. Git is asked both; when it cannot say, or a path is only known at run time, the
  command is refused. On a path that is not in conflict the same words throw its edits away.
- **A branch that adds nothing** may be deleted with `-D`: merging it into the default branch
  (what `origin/HEAD` points to, else `origin/main`, `origin/master`, else `main`, `master`; never
  the branch itself) would change no file. A branch with commits of its own whose files equal the
  merge base's (work done and undone) is "no". That is a branch after its pull request was
  squash-merged, where no ref holds its commits and nothing is lost. Git is asked (`git merge-tree
  --write-tree`, in memory, its objects in a folder that is thrown away); a conflict, a missing ref
  or a git without that command is "no". Nearly every `git branch -D` a session runs is clean-up
  after a squash merge (measured); refusing them would be the
  guard's main noise. `jst tidy` uses the same test.
- **A throwaway checkout** is exempt from the `reset`, `checkout`, `switch`, `restore`, `clean`,
  `rm`, `read-tree` and `checkout-index` rules: a checkout under a temporary folder of the
  session's own that the command itself went to (a clone made with `mktemp`, a repository in the
  scratch folder). The folder the session runs in is never one. Its branches, its stash and what
  it pushes are not exempt.
- **Regenerable names**: `.build`, `DerivedData`, `node_modules`, `__pycache__`, `.pytest_cache`,
  `.mypy_cache`, `.ruff_cache`, `.tox`, `.venv`, `venv`, `htmlcov`, `*.egg-info`, and the
  project's `guard.regenerable` (counted only inside the project's checkout). `.swiftpm` is not
  among them; a project whose sessions delete it adds it. An entry there is one folder name. The
  plain spellings of "everything" make `jackstay.json` unusable (`implementation.md` section 3.2); any other
  entry that would match every name, or holds a slash, is passed over by the guard and said
  (`regenerable-ignored`).
- **`find … -name <regenerable> -exec rm -rf {} +`** (or `-delete`) is allowed when every `-name`
  is a regenerable name and the expression has no `-o`, `!` or parentheses. Anything else
  `find -exec` or `xargs` runs is judged on a path nobody can name, so a recursive `rm` there is
  refused.
- **Where a path really leads** decides: links on disk are followed, and so are links the command
  itself made with `ln -s`. A temporary folder itself, or anything above one, is never disposable.

A path that is only known when the command runs (`rm -rf "$DIR"` with `DIR` read from a file, a
relative path after `cd -` or `cd "$X"`, a brace list, an unquoted word a shell would split) is
refused: the guard does not guess. One form passes where the place is unknown: a regenerable name
written out as the **last** name of a quoted path that is not entered (`"$ROOT/.build"`, or
`.build` after `cd "$X"`). `rm` then removes that entry and never goes through a link. `.build/`,
`.build/.` and `.build/*` go through a link, so they need a place that is known. Long options cut
short the way git accepts them (`--har`) count as the option (section 5). Every refusal other than
`zsh-glob` ends by saying that the owner can run the exact command in their own terminal, and that
the command must not be rephrased to get around the guard. No refusal's advice leads to another
refusal (section 4).

### How a command line is read

As a shell reads it, not as text: the line is parsed into a syntax tree and walked (sections 2
and 3). Stepped over to find the command: leading `VAR=value`; the wrappers of section 2a;
`if`/`then`/`do`/… and `coproc`; git's own global options (`-C`, `-c`, …, section 5). zsh's `=rm`
(the command by its path) is read as `rm`; GNU tools under their Homebrew names (`grm`, `gfind`,
`gxargs`, …) and `busybox`/`toybox <applet>` are read as the tool.

- **Data is not looked at**: text in quotes that is an argument of another command (a `grep`
  pattern, a commit message, an `echo`), a comment, the body of a here-document.
- **What a shell would run is looked at**: `$(…)`, backticks, `<(…)`, `bash -c '…'`, `eval`,
  `trap`, `script -c`, what `xargs`, `find -exec`, `watch`, `parallel` and
  `git submodule foreach` run, and a here-document or here-string fed to a shell (section 2's
  table).
- **Known variables are put in** before the command is judged, in command and in option position:
  `F=--hard; git reset $F` is `git reset --hard`. A word that starts with a dash and holds an
  unknown expansion (`-$F`, `--$mode`) is read as the dangerous option.
- **Only what holds on every path the shell may take is known** (section 3): where the command
  runs, and what a variable holds, is certain, one of a few, or unknown, and unknown is never
  disposable. A `cd` may fail, so after `cd X; …` the command is in X or where it was. **`set -e`
  does not** make X certain on the tool's own line: the Bash tool runs the line inside `eval`, in
  an `&&` list, where errexit stops nothing.
- A project's `deny` rule with an `env` condition is applied when the variable's value cannot be
  known at that point (after an unread `source` or `eval`, or set from something unreadable). A
  variable the line never names and the hook's own environment lacks is unset; the hook's
  environment counts.
- **The fallback scan** searches the whole raw text of the call for each rule's destructive words.
  It is used, with the warning `producer-scanned`, only for a script piped to a shell or to
  `parallel` by a producer the guard cannot model (`curl … | sh`, `cat file | sh`); `echo`,
  `printf` and `cat` of a here-document are modelled and read. This is the one place where data
  can be refused. A line that cannot be parsed, is nested too deep or is over the budget is refused
  (sections 2 and 3), not scanned.

### Failure behaviour

The hook's every outcome is in section 7. In short: input the guard cannot read is refused;
evidence it cannot get (git does not answer) makes the rule that needed it refuse; only a fault of
its own code fails open.

| What goes wrong | What happens |
|---|---|
| The guard's own code fails (a panic, a bug, standard input that is not a JSON object, past its 5 s deadline) | The command runs. `{"systemMessage": "jst guard failed and did NOT check this command (…)"}` is shown to the person, the same line goes to standard error, and the span records `failed_open` with the fault and its stack. |
| `jackstay.json` cannot be used (not JSON, an unknown key, a wrong type, a bad pattern, no `jackstay_schema`) | Not an open failure: the built-in rules are applied without the file, every answer says what is wrong with it (`config-unusable`), and the span records the error. The project's `deny` rules, regenerable names and rule levels are off until the file is mended; the Card push rule stays on (section 7). |
| One `guard.regenerable` entry, one `deny` rule, or one `guard.rules` path exception cannot be applied | That entry is passed over and said (`regenerable-ignored`, `project-rule-unreadable`, `guard-exception-not-applied`); the rest holds. |
| The binary cannot start (removed, wrong architecture) | The shell ends 126 or 127; Claude Code shows a hook error and runs the command. `jst doctor` reports a guard that does not refuse its probe (`implementation.md` section 7.7). |
| The journal or the mark cannot be written | The answer is unchanged: it is written before either. |
| The hook takes too long | It answers within 5 s and exits within 8 s (section 7); the settings give it 10 s, after which Claude Code goes on without it. |

It fails open on its own error on purpose: a guard that blocked on its own bug would stop every
session of every project at once, and the sandbox and the permission rules are still there. The
cost is that a broken guard protects nothing, so `failed_open` must stay at zero; `jst doctor`
reports each project's count of the last 7 days.

### What it cannot see

- A command inside a script file, a `Makefile` or `justfile` recipe, or a git alias defined in a
  config file (an alias given on the line, `git -c alias.x=…`, is refused).
- A sourced file: what follows it is read with nothing known, and a function or alias it defines
  is not seen.
- A command or a git subcommand that is itself an unknown variable (`$CMD -rf x`).
- An unknown variable that is a whole word among the options (`git reset $F` with `F` not set in
  the command): it is taken as a name, since such a word nearly always is one.
- A deletion by other means: `python -c`, `perl`, `rsync --delete`, `mv` over a file or into a
  temporary folder.
- A link made at a place that cannot be named.
- `$(…)` inside an unquoted here-document's body.
- A script piped into a shell from a command that cannot be modelled: only searched for the plain
  words.
- A path that is only known when the command runs: `rm -r` of it is refused rather than guessed,
  so this limit costs a refusal, not a loss.
- A file that changes between the guard's question to git and the command (the clean-path and
  clean-worktree allowances rest on git's answer at that moment).
- A destructive form not in the table.
- Tools other than Bash, and a session whose project folder was not trusted (Claude Code holds
  back project hooks until the trust dialog is accepted).
- The owner's own terminal, by design.

Known limits for a deliberate bypass ([W-017](../decisions/W-017-review-fixes-and-threat-model.md):
the guard is against accidents; the cheap bypasses `env -S`, `git -c alias.*` and
`-c clean.requireForce=false`, `grm`/`gxargs`/`gfind`, `busybox`/`toybox <applet>`,
`git submodule foreach` and `$(echo|printf <constant>)` are closed): options that reach a command
through `"$@"` after `set --`, through a function's parameters, an array built elsewhere,
`printf -v` or `read`; a constant `echo`/`printf` that is only part of a word (`$(echo --ha)rd`),
reaches the command through a variable, is one of several commands, or holds escapes zsh's `echo`
reads (`'\x2d-hard'`); a git alias from a config file the session's own environment names; git
settings that run a command (`core.fsmonitor`, `core.hooksPath`, `core.sshCommand`,
`diff.external`, `filter.*`) given by `-c` or by `GIT_CONFIG_COUNT`; and records forged in
`~/.jackstay` (receipts, step marks, the run record), which every session may write.

It lowers the rate of accidents. It is not a security boundary.

### Codex

As of Codex CLI 0.162.0 (learn.chatgpt.com/docs/hooks; checked by running the hook), Codex has the
same hook, a `PreToolUse`
event whose matcher `Bash` covers its shell tool, with `tool_input.command` and `cwd` on standard
input and the same `permissionDecision: "deny"` answer. So the same `jst-guard` serves it, from a
project file `.codex/hooks.json`. Codex loads a project's hooks only when the project's `.codex/`
layer is trusted **and** each hook has been approved by a person in `/hooks`, and the approval is
tied to the hook's text, so an edit needs approval again. Until then Codex runs without the guard
and says nothing; `jst doctor` is how that is noticed (a project that is expected and never
heard from).

In a regular clone with the layer trusted and the hook approved, Codex runs the guard through a
shell (`"$HOME/…"` expands) and it refuses `git reset --hard` with the guard's words (codex-rs
0.162.0). Beyond that:
- **Worktrees**: in a linked worktree Codex reads the hooks from the **main checkout's**
  `.codex/hooks.json`; the worktree's own file is ignored (the worktree still needs a `.codex/`
  folder). Trust is matched on the folder, the worktree root or the main root. So Codex in a
  worktree is guarded only while the main checkout's current branch carries the hook file.
- `codex exec` skips an unreviewed hook silently (no warning); `--dangerously-bypass-hook-trust`
  exists.
- The approval is `[hooks.state."<absolute path of hooks.json>:pre_tool_use:0:0"]` with
  `trusted_hash` = sha256 of the canonical JSON `{event_name, matcher, hooks:[{async, command,
  timeout, type}]}` in `~/.codex/config.toml`; `-c` splits quoted dotted keys, so pass an inline
  table.

Codex's exec-policy rules (`prefix_rule(…, decision="forbidden")`) are marked experimental and do
not see inside most `bash -lc "…"` lines, so they are not used.

## 1. Shape

```
internal/guard/
  hook.go       the PreToolUse hook and `jst guard check`: JSON in, a decision out, fail-open
  parse.go      Decide; the line to a syntax tree (mvdan.cc/sh), bash then zsh; the reader's settings
  walk.go       the reader: walks the tree, keeping what is known (section 3)
  state.go      what is known at a point: places, variables, exports, links; join and widening
  words.go      a word's value: known text, one of a few, or unknown; quoting kept per segment
  invoke.go     a simple command to an invocation: wrappers peeled, inner scripts and producers found
  gitopts.go    one model of git's options (section 5)
  gitjudge.go   the git rows of the table judged on the parsed record
  push.go       the push rows, and the Card push rule with its range reader (internal/cards)
  rules.go      the rules, as a table (section 4); rm, find, ln, zsh's patterns
  project.go    the project's `guard.deny` rows and regenerable names; the fallback scan
  paths.go      what may be deleted: temporary folders, own scratch, regenerable names, links
  gitq.go       the questions to git, each with its timeout, asked once per call
  telemetry.go  the decision on the guard's span (observability.md)
  export.go     `Words`: a line's simple commands, for `jst baseline`
internal/cards/  the `Card:` line rules (shared with `jst lint`)
internal/procs/  the note of which session a Claude Code process serves (`implementation.md` section 5.1)
internal/trash/  `jst trash` (section 10)
```

## 2. Reading a line: a parser, and refusing what it cannot read

- **mvdan.cc/sh/v3/syntax** (v3.14.1, 2026-09-06; BSD-3; maintained since 2016; used by `shfmt`
  and `go-task`) parses the line into a tree: lists, pipelines, `&&`/`||`, subshells, blocks,
  `if`/`case`/`for`/`while`, functions, here-documents, here-strings, `$(…)`, backticks,
  arithmetic, process substitution, parameter expansion. The guard walks the tree; it has no lexer.
- **A NUL byte** is not given to the parser as it is (the parser drops it, which loses the
  reading where it stands for a separator): the line is read twice, once with every NUL removed
  and once with each replaced by a space, with the warning `nul-byte`; it is refused when either
  reading is.
- A here-document left open runs to the end of the text, as bash reads it (with a warning): the
  parser is given the delimiter at the end. A line that does not parse as written is tried once
  more with `((` read as two subshells (`((cmd) && (x))`, which bash takes so when it is not
  arithmetic).
- The parser takes `f() { …; } && f a` as a function whose body is `{ …; } && f a`; bash and zsh
  define `f` and then run `f a`, so the walker reads it so.
- The line is parsed as **bash**; if that fails, as **zsh** (the parser's experimental zsh
  variant: `${(f)…}` flags, `for x (…)`). **If both fail, the command is refused** ("it could not
  be read as shell; write it plainly, one command per line") — not passed, and not searched for
  words (W-036, W-039). Lines neither parse reads are about one in ten thousand of the commands
  sessions run, and the ones looked at are lines the shell itself would reject (a backtick left
  open inside double quotes)
  (measured).
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
- **A producer the guard cannot model** (`curl … | sh`, `zsh <(sed s/a/b/ <<< '…')`,
  `… | parallel`, `bash -c "$X"` with X not known) is itself judged as any command is. What it
  writes is not known, and a producer may turn any of its texts into anything (`sed 's/^..//'
  <<< "';git reset --hard"`), so parsing its texts is not enough: the **fallback scan** (section 0)
  applies — every rule's destructive form (each row of section 4 carries its `Words`, e.g.
  `reset` … `--hard`, `rm` … `-r`) is searched as words in the **whole raw text of the tool
  call** — not only the producer's own words, since what it reads may come from
  anywhere earlier in the line (`D='git reset --hard'; D2=$(printf '%s' "$D" | cat); sed 's/^//'
  <<< "$D2" | sh`) — whether its parts parse or not, and a form found refuses the shell (or
  `parallel`) that receives it, with that row's rule id and the warning `producer-scanned`. A text that parses is also judged as a shell line, which adds the
  forms only a reading finds (a known variable put in). The project's `guard.deny` patterns are
  matched against the whole call's raw text as well. This is the one place where data can be refused.
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
  (`D=.build; if read D; then rm -rf "$D"; fi` reads the `rm` with D unknown); a loop's body is read again until nothing changes (up to 6 passes, then
  what it writes is unknown); a subshell, a pipeline's parts but the last, `&` and `$(…)` give nothing back;
  `eval` and `source` share the state; after an unread `source` or `eval` nothing is known.
- `cd X && …`, `cd X || exit`, `if cd X` make X certain; `cd X; …` leaves both places.
- Word values are read by jackstay's own small reader over the parser's word parts (`words.go`), not by
  mvdan.cc/sh/v3/expand: the rules need what `expand` does not keep — which segment stood in
  quotes (an unquoted unknown splits, `"$ROOT/.build"` does not), which characters are an unquoted
  pattern, and one reading per value a variable may hold. Command substitution is not run — its
  value is known only when it is a constant `echo`/`printf`/`cat <<` (W-017), and `$(mktemp …)`
  stands for a fresh temporary folder.
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
rules of `jackstay.json` (environment, command and `unless` patterns, RE2) are applied after the
built-in ones, as `project:<id>`. A refusal names each row's `Why` and `Instead`. **No row's advice
leads to a refusal** ([W-052](../decisions/W-052-guard-dead-end-and-clean-copy-gate.md)): every
command an `Instead` names passes in the situation the row refused, and a step taken "first" is
followed by a command that then passes; a test walks the table
([discard-and-clean-gate.md](discard-and-clean-gate.md) section 2). Dropping uncommitted changes
is advised as `jst discard <path>`. A project sets a row's level (`refuse`, `warn`, `off`) and, for
`rm-recursive` and `find-delete`, path exceptions in `guard.rules`
([W-053](../decisions/W-053-guard-rules-configured-per-project.md),
[W-055](../decisions/W-055-every-guard-rule-configurable-now.md); the same document, section 9);
`zsh-glob` warns by default.

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

- `paths.go`: what may be deleted — strictly inside a temporary folder that is the session's own
  (its scratch, a folder `mktemp` made in the same command), or a regenerable folder
  (`.build`, `node_modules`, …, jackstay.json's `guard.regenerable` inside the project); links on disk
  and links the command made are followed; a temporary folder itself or anything above one never.
- `gitq.go`: the questions the rows of section 0 ask (another ref holds a branch; does a branch
  exist; is a merge with conflicts under way; what is uncommitted under paths; does a branch add
  nothing to the default branch; what commits a push adds to `cards.branch` and whether their
  `Card:` lines are valid), each `git --no-optional-locks` with a timeout, asked once per call. A
  question git cannot answer counts as the unsafe answer.
- `internal/cards`: the `Card:` line rules and the range check, shared by the guard's push rule
  and `jst lint` (`implementation.md` section 4.1).

## 7. The hook

`encoding/json` in, `encoding/json` out, exit 0 always (only 2 would block, and the hook never
uses it). Every outcome, in order:

Three kinds of trouble are kept apart: input the guard cannot read is refused; evidence it cannot get makes the rule that needed it refuse; only a fault in its own code fails open.

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
| an internal fault: a panic of the guard's own code, or no answer within 5 s | `systemMessage` "failed and did not check" | `failed_open`, with the fault and its stack | 0 |

The answer is written **once**, before any log or mark: a log or a mark that cannot be written
changes nothing in it (each is best-effort, its error dropped).
**The whole hook is bounded**: standard input is read up to 4 MiB (more is `unreadable-call`,
refused); jackstay.json up to 1 MiB (more is unusable); the reading
and git have the bounds of sections 3 and 6; everything from start to the written answer runs
under one deadline of 5 s (past it the hook fails open with `internal-error`). After the answer,
the span and the marks run under what is left of an 8 s total, and the process exits at that
deadline whatever they are doing (a stalled write is abandoned, not waited for). The hook's
timeout in the projects' settings is 10 s (`.claude/settings.json`; Claude Code proceeds
unchecked past it), and the guard is done before it. A test stalls each of these (a fake git that
sleeps, a FIFO as jackstay.json, a log folder that blocks) and checks the hook answers within its
deadline. The recovery covers the whole command, from the table on.

Refusals and warnings carry stable identifiers beside their prose: the rule ids, and the warnings
`config-unusable`, `regenerable-ignored`, `project-rule-unreadable`, `guard-exception-not-applied`,
`nul-byte`, `producer-scanned`, `too-deep`, and `internal-error` in a fail-open message. The
guard's span carries them ([observability.md](observability.md)); `jst stats` and `jst doctor`
read them there.

## 8. How it is judged

- Go tests, table-driven, over `tests/guard/corpus.jsonl`: **versioned semantic cases**, each
  carrying everything the decision depends on: the line, the folder, the session's shell and
  `TMPDIR`, the scratch folder, the jackstay.json sections, the fixture (files, git repositories,
  links), and the answers to git's questions (the guard's git questions are an interface, so a
  case can answer them); and the expected verdict and rule ids. **These cases are the authority
  for the guard**: mocked answers make them deterministic. A decision changed on purpose changes
  the case's expectation in the same commit.
- `internal/guard/fixtures_test.go` runs the git rows against real repositories, where git
  answers for itself.
- The bounds of sections 3 and 7 each have a test that reaches them (`bounds_test.go`,
  `hook_test.go`); the advice of every row is walked (`advice_test.go`, section 4); the levels of
  `guard.rules` (`levels_test.go`).

`just guard-latency` measures the hook's wall time; it is outside the gate, since timing is no pass
or fail on a shared machine. The measured cost is about a millisecond a call; a change that moves
it records its numbers with the change.

## 9. What a session sees

A command that parses (nearly every one) is judged by the rules of section 0; a refusal says
why and what to do instead. A command that cannot be parsed, or is longer than the budget, is
refused with the advice to write it plainly.

## 10. `jst trash`: the undoable way to delete (W-042)

The guard refuses `rm -r` of anything that is not disposable, and its advice must be something a
session can do. macOS's `trash` cannot work inside the agent sandbox (no write to `~/.Trash`; it
fails with `afpAccessDenied`), so the advice names `jst trash`.

- `jst trash PATH…` moves each file or folder into `<jackstay home>/.trash/` (`~/.jackstay/.trash/`, writable
  inside the sandbox): one entry per path, a folder `<time>-<n>/` holding the item under its own
  name and `entry.json` (`path` as given and absolute, `moved_at`, `project`, `session`, `kind`).
  The move is a `rename` when the item is on the same volume as the jackstay home; otherwise a copy
  that is checked (sizes and modes) before the original is removed, and the original is never
  removed when the copy failed. A path inside a regenerable folder, a temporary folder or the
  trash itself is moved as asked; the jackstay home itself and a folder that holds it are refused.
- `jst trash list [--json]` lists entries, newest first; `jst trash restore ENTRY [--to PATH]` puts
  an item back where it was (or at `--to`) and refuses when something is already there; the
  entry goes once restored.
- An entry records the project and the `trash.keep_days` of the item itself (default 7, W-043): a
  folder's are read from inside it (a whole checkout carries its own `jackstay.json`), anything
  else's from the folder that holds it, whichever folder the command runs in; once older than that it is removed by the next `jst trash` or `jst discard` of any project
  (pruned before they move anything, so nothing they keep goes in the same call), or by
  `jst trash prune`. An entry made before entries recorded their days reads them from the project
  it came from, or takes the default when that is gone. An entry is held while its move is under
  way and no pruning removes a held entry, so a prune running at the same time cannot take a copy
  whose original is going. A move that stops part way (across volumes, the original removed only in
  part) leaves its entry held, its checked copy perhaps the only whole one, and the error names it
  for `jst trash restore`.
- The guard knows `jst trash` as a harmless command (it moves, never deletes) and every refusal of
  `rm -r`, `find -delete` or a recursive delete names `jst trash PATH` as its `Instead`.

## 11. Decided

- A line neither parse reads is refused outright, with no plain-word search over it and no
  destructive form named ([W-039](../decisions/W-039-unparsable-command-refused.md)).
