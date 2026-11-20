# Task context brief after agent compaction

Status: approved specification. Implementation must be checked against this contract before it
is presented as complete.

## Problem and outcome

Compaction removes detailed turns from an agent's active context. The remaining summary can miss
the current task, decisions, unfinished work, and verification state. Jackstay should let an
operator or agent explicitly maintain a short, local brief and present it after compaction. The
brief is a reminder of claimed state, not an authority or a replacement for checking the
repository and current user instructions.

The first public slice serves one active task in one worktree. Parallel tasks should use separate
worktrees. Parallel sessions in the *same* worktree can read the same brief; concurrent writes
must be detected rather than silently overwriting each other. A later session-specific layer can
add explicit bindings if a host exposes stable session identity to both the writer and hook.

## User workflow

1. In a project worktree, create a brief with an explicit task identifier, one-line task summary,
   1–12 concrete next actions, and up to 12 decisions. The input is JSON on stdin, so command
   arguments and telemetry do not contain its body. No background capture occurs.
2. Read the current brief and its revision. Update it using the observed revision. A mismatched
   revision or task identifier fails without replacing the current note. A new task requires an
   explicit clear or replace operation, making the identity change visible.
3. Clear the note when the task ends. A missing note is an ordinary no-op for the compaction hook.
4. Opt into a host hook separately. A supported host receives the brief only at a verified
   compaction boundary. Users of hosts without such a hook can run `jst brief show` manually.

CLI shape proposed for this slice: `jst brief save --expect REV < brief.json`, `jst brief show`,
`jst brief clear --expect REV`, and `jst brief claude-hook`. `REV=0` means no existing brief.
`show` returns a machine-readable JSON record including revision and freshness. The exact
flag names may be adjusted with the CLI tests, but compare-and-swap behavior and explicit task
identity are required. No command reads a transcript to generate a brief.

## Identity, state, and privacy

- **Worktree key:** resolved absolute worktree root, hashed before use as a local state path.
  Linked worktrees in one Git repository therefore have separate briefs. A command called from a
  subdirectory uses the same root. The hook must verify its event `cwd` resolves inside that exact
  root before emitting anything; it must never substitute a note from another worktree or
  project with the same display name.
- **Repository incarnation:** persist a digest of the Git common directory's filesystem device
  and inode (no raw path). A checkout replaced at the same worktree path has a different
  incarnation even if it reuses the branch and HEAD. `show` then exposes only revision,
  `freshness: "repository-changed"`, and `brief: null`; the hook emits nothing. The old state
  remains a revision tombstone until explicitly cleared at that revision. Saving a new task
  requires that clear, preventing the old brief from being adopted by the new checkout. If Git
  identity cannot be read, `show` likewise redacts the brief body.
- **Task key:** an explicit nonempty, bounded `task_id` in the record. Updates retain the same ID.
  One active task per worktree is supported initially. The task ID is not inferred from branch
  name or conversation text.
- **Revision:** a monotonic integer incremented on each accepted write, including clear. `save`
  and `clear` use an expected revision under a local lock or equivalent atomic compare-and-swap.
  Clear retains a revision tombstone with no brief; revision 0 exists only before the first save.
  The next task starts from the tombstone's revision, so a stale writer or clear operation cannot
  succeed against a later task through reuse of an old revision. Two writers that read the same
  revision cannot both succeed. A failed write leaves the previous state intact. `show` returns
  the current revision even for a cleared brief, with `brief: null`; after clear, creating a new
  task uses that displayed revision as `--expect`.
- **Freshness:** record the last update time, repository incarnation, branch identity and HEAD commit at save time. A
  configurable bounded expiry applies (default seven days; `max_age` may be shorter). Expired
  notes are not injected. A branch change suppresses injection and asks for an explicit update;
  a changed HEAD on the same branch marks the note as needing verification in `show` and hook
  output, because ordinary commits should not erase a useful task summary. Missing or broken Git
  identity fails closed for injection. The clock and Git facts are context checks, not proof that
  task claims are true.
- **Storage:** bounded versioned JSON under Jackstay's user-owned state directory, outside the
  repository, with mode 0600 and parent directories 0700. Use atomic replacement and bounded
  regular-file reads. Avoid raw root paths, transcripts, prompts, tool results, tokens and keys
  in the saved record. The content is explicitly supplied; Jackstay cannot reliably detect every
  secret inside arbitrary prose, so docs and examples must prohibit them. No sync, upload or
  telemetry body capture. `PrivateArgs` protects CLI spans; errors must not echo the body.
