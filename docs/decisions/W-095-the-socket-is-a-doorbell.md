# W-095 — Claude Code's socket is a doorbell; a chat message is delivered when it is read

Date: 2026-11-20.

## Context

The chat design (W-090) made Claude Code's per-session messaging socket the native route: jackstay
would push a message's text into the recipient session and read back whether its engine accepted,
held or refused it. A capture on throwaway sessions of Claude Code 2.1.328 found that the socket
writes nothing back to a writer that is not one of Claude Code's own processes: under the default
inbound policy the line reaches the model, under `hold` the receiving person is asked, under
`refuse` the session closes the connection, and accept and hold look the same to the writer. Under
the old rules every native push would have been reported as unconfirmed, and the route would have
switched itself off at once.

## Decision

- **The socket is only a doorbell.** jackstay writes one short line with no message text in it,
  saying how many messages wait and to read them with `chat.inbox` or `jst chat inbox`. The message
  lives only in jackstay's store.
- **Delivery is confirmed by the read.** A message counts as delivered when the recipient reads it
  from its inbox. The socket's silence means nothing; a connection closed before the 2 s timeout is
  the recipient's engine refusing the doorbell, recorded as a refused delivery. Nothing switches the
  route off, and a repeated doorbell is harmless.
- **The hooks ring the same doorbell**, for a Claude Code version outside the verified range and for
  a busy session. The verified range is 2.1.328, recorded with the redacted golden files of the
  capture; a later capture widens it.
- **No impersonation.** jackstay never poses as a Claude Code process to get its answers.
- **The spec, the TLA+ model and the delivery table change together.** The engine's hold, the
  native frame's handle and the hooks' announcement leave the model; "pushed at most once" becomes
  "a message's text reaches the model only through the inbox read", except for the Codex bridge,
  which still steers a message once.

## Consequences

See [the chat design](../design/chat.md), sections 4.2, 4.10 and 8, and
[its model](../design/chat-model/README.md). The delivery model shrinks from 30,736 states to 7,104.

## Amendment (2026-11-22): a closed connection is not a refusal

A refusing session closes the connection, and so does a Claude Code process that exits while the
doorbell waits; at the socket the two look the same. Recording a closed connection as a refusal
would hide unread messages of a conversation that may resume. So a closed connection counts as a
taken ring: what it told of is marked rung, and nothing is refused. The `engine_refused` reason and
the table's refusal event stay, for an engine that reports a refusal itself; the native pass never
applies them. Recording a ring then only marks, and a pass stopped part way leaves nothing to undo.
The delivery model loses the doorbell's refusal step (27,056 states to 26,496).

## Amendment (2026-11-22): no session liveness at all

**Context.** The chat tried to know whether a session still lived: a session ended by `SessionEnd`,
a dead process, or a closed connection became `gone`, its waiting deliveries were dropped, and a
reaper ended the sessions whose process had disappeared. Each attempt to make that safe (a reaper,
an ephemeral session bound to its server's process, an end on disconnect) opened the next race: a
session resumed under its key could be ended by a stale view, and a result it had not read could be
dropped.

**Decision.** There is no liveness. jackstay checks no process, ends nothing on a closed
connection, and reaps nothing. A message nobody reads expires by its own deadline, and its sender
gets the usual expiry notice. `SessionEnd` sets `ended`, a hint for listings and status that drops,
refuses and fires nothing; a resumed conversation reads what waited. The table of sessions is
bounded instead: past its bound, a new session evicts the one seen least recently, sparing a record
that holds, watches or is owed an idle subscription or notice, and an evicted session's inbox stays
under its key. Bindings remain only as the doorbell's socket addresses, bounded the same way; a ring
to the socket of a process that is gone is harmless.

**Consequences.** The delivery table loses the end event and the `dropped` state, and the
`recipient_gone` and `recipient_ended` reasons go. Idle subscriptions fire on an idle or expire at
their `until`. The delivery model loses the recipient's end (26,496 states to 8,505). See
[the chat design](../design/chat.md), sections 3.3, 4.2, 6.4 and 7.5.
