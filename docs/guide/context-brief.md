# Restore a short task brief after compaction

When Claude Code compacts a session, the agent can lose track of its task. `jst brief` keeps a
small task note you write yourself, outside the repository, and a Claude Code hook gives it back to
the session in the same worktree after a manual or automatic compaction. Nothing happens until you
opt in: no brief exists before `jst brief save`, and the hook is not installed for you.

## Save a brief

From the worktree that owns the task, write a JSON object on standard input:

```sh
jst brief show
jst brief save --expect 0 <<'JSON'
{
  "task_id": "parser-change",
  "task": "Finish the parser change",
  "next": ["Review the diff", "Run the project gate"],
  "decisions": ["Keep the existing file format"]
}
JSON
jst brief show
```

The first `show` prints `{"revision":0,"freshness":"absent","brief":null}`.

| Field | Holds |
|---|---|
| `task_id` | one active task in the worktree |
| `task` | one line |
| `next` | 1 to 12 one-line actions |
| `decisions` | at most 12 one-line facts (optional) |

The input is at most 4 KiB. jackstay never fills it from transcripts, shell history or agent
output, so it holds only what you wrote. Keep secrets and raw conversation out of it.

## Update it

`show` prints the brief's revision; pass it to `--expect` when you save the same task again. If
another writer changed it meanwhile, the save fails and names the current revision. To start a new
task, run `jst brief clear --expect REV`, then save with the new revision `show` prints. The clear
keeps the revision, so a stale writer cannot overwrite the new task.

The brief is kept with private file permissions under `~/.jackstay/.all/context-brief/` (or under
`JACKSTAY_HOME`), keyed by the worktree's root: linked worktrees have separate notes, and agents in
the same worktree share one, so coordinate if they have separate tasks.

## How long it stays fresh

A brief is fresh for seven days, or `brief.max_age_hours` in `jackstay.json`; `--max-age 24h` on
`save` chooses another limit, from one hour to 30 days. `show` reports its freshness:

| Freshness | The hook |
|---|---|
| `current` | delivers it |
| `head-changed` (a new HEAD on the same branch) | delivers it, with a warning to verify the note |
| `branch-changed`, `expired` | stays silent until you save again at the current revision |
| `repository-changed` (another repository at the same path) | stays silent; `show` hides the old body: clear it and save a new task |
| `git-unavailable`, `absent` | stays silent |

## Install the Claude Code hook

Merge this `SessionStart` entry into the `hooks` object of the project's `.claude/settings.json`,
keeping its other hooks:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "compact",
        "hooks": [
          {"type": "command", "command": "jst brief claude-hook", "timeout": 5}
        ]
      }
    ]
  }
}
```

The `compact` matcher covers manual and automatic compaction
([Claude Code hooks reference](https://code.claude.com/docs/en/hooks#sessionstart)). The command
reads the hook event on standard input and answers only a `SessionStart` with `source: "compact"`
whose `cwd` belongs to its own worktree. It returns the note as
`hookSpecificOutput.additionalContext`. A missing or invalid brief adds nothing and does not block
compaction. The note reminds the agent what to check against the repository; it has no authority
over you or the project's rules.

The hook does not read Claude's `transcript_path`, but Claude Code may record the returned note in
its own transcript: write the brief so it can sit there.

Codex has no documented compaction hook for this. After a Codex compaction, run `jst brief show`
yourself.

## Native compaction pilot

With Python 3.10 or later and Claude Code installed, build jackstay and run the pilot in its
checkout:

```sh
just build
python3 tests/claude-compact-pilot.py "$PWD/.build/jst"
```

The pilot uses a throwaway Git repository and a scripted loopback provider. With Claude Code
2.1.328 it ran a real `/compact`: the `SessionStart` event had source `compact`, and the next model
request held the saved brief, which was absent before the compaction.

What a brief must and must not hold, and why: [the brief's design](../design/context-brief.md).
