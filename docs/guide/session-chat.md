# Session chat

Agent sessions on this machine can send each other messages, requests and replies, and jackstay
tells a session when something waits for it. The design is
[docs/design/chat.md](../design/chat.md).

Enable chat in the project configuration:

```json
"chat": {
  "enabled": true
}
```

The section needs `"jackstay_schema": 3` (jackstay 0.6.0 or later). One store serves every
repository of the account, in `~/.jackstay/.all/chat/v1/` (or under `JACKSTAY_HOME`). Each
repository is a channel, named after its `project`; linked worktrees share it. A message body holds
at most 32 KiB, a sender makes at most 30 sends a minute, and the store keeps at most 4,096 messages
and 256 live sessions. A delivered message is kept 7 days, and an unanswered one expires after 72
hours unless its sender says otherwise.

## Connect a session

Register `jst chat serve` as a local stdio MCP server from the project:

```sh
claude mcp add --scope local agent-chat -- jst chat serve
codex mcp add agent-chat -- jst chat serve
```

A Claude Code session is identified through its hook (below), so its address follows the
conversation across a resume or a `/clear`. Any other host gives each session a fresh, random
`JACKSTAY_CHAT_SESSION_TOKEN` of 32 to 256 characters, kept in the launcher, outside Git, and the
same token again when that session's server restarts. Without either, a connection is ephemeral: it
can send, and receives only while it lives. `--name NAME` gives the session a display name; a
session can also rename itself with `chat.whoami`.

The tools:

- `chat.whoami`: this session's address, channel and delivery route; with `name`, renames it.
- `chat.sessions`, `chat.channels`, `chat.subscribe`: who can be addressed, and channel membership.
- `chat.send`: a message, a request (`request: {"expects": "decision"}` or `"answer"`), a reply
  (`reply_to`) or an idle subscription (`notify_when_idle`). Retry with the same `idempotency_key`.
- `chat.inbox`: unread messages (urgent first), and the notices owed to this session: a message of
  yours that was refused, dropped or expired, or a session you watch that went idle.
- `chat.ack`: declare a message `read` or `acted`. `chat.respond`: answer a request.
- `chat.outbox` and `chat.thread`: what you sent and what became of it, and a whole thread.

The 0.5.x names keep working: `chat.self` is `chat.whoami`, `chat.reply` is a send with `reply_to`,
`chat.send` takes `body` and `kind: request`, and `chat.ack` with only a `message_id` means acted.

A message's text reaches a session only through its own `chat.inbox`. An incoming message carries
its sender's text and address; evaluate it under the receiving session's own instructions and
permissions. A notice can come twice, never not at all.

## Ring Claude Code

In Claude Code's settings, run `jst hook claude` for these events:

```json
{
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "jst hook claude", "timeout": 10}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "jst hook claude", "timeout": 10}]}],
    "PostToolBatch": [{"hooks": [{"type": "command", "command": "jst hook claude", "timeout": 10}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "jst hook claude", "timeout": 10}]}],
    "StopFailure": [{"hooks": [{"type": "command", "command": "jst hook claude", "timeout": 10}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "jst hook claude", "timeout": 10}]}]
  }
}
```

Merge them into an existing `hooks` object. The hook rings a doorbell: one line saying how many
messages and notices wait, never their text. A prompt is reminded of everything unread, a tool
batch hears of each new message once, and a `Stop` holds the turn, at most three times in a row,
while an urgent (`now`) message is unread. An idle session on a verified Claude Code version
(2.1.328) is also rung on its messaging socket by `jst chat push`, which the hooks start when a
doorbell is due; any other version waits for its next prompt. The hook never fails a turn.
`jst chat claude-hook` is the old name, kept through 0.6.x.

[Claude Code hook events and output](https://code.claude.com/docs/en/hooks)

## Reviews

The same MCP connection offers `review.submit` and `review.status`. Submit `{ "base": "main",
"head": "HEAD", "task": "feature-branch", "reviewers": ["codex", "claude-code"] }` to start
`jst review` in the background on those pinned commits; an equal review already running is joined.
`review.status` takes the `task` and shows this repository's running review and how its latest one
ended. When the round ends, each submitter receives a `review.finished` event from `jackstay` in
`chat.inbox`. A result the chat could not take is kept by the review store and delivered when the
recipient next reads its inbox. See [Run a review in the background](review.md#run-it-in-the-background).

## Not yet: jackstay 1

- The Codex bridge's push into a running Codex turn. A Codex session reads its inbox through
  `jst chat serve`; `jst chat codex-bridge` says so and exits.
- The brief's part of `jst hook claude`: keep `jst brief claude-hook` for that.
- Telling which session ran a command through the process tree, for events about it.
- Events to a whole channel, such as a merged branch.
- A list of related repositories that limits whom a session may write to.

State written by jackstay 0.5.x under `~/.jackstay/.all/session-chat/` is not read by 0.6.0 and is
not imported.
