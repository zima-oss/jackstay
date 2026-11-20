# W-090 — The agent chat in 0.6.0: Claude Code's model, extended

This is the chat part of the 0.6.0 ruling W-090; its compatibility part is
[W-090-compatibility-of-0-6-0](W-090-compatibility-of-0-6-0.md).

Date: 2026-11-20.

## Context

The 0.5.x chat (`internal/sessionchat`) kept one store per repository, gave a session a random
identity per start, told Claude Code only how many messages were waiting and never woke an idle
session, let pending messages expire silently, and had no outbox. Its bounds could exceed its own
state file. Claude Code meanwhile ships cross-session messaging of its own: a per-session socket that
wakes an idle session, `SendMessage` with `to`, `summary` and `notify_when_idle`, and the outcomes
delivered, held and refused. The maintainer ruled on the shape of 0.6.0 (this record keeps the part
about chat; the rest of W-090 concerns review, landing and setup).

## Decision

- **Engines.** Claude Code is the primary engine. Codex is served mainly as the reviewer, talking
  with the author in both directions between review rounds; a live dialogue during a round comes
  later.
- **Scope.** One store per local account. A repository is a channel, and `#common` holds sessions
  outside a repository. An optional list of related repositories limits both the channels and the
  sessions a repository's sessions may write to.
- **Identity.** A session is its engine's session ID (Claude Code's `session_id`, Codex's thread
  ID); a token overrides it. Names are readable and disambiguated with a short ref.
- **Contract.** The chat inherits Claude Code's messaging model (`to`, `message`, `summary`,
  `notify_when_idle`, the outcomes; a message is never consent) and extends it with threads,
  structured requests and responses, receipts and an outbox, expiry with a notice to the sender,
  priority, attachments as references with `meta`, channels with explicit subscription, sender
  status, and system events from jackstay.
- **Delivery to Claude Code.** A layer over Claude Code's own per-session socket, which wakes an
  idle session. Hooks are the fallback: they carry the text, and `Stop` holds the turn while urgent
  messages are unread. Claude Code's channels are opt-in.
- **Setup** registers the MCP server and the hooks in one step, with a diff and a backup.
- **Limits are constants**; `jackstay.json` keeps `chat.enabled` and `chat.related`.

## Consequences

The [chat design](../design/chat.md) gives the contract: the tool and record schemas, bounds that
fit the store, the store on the shared durable package, delivery and its fallback, setup, the test
plan and the decisions on its open questions. The socket's line format is undocumented, so the native layer
is captured from a live session, pinned by a contract test and used only for verified Claude Code
versions. 0.5.x chat state is not imported, and its limit keys are constants that schema 3 refuses as
unknown keys.

## The open questions, decided (W-091)

The maintainer then decided the spec's open questions. The related list limits sending only. A
session hears its own repository's channel by default. No messaging token is presented. Chat is
enabled per repository in `jackstay.json`, never by setup (sessions outside a repository only by
the person's separate choice). A Codex session uses its thread ID when
it can be read, otherwise a launch token. `asyncRewake` waits until after 0.6.0. Delivery is at most
once, and a lost answer is reported as `unconfirmed`. `crossSessionInbound` is not imitated off the
native route. The delivery state machine is modelled formally before code.
