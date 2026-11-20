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
mailbox). Since W-095 the native and hooks routes ring a doorbell that carries no text, so a
message's text reaches the recipient's model only through its inbox read, or, on the Codex bridge,
through one steer. Concurrently, in every order:

- two passes of the bridge, each of which claims, steers, records the app-server's answer (or an
  answer that never comes back), fails before writing, pauses (another step runs first) or crashes
  at any step;
- the native doorbell refused by the recipient's engine (a ring it takes or ignores changes nothing,
  and a hook's ring changes nothing at all, so neither is a step of the model);
- the recipient's reads (one that marks it read, one that marks nothing), acks and answers;
- the sweep (a stale `sending`, deadlines nobody touched), the deadline itself, the recipient's end,
  and the freeze of a mailbox, as recipient or as requester (waiting for a steer of its questions),
  with a crash between a closure's two writes.

A recipient learns a request's ID from what it was shown, a read or a steer; only then can it
answer.

**`ChatSend`**: one response (a send that answers a request) and its retry with the same idempotency
key, through the steps of section 7.3, with a process crash or a machine crash at every step (a
machine crash loses a commit record whose folder could not be fsynced, `durable.ErrNotSynced`),
recovery before anything is served and the store stopped while an entry cannot be resolved, the
pusher (here: the message served to its recipient, by a steer or a read), sessions registering
near the store's limit (the capacity charges, scaled down), another
answer or the deadline closing the request, the sweep of leftovers, and retention's removal of the
message (unlink, fsync, the rest), which a crash can cut apart anywhere.

## What is checked

| property | model | what it says |
|---|---|---|
| `AtMostOnce` | delivery | the bridge steers the text at most once |
| `ContentOnlyThroughReads` | delivery | on every other route the text reaches the model only through the recipient's own reads |
| `NeverLooksUnpushed` | delivery | a steer that may have reached the app-server is never recorded `pending` |
| `NoPushStartsAfterClose` | delivery | no steer of a closed request begins |
| `NoWriteAfterFreeze` | delivery | no pass that still owns its claim writes a question after its mailbox froze |
| `DeadlineClosesRequest` | delivery | a step that finds the deadline passed closes an open request, whatever its delivery's state |
| `NoticeOwedOnce` | delivery | the table owes each `refused`, `dropped` or `expired` delivery exactly one notice; a request and its delivery that expire together owe one between them |
| `NoticeAtLeastOnce` | delivery | a notice marked shown reached the sender at least once; a stop between the page and the mark shows it again (W-096) |
| `RequestAgrees` | delivery | an answered request's delivery is final |
| `FinalStable` | delivery | final states are never left (a step property) |
| `DeadlineFirst` | delivery | an overdue `pending` delivery moves only to `expired` (or `dropped` at an end) |
| `SendingResolves`, `OverdueExpires` | delivery, TLC | with a fair sweep, `sending` is always resolved and an overdue delivery always expires |
| `AtMostOncePerKey` | send | a send and its retry are pushed at most once between them, while the key is remembered |
| `DuplicateIsReal` | send | a retry is told `duplicate` only of a message that exists |
| `FollowOnAgrees` | send | an answer written by a response stands only for a message that exists |
| `MarkersComplete` | send | a committed send keeps every marker it was admitted with |
| `Capacity` | send | the charges never pass the write limit |
| `AcceptedIsDurable` | send | `accepted` is said only of a send no crash can undo |
| `CommitHasFiles` | send | a commit record never stands for files that are gone |
| `RequestAgrees` | send | the request a response answers is answered or closed once, never overwritten |

TLC (TLC2 2.19 of the TLA+ tools, Java 17) finds **21,072** distinct states for a delivery (107,618
generated, the temporal properties checked over the whole graph) and **335** for a send (607
generated) with no error. The Go exploration reaches the same 21,072 and 335 states, and its tests
fail if either count changes, so the two forms cannot drift apart unnoticed. Showing a notice, with
a stop between the page and its mark, took the delivery model from 7,104 states to 21,072
([W-096](../../decisions/W-096-clear-limits-over-more-machinery.md)).
Before W-095, when the native route still pushed the text, both reached 30,736 delivery states; the
doorbell model is the same world with the native push, the engine's hold and the hooks'
announcement taken out.
With each constant below set
TRUE, TLC and the Go test both report the same broken property.

