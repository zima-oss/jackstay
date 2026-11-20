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
  and the freeze of a mailbox, as recipient or as requester (waiting for a push of its questions),
  with a crash between a closure's two writes.

A recipient learns a request's ID from what it was shown: a native or bridge frame, a hook's text,
or a read that returned the message; only then can it answer.

**`ChatSend`**: one response (a send that answers a request) and its retry with the same idempotency
key, through the steps of section 7.3, with a process crash or a machine crash at every step (a
machine crash loses a commit record whose folder could not be fsynced, `durable.ErrNotSynced`),
recovery before anything is served and the store stopped while an entry cannot be resolved, the
pusher, sessions registering near the store's limit (the capacity charges, scaled down), another
answer or the deadline closing the request, the sweep of leftovers, and retention's removal of the
message (unlink, fsync, the rest), which a crash can cut apart anywhere.

## What is checked

| property | model | what it says |
|---|---|---|
| `AtMostOnce` | delivery | the text reaches the model at most once, on every route |
| `NoEarlyExposure` | delivery | a read never returns a native body the engine did not accept |
| `NeverLooksUnpushed` | delivery | a line that may have reached the engine is never recorded `pending` |
| `NoPushStartsAfterClose` | delivery | no push of a closed request begins |
| `NoWriteAfterFreeze` | delivery | no pass that still owns its claim writes a question after its mailbox froze |
| `DeadlineClosesRequest` | delivery | a step that finds the deadline passed closes an open request, whatever its delivery's state |
| `NoticeOnce` | delivery | each `refused`, `dropped` or `expired` delivery owes exactly one notice; a request and its delivery that expire together owe one between them |
| `RequestAgrees` | delivery | an answered request's delivery is final |
| `FinalStable` | delivery | final states are never left (a step property) |
| `DeadlineFirst` | delivery | an overdue `pending` or `held` delivery moves only to `expired` (or `dropped` at an end) |
| `SendingResolves`, `OverdueExpires` | delivery, TLC | with a fair sweep, `sending` is always resolved and an overdue delivery always expires |
| `AtMostOncePerKey` | send | a send and its retry are pushed at most once between them, while the key is remembered |
| `DuplicateIsReal` | send | a retry is told `duplicate` only of a message that exists |
| `FollowOnAgrees` | send | an answer written by a response stands only for a message that exists |
| `MarkersComplete` | send | a committed send keeps every marker it was admitted with |
| `Capacity` | send | the charges never pass the write limit |
| `AcceptedIsDurable` | send | `accepted` is said only of a send no crash can undo |
| `CommitHasFiles` | send | a commit record never stands for files that are gone |
| `RequestAgrees` | send | the request a response answers is answered or closed once, never overwritten |

TLC (2026.11 tools, Java 21) finds **30,736** distinct states for a delivery (163,049 generated,
the temporal properties checked over the whole graph) and **335** for a send (607 generated) with
no error. The Go exploration reaches the same 30,736 and 335 states, and its tests
fail if either count changes, so the two forms cannot drift apart unnoticed. With each constant below set
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
| `DeadlineNeedsPending` | 4.10 | (review round 1) the deadline passes; a read of a `delivered` request reads it and leaves the request open. Fix: a deadline check closes the request whatever its delivery's state |
| `FreezeLeavesDelivery` | 4.4, 9.1 | the requesting mailbox freezes (or a closure crashes between its two writes); the closed question is still pushed to the author. Fix: the freeze expires the delivery with its request, and every step first repairs a closed request's pending or held delivery |
| `FreezeIgnoresPushes` | 9.1 | (review rounds 1 and 2) a pass claims a question; the mailbox freezes; the pass writes it to the author. Redesigned: the freeze waits until no question is `sending` |
| `RecoveryIgnoresSync` | 7.2, 7.3 | the commit folder cannot be fsynced; recovery rolls forward anyway; a machine crash loses the commit; the answer it applied stands for nothing (and a retry is told `duplicate` of nothing). Fix: an unsynced commit stops the operation; recovery fsyncs before it rolls forward |
| `DerivedChargedLate` | 7.4 | sessions fill the store; a send is admitted for its own records; rolling it forward drops its markers. Fix: a send is charged for every record it creates |
| `KeptEntryLetsOthersGo` | 7.3 | (review rounds 1 and 2) a response's commit cannot be fsynced and its entry is kept; another response answers the request; recovery rolls the first forward and answers it again. The same rule let a retry become a second copy, and a session take a kept send's room. Redesigned: a failed fsync stops the store |
| `DuplicateTrustsEntry` | 7.3 | (review round 2) retention removes a message's commit record and a crash stops it before the idempotency entry; a retry is told `duplicate` of nothing. Fix: `duplicate` only when the commit record exists; a stale entry is removed |
| `CleanupBeforeUnlink` | 7.3 | (review round 3) retention unlinks a commit record and removes its files; a machine crash undoes the unsynced unlink; the record stands for files that are gone. Fix: the unlink's folder is fsynced before anything else is removed |

One fault has no constant: a confirmed failure that returned a closed question to `pending` (section
8.3). It needed a freeze during a push, which the freeze now waits out; the rule that expires such a
delivery stays as a defence, with a unit test.

## What it leaves out

The model has one message and one recipient: the 50-per-recipient queue, rate, repeat and loop
limits, idle subscriptions' arming, paging and the 48 KiB result budget are not in it, and the
retention rule is checked as predicates (`NeedsMessage`, `MessageDone`) by unit tests only; the
first review found that it let pressure remove an expired request's only notice, now fixed in
section 7.4. A request that reaches its deadline while a push of it is under way may still be
written (the lock is not held during a push); its answer is refused as `request_closed`, and the
model does not count that as a fault. Nor does it count a frozen question that a hook returns just
after the freeze, or that a pass stalled past 60 s writes: the maintainer accepted both as they are
(2026-11-20, chat.md section 16). Time is two events (the
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
