# Session chat

Enable chat in the project configuration:

```json
"chat": {
  "enabled": true,
  "max_message_bytes": 4096,
  "max_pending": 128,
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

Short retention, a small pending limit and direct recipients keep chat useful for coordination.
The current stdio integration uses polling. The
[session chat design](../roadmap/session-chat.md#risks-and-follow-up) describes what each
engine adapter must prove before offering delivery during an active turn.