## What the model found

Each fault was in [`chat.md`](../chat.md) as it stood, and is fixed there. Each is kept as a
constant of the TLA+ modules (a variant of the Go tests) that replays the old rule; set TRUE, it
gives this counterexample, and the Go test fails if it ever stops doing so.

| constant | section | counterexample |
|---|---|---|
| `SeparateRequestExpiry` | 4.4 | the request and its delivery expire apart and the requester gets two notices. Fix: one step, one notice, on the request |
| `DeadlineSkipsRequest` | 4.10 | the deadline passes; a pass's claim expires the request's delivery but not the request, and nobody owes the notice. Fix: every deadline check closes both |
| `DeadlineNeedsPending` | 4.10 | (review round 1) the deadline passes; a read of a `delivered` request reads it and leaves the request open. Fix: a deadline check closes the request whatever its delivery's state |
| `FreezeLeavesDelivery` | 4.4, 9.1 | the requesting mailbox freezes (or a closure crashes between its two writes); the closed question is still steered to the author. Fix: the freeze expires the delivery with its request, and every step first repairs a closed request's pending delivery |
| `MarkBeforeShow` | 8.5 | (W-096) a notice is marked shown, then the step stops before its page is returned; the sender never sees it. Fix: the page first, then the mark, so a stop shows the notice again |
| `FreezeIgnoresPushes` | 9.1 | (review rounds 1 and 2) a pass claims a question; the mailbox freezes; the pass writes it to the author. Redesigned: the freeze waits until no question is `sending` |
| `RecoveryIgnoresSync` | 7.2, 7.3 | the commit folder cannot be fsynced; recovery rolls forward anyway; a machine crash loses the commit; the answer it applied stands for nothing (and a retry is told `duplicate` of nothing). Fix: an unsynced commit stops the operation; recovery fsyncs before it rolls forward |
| `DerivedChargedLate` | 7.4 | sessions fill the store; a send is admitted for its own records; rolling it forward drops its markers. Fix: a send is charged for every record it creates |
| `KeptEntryLetsOthersGo` | 7.3 | (review rounds 1 and 2) a response's commit cannot be fsynced and its entry is kept; another response answers the request; recovery rolls the first forward and answers it again. The same rule let a retry become a second copy, and a session take a kept send's room. Redesigned: a failed fsync stops the store |
| `DuplicateTrustsEntry` | 7.3 | (review round 2) retention removes a message's commit record and a crash stops it before the idempotency entry; a retry is told `duplicate` of nothing. Fix: `duplicate` only when the commit record exists; a stale entry is removed |
| `CleanupBeforeUnlink` | 7.3 | (review round 3) retention unlinks a commit record and removes its files; a machine crash undoes the unsynced unlink; the record stands for files that are gone. Fix: the unlink's folder is fsynced before anything else is removed |

One fault has left with the rule it broke: `BareIdIsEvidence` (section 4.10) found that a bare
message ID was taken as evidence that the engine showed a native body, and the fix gave each native
delivery a **handle**. Since W-095 no route shows a body before the inbox read, so neither the handle
nor the variant remains.

One fault has no constant: a confirmed failure that returned a closed question to `pending` (section
9.2). It needed a freeze during a steer, which the freeze now waits out; the rule that expires such a
delivery stays as a defence, with a unit test.

## What it leaves out

The model has one message and one recipient: the 50-per-recipient queue, rate, repeat and loop
limits, idle subscriptions' arming, paging and the 48 KiB result budget are not in it, and the
retention rule is checked as predicates (`NeedsMessage`, `MessageDone`) by unit tests only; the
first review found that it let pressure remove an expired request's only notice, now fixed in
section 7.4. A request that reaches its deadline while a steer of it is under way may still be
written (the lock is not held during a steer); its answer is refused as `request_closed`, and the
model does not count that as a fault. Nor does it count a frozen question that a bridge pass
stalled past 60 s writes: the maintainer accepted that as it is
(2026-11-20, chat.md section 16). Time is two events (the
deadline passing, a `sending` record going stale), not a clock. The doorbell's line format is the one
captured from Claude Code 2.1.328 (section 8.3), and its golden files check that part.

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
