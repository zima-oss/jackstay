---------------------------- MODULE ChatDelivery ----------------------------
(***************************************************************************)
(* One delivery of the agent chat (docs/design/chat.md, sections 4.2, 4.4, *)
(* 4.10, 8 and 9.1), on any of the five routes: two passes of the pusher   *)
(* (or the bridge) that may pause or crash at any step, the recipient's    *)
(* hook, its reads, acks and answers, another message of the thread that   *)
(* carries the bare ID, the sweep, the deadline, the recipient's end, and  *)
(* a review mailbox's freeze, of the recipient or of the requester.        *)
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
    MaxAttempts,            \* confirmed failures before the route moves to hooks (4)
    Passes,                 \* the pusher's passes, e.g. {"p1", "p2"}
    BareIdIsEvidence,       \* section 4.10 before: a bare message ID proves a shown frame
    SeparateRequestExpiry,  \* section 4.4 before: a request and its delivery expire apart
    FreezeLeavesDelivery,   \* section 9.1 before: a freeze closes requests, not their deliveries
    DeadlineSkipsRequest,   \* section 4.10 before: a step's deadline check expires the delivery only
    FailureReopens          \* section 8.3 before: a confirmed failure reopens a closed request's delivery

Routes    == {"native", "hooks", "bridge", "mailbox", "poll"}
States    == {"pending", "sending", "delivered", "held", "read", "acted",
              "refused", "dropped", "expired"}
Final     == {"acted", "refused", "dropped", "expired"}
Expirable == {"pending", "held"}
Answers   == {"accepted", "held", "refused", "queue_full"}

VARIABLES
    d,            \* the delivery record: route, state, reason, pusher, attempts, notice, request
    rq,           \* the request's commit record: state ("none" when not a request), notice
    isRequest, fromMailbox,       \* fixed for a behaviour
    engine,       \* what the engine really did with a written line
    pass,         \* each pass: its pc and the answer its socket carried back
    hook,         \* "announced": recorded delivered, text not yet returned
    shown,        \* times the text reached the recipient's model by a push
    handleKnown,  \* a frame the engine showed carried the handle and the request ID
    idKnown,      \* the bare ID reached the recipient through another message
    exposed,      \* a read returned a native body the engine never accepted
    pushedClosed, \* a request was pushed after it closed
    overdue, alive,
    dNotices, rNotices            \* times each record's notice became owed

vars == <<d, rq, isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown,
          exposed, pushedClosed, overdue, alive, dNotices, rNotices>>

----------------------------------------------------------------------------
(* The table (deliverystate.Apply). *)

Ev(kind, p, answer, late, named) ==
    [kind |-> kind, pusher |-> p, answer |-> answer, overdue |-> late, named |-> named]

Ok(dl) == [ok |-> TRUE, d |-> dl]
No(dl) == [ok |-> FALSE, d |-> dl]

Visible(dl, named) ==
    CASE dl.state \in {"refused", "dropped", "expired"} -> FALSE
      [] dl.state = "held"       -> dl.route = "native" /\ named = "handle"
      [] dl.route # "native"     -> TRUE
      [] dl.state \in {"read", "acted"} -> TRUE
      [] dl.state = "delivered"  -> dl.reason # "unconfirmed" \/ named = "handle"
      [] OTHER                   -> named = "handle"

Expire(dl) ==
    [dl EXCEPT !.state = "expired", !.pusher = "",
               !.reason = IF dl.state = "held" THEN "engine_held_unresolved" ELSE "expired",
               !.notice = IF dl.request THEN dl.notice ELSE "owed"]

Confirmed(dl, st) ==
    [dl EXCEPT !.state = st, !.pusher = "",
               !.reason = IF dl.reason = "unconfirmed" THEN "" ELSE dl.reason]

ChecksDeadline(kind) == kind \in {"claim", "announce", "read-whole", "ack-read", "ack-acted", "confirm"}
NamesIt(kind)        == kind \in {"ack-read", "ack-acted", "confirm"}

DeadlineFires(dl, ev) ==
    /\ ev.overdue /\ dl.state \in Expirable /\ ChecksDeadline(ev.kind)
    /\ (dl.state = "pending" \/ NamesIt(ev.kind))

AnswerOf(dl, answer) ==
    CASE answer = "accepted"   -> [dl EXCEPT !.state = "delivered", !.pusher = ""]
      [] answer = "lost"       -> [dl EXCEPT !.state = "delivered", !.reason = "unconfirmed", !.pusher = ""]
      [] answer = "held"       -> [dl EXCEPT !.state = "held", !.pusher = ""]
      [] answer = "refused"    -> [dl EXCEPT !.state = "refused", !.reason = "engine_refused",
                                             !.notice = "owed", !.pusher = ""]
      [] answer = "queue_full" -> [dl EXCEPT !.state = "dropped", !.reason = "engine_queue_full",
                                             !.notice = "owed", !.pusher = ""]

NamedCall(dl, ev) ==
    IF dl.state \in Final THEN Ok(dl)
    ELSE IF ~Visible(dl, ev.named) THEN No(dl)
    ELSE CASE ev.kind = "confirm" ->
                IF dl.state = "held" \/ (dl.state = "delivered" /\ dl.reason = "unconfirmed")
                THEN Ok([dl EXCEPT !.state = "delivered", !.reason = ""]) ELSE Ok(dl)
           [] ev.kind = "ack-read" ->
                IF dl.state \in {"pending", "delivered", "held"} THEN Ok(Confirmed(dl, "read")) ELSE Ok(dl)
           [] OTHER -> Ok(Confirmed(dl, "acted"))

Apply(dl, ev) ==
    IF DeadlineFires(dl, ev) THEN Ok(Expire(dl))
    ELSE CASE ev.kind = "claim" ->
                IF dl.state = "pending" /\ dl.route \in {"native", "bridge"} /\ dl.attempts < MaxAttempts
                THEN Ok([dl EXCEPT !.state = "sending", !.pusher = ev.pusher]) ELSE No(dl)
           [] ev.kind = "announce" ->
                IF dl.route = "hooks" /\ dl.state = "pending"
                THEN Ok([dl EXCEPT !.state = "delivered"]) ELSE No(dl)
           [] ev.kind = "answer" ->
                IF dl.state = "sending" /\ dl.pusher = ev.pusher THEN Ok(AnswerOf(dl, ev.answer)) ELSE No(dl)
           [] ev.kind = "failure" ->
                IF dl.state = "sending" /\ dl.pusher = ev.pusher
                THEN Ok([dl EXCEPT !.state = "pending", !.pusher = "", !.attempts = dl.attempts + 1,
                                   !.route = IF dl.attempts + 1 >= MaxAttempts /\ dl.route = "native"
                                             THEN "hooks" ELSE dl.route])
                ELSE No(dl)
           [] ev.kind = "stale" ->
                IF dl.state = "sending"
                THEN Ok([dl EXCEPT !.state = "delivered", !.reason = "unconfirmed", !.pusher = ""]) ELSE No(dl)
           [] ev.kind \in {"expire", "close-request"} ->
                IF dl.state \in Expirable THEN Ok(Expire(dl)) ELSE No(dl)
           [] ev.kind = "end" ->
                IF dl.state \in {"pending", "sending", "held"}
                THEN Ok([dl EXCEPT !.state = "dropped", !.reason = "recipient_ended", !.pusher = "",
                                   !.notice = "owed"])
                ELSE No(dl)
           [] ev.kind = "read-whole" ->
                IF dl.state \in {"pending", "delivered"} /\ Visible(dl, ev.named)
                THEN Ok(Confirmed(dl, "read")) ELSE No(dl)
           [] ev.kind \in {"ack-read", "ack-acted", "confirm"} -> NamedCall(dl, ev)
           [] ev.kind = "freeze" ->
                IF dl.route = "mailbox" /\ dl.state = "pending"
                THEN Ok([dl EXCEPT !.state = "read"]) ELSE No(dl)
           [] OTHER -> No(dl)

(* The request's own record (deliverystate.ExpireRequest, ApplyMessage, Respond). *)

ClosedRq == [state |-> "expired", notice |-> "owed"]

ExpireRequest(r, dl) ==
    [ok |-> TRUE, rq |-> ClosedRq, d |-> IF dl.state \in Expirable THEN Expire(dl) ELSE dl]

ApplyMessage(r, dl, ev) ==
    IF dl.request /\ r.state = "open" /\ DeadlineFires(dl, ev)
    THEN LET closed == ExpireRequest(r, dl)
             after  == Apply(closed.d, ev)
         IN [ok |-> TRUE, rq |-> ClosedRq, d |-> IF after.ok THEN after.d ELSE closed.d]
    ELSE LET after == Apply(dl, ev)
         IN IF ~after.ok THEN [ok |-> FALSE, rq |-> r, d |-> dl]
            ELSE [ok |-> TRUE, rq |-> r,
                  d  |-> IF dl.request /\ r.state = "expired" /\ after.d.state \in Expirable
                         THEN Expire(after.d) ELSE after.d]

Respond(r, dl, late, named) ==
    IF r.state # "open" THEN [ok |-> FALSE, rq |-> r, d |-> dl]
    ELSE IF late THEN ExpireRequest(r, dl)
    ELSE IF dl.state \notin Final /\ ~Visible(dl, named) THEN [ok |-> FALSE, rq |-> r, d |-> dl]
    ELSE LET after == Apply(dl, Ev("ack-acted", "", "none", FALSE, named))
         IN [ok |-> after.ok, rq |-> [r EXCEPT !.state = "answered"], d |-> after.d]

(* One event through the table, with the request unless a variant keeps them apart. *)
Step(ev) ==
    IF /\ isRequest /\ ~SeparateRequestExpiry
       /\ ~(DeadlineSkipsRequest /\ ChecksDeadline(ev.kind))
       /\ ~(FailureReopens /\ ev.kind \in {"answer", "failure"})
    THEN ApplyMessage(rq, d, ev)
    ELSE LET after == Apply(d, ev) IN [ok |-> after.ok, rq |-> rq, d |-> after.d]

\* Write a result back, counting each record's notice the moment it becomes owed.
SetMsg(r) ==
    /\ d' = r.d /\ rq' = r.rq
    /\ dNotices' = dNotices + (IF d.notice = "" /\ r.d.notice = "owed" THEN 1 ELSE 0)
    /\ rNotices' = rNotices + (IF rq.notice = "" /\ r.rq.notice = "owed" THEN 1 ELSE 0)

KeepMsg == UNCHANGED <<d, rq, dNotices, rNotices>>

Naming ==
    IF handleKnown THEN "handle"
    ELSE IF idKnown /\ BareIdIsEvidence THEN "handle"
    ELSE IF idKnown THEN "id"
    ELSE "nothing"

ShowsNative(dl) == d.route = "native" /\ engine # "accepted"

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
    /\ pass = [p \in Passes |-> [pc |-> "idle", reply |-> "none"]]
    /\ hook = "idle" /\ shown = 0
    /\ handleKnown = FALSE /\ idKnown = FALSE /\ exposed = FALSE /\ pushedClosed = FALSE
    /\ overdue = FALSE /\ alive = TRUE
    /\ dNotices = 0 /\ rNotices = 0

(* The pusher's passes, or the bridge (sections 8.3 and 9.2). *)

Claim(p) ==
    /\ pass[p].pc = "idle" /\ d.route \in {"native", "bridge"}
    /\ LET r == Step(Ev("claim", p, "none", overdue, "nothing")) IN
       /\ r.ok /\ SetMsg(r)
       /\ pass' = IF r.d.state = "sending" THEN [pass EXCEPT ![p].pc = "claimed"] ELSE pass
       /\ pushedClosed' = (pushedClosed \/ (r.d.state = "sending" /\ isRequest /\ rq.state # "open"))
    /\ UNCHANGED <<isRequest, fromMailbox, engine, hook, shown, handleKnown, idKnown, exposed, overdue, alive>>

WriteFails(p) ==
    /\ pass[p].pc = "claimed"
    /\ pass' = [pass EXCEPT ![p].pc = "failed"]
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

Write(p) ==
    /\ pass[p].pc = "claimed"
    /\ \E answer \in (IF d.route = "bridge" THEN {"accepted"} ELSE Answers) :
          /\ pass' = [pass EXCEPT ![p] = [pc |-> "written", reply |-> answer]]
          /\ engine' = CASE answer = "accepted" -> "accepted"
                         [] answer = "held"     -> "held"
                         [] answer = "refused"  -> "refused"
                         [] OTHER               -> "full"
          /\ shown' = IF answer = "accepted" THEN shown + 1 ELSE shown
          /\ handleKnown' = (handleKnown \/ answer = "accepted")
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, hook, idKnown, exposed, pushedClosed, overdue, alive>>

Record(p) ==
    /\ pass[p].pc = "written"
    /\ \E answer \in {pass[p].reply, "lost"} :
          LET r == Step(Ev("answer", p, answer, FALSE, "nothing")) IN
          IF r.ok THEN SetMsg(r) ELSE KeepMsg
    /\ pass' = [pass EXCEPT ![p] = [pc |-> "idle", reply |-> "none"]]
    /\ UNCHANGED <<isRequest, fromMailbox, engine, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

RecordFailure(p) ==
    /\ pass[p].pc = "failed"
    /\ LET r == Step(Ev("failure", p, "none", FALSE, "nothing")) IN
       IF r.ok THEN SetMsg(r) ELSE KeepMsg
    /\ pass' = [pass EXCEPT ![p] = [pc |-> "idle", reply |-> "none"]]
    /\ UNCHANGED <<isRequest, fromMailbox, engine, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

Crash(p) ==
    /\ pass[p].pc \in {"claimed", "written", "failed"}
    /\ pass' = [pass EXCEPT ![p] = [pc |-> "dead", reply |-> "none"]]
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

\* Claude Code releases a held copy to the model, or silently discards it (section 4.2).
EngineReleases ==
    /\ engine = "held"
    /\ engine' = "accepted" /\ shown' = shown + 1 /\ handleKnown' = TRUE
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, pass, hook, idKnown, exposed, pushedClosed, overdue, alive>>

EngineDiscards ==
    /\ engine = "held" /\ engine' = "dropped"
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

(* The hooks route (section 8.4). *)

HookAnnounces ==
    /\ alive /\ hook = "idle"
    /\ LET r == Step(Ev("announce", "", "none", overdue, "nothing")) IN
       /\ r.ok /\ SetMsg(r)
       /\ hook' = IF r.d.state = "delivered" THEN "announced" ELSE hook
       /\ pushedClosed' = (pushedClosed \/ (r.d.state = "delivered" /\ isRequest /\ rq.state # "open"))
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, shown, handleKnown, idKnown, exposed, overdue, alive>>

HookReturns ==
    /\ hook = "announced" /\ hook' = "idle" /\ shown' = shown + 1
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, handleKnown, idKnown, exposed, pushedClosed,
                   overdue, alive>>

HookLoses ==
    /\ hook = "announced" /\ hook' = "idle"
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

(* The recipient's own calls; a review mailbox makes none. *)

Calls == alive /\ d.route # "mailbox"

InboxReads ==
    /\ Calls
    /\ LET r == Step(Ev("read-whole", "", "none", overdue, "nothing")) IN
       /\ r.ok /\ SetMsg(r)
       /\ exposed' = (exposed \/ (r.d.state = "read" /\ ShowsNative(d)))
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown,
                   pushedClosed, overdue, alive>>

InboxReadsAgain ==
    /\ Calls /\ ~overdue /\ Visible(d, "nothing")
    /\ exposed' = (exposed \/ ShowsNative(d))
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown,
                   pushedClosed, overdue, alive>>

InboxNamesIt ==
    /\ Calls /\ Naming # "nothing"
    /\ LET r == Step(Ev("confirm", "", "none", overdue, Naming)) IN
       /\ r.ok /\ SetMsg(r)
       /\ exposed' = (exposed \/ (Visible(r.d, Naming) /\ ShowsNative(d)))
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown,
                   pushedClosed, overdue, alive>>

Acks ==
    /\ Calls /\ Naming # "nothing"
    /\ \E kind \in {"ack-read", "ack-acted"} :
          LET r == Step(Ev(kind, "", "none", overdue, Naming)) IN r.ok /\ SetMsg(r)
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

Responds ==
    /\ Calls /\ isRequest /\ Naming = "handle"
    /\ LET r == Respond(rq, d, overdue, "handle") IN r.ok /\ SetMsg(r)
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

\* Another message of the thread carries the bare ID (its `thread` or `reply_to`).
IdLeaks ==
    /\ ~idKnown /\ idKnown' = TRUE
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, exposed,
                   pushedClosed, overdue, alive>>

(* The sweep, the deadline, the end, the freezes (sections 4.10, 7.4, 8.2, 9.1). *)

SweepStale ==
    /\ LET after == Apply(d, Ev("stale", "", "none", FALSE, "nothing")) IN
       after.ok /\ SetMsg([rq |-> rq, d |-> after.d])
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

SweepExpiresRequest ==
    /\ overdue /\ isRequest /\ rq.state = "open"
    /\ IF SeparateRequestExpiry THEN SetMsg([rq |-> ClosedRq, d |-> d])
       ELSE SetMsg(ExpireRequest(rq, d))
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

SweepExpiresDelivery ==
    /\ overdue /\ (~isRequest \/ SeparateRequestExpiry \/ rq.state # "open")
    /\ LET after == Apply(d, Ev("expire", "", "none", TRUE, "nothing")) IN
       after.ok /\ SetMsg([rq |-> rq, d |-> after.d])
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

RecipientMailboxFreezes ==
    /\ alive /\ ~overdue /\ d.route = "mailbox"
    /\ LET after == Apply(d, Ev("freeze", "", "none", FALSE, "nothing")) IN
       after.ok /\ SetMsg([rq |-> rq, d |-> after.d])
    /\ alive' = FALSE
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue>>

RequestingMailboxFreezes ==
    /\ fromMailbox /\ rq.state = "open" /\ ~overdue
    /\ IF FreezeLeavesDelivery THEN SetMsg([rq |-> ClosedRq, d |-> d])
       ELSE SetMsg(ExpireRequest(rq, d))
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue, alive>>

RecipientEnds ==
    /\ alive /\ alive' = FALSE
    /\ LET after == Apply(d, Ev("end", "", "none", FALSE, "nothing")) IN
       IF after.ok THEN SetMsg([rq |-> rq, d |-> after.d]) ELSE KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, overdue>>

DeadlinePasses ==
    /\ ~overdue /\ overdue' = TRUE
    /\ KeepMsg
    /\ UNCHANGED <<isRequest, fromMailbox, engine, pass, hook, shown, handleKnown, idKnown, exposed,
                   pushedClosed, alive>>

Sweep == SweepStale \/ SweepExpiresRequest \/ SweepExpiresDelivery

Next ==
    \/ \E p \in Passes : Claim(p) \/ WriteFails(p) \/ Write(p) \/ Record(p) \/ RecordFailure(p) \/ Crash(p)
    \/ EngineReleases \/ EngineDiscards
    \/ HookAnnounces \/ HookReturns \/ HookLoses
    \/ InboxReads \/ InboxReadsAgain \/ InboxNamesIt \/ Acks \/ Responds \/ IdLeaks
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
    /\ pass \in [Passes -> [pc : {"idle", "claimed", "written", "failed", "dead"},
                            reply : Answers \cup {"none"}]]
    /\ engine \in {"none", "accepted", "held", "refused", "full", "dropped"}

\* Every route pushes a message at most once.
AtMostOnce == shown <= 1

\* A native body is never returned by a read before Claude Code accepted it.
NoEarlyExposure == ~exposed

\* A line that may have reached the engine, or a hook's announcement, is never recorded as not pushed.
NeverLooksUnpushed == (engine # "none" \/ hook = "announced") => d.state # "pending"

\* A request closed before its deadline is never pushed afterwards.
NoPushAfterClose == ~pushedClosed

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

\* The deadline is applied before an ack, a read or a push.
DeadlineFirst ==
    [][(overdue /\ d.state \in Expirable /\ d'.state # d.state) => d'.state \in {"expired", "dropped"}]_vars

\* Liveness: a `sending` record is always resolved, and an overdue delivery always expires.
SendingResolves == d.state = "sending" ~> d.state # "sending"
OverdueExpires  == (overdue /\ d.state \in Expirable) ~> d.state \notin Expirable
=============================================================================
