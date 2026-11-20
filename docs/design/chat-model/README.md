# The chat delivery model

The formal model that [the chat design](../chat.md#150-before-code-the-formal-model) requires
before any chat code (W-091). It is written twice, and both forms check the same rules:

- in TLA+, checked with TLC: [`ChatDelivery.tla`](ChatDelivery.tla) and
  [`ChatSend.tla`](ChatSend.tla), each with its `.cfg`;
- in Go, as the tests of
  [`internal/sessionchat/deliverystate`](../../../internal/sessionchat/deliverystate), the package
  that holds the transition table and the journal rules the chat store imports. Those tests explore
  the same worlds breadth first, to the last reachable state, and run in `just gate`.

## What is modelled

**`ChatDelivery`**: one message to one recipient, on each of the routes `native`, `hooks`, `bridge`,
`mailbox` and `poll`, as a plain message or as a request (asked by a session or by a review
mailbox). Concurrently, in every order:

- two passes of the native pusher, or of the bridge, each of which claims, writes, records, fails
  before writing, pauses (another step runs first) or crashes at any step;
- the engine's real decision (accepted, held, refused, queue full), its later release or discard of
  a held copy, and an answer that never comes back;
- the recipient's hook (announce, then return the text or crash before returning);
- the recipient's reads, `ids` reads, acks and answers, naming the delivery by what it knows;
- another message of the thread carrying the bare message ID;
- the sweep (a stale `sending`, deadlines nobody touched), the deadline itself, the recipient's end,
  and the freeze of a mailbox, as recipient or as requester.

**`ChatSend`**: one send and its retry with the same idempotency key, through the steps of section
7.3, with a process crash or a machine crash at every step (a machine crash loses a commit record
whose folder could not be fsynced, `durable.ErrNotSynced`), recovery before anything is served, the
pusher, sessions registering near the store's limit (the capacity charges, scaled down), and the
sweep of leftovers.

## What is checked

| property | model | what it says |
|---|---|---|
| `AtMostOnce` | delivery | the text reaches the model at most once, on every route |
| `NoEarlyExposure` | delivery | a read never returns a native body the engine did not accept |
| `NeverLooksUnpushed` | delivery | a line that may have reached the engine is never recorded `pending` |
| `NoPushAfterClose` | delivery | a request closed early is never pushed |
| `NoticeOnce` | delivery | each `refused`, `dropped` or `expired` delivery owes exactly one notice; a request and its delivery that expire together owe one between them |
| `RequestAgrees` | delivery | an answered request's delivery is final |
| `FinalStable` | delivery | final states are never left (a step property) |
| `DeadlineFirst` | delivery | an overdue `pending` or `held` delivery moves only to `expired` (or `dropped` at an end) |
| `SendingResolves`, `OverdueExpires` | delivery, TLC | with a fair sweep, `sending` is always resolved and an overdue delivery always expires |
| `AtMostOncePerKey` | send | a send and its retry are pushed at most once between them |
| `DuplicateIsReal` | send | a `duplicate` names a message that exists |
| `FollowOnAgrees` | send | a subscription or an answer written by a send stands only for a message that exists |
| `MarkersComplete` | send | a committed send keeps every marker it was admitted with |
| `Capacity` | send | the charges never pass the write limit |
| `AcceptedIsDurable` | send | `accepted` is said only of a send no crash can undo |

TLC (2026.11 tools, Java 21) finds **22,336** distinct states for a delivery (102,123 generated,
depth 20, the temporal properties checked over the whole graph) and **152** for a send (302
generated, depth 16), with no error. The Go exploration reaches the same 22,336 and 152 states;
`go test -v ./internal/sessionchat/deliverystate` prints its counts. With each constant below set
TRUE, TLC and the Go test both report the same broken property.

## What the model found

Each fault was in [`chat.md`](../chat.md) as it stood, and is fixed there. Each is kept as a
constant of the TLA+ modules (a variant of the Go tests) that replays the old rule; set TRUE, it
gives this counterexample, and the Go test fails if it ever stops doing so.

| constant | section | counterexample |
|---|---|---|
| `BareIdIsEvidence` | 4.10 | another message carries the ID; the recipient names it in `ids`; a body its engine held is returned. Fix: the native frame carries a per-delivery **handle**, the only evidence |
| `SeparateRequestExpiry` | 4.4 | the request and its delivery expire apart and the requester gets two notices. Fix: one step, one notice, on the request |
| `DeadlineSkipsRequest` | 4.10 | the deadline passes; a pass's claim expires the request's delivery but not the request, and nobody owes the notice. Fix: every deadline check closes both |
| `FreezeLeavesDelivery` | 9.1 | the requesting mailbox freezes; the closed question is still pushed to the author. Fix: the freeze expires the pending or held delivery with its request |
| `FailureReopens` | 8.3 | a pass claims; the mailbox freezes; the pass records a confirmed failure; the closed question is pending and pushed again. Fix: it expires instead |
| `RecoveryIgnoresSync` | 7.2, 7.3 | the commit folder cannot be fsynced; recovery rolls forward anyway; a machine crash loses the commit; the idempotency entry names nothing, and the retry is told `duplicate`. Fix: an unsynced commit stops the operation; recovery fsyncs before it rolls forward, or keeps the entry; nothing is served while an entry is kept |
| `RetryIgnoresEntry` | 7.2 | the send is `uncertain` with its entry kept; the retry becomes a fresh send; both become durable and are pushed. Fix: a retry whose key is in a kept entry answers `uncertain` again |
| `DerivedChargedLate`, `ChargeNotReserved` | 7.4 | the send is admitted for its own records; sessions register beside its kept entry; rolling forward passes the limit or drops markers. Fix: a send is charged for every record it creates, from its journal entry on |

## What it leaves out

The model has one message and one recipient: the 50-per-recipient queue, rate, repeat and loop
limits, idle subscriptions' arming, paging and the 48 KiB result budget are not in it, and the
retention rule is checked as a predicate (`NeedsMessage`) by unit tests only. Time is two events (the
deadline passing, a `sending` record going stale), not a clock. The engine's line format and answers
are what section 8.3 says they are; the pilot's captured fixtures check that part.

## Running it

The Go form runs in `just gate` (the `go` part), or alone:

```sh
go test -count=1 -v ./internal/sessionchat/deliverystate
```

TLC needs Java 11 or newer and the TLA+ tools' `tla2tools.jar` (from
https://github.com/tlaplus/tlaplus/releases). `just tla` runs both models when it finds them:
`JAVA` names the java binary (default `java` on the `PATH`), and `TLA2TOOLS` the jar (default
`~/.cache/tla/tla2tools.jar`). Without them it says so and passes, since the gate already runs the
Go form. By hand, from this folder:

```sh
java -XX:+UseParallelGC -cp tla2tools.jar tlc2.TLC -workers auto -config ChatDelivery.cfg ChatDelivery.tla
java -XX:+UseParallelGC -cp tla2tools.jar tlc2.TLC -workers auto -config ChatSend.cfg ChatSend.tla
```

To see a counterexample, set one of the constants above to `TRUE` in the `.cfg`.
