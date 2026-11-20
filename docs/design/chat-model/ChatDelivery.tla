---------------------------- MODULE ChatDelivery ----------------------------
(***************************************************************************)
(* One delivery of the agent chat (docs/design/chat.md, sections 4.2, 4.4, *)
(* 4.10, 8 and 9.1), on any of the five routes. Since W-095 the native and *)
(* hooks routes ring a doorbell and carry no text: a message's text        *)
(* reaches the recipient's model only through its inbox read, or, on the   *)
(* Codex bridge, through one steer. Modelled: two bridge passes that may   *)
(* pause or crash at any step, the native doorbell and its refusal, the    *)
(* recipient's reads, acks and answers, the sweep, the deadline, the       *)
(* recipient's end, and a review mailbox's freeze, of the recipient or of  *)
(* the requester.                                                          *)
(*                                                                         *)
(* The transitions are the table of internal/sessionchat/deliverystate,    *)
(* written again here operator for operator (Apply, ApplyMessage,          *)
(* ExpireRequest, Respond), and the package's model test explores the same *)
(* world in Go. The constants named after a rule replay that rule as the   *)
(* spec stated it before this model; each one set TRUE yields the          *)
(* counterexample recorded in README.md.                                   *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
    MaxAttempts,            \* confirmed failures before the bridge stops steering (4)
    Passes,                 \* the bridge's passes, e.g. {"p1", "p2"}
    SeparateRequestExpiry,  \* section 4.4 before: a request and its delivery expire apart
    FreezeLeavesDelivery,   \* section 9.1 before: a freeze closes requests, not their deliveries
    DeadlineSkipsRequest,   \* section 4.10 before: a step's deadline check expires the delivery only
    DeadlineNeedsPending,   \* review round 1: a deadline closed a request only if its delivery could expire
    FreezeIgnoresPushes     \* review rounds 1 and 2: a freeze closed its questions while one was being steered

Routes    == {"native", "hooks", "bridge", "mailbox", "poll"}
States    == {"pending", "sending", "delivered", "read", "acted", "refused", "dropped", "expired"}
Final     == {"acted", "refused", "dropped", "expired"}
Expirable == {"pending"}

VARIABLES
    d,            \* the delivery record: route, state, reason, pusher, attempts, notice, request
    rq,           \* the request's commit record: state ("none" when not a request), notice
    isRequest, fromMailbox,       \* fixed for a behaviour
    engine,       \* what the app-server did with a steer: "none" or "accepted"
    pass,         \* each bridge pass's pc
    shown,        \* times the text reached the recipient's model by a steer
    requestKnown, \* the recipient was shown the request (a read, or a steer), so it can answer
    pushedClosed, \* a steer of a request began (a claim) after it closed
    lateOpen,     \* a step that checks the deadline found it passed and left the request open
    frozen,       \* the requesting mailbox froze
    writtenFrozen,\* a pass that still owned its claim wrote a question its mailbox had frozen
    overdue, alive,
    dNotices, rNotices            \* times each record's notice became owed

vars == <<d, rq, isRequest, fromMailbox, engine, pass, shown, requestKnown,
          pushedClosed, lateOpen, frozen, writtenFrozen, overdue, alive, dNotices, rNotices>>

----------------------------------------------------------------------------
(* The table (deliverystate.Apply). *)

Ev(kind, p, answer, late) == [kind |-> kind, pusher |-> p, answer |-> answer, overdue |-> late]

Ok(dl) == [ok |-> TRUE, d |-> dl]
No(dl) == [ok |-> FALSE, d |-> dl]

Visible(dl) == dl.state \notin {"refused", "dropped", "expired"}

Expire(dl) ==
    [dl EXCEPT !.state = "expired", !.pusher = "", !.reason = "expired",
               !.notice = IF dl.request THEN dl.notice ELSE "owed"]

Confirmed(dl, st) ==
    [dl EXCEPT !.state = st, !.pusher = "",
               !.reason = IF dl.reason = "unconfirmed" THEN "" ELSE dl.reason]

ChecksDeadline(kind) == kind \in {"claim", "ring-refused", "read-whole", "ack-read", "ack-acted"}

DeadlineFires(dl, ev) == ev.overdue /\ dl.state \in Expirable /\ ChecksDeadline(ev.kind)

Apply(dl, ev) ==
    IF DeadlineFires(dl, ev) THEN Ok(Expire(dl))
    ELSE CASE ev.kind = "claim" ->
                IF dl.state = "pending" /\ dl.route = "bridge" /\ dl.attempts < MaxAttempts
                THEN Ok([dl EXCEPT !.state = "sending", !.pusher = ev.pusher]) ELSE No(dl)
           [] ev.kind = "answer" ->
                IF dl.state = "sending" /\ dl.pusher = ev.pusher
                THEN Ok(IF ev.answer = "accepted"
                        THEN [dl EXCEPT !.state = "delivered", !.pusher = ""]
                        ELSE [dl EXCEPT !.state = "delivered", !.reason = "unconfirmed", !.pusher = ""])
                ELSE No(dl)
           [] ev.kind = "failure" ->
                IF dl.state = "sending" /\ dl.pusher = ev.pusher
                THEN Ok([dl EXCEPT !.state = "pending", !.pusher = "", !.attempts = dl.attempts + 1])
                ELSE No(dl)
           [] ev.kind = "stale" ->
                IF dl.state = "sending"
                THEN Ok([dl EXCEPT !.state = "delivered", !.reason = "unconfirmed", !.pusher = ""]) ELSE No(dl)
           [] ev.kind = "ring-refused" ->
                IF dl.route = "native" /\ dl.state = "pending"
                THEN Ok([dl EXCEPT !.state = "refused", !.reason = "engine_refused", !.notice = "owed"])
                ELSE No(dl)
           [] ev.kind \in {"expire", "close-request"} ->
                IF dl.state \in Expirable THEN Ok(Expire(dl)) ELSE No(dl)
           [] ev.kind = "end" ->
                IF dl.state \in {"pending", "sending"}
                THEN Ok([dl EXCEPT !.state = "dropped", !.reason = "recipient_ended", !.pusher = "",
                                   !.notice = "owed"])
                ELSE No(dl)
           [] ev.kind = "read-whole" ->
                IF dl.state \in {"pending", "delivered"} THEN Ok(Confirmed(dl, "read")) ELSE No(dl)
           [] ev.kind = "ack-read" ->
                IF dl.state \in Final THEN Ok(dl)
                ELSE IF dl.state \in {"pending", "delivered"} THEN Ok(Confirmed(dl, "read")) ELSE Ok(dl)
           [] ev.kind = "ack-acted" ->
                IF dl.state \in Final THEN Ok(dl) ELSE Ok(Confirmed(dl, "acted"))
           [] ev.kind = "freeze" ->
                IF dl.route = "mailbox" /\ dl.state = "pending"
                THEN Ok([dl EXCEPT !.state = "read"]) ELSE No(dl)
           [] OTHER -> No(dl)

(* The request's own record (deliverystate.ExpireRequest, ApplyMessage, Respond). *)

ClosedRq == [state |-> "expired", notice |-> "owed"]

ExpireRequest(r, dl) ==
    [ok |-> TRUE, rq |-> ClosedRq, d |-> IF dl.state \in Expirable THEN Expire(dl) ELSE dl]

\* A closure writes the request's record, then the delivery's; a crash between them is repaired by
\* the next step, before anything else, so a closed question is never claimed.
ApplyMessage(r, dl, ev) ==
    IF dl.request /\ r.state = "expired" /\ dl.state \in Expirable
    THEN LET repaired == Expire(dl)
             after    == Apply(repaired, ev)
         IN [ok |-> TRUE, rq |-> r, d |-> IF after.ok THEN after.d ELSE repaired]
    ELSE IF dl.request /\ r.state = "open" /\ ev.overdue /\ ChecksDeadline(ev.kind)
    THEN LET closed == ExpireRequest(r, dl)
             after  == Apply(closed.d, ev)
         IN [ok |-> TRUE, rq |-> ClosedRq, d |-> IF after.ok THEN after.d ELSE closed.d]
    ELSE LET after == Apply(dl, ev)
         IN IF ~after.ok THEN [ok |-> FALSE, rq |-> r, d |-> dl]
            ELSE [ok |-> TRUE, rq |-> r,
                  d  |-> IF dl.request /\ r.state = "expired" /\ after.d.state \in Expirable
                         THEN Expire(after.d) ELSE after.d]

\* An answered request refuses a second answer; a closed one is refused unchanged; past the deadline
\* the request closes with its delivery and the answer is refused (request_closed).
Respond(r, dl, late) ==
    IF r.state = "answered" THEN [ok |-> FALSE, rq |-> r, d |-> dl]
    ELSE IF r.state = "expired" THEN [ok |-> TRUE, rq |-> r, d |-> dl]
    ELSE IF late THEN ExpireRequest(r, dl)
    ELSE LET after == Apply(dl, Ev("ack-acted", "", "none", FALSE))
         IN [ok |-> after.ok, rq |-> [r EXCEPT !.state = "answered"], d |-> after.d]

(* One event through the table, with the request unless a variant keeps them apart. *)
Step(ev) ==
    IF /\ isRequest /\ ~SeparateRequestExpiry
       /\ ~(DeadlineSkipsRequest /\ ChecksDeadline(ev.kind))
       /\ ~(DeadlineNeedsPending /\ d.state \notin Expirable)
       /\ ~(FreezeLeavesDelivery /\ rq.state = "expired")
    THEN ApplyMessage(rq, d, ev)
    ELSE LET after == Apply(d, ev)
         IN [ok |-> after.ok, rq |-> rq, d |-> after.d,
             late |-> isRequest /\ ev.overdue /\ ChecksDeadline(ev.kind) /\ rq.state = "open"]

\* Write a result back, counting each record's notice the moment it becomes owed.
SetMsg(r) ==
    /\ d' = r.d /\ rq' = r.rq
    /\ dNotices' = dNotices + (IF d.notice = "" /\ r.d.notice = "owed" THEN 1 ELSE 0)
    /\ rNotices' = rNotices + (IF rq.notice = "" /\ r.rq.notice = "owed" THEN 1 ELSE 0)
    /\ lateOpen' = (lateOpen \/ ("late" \in DOMAIN r /\ r.late))

KeepMsg == UNCHANGED <<d, rq, dNotices, rNotices, lateOpen>>

----------------------------------------------------------------------------
Init ==
    /\ isRequest \in BOOLEAN
    /\ fromMailbox \in BOOLEAN
    /\ fromMailbox => isRequest
    /\ d \in [route : Routes, state : {"pending"}, reason : {""}, pusher : {""},
              attempts : {0}, notice : {""}, request : {isRequest /\ ~SeparateRequestExpiry}]
    /\ (d.route = "mailbox" => ~isRequest)
    /\ rq = [state |-> IF isRequest THEN "open" ELSE "none", notice |-> ""]
    /\ engine = "none"
    /\ pass = [p \in Passes |-> "idle"]
    /\ shown = 0 /\ requestKnown = FALSE /\ pushedClosed = FALSE
    /\ lateOpen = FALSE /\ frozen = FALSE /\ writtenFrozen = FALSE
    /\ overdue = FALSE /\ alive = TRUE
    /\ dNotices = 0 /\ rNotices = 0

(* The bridge's passes (section 9.2): the one route that still carries a message's text. *)

Claim(p) ==
    /\ pass[p] = "idle" /\ d.route = "bridge"
    /\ LET r == Step(Ev("claim", p, "none", overdue)) IN
       /\ r.ok /\ SetMsg(r)
       /\ LET owns == r.d.state = "sending" /\ r.d.pusher = p /\ d.state = "pending" IN
          /\ pass' = IF owns THEN [pass EXCEPT ![p] = "claimed"] ELSE pass
          /\ pushedClosed' = (pushedClosed \/ (owns /\ isRequest /\ rq.state # "open"))
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, shown, requestKnown, overdue, alive>>

WriteFails(p) ==
    /\ pass[p] = "claimed"
    /\ pass' = [pass EXCEPT ![p] = "failed"]
    /\ KeepMsg
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, shown, requestKnown,
                   pushedClosed, overdue, alive>>

Steer(p) ==
    /\ pass[p] = "claimed"
    /\ pass' = [pass EXCEPT ![p] = "written"]
    /\ engine' = "accepted" /\ shown' = shown + 1 /\ requestKnown' = TRUE
    /\ writtenFrozen' = (writtenFrozen \/ (frozen /\ d.state = "sending" /\ d.pusher = p))
    /\ KeepMsg
    /\ UNCHANGED <<frozen, isRequest, fromMailbox, pushedClosed, overdue, alive>>

Record(p) ==
    /\ pass[p] = "written"
    /\ \E answer \in {"accepted", "lost"} :
          LET r == Step(Ev("answer", p, answer, FALSE)) IN
          IF r.ok THEN SetMsg(r) ELSE KeepMsg
    /\ pass' = [pass EXCEPT ![p] = "idle"]
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, shown, requestKnown,
                   pushedClosed, overdue, alive>>

RecordFailure(p) ==
    /\ pass[p] = "failed"
    /\ LET r == Step(Ev("failure", p, "none", FALSE)) IN
       IF r.ok THEN SetMsg(r) ELSE KeepMsg
    /\ pass' = [pass EXCEPT ![p] = "idle"]
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, shown, requestKnown,
                   pushedClosed, overdue, alive>>

Crash(p) ==
    /\ pass[p] \in {"claimed", "written", "failed"}
    /\ pass' = [pass EXCEPT ![p] = "dead"]
    /\ KeepMsg
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, shown, requestKnown,
                   pushedClosed, overdue, alive>>

(* The native doorbell (section 8.3): a ring the engine takes or ignores changes nothing; one it *)
(* refuses (the connection closed) refuses the delivery. A hook's ring changes nothing at all.   *)

DoorbellRefused ==
    /\ alive
    /\ LET r == Step(Ev("ring-refused", "", "none", overdue)) IN r.ok /\ SetMsg(r)
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue, alive>>

(* The recipient's own calls; a review mailbox makes none. *)

Calls == alive /\ d.route # "mailbox"

InboxReads ==
    /\ Calls
    /\ LET r == Step(Ev("read-whole", "", "none", overdue)) IN
       /\ r.ok /\ SetMsg(r)
       /\ requestKnown' = (requestKnown \/ (r.d.state = "read" /\ d.state # "read"))
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown,
                   pushedClosed, overdue, alive>>

InboxReadsAgain ==
    /\ Calls /\ ~overdue /\ Visible(d) /\ ~requestKnown
    /\ requestKnown' = TRUE
    /\ KeepMsg
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown,
                   pushedClosed, overdue, alive>>

Acks ==
    /\ Calls
    /\ \E kind \in {"ack-read", "ack-acted"} :
          LET r == Step(Ev(kind, "", "none", overdue)) IN r.ok /\ SetMsg(r)
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue, alive>>

Responds ==
    /\ Calls /\ isRequest /\ requestKnown
    /\ LET r == Respond(rq, d, overdue) IN r.ok /\ SetMsg(r)
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue, alive>>

(* The sweep, the deadline, the end, the freezes (sections 4.10, 7.4, 8.2, 9.1). *)

SweepStale ==
    /\ LET after == Apply(d, Ev("stale", "", "none", FALSE)) IN
       after.ok /\ SetMsg([rq |-> rq, d |-> after.d])
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue, alive>>

SweepExpiresRequest ==
    /\ overdue /\ isRequest /\ rq.state = "open"
    /\ IF SeparateRequestExpiry THEN SetMsg([rq |-> ClosedRq, d |-> d])
       ELSE \/ SetMsg(ExpireRequest(rq, d))
            \/ SetMsg([rq |-> ClosedRq, d |-> d])   \* a crash after the request's write
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue, alive>>

SweepExpiresDelivery ==
    /\ overdue /\ (~isRequest \/ SeparateRequestExpiry \/ rq.state # "open")
    /\ LET after == Apply(d, Ev("expire", "", "none", TRUE)) IN
       after.ok /\ SetMsg([rq |-> rq, d |-> after.d])
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue, alive>>

RecipientMailboxFreezes ==
    /\ alive /\ ~overdue /\ d.route = "mailbox"
    /\ LET after == Apply(d, Ev("freeze", "", "none", FALSE)) IN
       after.ok /\ SetMsg([rq |-> rq, d |-> after.d])
    /\ alive' = FALSE
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue>>

RequestingMailboxFreezes ==
    /\ fromMailbox /\ rq.state = "open" /\ ~overdue
    /\ FreezeIgnoresPushes \/ d.state # "sending"   \* deliverystate.FreezeMayProceed
    /\ IF FreezeLeavesDelivery THEN SetMsg([rq |-> ClosedRq, d |-> d])
       ELSE \/ SetMsg(ExpireRequest(rq, d))
            \/ SetMsg([rq |-> ClosedRq, d |-> d])   \* a crash after the request's write
    /\ frozen' = TRUE
    /\ UNCHANGED <<writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue, alive>>

RecipientEnds ==
    /\ alive /\ alive' = FALSE
    /\ LET after == Apply(d, Ev("end", "", "none", FALSE)) IN
       IF after.ok THEN SetMsg([rq |-> rq, d |-> after.d]) ELSE KeepMsg
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, overdue>>

DeadlinePasses ==
    /\ ~overdue /\ overdue' = TRUE
    /\ KeepMsg
    /\ UNCHANGED <<frozen, writtenFrozen, isRequest, fromMailbox, engine, pass, shown, requestKnown,
                   pushedClosed, alive>>

Sweep == SweepStale \/ SweepExpiresRequest \/ SweepExpiresDelivery

Next ==
    \/ \E p \in Passes : Claim(p) \/ WriteFails(p) \/ Steer(p) \/ Record(p) \/ RecordFailure(p) \/ Crash(p)
    \/ DoorbellRefused
    \/ InboxReads \/ InboxReadsAgain \/ Acks \/ Responds
    \/ Sweep
    \/ RecipientMailboxFreezes \/ RequestingMailboxFreezes \/ RecipientEnds \/ DeadlinePasses

\* The sweep runs at most every 10 minutes, but it runs (section 7.4).
Spec == Init /\ [][Next]_vars /\ WF_vars(SweepStale) /\ WF_vars(SweepExpiresRequest)
                              /\ WF_vars(SweepExpiresDelivery)

----------------------------------------------------------------------------
(* Properties (section 15.0). *)

TypeOK ==
    /\ d.route \in Routes /\ d.state \in States /\ d.attempts \in 0..MaxAttempts
    /\ d.notice \in {"", "owed"} /\ rq.state \in {"none", "open", "answered", "expired"}
    /\ pass \in [Passes -> {"idle", "claimed", "written", "failed", "dead"}]
    /\ engine \in {"none", "accepted"}

\* The bridge steers a message's text at most once.
AtMostOnce == shown <= 1

\* On every other route the text reaches the model only through the recipient's own reads (a steer is
\* the only push): a doorbell carries none.
ContentOnlyThroughReads == d.route # "bridge" => shown = 0

\* A steer that may have reached the engine is never recorded as not steered.
NeverLooksUnpushed == engine # "none" => d.state # "pending"

\* No steer of a request begins (a claim) once it is closed. A steer already claimed when its request
\* closes may still arrive; the answer to it is refused as request_closed.
NoPushStartsAfterClose == ~pushedClosed

\* No question is written to the author by a pass that still owned its claim when the mailbox froze:
\* the freeze waits for steers under way (a pass stalled past 60 s was swept and owns nothing).
NoWriteAfterFreeze == ~writtenFrozen

\* A step that finds the deadline passed closes an open request, whatever its delivery's state.
DeadlineClosesRequest == ~lateOpen

\* Every refused, dropped or expired delivery owes exactly one notice; a request and its delivery
\* that expire together owe one between them.
NoticeOnce ==
    /\ dNotices <= 1 /\ rNotices <= 1
    /\ (isRequest /\ d.state = "expired" /\ rq.state = "expired") => dNotices + rNotices = 1
    /\ d.state \in {"refused", "dropped"} => dNotices = 1
    /\ d.state = "expired" => dNotices + rNotices >= 1

\* A request and its response agree: an answered request's delivery is final.
RequestAgrees == rq.state = "answered" => d.state \in Final

\* Final states are never left.
FinalStable == [][d.state \in Final => d'.state = d.state]_vars

\* The deadline is applied before a steer, a read, an ack or a refused doorbell.
DeadlineFirst ==
    [][(overdue /\ d.state \in Expirable /\ d'.state # d.state) => d'.state \in {"expired", "dropped"}]_vars

\* Liveness: a `sending` record is always resolved, and an overdue delivery always expires.
SendingResolves == d.state = "sending" ~> d.state # "sending"
OverdueExpires  == (overdue /\ d.state \in Expirable) ~> d.state \notin Expirable
=============================================================================
