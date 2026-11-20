# Background processes and waiting

A dev server, watcher or log follower you start with `jst bg` ends on its own: at a deadline, or
when the session that started it is gone. `jst wait` replaces the hand-written
`until …; do sleep 1; done` loop with one command that has a deadline.

## Start a process with a deadline: `jst bg`

```sh
jst bg --for 20 --name server -- npm run dev
jst bg --for 45 --name review-parser -- jst review origin/main..HEAD --task parser --reviewer codex
```

`jst bg` stays in front of its own call and supervises the command, so start it the way you start
any background command: with the agent tool's background flag, or `&` in a terminal.

- `--for MINUTES`: the deadline. Default `procs.default_minutes` (30), never more than
  `procs.max_minutes` (240).
- `--name NAME`: a name to stop it by.
- `--keep`: no deadline or session check; only `jst procs stop` ends it. If jackstay cannot save
  the process entry at startup, it requests shutdown immediately.

The command runs in a session of its own. Besides the deadline, `jst bg` checks every 30 seconds
that the process it was started under still lives. In a Claude Code session with the guard hook,
that is the session's own process, so the command ends when the session does. Elsewhere (a
terminal, an agent without the hook) it watches the nearest process that may be a session's, or
the deadline alone, and says so. After the command ends, whatever it left in its session is
stopped too.

A process holding a file open for writing at the deadline is not stopped mid-write: its entry says
`held`, and it is stopped once the write is done. `jst procs stop` makes no such exception.

`jst bg` exits with the command's own status. Every flag and exit status: `jst bg --help`.

## See and stop them: `jst procs`

```sh
jst procs
jst procs stop server
jst procs stop --all server
jst procs stop 1794955688-45037
```

`jst procs` lists this session's entries (`--all`: every session's) with id, name, state
(`running`, `held`, `orphaned`, `gone`), age, deadline, session, folder and command. New entries
record arguments using the project's telemetry masking mode. On the way it stops orphans past their
deadline and removes gone entries.

`stop` sends TERM, waits 5 seconds, then sends KILL. By ID it stops that one, whoever started it.
By NAME it stops this session's entries of that name; when jackstay cannot tell which session this
is (a plain terminal), a NAME is refused: give the ID, or `--all NAME` for every session's. A task
whose cleanup could not be confirmed stays listed as `cleanup-incomplete`, and `stop` exits 1.

## Wait for something: `jst wait`

```sh
log=$(mktemp)
jst bg --for 30 --name api -- sh -c './serve > "$1" 2>&1' sh "$log" &
jst wait --line 'listening on' --in "$log" --fail 'error' --timeout 60
jst wait --quiet 10 --in build.log --timeout 300
jst wait --pid 4242 --timeout 120
```

The first three lines start a server with a fresh log of its own, in the background, and wait for
it to listen. `jst wait` takes exactly one condition:

| Condition | Waits until |
|---|---|
| `--pid PID` | the process has ended |
| `--file PATH` (`--nonempty`) | the file exists (and holds something) |
| `--gone PATH` | the file or folder is gone |
| `--line REGEX --in PATH` | a line of the file matches; the line is printed |
| `--quiet SECONDS --in PATH` | the file has not grown for that long |

- `--timeout`: the deadline, default 540 seconds, under a tool call's ten minutes. Exit 3 when it
  comes first.
- On the way out it prints the file's last lines (`--tail`, default 20), so nobody reads the file
  again.
- `--fail REGEX` ends at once with 1 when a line of the `--in` file matches it.
- `--while-pid PID` ends with 1 when the writing process dies first.
- `--new` counts only lines written after `jst wait` started, for a log that already holds an old
  "listening" line. A line written before the waiter starts is then missed, so prefer a fresh log,
  as above.

Every flag and exit status: `jst wait --help`.

## What can go wrong

- **A daemon that leaves its process tree** (`setsid`, a double fork) cannot be caught. Prefer a
  server's foreground mode.
- **Something started without `jst bg`** is invisible to it. With the Claude Code hook, the guard
  counts such requests (`jst stats background`) but refuses none.
- **`jst procs stop NAME` refused**: this terminal is not a known session; use the ID or `--all`.
- **Inside an agent sandbox**, a process the sandbox did not start cannot be signalled; jackstay
  says so and names what to run outside it.
- **On Linux**, the held-file check needs `lsof`. Without an answer from it, the deadline wins.

The design: [implementation.md, section 5](../design/implementation.md#5-processes-jst-bg-jst-procs-jst-wait-jst-lock).