- **Size:** cap total input and stored record to 4 KiB. Reject unknown input fields, multiline
  items, empty required fields and oversized arrays. A brief contains task, next steps and
  decisions only; derived Git metadata and timestamps live outside the user-authored body.

The hook may cause Claude Code to save injected context in its own transcript. The guide must
state this plainly. It must not claim the brief stays outside every agent transcript after use.

## Host capability and delivery

| Host | Verified capability | First slice |
|---|---|---|
| Claude Code | The documented `SessionStart` command hook supports matcher `compact` and reports `source: "compact"`. It can add context through `hookSpecificOutput.additionalContext`. | Opt-in `.claude/settings.json` entry runs `jst brief claude-hook`. Validate event name, source, cwd and worktree identity, then emit bounded factual context. Silent exit 0 if no current note. |
| Codex | No compatible public compaction lifecycle hook confirmed for this integration. | `jst brief show` is the manual fallback. Do not install an assumed hook or claim automatic delivery. Revisit only after confirming a supported contract. |
| Other agents | Capabilities vary. | Manual `show`; add adapters only against documented events and a stable workspace identity. |

The host hook is a one-way context reminder. It does not issue a user instruction, block
compaction, ask an agent to execute requests, or certify completion. The output labels the brief
as local claimed state and asks the agent to check the current repository before acting.

## Failure behavior

- Missing or cleared brief, wrong event/source, mismatched worktree or repository incarnation, expired note, changed branch or broken
  Git identity: hook emits nothing and exits 0 so compaction proceeds.
- Corrupt/oversized/non-regular state: `show` reports a bounded diagnostic and nonzero status;
  hook emits nothing and exits 0. It must not print the corrupt body.
- Unwritable state or revision conflict: `save`/`clear` fail without changing the old note. A
  conflict reports the current revision, not the brief content.
- Hook output failure: keep compaction unblocked; emit only a short diagnostic without content.
- Stale state: `show` names the reason and allows the operator to inspect/update or clear it.
  Stale data must never be silently presented as current context.

## Phased implementation plan

1. **Contract and storage:** add the versioned record, task identity, bounded strict input,
   private atomic state, revision conflict handling and stale evaluation. Tests cover same-root
   subdirectories, linked worktree isolation, concurrent revision conflict, mode, size and
   corruption handling.
2. **CLI:** register `save`, `show`, `clear` with private arguments and stable output/status.
   Update CLI help goldens and command import. Tests prove bad input does not replace a note.
3. **Claude Code adapter:** add `claude-hook` and opt-in documented settings example using
   `SessionStart`/`compact`. Tests feed representative host JSON for compact, startup, wrong cwd,
   expiry, branch switch and malformed input; no test requires a real agent or timing waits.
4. **Integration and gate:** update the guide, design index and any generated adoption text only
   if this command changes agent instructions. Run focused package tests, CLI tests and full
   `just gate`; repair failures before committing.
5. **Later capability review:** evaluate a Codex adapter only when current official documentation
   defines a compatible event and output contract. Session-specific briefs require a proven
   binding from writer to hook session ID and are separate work.

## Acceptance checks

- A saved brief survives process exit and is restored after a matching Claude Code compact event
  in the same worktree, with task, next steps and decisions intact.
- Another linked worktree, project, event source or branch receives no brief.
- A new repository initialized at the same filesystem path receives no old brief even if its
  branch and HEAD happen to match; `show` does not reveal the old body.
- Two writers updating the same revision produce exactly one successful new revision.
- An expired note is visible as stale in `show` but never injected.
- No transcript path, raw prompt, command body or secret fixture appears in state, telemetry or
  hook output except the deliberately authored brief text itself.
- Empty, malformed and oversized input cannot replace a valid note; corrupt state does not hang
  or block compaction.
- The CLI help and full `just gate` pass on macOS and Linux after implementation.

## Sources

- [Claude Code hooks reference](https://code.claude.com/docs/en/hooks#sessionstart):
  `SessionStart` matcher values and context output.
- [Claude Code hooks reference, context output](https://code.claude.com/docs/en/hooks#add-context-for-claude):
  `additionalContext` behavior, including transcript persistence and size behavior.
