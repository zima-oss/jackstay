# claudenative testdata

What Claude Code's per-session messaging socket does, captured on throwaway sessions (W-095,
docs/design/chat.md section 8.3). Every token, session ID, process ID, path and name was replaced
with a fixed placeholder in memory, before anything was written. Each folder is one Claude Code
version; the verified native range is the list of these folders.

- `sender-lines.jsonl`: what a Claude Code sender wrote to a receiver's socket (its auth line and
  one message line).
- `receiver-answers.json`: what a receiver wrote back to a plain writer under each inbound policy,
  and the line shape jackstay's doorbell writes.
