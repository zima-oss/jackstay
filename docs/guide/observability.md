# What jackstay did

You can find out afterwards what jackstay did: every command, guard decision, test run, lock wait,
background process and review round is written to a local journal, and `jst log` and `jst stats`
read it. `jst inventory` relates recent runner observations to local Git worktrees. `jst timeline` brings
PR, test, review and background stages into one time window.
`jst doctor` says whether jackstay is healthy on this machine; `jst owner` reads what you said
in a Claude Code session.

## The journal

One file per project and day, `~/.jackstay/<project>/telemetry/<date>.otlp.jsonl`, as
OpenTelemetry spans in the standard OTLP JSON encoding, so any OTLP file reader can open it.
Nothing is sent anywhere. Secrets in command text are masked; `"commands": "skeleton"` records only
the shape of commands.

```json
"telemetry": {"keep_days": 180, "compress_after_days": 14, "commands": "masked"}
```

`JACKSTAY_TELEMETRY=off` turns the journal off for a process. A span that cannot be written is
lost; no command fails for it.

## Local inventory

```sh
jst inventory --since 48h
```

The JSON report gives the current worktree path, local repository ID, registered worktrees and
recent session observations. `--since` defaults to 24 hours and accepts Go durations up to 720
hours. Each session row's `worktree_id` refers to a worktree in that report.

The repository ID is derived from Git's resolved common metadata directory. Linked worktrees
share it. Independent clones have different IDs even if their configured project names match.
These IDs are for this machine and metadata location. Moving that directory changes the ID.
Prunable worktrees remain in the report, with no ID when their metadata cannot be read.

Observations come from the local journal. The command associates a session with a worktree using
its recorded path or the path of its trace ancestor. A nested independent checkout keeps its own
history. Explicit Claude Code and Codex evidence identifies the runner. Records without enough
evidence, including ordinary command records with no session ID, appear as `unknown`. A recorded
guard decision shows that the guard ran for a session. Missing decisions leave coverage `unknown`.
The report contains session IDs and local paths, so consider its audience before sharing it.

Exit 1 indicates missing parts of the journal, such as a corrupt line. The JSON contains the
observations that could be read. Exit 2 indicates invalid options or unavailable Git information.

## Delivery timeline

```sh
jst timeline --since 4h --limit 50
jst timeline --since 2h --until 2026-11-18T16:00:00Z
```

The versioned JSON report covers this repository and its linked worktrees. Rows give a stage,
status, UTC timestamp and duration in milliseconds. A command or phase is shown at its completion
time. Background lifecycle points use the event timestamp and a zero duration. Repeated points of
the same kind on one background span contribute their latest point in the window. Machine-wide
`procs` operations lack the stopped group's repository path, so their lifecycle points are omitted.

Trace, span and parent identifiers link recorded steps. A row with its own worktree location belongs
there. A row without a location can inherit its trace ancestor's worktree. Rows with an unverified
location or ancestry are omitted, even if the project name matches. New GitHub records can identify
the PR by an opaque repository digest and its number. Older GitHub records appear under
`github.command`. A commit or run identifier is included where recorded. Recorded review-agent
tokens are shown when available. The report excludes raw arguments, prompts, paths and model names.

The interval includes its start and excludes its end. The default is the last 24 hours and a limit
of 100 rows. The command keeps the newest matching rows up to `--limit` (maximum 1000) and presents
them oldest to newest. The ancestry scan retains at most 8192 recent spans. If the scan exceeds that
budget, `scan_partial` is true and some rows may lack enough ancestry to be shown.
The timeline skips journal lines larger than 16 MiB and counts them as unreadable.

Exit 1 produces a partial report when the ancestry scan is truncated, journal lines are unreadable
or files cannot be read. Exit 2 reports an invalid request or unavailable Git metadata.

## Read the journal: `jst log`

```sh
jst log
jst log --command guard --outcome refused --since 7d
jst log --session "$CLAUDE_CODE_SESSION_ID" --tree
```

- No option: this project's commands of the last day, one line each.
- `--session ID`: everything a Claude Code session did through jackstay, across projects
  (`$CLAUDE_CODE_SESSION_ID` is the current one).
- `--trace ID`: one run's tree: phases, waits, children with exit codes and signals.
- `--tool-call ID`: the guard's decision on one tool call, and what it led to.
- `--command NAME`, `--outcome WORD` (`refused`, `failed`, …, or `error` for every span that ended
  in one).
- `--json`: one JSON object per span, for `jq`.

Every flag: `jst log --help`.

## Count over the journal: `jst stats`

```sh
jst stats refusals --by project --since 7d
jst stats flaky
jst stats guard --by version
```

The first counts the guard's refusals per rule and project over 7 days, with how often each was
retried within 2 minutes or given up. The second lists test runs of one key and head with both a
pass and a failure. The third shows the guard's answers and latency per jackstay version, so you
see whether a version made things better or worse. Other reports: `tests`, `phases`, `reviews`,
`background`, `sessions` (`--claude` adds each Claude session's tokens and cost) and `load`. The
default range is 30 days. Every report and flag: `jst stats --help`.

## Check the machine: `jst doctor`

```sh
jst doctor
jst doctor --expect my-service
jst doctor --json | jq .problems
```

`jst doctor` names each problem with a fixed kind, and the line after it, `fix:`, says what fixes
it: the command to run, or what to change and where.

```text
PROBLEM: /home/you/.local/bin/jst-guard is missing: hooks that name it do not block anything.
  fix: `jst install --prefix /home/you/.local` from a release or checkout, or the quick install again (docs/guide/install.md)
```

It checks:

- the installed layout (launchers, the live version, leftovers);
- that the installed `jst-guard` refuses a probe;
- the guard's failures in the last 7 days, per project (`failed-open` should be zero);
- which projects' guards were heard from: a project you `--expect` that never called is a problem,
  so a missing hook is noticed;
- Claude Code's sandbox: whether it may write the folders jst needs there, and only those (no evidence);
- the lock's folder;
- inside a project, whether it is up to date with this jackstay (`jst adopt --plan`);
- on macOS, the signing setting and the live binary's signature.

On a new machine `guard-never-called` is expected until a session with the hook makes its first
shell call. A project quiet for more than three days is a note, not a problem. It exits 1 when it
found a problem.

## Find what you said: `jst owner`

```sh
jst owner --session all --grep 'force-push'
jst owner --kind widget --since 2026-11-01
```

`jst owner` reads what the person running a Claude Code session said, from its transcript: typed
messages, messages queued while the agent worked, and answers to the agent's questions. Use it to
find a ruling to quote word for word. `--stats` says where the session's tool calls went instead.
It reads Claude Code's transcripts only (from `CLAUDE_CONFIG_DIR`, else `~/.claude/projects`), for
the checkout it runs in. Every flag and exit status: `jst owner --help`.

## What can go wrong

- **`jst log` shows nothing for a session**: the session ran inside a sandbox that cannot write
  the journal's folder, so its spans were lost; `jst setup` adds it. `jst doctor` says so (`sandbox-blocks-state`); the fix is
  in [Claude Code](claude-code.md#the-sandbox).
- **Unreadable lines**: `jst log` and `jst stats` count lines they could not decode and say so on
  standard error.
- **The journal holds command text.** Masked, local and 0600, but check what you paste from it.

The design: [observability.md](../design/observability.md);
[doctor](../design/implementation.md#77-jst-doctor).
