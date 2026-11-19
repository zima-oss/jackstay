# Local session chat over MCP

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

Polling provides an interoperable first delivery path across MCP hosts. Monitoring an inbox
and injecting a message into a running agent turn are separate capabilities. A future host
bridge needs explicit addressed delivery, replay on reconnect and an acknowledgment from the
receiving agent. Claude Code hooks expose a later execution boundary. Managed Codex sessions
can be evaluated separately for active-turn injection. Setup labels the capability actually
verified for that host.

Verify with two independent subprocesses in linked worktrees, restart a recipient after
delivery, retry a send, confirm reply direction and reject a forged acknowledgment. Also
verify unknown recipients, full inboxes and isolation for unrelated repositories.

[MCP stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports) ·
[MCP tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)
