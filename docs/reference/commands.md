# Command reference

Every `jst` command, as its `--help` prints it, under the headings of `jst help`. This page is
generated from the binary by `just reference`; change a command's help in its package, not here.

## Tests and processes

| Command | What it does |
|---|---|
| [`jst test`](#jst-test) | run a test scope or command, judged, recorded and keyed by the tree |
| [`jst test last`](#jst-test-last) | the last run of this project: outcome, what went wrong, where its output is |
| [`jst test list`](#jst-test-list) | the project's test scopes: what each runs, and when the project runs it |
| [`jst test plan`](#jst-test-plan) | the words a scope would run, step by step; nothing is run |
| [`jst receipts`](#jst-receipts) | the receipts: key, stats, list, export and verify |
| [`jst receipts export`](#jst-receipts-export) | write a portable record of a stored test pass |
| [`jst receipts key`](#jst-receipts-key) | the key of a command here and what went into it; nothing is run |
| [`jst receipts list`](#jst-receipts-list) | the stored receipts |
| [`jst receipts stats`](#jst-receipts-stats) | runs, reuses and seconds saved, from the run log |
| [`jst receipts verify`](#jst-receipts-verify) | verify a portable test record against expected digests and revision |
| [`jst baseline`](#jst-baseline) | test runs by kind, their time, merges and destructive commands, from transcripts, git and the log |
| [`jst lock`](#jst-lock) | run a heavy command holding the machine-wide lock, in its turn |
| [`jst lock status`](#jst-lock-status) | who runs under the machine-wide lock and who waits |
| [`jst bg`](#jst-bg) | run a long-lived command with a deadline, stopped when its session is gone |
| [`jst procs`](#jst-procs) | what jst bg supervises: this session's, or --all |
| [`jst procs stop`](#jst-procs-stop) | stop what jst bg supervises, by ID or by this session's NAME |
| [`jst wait`](#jst-wait) | wait for a process, a file, a line or quiet, with a deadline |

### jst test

Run a test scope or command, judged, recorded and keyed by the tree.

```text
usage: jst test SCOPE [ARGS...] [--no-reuse] [--wait S] [--base REF] [--lock | --no-lock]
                [--resume] [--keep-going] [--commit REV]
       jst test [--no-reuse] [--lock | --no-lock] [--project NAME] [--wait SECONDS] -- COMMAND [ARG ...]

Runs a scope (`jst test list`) or, after --, any command. Only the outcome passed is a pass: read
the last `jst test:` line. With test.selection, `jst test changed [BASE..REV]` picks the scope from
the changed paths.
  --no-reuse      run even when a receipt of this tree could be reused
  --wait S        give up waiting for the machine-wide lock after S seconds
  --lock          take the machine-wide lock; --no-lock: do not (default: the scope's lock)
  --project NAME  the project's name in the record (default jackstay.json's)
  --base REF      the scope's {base} or the base for automatic selection
  --resume        a gate: skip the steps that passed on this same tree
  --keep-going    a gate: run the remaining steps after one fails
  --commit REV    run on a clean copy of REV that jackstay makes and then removes

A scope with steps needs receipts.toolchain in jackstay.json: the commands that print your tools'
versions (`[["go", "version"]]`), or [] for none.

exit: 0 passed (or exited 0 uncounted); 1 failed; 2 not started or refused, nothing ran;
75 gave up waiting for the lock; 76 no test ran; 77 unjudged (output not read to its end, or
leftovers that cannot be attributed); 78 passed only on retry (not a clean pass); 124 timed out;
127 the command cannot start. A command that ran and failed passes its own status on, so 2 or 77
may be its own: the last jst test: line names the outcome.

example: jst test gate --commit HEAD
```

### jst test last

The last run of this project: outcome, what went wrong, where its output is.

```text
usage: jst test last [--project NAME] [--scope S] [--json]

Runs nothing.
  --project NAME   that project's last run (default this project's)
  --scope S        the last run of scope S
  --json           as JSON

exit: 0 shown, whatever the run's outcome; 1 no run of that project (or scope) is in the record;
2 a bad request, or (without --project) jackstay.json cannot be used.

example: jst test last --scope quick
```

### jst test list

The project's test scopes: what each runs, and when the project runs it.

```text
usage: jst test list [--project NAME]

Also shows each scope's timeout. Takes the options of `jst test SCOPE`; only --project changes the
output.
  --project NAME   the project's name in the heading (default jackstay.json's)

exit: 0 listed; 2 the request, the working folder or jackstay.json's test section cannot be used.

example: jst test list
```

### jst test plan

The words a scope would run, step by step; nothing is run.

```text
usage: jst test plan SCOPE [ARGS...] [OPTIONS]

Each step with its judge, timeout, folder, environment, base and checks. Takes the options of
`jst test SCOPE`. SCOPE auto (or changed, with test.selection) picks a scope from the changed paths
and prints the matching rules; the base defaults to origin/HEAD (without it, pass --base). auto
takes no test names or --commit; `changed BASE..REV` plans on a temporary clean copy of REV.
  --base REF       the {base} the words are planned with
  --project NAME   the project's name (default jackstay.json's)

exit: 0 the plan (a step that cannot run as asked says so on its line); 2 refused: no scope, not a
scope of the project, or the request or jackstay.json's test section cannot be used.

example: jst test plan auto --base origin/main
```

### jst receipts

The receipts: key, stats, list, export and verify.

```text
usage: jst receipts key -- COMMAND [ARG ...]
       jst receipts stats [--project NAME] [--json]
       jst receipts list [--project NAME]
       jst receipts export KEY
       jst receipts verify --sha256 DIGEST --head SHA --key KEY FILE

exit: 2 always: runs nothing and prints this usage on standard error; each command under it names
its own.

example: jst receipts stats --json
```

### jst receipts export

Write a portable record of a stored test pass.

```text
usage: jst receipts export KEY

Writes the record of a stored counted pass (with its HEAD and input manifest) to standard output,
and the SHA-256 of its exact bytes to standard error.

exit: 0 exported; 1 no usable export or a read/write failure; 2 bad request.

example: jst receipts export "$receipt_key" > evidence.json
```

### jst receipts key

The key of a command here and what went into it; nothing is run.

```text
usage: jst receipts key -- COMMAND [ARG ...]

Also says whether a receipt would be reused. Runs nothing.

exit: 0 the key; 1 no key could be made (the reason named); 2 a bad request, or jackstay.json or
its receipts section cannot be used.

example: jst receipts key -- go test ./...
```

### jst receipts list

The stored receipts.

```text
usage: jst receipts list [--project NAME]

Lists project, end, seconds, command, head, key.
  --project NAME   only that project's

exit: 0 the list (a receipt that cannot be read is left out); 2 a bad request, or jackstay's home
folder cannot be found.

example: jst receipts list
```

### jst receipts stats

Runs, reuses and seconds saved, from the run log.

```text
usage: jst receipts stats [--project NAME] [--json]

  --project NAME   only that project
  --json           as JSON

exit: 0 the counts (an empty log included); 1 the counts could not be written as JSON; 2 a bad
request.

example: jst receipts stats --project widgets
```

### jst receipts verify

Verify a portable test record against expected digests and revision.

```text
usage: jst receipts verify --sha256 DIGEST --head SHA --key KEY FILE

Take DIGEST, SHA and KEY from a source other than FILE. Needs no receipt store or Git repository.

exit: 0 verified; 1 unverified or unreadable evidence; 2 bad request.

example: jst receipts verify --sha256 "$expected_digest" --head "$expected_head" --key "$expected_key" evidence.json
```

### jst baseline

Test runs by kind, their time, merges and destructive commands, from transcripts, git and the log.

```text
usage: jst baseline --repo DIR --since YYYY-MM-DD --until YYYY-MM-DD [--project NAME] [--config FILE]
                   [--kind NAME=REGEX ...] [--gate NAME ...] [--merge-unit pr|commit] [--branch B]
                   [--transcripts GLOB ...] [--also NAME=DIR ...] [--projects-dir DIR] [--jackstay-log FILE]
                   [--examples FILE] [--json]

The kinds of run come from jackstay.json's `baseline` section (or --config); a flag replaces
what it names. Writes nothing but --examples.
  --repo DIR             the project's checkout (required)
  --since, --until DAY   the first and last UTC day, YYYY-MM-DD (required)
  --project NAME         the project's name (default: its jackstay.json's, else the folder's)
  --config FILE          read the baseline section from FILE instead of the project's jackstay.json
  --kind NAME=REGEX      a kind of test run: a command matching REGEX (repeatable)
  --gate NAME            a kind that is a gate, timed for overlap (repeatable)
  --merge-unit pr|commit a merge is a subject ending in (#N), or each commit (alias --per)
  --branch B             the branch merges are counted on (default main)
  --transcripts GLOB     the session folders to read, by name (repeatable)
  --also NAME=DIR        another project whose gate runs count for overlap (repeatable)
  --projects-dir DIR     where Claude Code keeps sessions (default ~/.claude/projects)
  --jackstay-log FILE    read this run log instead of jackstay's own
  --examples FILE        write each command the guard would refuse to FILE (may hold private text)
  --json                 the report as one JSON object

exit: 0 the report; 1 the --examples file or the JSON report could not be written; 2 the request
(an option missing or wrong, a bad day, a jackstay.json, --config or --also that cannot be used).

example: jst baseline --repo . --since 2026-11-01 --until 2026-11-14
```

### jst lock

Run a heavy command holding the machine-wide lock, in its turn.

```text
usage: jst lock [--project NAME] [--wait SECONDS] [--also-hold PATH=VARIABLE]... [--note-words WORDS] -- COMMAND [ARG ...]

  --project NAME            the project in the holder's note (default: jackstay.json's, else the
                            main checkout's folder name)
  --wait SECONDS            give up after that long, having run nothing (default: no limit)
  --also-hold PATH=VARIABLE also hold the older lock file PATH, setting VARIABLE as its script
                            reads it (repeatable)
  --note-words WORDS        the command's words in the holder's note (default JACKSTAY_LOCK_NOTE_WORDS)

exit: the command's own status (stopped by a signal, it ends by that signal); 2 a lock,
jackstay.json or the words cannot be used; 75 gave up waiting, nothing was run; 127 the command
cannot start.

example: jst lock -- xcodebuild test -scheme App
```

### jst lock status

Who runs under the machine-wide lock and who waits.

```text
usage: jst lock status [--json]

In order, and the same for each older lock.
  --json   the report as one JSON object

exit: 0 the report; 2 the request, jackstay.json or a lock file cannot be used.

example: jst lock status
```

### jst bg

Run a long-lived command with a deadline, stopped when its session is gone.

```text
usage: jst bg [--for MINUTES] [--name NAME] [--keep] -- COMMAND ...

Runs COMMAND in a process group of its own. Start it with the tool's background flag.
  --for MINUTES       the deadline (default procs.default_minutes, 30; at most procs.max_minutes, 240)
  --name NAME         a name to stop it by (`jst procs stop NAME`)
  --keep              no deadline and no session check: stopped only by `jst procs stop`
  --watch PID, --check-every SECONDS   for jackstay's own tests only

exit: the command's own status (128+N for signal N); 2 a bad request; 126 the command could not
start; 127 it was not found; 1 when registration or cleanup fails and the command itself
succeeded. Stopped by a signal, jst bg ends by that signal.

example: jst bg --for 20 --name server -- npm run dev
```

### jst procs

What jst bg supervises: this session's, or --all.

```text
usage: jst procs [--all] [--json]

Lists id, name, age, deadline, session, folder, command. Stops orphans past their deadline on the
way; an incomplete cleanup stays listed.
  --all    every session's, not only this session's
  --json   as JSON

exit: 0 the list (an empty one included); 2 a bad request, or jst bg's records cannot be opened.

example: jst procs --all
```

### jst procs stop

Stop what jst bg supervises, by ID or by this session's NAME.

```text
usage: jst procs stop [--all] ID|NAME...

TERM, 5 seconds, then KILL. By ID whoever started it; by NAME this session's only.
  --all   with a NAME: every session's of that name

exit: 0 stopped; 1 a name or id not stopped or cleanup unconfirmed; 2 a bad request.

example: jst procs stop server
```

### jst wait

Wait for a process, a file, a line or quiet, with a deadline.

```text
usage: jst wait --pid PID                    until the process has ended
       jst wait --file PATH [--nonempty]       until the file exists (and holds something)
       jst wait --gone PATH                    until the file or folder is gone
       jst wait --line REGEX --in PATH         until a line of the file matches (printed)
       jst wait --quiet SECONDS --in PATH      until the file has not grown for that long
  common: [--fail REGEX] [--while-pid PID] [--new] [--timeout SECONDS] [--tail N]

A line longer than 1 MiB ends a pattern match with exit 1.
Exactly one condition:
  --pid PID          until that process has ended
  --file PATH        until the file exists
  --nonempty         with --file: until it exists and holds something
  --gone PATH        until the file or folder is gone
  --line REGEX       until a line of the --in file matches (the line is printed)
  --quiet SECONDS    until the --in file has not grown for that long
Common options:
  --in PATH          the regular file --line, --quiet and --fail read (it can start out missing)
  --fail REGEX       end with 1 as soon as a line of the --in file matches
  --while-pid PID    end with 1 when that process (the writer) ends first
  --new              count only lines written after jst wait started
  --timeout SECONDS  the deadline (default 540)
  --tail N           how many of the file's last lines to print on the way out (default 20)

exit: 0 it happened; 1 it cannot happen any more, or the log ended the wait in failure;
2 the request is wrong; 3 the deadline came first.

example: jst wait --line 'listening on' --in server.log --fail 'error' --timeout 60
```

## Branches and landing

| Command | What it does |
|---|---|
| [`jst start`](#jst-start) | a new branch and its worktree on the default branch as fetched |
| [`jst sync`](#jst-sync) | bring this branch up to date with the default branch |
| [`jst tidy`](#jst-tidy) | report, or remove, the branches and worktrees whose work is done |
| [`jst discard`](#jst-discard) | put paths back to HEAD, keeping what that drops in jackstay's trash (undoable) |
| [`jst trash`](#jst-trash) | move files and folders into jackstay's trash, undoably; list, restore, prune |
| [`jst land`](#jst-land) | push and open a change request, wait for its required checks, merge when the policy allows |
| [`jst checks`](#jst-checks) | wait on a pull request's checks and a workflow's runs on GitHub |

### jst start

A new branch and its worktree on the default branch as fetched.

```text
usage: jst start NAME [--from REF] [--path DIR] [--no-fetch]

  --from REF    start from REF instead of the default branch
  --path DIR    the worktree's folder (default .claude/worktrees/NAME when git ignores it there,
                else a folder beside the checkout, CHECKOUT-worktrees/NAME)
  --no-fetch    do not fetch the remote first

exit: 0 made; 1 git refused; 2 the request (a taken name or folder, a REF that is not a commit).

example: jst start fix-parser
```

### jst sync

Bring this branch up to date with the default branch.

```text
usage: jst sync [--no-fetch] [--push] [DIR]

A fast-forward or a merge, never a rebase. DIR defaults to the working folder.
  --no-fetch   use the remote-tracking branch as it is; do not fetch first
  --push       push the branch after the merge
  --rebase     refused: jst sync merges only

exit: 0 done or nothing to do; 1 a conflict (the files are named, the merge is left to resolve or
abort), or git refused; 2 the request (uncommitted work, a detached HEAD, a merge under way).

example: jst sync --push
```

### jst tidy

Report, or remove, the branches and worktrees whose work is done.

```text
usage: jst tidy [--fix] [--branch NAME]... [--worktree PATH]... [--no-fetch] [--no-github] [--json] [DIR]

  --fix          remove what is finished (default: only report)
  --branch NAME  only this branch (and its worktree); may repeat
  --worktree P   only this worktree (and its branch); may repeat
  --no-fetch     do not fetch the remote first
  --no-github    do not ask GitHub about pull requests
  --json         the report as JSON
  DIR            the checkout to look at (default the working folder)

exit: 0 nothing finished is left; 1 something finished is listed and not removed, or a removal
failed or was called off; 2 the request is wrong.

example: jst tidy --fix --branch fix-parser
```

### jst discard

Put paths back to HEAD, keeping what that drops in jackstay's trash (undoable)

```text
usage: jst discard [--] PATH...

Each file or folder goes back to HEAD; the file on disk and a differing index version go to the
trash first (`jst trash restore ENTRY` puts them back). An untracked file is left alone
(`jst trash PATH` moves it). Every path is checked before anything changes; -- lets a path
start with -.

exit: 0 done; 1 a path could not be put back (the message says which); 2 refused, nothing was
changed.

example: jst discard src/parser.go docs/draft.md
```

### jst trash

Move files and folders into jackstay's trash, undoably; list, restore, prune.

```text
usage: jst trash PATH...                      move each into jackstay's trash (undoable)
       jst trash list [--json]                 the entries, newest first
       jst trash restore ENTRY [--to PATH]     put an item back (refused when something is there)
       jst trash prune                         remove entries older than trash.keep_days

Entries older than trash.keep_days (default 7) are removed on the way.
  PATH...        files or folders to move; -- before them lets a path be named list, restore,
                 prune or start with -
  --json         with list: as JSON
  --to PATH      with restore: put the item back at PATH instead of where it was

exit: 0 done; 1 a path could not be moved, or no such entry; 2 a bad request.

example: jst trash build/old-output
```

### jst land

Push and open a change request, wait for its required checks, merge when the policy allows.

```text
usage:
  jst land [--change N|URL] [--title T] [--body-file F] [--card ID]... [--reason TEXT]
           [--prepare | --confirm] [--plan] [--timeout S] [--interval S] [--lines N]
  jst land status [--change N|URL]
  jst land find (--head BRANCH | --search TEXT) [--repo URL] [--state STATE] [--wait S]

Push the branch, open the change request, wait for the checks the base's rules require (and those
land.checks lists), and merge at the head that was checked. The plan is printed first. The agent
merges only when land.autonomy is merge-when-green and the change touches no human path
(jackstay.json, .github/workflows/**, .claude/settings.json, and land.human_paths); any other
change stops ready for a person, who merges it on the host's page or with --confirm. Checks that
are not required are reported, never waited for and never blocking. Autonomy and checks are read
from the base's jackstay.json; human paths from the base's and the change's. This checkout is
never switched, pulled or rebased.

  --change N|URL   an existing open change request from this branch
  --title T        the new change request's title
  --body-file F    its description, from a file
  --card ID        the card the change is for (repeatable), or none with --reason
  --reason TEXT    why the change is no card's work (with --card none)
  --prepare        stop ready for a person, whatever land.autonomy says
  --confirm        a person at a terminal types the change's number to merge it; the policy and
                   checks are judged again after the number is typed
  --plan           print the plan and stop
  --timeout S      seconds to wait for the checks (github.wait.timeout_seconds, else 1800)
  --interval S     seconds between looks (github.wait.interval_seconds, else 20)
  --lines N        log lines shown per failed check (github.wait.log_lines, else 25)

  status           where this branch and its change request stand
  find             one change request by head branch or search text; none found is exit 1

The last line on standard error is "jst land: STATE".
exit: 0 landed (merged; with --confirm on a base with a merge queue, merged by the host's queue
within the wait), or planned (--plan); 1 refused (a required check failed or was cancelled, a draft,
a conflict, an operation the host does not offer); 2 invalid (usage, configuration, no account, no
land section); 3 pending (checks or the queue still pending at the deadline); 4 moved (the head, the
base, the test merge or the description moved: run it again); 5 unexpected (a host answer jackstay
does not expect); 6 ready for a person (also every change on a base that requires the host's merge
queue).

example: jst land --title 'Fix the parser' --card W-012
```

### jst checks

Wait on a pull request's checks and a workflow's runs on GitHub.

```text
usage:
  jst checks wait [PR|URL] [--timeout S] [--interval S] [--lines N] [--any-head]
  jst checks status [PR|URL]
  jst checks run-wait WORKFLOW [--ref BRANCH|SHA] [--repo URL] [--timeout S] [--interval S] [--appear S]

  wait [PR|URL]      wait until a pull request's checks settle (default: this branch's)
    --timeout S      seconds to wait for every check
    --interval S     seconds between looks
    --lines N        log lines shown per failed check
    --any-head       follow the head when it moves, instead of ending with 4
  status [PR|URL]    the checks as they are now; nothing is waited for
  run-wait WORKFLOW  wait for a workflow run to complete
    --ref BRANCH|SHA the run's branch or commit (a SHA prefix of 7 or more); default the newest
    --repo URL       https://github.com/OWNER/REPO (default: this checkout's)
    --timeout S, --interval S  as for wait
    --appear S       seconds the run may take to be scheduled (default 120)

exit: 0 fine; 1 not fine, the reason printed; 2 a bad request (also no account, a wrong login, a
host that is not github.com); 3 still pending at the deadline (for status: pending, or no check
registered yet, at once); 4 the head moved while waiting or while the checks were read; 5 an
external format is not what jackstay assumes.

example: jst checks wait --timeout 1200
```

## Review

| Command | What it does |
|---|---|
| [`jst review`](#jst-review) | a cold review of a commit or a range, in rounds |
| [`jst review check`](#jst-review-check) | is this change reviewed and clear? |
| [`jst review last`](#jst-review-last) | the newest round's report, or the newest attempt's failure |
| [`jst review list`](#jst-review-list) | the reviews kept |
| [`jst review pool`](#jst-review-pool) | queue, inspect and maintain local reviews across sessions |
| [`jst review prune`](#jst-review-prune) | remove tasks past review.keep_days, now |
| [`jst review show`](#jst-review-show) | one round's report |

### jst review

A cold review of a commit or a range, in rounds.

```text
usage: jst review [REV | BASE..REV] [--task NAME] [--reviewer NAME,...] [--brief FILE] [--focus TEXT]
                 [--fresh] [--model M] [--effort E] [--timeout MIN] [--past-limit] [--json]

REV alone is reviewed against its first parent (default HEAD). An empty review.use needs
--reviewer. Give a revision named like a subcommand after --.
  --task NAME         the task the rounds belong to (default: the branch)
  --reviewer NAMES    codex, claude-code or both, comma-separated (default: review.use, else codex)
  --brief FILE        a brief file added to the prompt
  --focus TEXT        where to look hardest (it never narrows the round)
  --fresh             start a new chain: a full review from BASE
  --model M           the model, for this call
  --effort E          the reasoning effort, for this call
  --timeout MIN       minutes per run, for this call
  --past-limit        a round past review.round_limit, after the owner's word
  --json              the result as one JSON object instead of the summary

exit: 0 the round does not block; 1 it blocks; 2 usage or configuration; 3 a place is due for
redesign; 4 the round limit is reached, the owner decides; 5 a reviewer gave no usable answer;
6 the environment refuses (a sandbox, no login, reviewer unavailable).

example: jst review origin/main..HEAD --task parser-fix --reviewer codex
```

### jst review check

Is this change reviewed and clear?

```text
usage: jst review check [--task NAME] BASE..REV

  --task NAME   the task (default: the branch)

exit: 0 reviewed and clear; 1 not clear, each reason named; 2 the request or configuration.

example: jst review check origin/main..HEAD --task parser-fix
```

### jst review last

The newest round's report, or the newest attempt's failure.

```text
usage: jst review last [--task NAME] [--json]

  --task NAME   the task (default: the branch)
  --json        as JSON

exit: 0 shown, whatever the round's outcome; 2 the request or configuration, a task with no review,
or a record that cannot be read.

example: jst review last
```

### jst review list

The reviews kept.

```text
usage: jst review list [--task NAME] [--rebuild] [--json]

  --task NAME   only this task
  --rebuild     write the index anew from the folders first
  --json        as JSON lines

exit: 0 the list; 2 the request or configuration, or the index cannot be read or written anew.

example: jst review list --task parser-fix
```

### jst review pool

Queue, inspect and maintain local reviews across sessions.

```text
usage: jst review pool add --repo ABSOLUTE_PATH --base REF --head REF --task NAME --reviewer NAMES
       jst review pool list [--json]
       jst review pool inspect [--json]
       jst review pool prune [--apply] [--json]
       jst review pool migrate [--apply] [--json]
       jst review pool subscribers JOB_ID [--json]
       jst review pool unsubscribe JOB_ID SESSION_ID --generation TOKEN [--apply] [--json]
       jst review pool next
       jst review pool retry JOB_ID

A local, persistent, opt-in queue shared by agent sessions. add pins both Git revisions now;
NAMES is codex, claude-code, or both separated by a comma. next runs one queued review in
the foreground from its saved repository. It starts no background daemon. retry requeues a
failed attempt without changing its commits. list reads local jobs; inspect reports queue
health and notification handoffs without repository or session details. prune previews
expired terminal history; migrate previews a queue schema upgrade. Both require --apply
to write. prune retains active work and terminal outcomes awaiting chat delivery.
subscribers shows recipient IDs and the job generation. unsubscribe previews closing
one subscription, and requires --apply and that generation to make the change.

exit: 0 request or maintenance succeeded, or no pending job; 1, 3, 4 a completed review verdict;
2 invalid request or queue state; 5, 6 review failed; 2 when another worker holds the pool.

example: jst review pool add --repo /path/to/repo --base main --head HEAD --task parser --reviewer codex
```

### jst review prune

Remove tasks past review.keep_days, now.

```text
usage: jst review prune

exit: 0 done (nothing to remove included); 2 the request or configuration, or a removal failed.

example: jst review prune
```

### jst review show

One round's report.

```text
usage: jst review show TASK ROUND [--json]

  --json   the round's record

exit: 0 shown; 2 the request or configuration, or no such round (its file cannot be read).

example: jst review show parser-fix 2
```

## Chat and agents

| Command | What it does |
|---|---|
| [`jst chat claude-hook`](#jst-chat-claude-hook) | announce pending chat at a Claude Code boundary |
| [`jst chat codex-bridge`](#jst-chat-codex-bridge) | deliver pending chat to a managed Codex turn |
| [`jst chat serve`](#jst-chat-serve) | serve repository-local agent session chat over MCP |
| [`jst brief claude-hook`](#jst-brief-claude-hook) | restore a brief after Claude Code compaction |
| [`jst brief clear`](#jst-brief-clear) | clear this worktree's brief while preserving its revision |
| [`jst brief save`](#jst-brief-save) | save a task brief for this worktree |
| [`jst brief show`](#jst-brief-show) | show this worktree's saved task brief |
| [`jst guard`](#jst-guard) | the Claude Code hook that refuses destructive commands |
| [`jst inventory`](#jst-inventory) | local Git worktrees and observed runner sessions as JSON |
| [`jst timeline`](#jst-timeline) | local PR, test, review and background stages as JSON |
| [`jst workspace board`](#jst-workspace-board) | read local status across named repositories |
| [`jst owner`](#jst-owner) | what the owner said in a Claude Code session, or where its tool calls went |
| [`jst log`](#jst-log) | what jackstay did: commands, a run's tree, a Claude session, a tool call |
| [`jst stats`](#jst-stats) | reports over jackstay's journal |

### jst chat claude-hook

Announce pending chat at a Claude Code boundary.

```text
usage: jst chat claude-hook --name NAME --event UserPromptSubmit|PostToolUse|PostToolBatch

Run from a Claude Code command hook at the named event. Use the same
JACKSTAY_CHAT_SESSION_TOKEN as this session's jst chat serve MCP process.
Enable chat.claude_code in jackstay.json. Pending messages are announced to
Claude at the next configured boundary and remain pending until chat.ack.

exit: 0 no message or notice emitted; 1 chat setup unavailable; 2 invalid arguments.

example: jst chat claude-hook --name reviewer --event PostToolUse
```

### jst chat codex-bridge

Deliver pending chat to a managed Codex turn.

```text
usage: jst chat codex-bridge --socket PATH --thread ID --name NAME

Attach to an owner-managed Codex app-server Unix socket and one thread. Start the
MCP server and the bridge with the same JACKSTAY_CHAT_SESSION_TOKEN. Enable
chat.codex_bridge in jackstay.json. The bridge delivers to a running turn and
leaves the message pending until the recipient calls chat.ack.

exit: 0 stopped; 2 invalid setup or another bridge owns the session.

example: jst chat codex-bridge --socket "$APP_SOCKET" --thread "$APP_THREAD" --name reviewer
```

### jst chat serve

Serve repository-local agent session chat over MCP.

```text
usage: jst chat serve --name NAME

Serve a local stdio MCP session. Set JACKSTAY_CHAT_SESSION_TOKEN to a random 32+ character secret in the host launcher to resume an identity. The project must enable chat in jackstay.json. Messages are delivered when recipients poll their inbox.

exit: 0 end of input; 2 invalid configuration, arguments or transport.

example: jst chat serve --name reviewer
```

### jst brief claude-hook

Restore a brief after Claude Code compaction.

```text
usage: jst brief claude-hook

Use in an opt-in Claude Code SessionStart hook with matcher compact. Emits context only for this exact worktree and a fresh brief.

exit: 0 context or no context; 2 invalid arguments.

example: jst brief claude-hook
```

### jst brief clear

Clear this worktree's brief while preserving its revision.

```text
usage: jst brief clear --expect REV

Leave a revision tombstone, so stale writers cannot overwrite a later task.

exit: 0 cleared; 2 invalid arguments, conflict or unavailable state.

example: jst brief clear --expect 1
```

### jst brief save

Save a task brief for this worktree.

```text
usage: jst brief save --expect REV [--max-age DURATION] < brief.json

Save explicit JSON with task_id, task, next (1–12 strings), optional decisions (up to 12 strings). REV is the current revision from `brief show`, initially 0. Max age defaults to `brief.max_age_hours` in jackstay.json, else 168h, and must be 1h–720h. No transcript is read.

exit: 0 saved; 2 invalid input, conflict or unavailable state.

example: jst brief save --expect 0 < brief.json
```

### jst brief show

Show this worktree's saved task brief.

```text
usage: jst brief show

Print JSON with revision, freshness and brief (null after clear).

exit: 0 printed; 2 invalid arguments or unavailable state.

example: jst brief show
```

### jst guard

The Claude Code hook that refuses destructive commands.

```text
usage: jst guard                                the hook: a tool call on standard input
       jst guard check [--cwd DIR] -- 'COMMAND'

Also installed as jst-guard. A refusal names what to run instead; a project adds rules in
jackstay.json's guard section.
  check           judge COMMAND as the hook would, without a tool call
  --cwd DIR       with check: judge it as run in DIR (default the working folder)

exit: the hook 0 always (a refusal is in its answer); check: 0 allowed, 2 refused (the reason on
standard error); 64 the words are wrong.

example: jst guard check -- 'git push --force origin main'
```

### jst inventory

Local Git worktrees and observed runner sessions as JSON.

```text
usage: jst inventory [--since DURATION]

Print the local repository, its linked worktrees, and recent session observations as JSON.
  --since DURATION  look back at most this long (default 24h, maximum 720h)

Runner and guard coverage are reported from recorded evidence. Missing evidence is unknown.
exit: 0 report written; 1 journal incomplete; 2 bad request or repository unavailable.

example: jst inventory --since 48h
```

### jst timeline

Local PR, test, review and background stages as JSON.

```text
usage: jst timeline [--since DURATION] [--until RFC3339] [--limit N]

Print local delivery stages for this Git repository as versioned JSON.
  --since DURATION  window before --until (default 24h, maximum 720h from now)
  --until RFC3339   exclusive end of the window (default now)
  --limit N         keep at most N of the newest rows (default 100, maximum 1000)

exit: 0 report written; 1 incomplete journal or ancestry scan; 2 bad request or repository unavailable.

example: jst timeline --since 4h --limit 50
```

### jst workspace board

Read local status across named repositories.

```text
usage: jst workspace board [--json] [REPO...]

Read a summary of each explicitly supplied Git checkout. Linked worktrees share chat
state; gate and receipt summaries are for the named checkout. No remote service is contacted.
No message bodies, receipt contents, or card bodies are printed.
At most 32 repositories may be supplied in one call. With none named, the repositories are the
current project's status.repos in jackstay.json (relative to its folder).

exit: 0 every requested source read; 1 at least one source is unknown or partial; 2 invalid use.

example: jst workspace board --json ~/src/app ~/src/service
```

### jst owner

What the owner said in a Claude Code session, or where its tool calls went.

```text
usage: jst owner [--session S|all] [--kind typed|queued|widget|declined] [--grep RE] [--since D] [--max-chars N]
       jst owner --stats [--session S]

  --session S|all  which session: a substring of its file's name, or all (default: the newest)
  --kind KIND      only typed, queued, widget (an answer to a question) or declined messages
  --grep RE        only messages matching RE (RE2, case-insensitive)
  --since D        only messages from that ISO time prefix on (2026-09-11)
  --max-chars N    only messages of at most N characters
  --stats          where the session's tool calls went, instead of the messages

exit: 0 read (also when the filters match nothing, said so); 1 nothing to read (no transcript
folder, no matching session file, an empty one) or a file that cannot be read; 2 a bad request;
5 the transcripts are in a shape jackstay does not know (also: no message of the owner's, or
--stats finding no tool call); 6 a widget answer in a spelling jackstay does not know.

example: jst owner --session all --grep 'force-push'
```

### jst log

What jackstay did: commands, a run's tree, a Claude session, a tool call.

```text
usage: jst log [--since DUR|TIME] [--until TIME] [--project P | --all-projects] [--session ID]
              [--trace ID] [--tool-call ID] [--command NAME] [--outcome WORD|error] [--tree] [--json] [--limit N]

Reads jackstay's journal: commands, the guard's decisions, test runs, lock waits, started processes,
review rounds.
  (no option)        this project's commands of the last day, one line each
  --session ID       a Claude session's (every project, 30 days; $CLAUDE_CODE_SESSION_ID is this one)
  --trace ID         one run's tree: phases, waits, children with exit codes and signals, stacks
  --tool-call ID     the guard's decision on a tool call and the jackstay command it ran
  --command NAME     one command (test, review, guard, ...)
  --outcome WORD     an outcome (refused, failed, ...); error: every span that ended in error
  --since DUR|TIME   2h, 7d, 2026-11-17 or RFC 3339 (default 24h; 30d with --session/--trace)
  --until TIME       the end of the range (default now)
  --project P        that project's; --all-projects: every project's
  --tree             show each run's tree
  --json             one JSON object per span
  --limit N          only the newest N

exit: 0 an answer (an empty one included); 1 no journal could be read; 2 the request is wrong.

example: jst log --session "$CLAUDE_CODE_SESSION_ID" --tree
```

### jst stats

Reports over jackstay's journal.

```text
usage: jst stats [REPORT] [--since DUR|TIME] [--until TIME] [--project P | --all-projects]
                [--session ID] [--by version|project|day] [--json] [--claude]

Computed from jackstay's journal when asked. A span counts at the moment it ended; its time is
clipped to the range. Without REPORT, the list of reports:
  guard        calls by outcome, answer latency by version, failures with caller and stack
  refusals     per rule: refusals, share, retried within 2 minutes, given up
  flaky        test runs of one key and head with both a pass and a failure
  tests        per project: runs by outcome, time in failed and stopped runs
  phases       each phase's and lock wait's count, median, p95, max
  reviews      per task: rounds, attempts, Codex runs and failures, tokens, minutes
  background   jst bg starts and ends; background asked for outside jst bg
  sessions     per Claude session: jackstay's time, refusals; --claude adds the session's tokens and cost
  load         per project and day: busy time, the most commands at once
  --since DUR|TIME   the start of the range (default 30d); --until TIME: its end
  --project P        that project's; --all-projects: every project's
  --session ID       one Claude session's
  --by WHAT          group by version, project or day
  --json             as JSON
  --claude           with sessions: add the session's tokens and cost

exit: 0 an answer; 1 no journal could be read; 2 the request is wrong.

example: jst stats refusals --by project --since 7d
```

## Setup and health

| Command | What it does |
|---|---|
| [`jst install`](#jst-install) | put a whole version into PREFIX/lib/jackstay-\<id\> and make it live in one rename |
| [`jst uninstall`](#jst-uninstall) | remove what install wrote (~/.jackstay is left) |
| [`jst setup`](#jst-setup) | plan and apply selected machine setup repairs |
| [`jst doctor`](#jst-doctor) | is jackstay alive on this machine: the layout, the guard, its calls, the sandbox |
| [`jst adopt`](#jst-adopt) | bring jackstay.json, the AGENTS block, the guard hook and workflows to jackstay's reference |
| [`jst check`](#jst-check) | is this project's jackstay.json usable? |
| [`jst profile export`](#jst-profile-export) | export an offline adoption profile bundle |
| [`jst profile verify`](#jst-profile-verify) | verify a portable adoption profile bundle |
| [`jst patterns`](#jst-patterns) | the patterns of a jackstay.json that are not RE2 |
| [`jst version`](#jst-version) | the version and the commit it was built from |
| [`jst help`](#jst-help) | the commands |

### jst install

Put a whole version into PREFIX/lib/jackstay-\<id\> and make it live in one rename.

```text
usage: jst install [--prefix DIR] [--checkout DIR | --binary FILE]

Without --checkout or --binary: the checkout around the working folder, else the running binary.
On macOS, when ~/.jackstay/.all/codesign-identity names a code-signing identity (as
`security find-identity -v -p codesigning` prints it), an unsigned or ad-hoc signed binary is
signed with it (identifier jst) and verified before it goes live; a release's notarised signature
is kept. A live install of the same version without that signature is replaced.
  --prefix DIR     install under DIR (default ~/.local)
  --checkout DIR   build the binary from that jackstay checkout
  --binary FILE    install that built binary

exit: 0 installed; 1 a step failed half-way (the message says what is left); 2 refused, nothing
was changed.

example: jst install --checkout ~/repositories/jackstay
```

### jst uninstall

Remove what install wrote (~/.jackstay is left)

```text
usage: jst uninstall [--prefix DIR]

  --prefix DIR   remove what install wrote under DIR (default ~/.local); ~/.jackstay is left

exit: 0 removed, or nothing was installed there; 1 something could not be removed (each named); 2 a
bad request, a held install lock or a prefix that cannot be read: nothing changed.

example: jst uninstall
```

### jst setup

Plan and apply selected machine setup repairs.

```text
usage: jst setup [--plan] [--json] [--chat] [--prefix DIR] | jst setup --yes --select path|sandbox [--prefix DIR] | jst setup --autonomy MODE

Plan repairs from doctor findings. Available repairs add the installed bin folder to a supported
POSIX shell profile and allow jackstay's state folder in Claude Code's enabled sandbox.
  --plan          print the plan without changing files (also the nonterminal default)
  --json          print the plan as JSON; implies --plan
  --prefix DIR    inspect an install under DIR (default: this binary's own prefix)
  --chat          show session chat and agent-engine setup steps
  --yes           apply only explicitly selected repair IDs; requires --select
  --select ID     select path or sandbox; repeat to select both
  --autonomy MODE keep merge-when-green or prepare as this machine's answer to who merges

Once per machine, a terminal is also asked who merges a project's changes: the agent when the
required checks pass, or a person. `jst adopt` offers that answer first when it writes a project's
land section; the project's jackstay.json decides.

Mail steps report detected engines and their current integration limits. They are guidance only;
setup does not install an unverified bridge or claim active-turn delivery. Without options, a
terminal offers apply, show and skip. An existing profile is copied to a
unique backup beside it before replacement. Unsupported shells and conflicting profiles are
left for the owner. Open a new shell after applying a PATH repair.

exit: 0 planned or applied; 1 an applicable repair failed; 2 the request is wrong.

example: jst setup --plan --json
```

### jst doctor

Is jackstay alive on this machine: the layout, the guard, its calls, the sandbox.

```text
usage: jst doctor [--json] [--prefix DIR] | jst doctor --expect NAME | jst doctor --forget NAME

Checks the installed layout, the jst on PATH, the guard refusing, its logs of the last 7 days, the
projects heard from and expected, Claude Code's sandbox settings and the lock.
  --json          the report as one JSON object
  --prefix DIR    the install prefix to inspect (default: this binary's own)
  --expect NAME   add project NAME to the projects whose guard is expected to be heard from
  --forget NAME   take project NAME off that list

exit: 0 no problem; 1 problems, each named; 2 the request is wrong.

example: jst doctor
```

### jst adopt

Bring jackstay.json, the AGENTS block, the guard hook and workflows to jackstay's reference.

```text
usage: jst adopt [--plan] [--settings-only] [--profile PATH] [--profile-sha256 DIGEST] [--json] [DIR]
       jst adopt --apply [--accept ID]... [--pin] [--settings-only] [--autonomy MODE] [--profile PATH] [--profile-sha256 DIGEST] [DIR]
       jst adopt --block [--profile PATH] [--profile-sha256 DIGEST] [DIR]

Works on the project around DIR (default: here) and changes nothing else; what it must not decide
is a conflict.
  --plan           what would change and the conflicts left; writes nothing (the default)
  --apply          write the changes; with a conflict left, nothing is written
  --accept ID      with --apply: overwrite what conflict ID protects, when its settlement says
                   --accept (repeatable)
  --pin            with --apply: set adopt.pin, the jst release the workflows download, to this one
  --settings-only  plan or write jackstay.json alone; land it in its own pull request, then
                   --apply again on the merged base
  --autonomy MODE  merge-when-green or prepare: who merges, for a new land section (at a
                   terminal --apply asks; without one it needs this flag); an existing one is kept
  --profile PATH   an organisation's profile; --apply records it in jackstay.json
  --profile-sha256 DIGEST  the exact bundle bytes to accept; recorded by --apply
  --json           the plan as one JSON object
  --block          print the AGENTS block this project gets; writes nothing

exit: 0 adopted (--plan: nothing to change, no conflict; --apply: written); 1 conflicts left;
2 usage, or jackstay.json or the profile cannot be read, or --apply needs --autonomy; 3 (--plan)
changes pending, no conflict.

example: jst adopt --plan --json
```

### jst check

Is this project's jackstay.json usable?

```text
usage: jst check [DIR]

Names every problem of every section, and warnings for the test section. DIR defaults to the
working folder.

exit: 0 usable (perhaps with warnings); 2 a problem, or no jackstay.json.

example: jst check
```

### jst profile export

Export an offline adoption profile bundle.

```text
usage: jst profile export PROFILE

Writes the bundle (the profile and its workflow templates) to standard output, and the SHA-256 of
its exact bytes to standard error.

exit: 0 exported; 1 read, validation or write error; 2 bad arguments.

example: jst profile export profile.json > policy.bundle.json
```

### jst profile verify

Verify a portable adoption profile bundle.

```text
usage: jst profile verify --sha256 DIGEST BUNDLE

Take DIGEST from a source other than the bundle. Also checks that this jackstay can use the
bundled profile and templates offline.

exit: 0 verified; 1 digest, profile or template error; 2 bad arguments.

example: jst profile verify --sha256 "$expected" policy.bundle.json
```

### jst patterns

The patterns of a jackstay.json that are not RE2.

```text
usage: jst patterns [DIR...]

Lists the patterns RE2 (Go's dialect) does not take, in the jackstay.json at or above each DIR
(default the working folder).

exit: 0 every pattern is RE2; 1 some are not; 2 a jackstay.json is missing or is not JSON.

example: jst patterns .
```

### jst version

The version and the commit it was built from.

```text
usage: jst version
       jst --version (or -V), the same

It takes no options.

exit: 0 printed; 2 any word after it.

example: jst --version
```

### jst help

The commands.

```text
usage: jst help                  every command with its summary
       jst help COMMAND [WORD]   the same as `jst COMMAND [WORD] --help`

`jst COMMAND --help` (or -h), given as the only word after a command's name, prints
its summary and usage and runs nothing. For the first word of two-word commands
(`jst changes`), it lists those commands.

exit: 0 printed; 2 more than two words, or no command of that name (the usage on standard
error).

example: jst help test last
```

## Maintenance

| Command | What it does |
|---|---|
| [`jst changes check`](#jst-changes-check) | every changelog entry, valid or each problem named |
| [`jst changes collect`](#jst-changes-collect) | write the entries into the changelog (projects without `release`) |
| [`jst changes new`](#jst-changes-new) | a new changelog entry |
| [`jst changes view`](#jst-changes-view) | the next release's section, as it would be written |
| [`jst release notes`](#jst-release-notes) | a release's notes: markdown, text, TestFlight, Sparkle |
| [`jst release override`](#jst-release-override) | a version given by hand, with its reason, as a file for a pull request |
| [`jst release pr`](#jst-release-pr) | the release workflow's step: create, refresh or close the release pull request |
| [`jst release prepare`](#jst-release-prepare) | print the next release as the workflow would make it; writes nothing |
| [`jst release publish`](#jst-release-publish) | the release workflow's step: make, tag and publish a landed release |
| [`jst release yank`](#jst-release-yank) | mark a release yanked, as an entry for a pull request |
| [`jst cards`](#jst-cards) | what landed per card, read from history |
| [`jst lint pr`](#jst-lint-pr) | a pull request's Card lines, its changelog entry and the tree's rules |
| [`jst lint range`](#jst-lint-range) | every commit in A..B has valid Card lines |
| [`jst lint tree`](#jst-lint-tree) | the tree's rules (numbers, new cards, snapshots, records) |
| [`jst id`](#jst-id) | hand out numbers from one counter per project on this machine |
| [`jst new`](#jst-new) | a numbered record as a file of its own |
| [`jst view`](#jst-view) | print a series' records as a table |

### jst changes check

Every changelog entry, valid or each problem named.

```text
usage: jst changes check [--json]

  --json   the result as one JSON object

exit: 0 every entry is valid; 1 problems, each named; 2 jackstay.json cannot be used.

example: jst changes check
```

### jst changes collect

Write the entries into the changelog (projects without `release`)

```text
usage: jst changes collect --version V [--date D] [--dry-run]

Writes the entries as version V's section and removes them. With a release section,
`jst release prepare` and its pull request do this instead.
  --version V   the section's version (required)
  --date D      the date in the heading (default today)
  --dry-run     print the section and change nothing

exit: 0 collected (or printed with --dry-run, or the section was already there); 1 nothing or not
all collected (the reason named: no entry, an invalid entry, uncommitted changes, a changed
changelog or entry, the heading there with other entries); 2 the request, jackstay.json, a project
with a release section, or a file that cannot be read or written.

example: jst changes collect --version 1.2.0 --dry-run
```

### jst changes new

A new changelog entry.

```text
usage: jst changes new --slug SLUG [--type TYPE] [--audience AUDIENCE] [--title TEXT] [--migration TEXT] [--card ID]

Prints the new file's path. With a release section, --type, --audience and --title are required.
  --slug SLUG        the entry's name: lower-case letters, digits and -, at most 80
  --type TYPE        breaking, feature, fix, deprecated, security or note
  --audience WHO     consumer or internal
  --title TEXT       the entry's title, one line
  --migration TEXT   what a consumer changes (required with --type breaking)
  --card ID          the card the change is for (needs a cards section)

exit: 0 written, its path printed; 1 the entry would not be valid; 2 the request (a bad or taken
slug, a missing option), jackstay.json, the template or the release state cannot be used.

example: jst changes new --slug faster-lock --type fix --audience consumer --title 'The lock waits less'
```

### jst changes view

The next release's section, as it would be written.

```text
usage: jst changes view [--version V] [--date D] [--audience AUDIENCE]

  --version V        the version in the heading
  --date D           the date in the heading (default today)
  --audience WHO     consumer (default), internal or all

exit: 0 printed (the section, or that there is no entry); 2 the request, jackstay.json or the
release state cannot be used, or (release section, no --version) no entry moves the version.

example: jst changes view --version 1.4.0
```

### jst release notes

A release's notes: markdown, text, TestFlight, Sparkle.

```text
usage: jst release notes [VERSION] [--format FORMAT] [--audience AUDIENCE]

Without VERSION, the next release's.
  --format FORMAT     markdown (default), text, testflight or sparkle-html
  --audience WHO      consumer (default), internal or all; testflight and sparkle-html are for
                      consumers only

exit: 0 printed; 1 VERSION is not a release on this branch, an entry is invalid, or the testflight
notes do not fit even as titles; 2 the request, jackstay.json or the release state cannot be used.

example: jst release notes 1.3.0 --format text
```

### jst release override

A version given by hand, with its reason, as a file for a pull request.

```text
usage: jst release override --version V --reason TEXT

  --version V      the version the next release gets (required)
  --reason TEXT    why; recorded in the release (required)

exit: 0 written (or already there, the same), its path printed; 1 V is not greater than the last
release, or another override is there; 2 the request, jackstay.json or the release state cannot be
used.

example: jst release override --version 2.0.0 --reason 'the new file format'
```

### jst release pr

The release workflow's step: create, refresh or close the release pull request.

```text
usage: jst release pr [--dry-run]

Outside GitHub Actions it is always a dry run.
  --dry-run   say what would be done; write nothing

exit: 0 done, or nothing to do; 1 a problem reported, the rest done (the base's entries cannot be
released or prepared, a pull request not closed, the pull request kept changing), or GitHub's rate
limit; 2 the request, jackstay.json, the release state, git or GitHub cannot be used.

example: jst release pr --dry-run
```

### jst release prepare

Print the next release as the workflow would make it; writes nothing.

```text
usage: jst release prepare [--json]

Shows version, notes and files.
  --json   the plan as one JSON object

exit: 0 the plan; 1 nothing to release (no entry bumps the version, no override), or an entry is
invalid; 2 the request, jackstay.json or the release state (the checkout, the history, the version
files) cannot be used.

example: jst release prepare
```

### jst release publish

The release workflow's step: make, tag and publish a landed release.

```text
usage: jst release publish [--dry-run]

Outside GitHub Actions it is always a dry run.
  --dry-run   say what would be done; write nothing

exit: 0 done, or nothing to do; 1 a problem reported, the rest done (a tag on another commit, a
trailer or tag on no release, a draft release, a tag or release not made, the base kept moving), or
GitHub's rate limit; 2 the request, jackstay.json, the release state, git or GitHub cannot be used,
or the release the request asks for could not be made.

example: jst release publish --dry-run
```

### jst release yank

Mark a release yanked, as an entry for a pull request.

```text
usage: jst release yank X.Y.Z --reason TEXT

  --reason TEXT   why the release is yanked (required)

exit: 0 written (or already there, the same), its path printed; 1 X.Y.Z is not a release on this
branch, or its entry file is there with other content; 2 the request, jackstay.json or the release
state cannot be used.

example: jst release yank 1.3.1 --reason 'it loses the settings file'
```

### jst cards

What landed per card, read from history.

```text
usage: jst cards [ID...] [--json] [--ref REF]
       jst cards --check [--json]
       jst cards --stale [--commit-map FILE]
       jst cards snapshot [--write]
       jst cards compare FILE

Each card's state and its commits, read from the history of cards.branch.
  ID...               only these cards
  --json              as JSON
  --ref REF           read REF's history instead of cards.branch
  --check             only the disagreements between cards and history
  --stale             hashes in cards' commits fields that are not on the branch
  --commit-map FILE   with --stale: a file of old and new hashes, to print both
  snapshot            print what landed, as a snapshot
  --write             with snapshot: write it to <cards.snapshots>/landed-<date>.json
  compare FILE        after a history rewrite: the entries of snapshot FILE that history lost

exit: 0 shown (--stale too, whatever it lists); 1 --check found a disagreement, or compare found
an entry history no longer has or commits that landed after the snapshot; 2 the request,
jackstay.json, the cards folder, git or a file cannot be used; 5 git log gave a record of another
shape.

example: jst cards W-012 W-013
```

### jst lint pr

A pull request's Card lines, its changelog entry and the tree's rules.

```text
usage: jst lint pr (--event FILE | --pr N | --body-file F) [--rev REV] [--base B] [--head H] [--head-branch NAME]

Exactly one of --event, --pr, --body-file gives the description.
  --event FILE        a GitHub pull_request event file
  --pr N              that pull request, read from GitHub
  --body-file F       a description in a file
  --rev REV           judge that commit's tree instead of the working tree
  --base B            the base commit
  --head H            the head commit
  --head-branch NAME  the head branch

exit: 0 no problem; 1 problems, each named, or (--pr) the head does not merge with the base
without conflicts; 2 the request, jackstay.json, git or GitHub cannot be used; 5 the --event file
is not JSON, or not a pull_request event as GitHub documents it.

example: jst lint pr --body-file pr-body.md --base origin/main --head HEAD
```

### jst lint range

Every commit in A..B has valid Card lines.

```text
usage: jst lint range A..B

exit: 0 every commit is valid; 1 problems, each named; 2 the request, jackstay.json, the card
files or git cannot be used; 5 git log gave a record of another shape.

example: jst lint range origin/main..HEAD
```

### jst lint tree

The tree's rules (numbers, new cards, snapshots, records)

```text
usage: jst lint tree [--rev REV]

  --rev REV   judge that commit's tree instead of the working tree

exit: 0 no problem; 1 problems, each named; 2 the request, jackstay.json or git cannot be used.

example: jst lint tree
```

### jst id

Hand out numbers from one counter per project on this machine.

```text
usage: jst id next SERIES [--count N] [--json]
       jst id list [SERIES] [--json]
       jst id peek SERIES

Series come from jackstay.json's ids. A number is never handed out twice, whatever branch or
worktree asks.
  next SERIES   hand out the next number of SERIES, recorded
  --count N     with next: hand out N numbers (1 to 100)
  list          the numbers handed out, of every series or of SERIES
  peek SERIES   the number next would hand out; nothing is recorded
  --json        as JSON

exit: 0 done; 2 the request, the series, jackstay.json, git or the record cannot be used (no
unrecorded number is printed; one recorded before the failure is named as a gap).

example: jst id next W
```

### jst new

A numbered record as a file of its own.

```text
usage: jst new SERIES --slug SLUG [--card ID] [--set KEY=VALUE]...

SERIES is one of jackstay.json's records. Takes the next number from `jst id next`, fills id
(and card) into the series' template, and prints the file's path.
  --slug SLUG        the file name's words after the number (required)
  --card ID          the card the record is for (needs a cards section)
  --set KEY=VALUE    a field of the template set to VALUE (repeatable; not id)

exit: 0 written, its path printed; 2 the request, jackstay.json, the counter, the template or the
records folder cannot be used, or the file exists (its number is spent, a gap); no file is written.

example: jst new W --slug rename-the-lock
```

### jst view

Print a series' records as a table.

```text
usage: jst view SERIES [--where KEY=VALUE]... [--format table|list|json]

  --where KEY=VALUE   only records whose field KEY is VALUE (repeatable; all must hold)
  --format FORMAT     table (default), list or json

exit: 0 printed; 1 a record file could not be read (named on standard error; the other rows are
printed); 2 the request, jackstay.json or the records folder cannot be used.

example: jst view W --where status=open
```
