# Local session chat over MCP

This note describes the 0.5.x chat. The 0.6.0 contract that replaces it is the
[chat design](../design/chat.md) ([W-090](../decisions/W-090-agent-chat.md)).

Parallel agents need a shared conversation for status, questions, requests and replies. A
session can ask another session to run a check or coordinate file ownership. The receiving
agent interprets the request under its own instructions and permissions.

## First usable contract

- Each agent starts a local stdio MCP connection. Processes share a durable inbox through
  the same Git repository and local account. Linked worktrees share a namespace. Separate
  clones stay isolated unless a future explicit federation mechanism connects them.
- A server binds its connection to a session identity. Display names are labels and may
  repeat. Discovery provides opaque recipient IDs. A launcher may supply a stable, random
  session capability outside project configuration to recover the same inbox after reconnect.
- Tools discover sessions, send a direct message or request, read pending messages, reply to
  a message and acknowledge processing. A caller can retry a send with the same idempotency
  key. Acknowledgment belongs to the recipient. Pending messages stay visible until
  acknowledgment or the published retention boundary.
- Store creation, delivery and acknowledgment under one writer lock across processes.
  Bound message bytes, pending volume, session registration and retention. An over-capacity
  send reports refusal. A successful send warrants durable acceptance; a later acknowledgment
  records the recipient's declaration of processing. A reply can carry evidence for the requested work.
- Enable the service explicitly in the project. The repo chooses quotas and retention. The
  owner-controlled launcher chooses local identity and credentials. Keep messages, session
  capabilities and state out of Git and command telemetry.

## Risks and follow-up

A name such as `reviewer` is unsuitable for authentication because several worktrees may use
it. Broadcast creates ambiguous responsibility and accidental fan-out. Start with one named
recipient, replies and stable IDs. Treat identical rewrites, duplicate requests and refreshes
as normal operational cases. Attachments, groups and routing rules can follow after measuring
real coordination traffic.

The local boundary is an account and a repository namespace. Other software that can write
the local account's private files shares that account's authority. Treat message bodies as
untrusted communication. The service does not invoke shell commands or delegate credentials.

Polling provides the interoperable route. The opt-in managed Codex bridge addresses one active
turn through the owner's Unix app-server endpoint. It persists transport acceptance, reconciles
the stable chat ID after reconnect and waits for the recipient's acknowledgment. Pending
messages may replay into a later active turn. The optional Claude Code command hook announces
pending work at a submitted prompt or after a tool batch, with the same recipient token as MCP.
The recipient still calls `chat.inbox` and `chat.ack`. An installed Claude Code 2.1.322
with a scripted loopback model exercised `UserPromptSubmit`, `PostToolUse` and `PostToolBatch`.
The model received the notice, read the peer body via MCP and acknowledged the message.

Verify with two independent subprocesses in linked worktrees, restart a recipient after
delivery, retry a send, confirm reply direction and reject a forged acknowledgment. Also
verify unknown recipients, full inboxes and isolation for unrelated repositories.

## Managed Codex pilot

With Python 3.10 or later, run `just build`, then `python3 tests/codex-chat-pilot.py "$PWD/.build/jst"` from the
Jackstay checkout. The pilot launches two installed Codex app-server processes with
private temporary settings and a scripted loopback model, then starts a real Jackstay
MCP server for each session. The sender calls `chat.send`. The recipient receives the
stable message ID in an already active turn. The pilot kills the bridge before the
recipient can acknowledge processing, restarts it and observes `chat.ack` from the
recipient through MCP. Codex CLI 0.169.0 was verified with legacy thread history.
The scripted provider makes the transport and acknowledgment assertions reproducible.

[MCP stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports) ·
[MCP tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)
