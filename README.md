# jackstay

jackstay is one Go binary, `jst`, that sits beside the AI coding agents working in your
repositories. It refuses the shell commands that destroy work and names a safe one instead. It runs
your tests, judges their output and records each run, with a receipt keyed by the exact tree that
passed. It starts background processes that stop at a deadline. It runs cold code reviews, keeps
numbers and changelog entries as one file each, prepares releases, and tidies the branches and
worktrees that agents leave behind. Every command answers `--help`, and everything it did is
written to a local journal you can query.

## What it does for a team running many agent sessions

When several agent sessions work on the same machine and the same repositories, the same accidents
repeat: a `git reset --hard` that throws away another session's edits, a "tests pass" that was
never a test run, a dev server that outlives its session, two sessions taking the same decision
number, a branch deleted before its work landed. jackstay removes each of these with a command
that an agent can use without judgement:

| Problem | jackstay |
|---|---|
| Destructive git and `rm -r` commands | The guard: a hook that refuses them and names the safe command (`jst discard`, `jst trash`, `jst tidy`) |
| "The tests pass" with nothing to show for it | `jst test`: a single command is `passed` only when its tests were counted (a gate's line shows each step's outcome); every run is recorded, with its output kept |
| The same suite run again on the same tree | Receipts: a pass keyed by the tree, reused only where you opt in |
| Background processes that never end | `jst bg`: a deadline, and a stop when the session is gone |
| `sleep` loops waiting for a log line or a CI check | `jst wait`, `jst gh ci-wait` |
| Branch and worktree clean-up guessed by hand | `jst tidy`: asks git, GitHub and the process table before it removes anything |
| Two sessions taking one number, one shared changelog | `jst id`, `jst new`, `jst changes`: one counter per machine, one file per record |
| Reviews built by hand around another agent | `jst review`: a cold review by Codex, in rounds, with a verdict |
| "What happened?" | `jst log`, `jst stats`: a local OpenTelemetry journal of every command |

The guard is against accidents, not an adversary: it lowers the rate of mistakes and is not a
security boundary ([SECURITY.md](SECURITY.md)).

## Install

On macOS (Apple silicon or Intel) or Linux (x86-64 or arm64), in sh, bash or zsh:

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
```

[install.sh](install.sh) downloads the newest release for your platform from the
[releases page](https://github.com/zima-oss/jackstay/releases), checks the archive against the
release's `checksums.txt` before it unpacks or runs anything, and lets the binary install itself
into `~/.local` (`JACKSTAY_PREFIX` for another place, `JACKSTAY_VERSION` for another release). It
never uses `sudo`.

`sh` runs the script only once curl has downloaded all of it; a failed download runs nothing and the
line exits with curl's status and message. `-q` keeps your own curl settings out of it, and
`--proto '=https' --tlsv1.2` keeps the download on https. To read the script before you run it,
download it into a fresh folder (so an older copy is never run) and run it from there:

```sh
dir=$(mktemp -d)
curl -q --proto '=https' --tlsv1.2 -fsSL -o "$dir/install.sh" https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh &&
  less "$dir/install.sh" &&
  sh "$dir/install.sh"
```

Then, with `~/.local/bin` on your `PATH`:

```sh
jst --version           # what was installed
jst doctor              # is this machine ready
cd your-project
jst adopt --plan        # what jackstay would write here; `jst adopt --apply` writes it
```

On a new machine `jst doctor` exits 1 with `guard-never-called` ("the guard has never been called
on this machine"). That is expected: it reports it until a project is adopted and a session has run
its guard hook once. Every other problem it reports names its fix.

The [install guide](docs/guide/install.md) has the same steps by hand, `go install`, a build from
source, and what can go wrong.

## Five minutes in a fresh repository

```sh
mkdir demo && cd demo && git init -b main
echo hello > notes.txt && git add . && git commit -m "first commit"

jst adopt               # the plan: what jackstay would write (exit 3: changes pending)
jst adopt --apply       # jackstay.json, AGENTS.md with the tools block, CLAUDE.md, the guard hook
jst adopt               # nothing left to change: exit 0
git add . && git commit -m "Adopt jackstay"   # what adopt wrote belongs to the project
```

Ask the guard about a command, as the hook would:

```sh
jst guard check -- 'git reset --hard'    # refused (exit 2), and it names `jst discard <path>`
echo edit >> notes.txt
jst discard notes.txt                    # back to HEAD; your edit is kept in jackstay's trash
jst trash list
```

Run a command through `jst test` and read the last `jst test:` line:

```sh
jst test -- sh -c 'echo "Ran 3 tests"'
# jst test: outcome=exited-0 (no test count: not proof that tests ran), 0 s
```

`exited-0` is not a pass: nothing counted the tests. Declare a scope with an `evidence` pattern in
`jackstay.json`, and the same command becomes a counted pass:

```json
{
  "jackstay_schema": 2,
  "project": "demo",
  "test": {
    "timeout": 600,
    "scopes": {
      "quick": {"run": ["sh", "-c", "echo 'Ran 3 tests'"], "judge": "steps",
                "evidence": "Ran ([0-9]+) tests?", "when": "before every commit"}
    }
  }
}
```

```sh
jst check               # is jackstay.json usable?
jst test list           # the scopes and when each is required
jst test quick          # jst test: outcome=passed (0 s, counted=3)
jst test last           # the last run again, without running it
```

Start something long-lived with a deadline, and wait for it without a `sleep` loop:

```sh
jst bg --for 10 --name server -- sh -c 'echo listening > server.log; sleep 600' &
jst wait --line listening --in server.log --timeout 30
jst procs --all                   # find the demo's line: its ID is the first column
jst procs stop ID                 # stop it by that ID (a name with --all would stop every
                                  # session's job of that name)
```

See what jackstay did:

```sh
jst log
jst help
```

The [guide](docs/guide/README.md) goes on from here: profiles, the test gate, receipts, GitHub,
reviews and releases.

## Status

jackstay is young. It is used every day by its author, across several projects and many parallel
agent sessions, and it changes quickly. Expect new commands and settings between minor versions;
`jackstay.json` is versioned (`jackstay_schema`) and an older jackstay refuses a file written for a
newer one rather than half-reading it.

## Platforms

- **macOS** on Apple silicon and on Intel.
- **Linux** on x86-64 and arm64. `lsof` must be installed (`jst bg` and `jst tidy` use it).

Windows is not supported.

jackstay works without any agent. The guard hook is written for
[Claude Code](docs/guide/claude-code.md), and `jst review` runs [Codex](docs/guide/codex.md); both
are optional, and each page says what works without them.

## Documentation

- [The guide](docs/guide/README.md), starting with [getting started](docs/guide/getting-started.md):
  install, adopting a project, every group of commands, the Claude Code and Codex integrations.
- [The design](docs/design/implementation.md): the current specification of the binary; its
  section 0 lists the feature specs (the guard, tests, reviews, releases, adoption, the journal).
- `jst help` and `jst <command> --help`: every command, flag and exit status.

## Contributing, security, licence

- [CONTRIBUTING.md](CONTRIBUTING.md): how to build, test and propose a change.
- [SECURITY.md](SECURITY.md): how to report a vulnerability privately.
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- [LICENSE](LICENSE): Apache License 2.0. The third-party modules compiled into `jst` and their
  licences are listed in [docs/licences.md](docs/licences.md).
