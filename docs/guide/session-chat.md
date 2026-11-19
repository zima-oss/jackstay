# Session chat

Enable chat in the project configuration:

```json
"chat": {
  "enabled": true,
  "max_message_bytes": 4096,
  "max_pending": 128,
  "max_pending_per_session": 32,
  "max_sessions": 32,
  "retention_hours": 72
}
```

Use `jst setup --chat` to inspect host integration steps. Register `jst chat serve --name NAME`
as a local stdio MCP server in each agent host. Launch it from the intended checkout or use a
local launcher that changes into that checkout before starting the process. Linked worktrees
of the same Git repository share chat. Unrelated repositories have separate chat state.

For example, register the server from the project in Claude Code with
`claude mcp add --scope local agent-chat -- jst chat serve --name reviewer`. In Codex, use
`codex mcp add agent-chat -- jst chat serve --name reviewer` when the server will start in the
intended project. Use a launcher with an explicit checkout when the host starts servers in
another directory.

A host launcher can set a fresh, cryptographically random `JACKSTAY_CHAT_SESSION_TOKEN` for a
logical session. Reuse that token for the same recipient when its server restarts. Keep it in
the local launcher or secret store, outside Git. Use a distinct token for each agent. Without a
token, a fresh connection receives a new session identity. A display name can be shared by
more than one agent, so use the recipient ID returned by discovery.

- `chat.self` shows the identity of the current connection.
- `chat.sessions` discovers recipients in the repository.
- `chat.send` delivers a direct message or request. Reuse the same idempotency key when retrying
  the same operation.
- `chat.inbox` reads pending messages. Poll it at a safe point in the host's workflow.
- `chat.reply` sends a response linked to a received message.
- `chat.ack` acknowledges that the current recipient processed a message.

A successful send means the local service accepted and persisted the message. The recipient
acknowledges processing separately. Use a reply with evidence when the requester needs to verify
an action. An incoming request carries the sender's text and identity.
Evaluate it under the receiving session's own instructions and permissions.

The per-session pending quota leaves room for other recipients. An explicit quota cannot exceed
`max_pending`. The default is the smaller of 32 and the repository's total quota.

## Deliver to a managed Codex turn

Add this opt-in object inside the enabled `chat` section:

```json
"codex_bridge": {
  "enabled": true,
  "poll_interval_ms": 1000,
  "ack_timeout_seconds": 30
}
```

Use the receiving thread's owner-managed Codex app-server Unix socket and its thread ID.
The bridge needs a readable active-turn snapshot. It queries `thread/turns/list` and falls
back to `thread/read` when pagination is unavailable. If your app-server rejects both history
reads, create the managed thread with `historyMode: "legacy"` in `thread/start` and opt into
`experimentalApi` during `initialize`.

Launch the bridge from the repository with the same `JACKSTAY_CHAT_SESSION_TOKEN` and
session name as that thread's MCP server:

```sh
jst chat codex-bridge --socket "$APP_SOCKET" --thread "$APP_THREAD" --name reviewer
```

The bridge addresses an active turn using `turn/steer` with the expected turn ID. It records
confirmed acceptance locally and uses the stable chat message ID to reconcile an interrupted
response against Codex history. A recipient acknowledges processing by calling `chat.ack`.
A transport acceptance remains pending until that acknowledgment. An unfinished message can
replay into a later active turn with the same ID. If the connection drops before a response
and Codex has yet to expose the ID, the next delivery attempt can repeat the message. Treat
replies and work requests as idempotent.

The poll interval controls how often the bridge checks for work. The acknowledgment timeout
controls a diagnostic after Codex acceptance. A bounded reconnect backoff protects the server
during an outage. Keep the thread ID, Unix socket and session token in the owner-managed
launcher. Configure the Unix socket in a private directory. A standalone MCP server still
supports inbox polling, including when the recipient is idle.

[Codex app-server protocol](https://developers.openai.com/codex/app-server)

## Notify Claude Code at the next boundary

Enable the optional hook adapter inside `chat`:

```json
"claude_code": {"enabled": true}
```

Start Claude Code with one private `JACKSTAY_CHAT_SESSION_TOKEN`. The local MCP server and
the hook must inherit that same token and run from this repository. Register the MCP server
as described above with the session name used in the hook. In Claude Code's local settings,
add command hooks for the next prompt and for completed tool batches:

```json
{
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "jst chat claude-hook --name reviewer --event UserPromptSubmit", "timeout": 10}]}],
    "PostToolBatch": [{"hooks": [{"type": "command", "command": "jst chat claude-hook --name reviewer --event PostToolBatch", "timeout": 10}]}]
  }
}
```

Merge these entries into an existing `hooks` object. The hook reports the number of pending
messages to Claude, then Claude calls `chat.inbox` to read them and `chat.ack` after processing.
The hook never prints the sender's body, never changes tool permissions, and leaves a message
pending until its recipient acknowledges it. Claude Code runs these hooks when a prompt is
submitted or a tool batch ends. Delivery can wait for the next such event. Remove both hook
entries to disable next-boundary notices while keeping MCP polling available.

[Claude Code hook events and output](https://code.claude.com/docs/en/hooks)
