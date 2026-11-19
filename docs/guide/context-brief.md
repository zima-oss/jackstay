# Restore a short task brief after compaction

`jst brief` stores a small, explicit task note outside the repository. A Claude Code hook can
return it to the same worktree after manual or automatic compaction. This is opt-in: no brief is
created until `jst brief save` runs, and no hook is installed automatically.

From the worktree that owns the task, write a JSON object on standard input:

```sh
jst brief show                         # initially: {"revision":0,"freshness":"absent","brief":null}
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

`task_id` identifies one active task in the worktree; `task` is one line, `next` contains 1–12
one-line actions, and `decisions` contains at most 12 one-line facts. The brief input is limited
to 4 KiB. `show` returns its revision. Update the same task with `--expect` set to that revision.
If another writer changed it, the update fails and reports the current revision. To start a new
task, run `jst brief clear --expect REV`, then use the *new* revision returned by `show` for the
next save. Clear keeps a revision tombstone so a stale writer cannot overwrite the new task.
Jackstay does not infer progress or decisions from transcripts, shell
history, or agent output. Do not put secrets or raw conversation content in the brief. State is
written with private file permissions under `~/.jackstay/.all/context-brief/` (or `JACKSTAY_HOME`)
and keyed by the resolved worktree root, so linked worktrees have separate notes. Agents in the
*same* worktree share one note; coordinate updates if they have separate tasks.

The default maximum age is seven days. Set `--max-age 24h` (from one hour to 30 days) on `save`
to choose another limit. `show` reports `current`, `head-changed`, `branch-changed`, `expired`,
`git-unavailable` or `absent`. A new HEAD on the same branch keeps the hook active but adds a
warning to verify the note. A branch change or expiration suppresses automatic delivery until
you save the brief again at the current revision.

If a repository is replaced at the same path, `show` returns `repository-changed` and hides the
old body. The hook stays silent. Clear the old revision and save a new task for the new checkout.

## Claude Code hook

Add this entry to the project's `.claude/settings.json` `hooks` object, alongside existing hooks:

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

Merge the `SessionStart` entry into an existing settings file; do not replace its other hooks.
The command reads Claude Code's hook event from standard input. It responds only to `SessionStart`
with `source: "compact"`, and only when the event's `cwd` belongs to the command's exact worktree.
It emits `hookSpecificOutput.additionalContext` containing the saved note. An absent or invalid
brief adds no context and does not block compaction. The note is a reminder to check against the
current repository state, not an instruction with authority over the user or project rules.

The hook contract follows the [Claude Code hooks reference](https://code.claude.com/docs/en/hooks#sessionstart):
the `compact` matcher covers manual and automatic compaction, and a command hook can add context
at `SessionStart`. The hook does not read Claude's `transcript_path`. Claude Code may record the
injected context in its own session transcript; keep the brief suitable for that location.

Codex has no equivalent documented compaction hook in this integration. Use `jst brief show`
manually after a Codex compaction until a compatible event is confirmed.

## Native compaction pilot

With Python 3.10 or later and Claude Code installed, build Jackstay and run the
pilot in this checkout.

```sh
just build
python3 tests/claude-compact-pilot.py "$PWD/.build/jst"
```

The pilot uses a disposable Git repository and a scripted loopback provider.
Claude Code 2.1.328 ran an actual `/compact` and reported a compact boundary.
Its `SessionStart` event had source `compact`, and the next model request
contained the saved brief. The pilot also checks that the note is absent
before compaction.
