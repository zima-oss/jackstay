---------------------------- MODULE ChatDelivery ----------------------------
(***************************************************************************)
(* A first sketch of one delivery of the agent chat (docs/design/chat.md,  *)
(* sections 4.2, 4.10, 7.3 and 8): one message, one recipient, on the      *)
(* native route (with its fallback to the hooks route), a pusher that can  *)
(* crash at any step, a recipient that reads, acknowledges and ends, the   *)
(* sweep, and a deadline that can pass at any moment.                      *)
(*                                                                         *)
(* It has not been run through TLC yet; checking it, and widening it to    *)
(* the bridge route, requests, notices and the journal of section 7.3, is  *)
(* the first deliverable of the implementation (section 15.0).             *)
(***************************************************************************)
EXTENDS Naturals

CONSTANT MaxAttempts            \* confirmed failures before the route moves to hooks (4)

States    == {"pending", "sending", "delivered", "held", "read", "acted",
              "refused", "dropped", "expired"}
Final     == {"acted", "refused", "dropped", "expired"}
Engines   == {"none", "accepted", "held", "refused"}
PusherPcs == {"idle", "claimed", "written", "failed", "dead"}

VARIABLES
    route,        \* "native" or "hooks"
    state,        \* the delivery's recorded state
    unconfirmed,  \* the reason `unconfirmed` on a `delivered` delivery
    attempts,     \* confirmed native failures
    pc,           \* where the pusher is
    owner,        \* the pusher still owns its `sending` record (compare-and-set)
    engine,       \* what Claude Code actually did with a written line (it may change later)
    reply,        \* the answer the socket carried back, fixed when the line is written
    shown,        \* how many times the text reached the recipient's model by a push
    exposed,      \* a read showed a native body the engine had not accepted
    overdue,      \* expires_at has passed
    alive         \* the recipient session still exists

vars == <<route, state, unconfirmed, attempts, pc, owner, engine, reply, shown, exposed, overdue, alive>>

TypeOK ==
    /\ route \in {"native", "hooks"}
    /\ state \in States
    /\ unconfirmed \in BOOLEAN
    /\ attempts \in 0..MaxAttempts
    /\ pc \in PusherPcs
    /\ owner \in BOOLEAN
    /\ engine \in Engines
    /\ reply \in Engines
    /\ shown \in 0..3
    /\ exposed \in BOOLEAN
    /\ overdue \in BOOLEAN
    /\ alive \in BOOLEAN

Init ==
    /\ route = "native"
    /\ state = "pending"
    /\ unconfirmed = FALSE
    /\ attempts = 0
    /\ pc = "idle"
    /\ owner = FALSE
    /\ engine = "none"
    /\ reply = "none"
    /\ shown = 0
    /\ exposed = FALSE
    /\ overdue = FALSE
    /\ alive = TRUE

(* Section 4.10: is the body visible to the recipient's own reads? *)
Visible ==
    IF route = "native"
    THEN \/ (state = "delivered" /\ ~unconfirmed)
         \/ state \in {"read", "acted"}
    ELSE state \notin {"held", "refused", "dropped", "expired"}

----------------------------------------------------------------------------
(* The native pusher (section 8.3). *)

\* Step 1: under the lock, check the deadline, then claim.
Claim ==
    /\ route = "native" /\ state = "pending" /\ pc = "idle" /\ alive
    /\ IF overdue
       THEN /\ state' = "expired"
            /\ UNCHANGED <<pc, owner>>
       ELSE /\ state' = "sending" /\ pc' = "claimed" /\ owner' = TRUE
    /\ UNCHANGED <<route, unconfirmed, attempts, engine, reply, shown, exposed, overdue, alive>>

\* Step 2: the line is written; the engine decides; the answer may or may not come back.
Write ==
    /\ pc = "claimed"
    /\ \E decision \in {"accepted", "held", "refused"} :
          /\ engine' = decision /\ reply' = decision
          /\ shown' = IF decision = "accepted" THEN shown + 1 ELSE shown
    /\ pc' = "written"
    /\ UNCHANGED <<route, state, unconfirmed, attempts, owner, exposed, overdue, alive>>

\* Step 2, failed before anything was written.
WriteFails ==
    /\ pc = "claimed"
    /\ pc' = "failed"
    /\ UNCHANGED <<route, state, unconfirmed, attempts, owner, engine, reply, shown, exposed, overdue, alive>>

\* Step 3: the answer arrived; recorded only if the pusher still owns the record.
RecordAnswer ==
    /\ pc = "written"
    /\ IF owner /\ state = "sending"
       THEN state' = CASE reply = "accepted" -> "delivered"
                       [] reply = "held"     -> "held"
                       [] reply = "refused"  -> "refused"
                       [] OTHER              -> state
       ELSE UNCHANGED state
    /\ pc' = "idle" /\ owner' = FALSE
    /\ UNCHANGED <<route, unconfirmed, attempts, engine, reply, shown, exposed, overdue, alive>>

\* Step 3: the answer was lost: `delivered` with `unconfirmed`, never retried.
RecordLost ==
    /\ pc = "written"
    /\ IF owner /\ state = "sending"
       THEN /\ state' = "delivered" /\ unconfirmed' = TRUE
       ELSE UNCHANGED <<state, unconfirmed>>
    /\ pc' = "idle" /\ owner' = FALSE
    /\ UNCHANGED <<route, attempts, engine, reply, shown, exposed, overdue, alive>>

\* Step 3: a confirmed failure: back to pending; the fourth moves the route to hooks.
RecordFailure ==
    /\ pc = "failed"
    /\ IF owner /\ state = "sending"
       THEN /\ state' = "pending"
            /\ attempts' = attempts + 1
            /\ route' = IF attempts + 1 = MaxAttempts THEN "hooks" ELSE route
       ELSE UNCHANGED <<state, attempts, route>>
    /\ pc' = "idle" /\ owner' = FALSE
    /\ UNCHANGED <<unconfirmed, engine, reply, shown, exposed, overdue, alive>>

\* The pusher dies wherever it is.
Crash ==
    /\ pc \in {"claimed", "written", "failed"}
    /\ pc' = "dead"
    /\ UNCHANGED <<route, state, unconfirmed, attempts, owner, engine, reply, shown, exposed, overdue, alive>>

\* A held copy that Claude Code later releases to the model (section 4.2).
EngineReleases ==
    /\ engine = "held"
    /\ engine' = "accepted" /\ shown' = shown + 1
    /\ UNCHANGED <<route, state, unconfirmed, attempts, pc, owner, reply, exposed, overdue, alive>>

----------------------------------------------------------------------------
(* The hooks route (section 8.4): recorded `delivered`, then shown, or lost in a crash. *)

HookAnnounces ==
    /\ route = "hooks" /\ state = "pending" /\ alive
    /\ IF overdue
       THEN /\ state' = "expired" /\ UNCHANGED shown
       ELSE /\ state' = "delivered"
            /\ shown' \in {shown, shown + 1}
    /\ UNCHANGED <<route, unconfirmed, attempts, pc, owner, engine, reply, exposed, overdue, alive>>

----------------------------------------------------------------------------
(* The recipient, the sweep and time. *)

\* chat.inbox without ids: it returns the body only when Visible says so (section 4.10), and
\* exposed records a body returned for a native delivery the engine never accepted.
InboxRead ==
    /\ alive
    /\ IF overdue /\ state \in {"pending", "held"}
       THEN /\ state' = "expired" /\ UNCHANGED exposed
       ELSE /\ state' = IF Visible /\ state \in {"pending", "delivered"} THEN "read" ELSE state
            /\ exposed' = (exposed \/ (Visible /\ route = "native" /\ engine # "accepted"))
    /\ UNCHANGED <<route, unconfirmed, attempts, pc, owner, engine, reply, shown, overdue, alive>>

Ack ==
    /\ alive
    /\ state \in {"pending", "sending", "delivered", "held", "read"}
    /\ route = "native" => engine = "accepted"     \* the recipient knows the ID only from a shown copy
    /\ state' = IF overdue /\ state \in {"pending", "held"} THEN "expired" ELSE "acted"
    /\ owner' = IF state = "sending" THEN FALSE ELSE owner
    /\ UNCHANGED <<route, unconfirmed, attempts, pc, engine, reply, shown, exposed, overdue, alive>>

\* chat.inbox naming the ID: the recipient knows it only from a frame its engine showed it.
ReadById ==
    /\ alive /\ route = "native" /\ engine = "accepted"
    /\ \/ state = "held"
       \/ state = "delivered" /\ unconfirmed
    /\ IF overdue /\ state = "held"
       THEN /\ state' = "expired" /\ UNCHANGED unconfirmed
       ELSE /\ state' = "delivered" /\ unconfirmed' = FALSE
    /\ UNCHANGED <<route, attempts, pc, owner, engine, reply, shown, exposed, overdue, alive>>

SessionEnds ==
    /\ alive
    /\ alive' = FALSE
    /\ state' = IF state \in {"pending", "sending", "held"} THEN "dropped" ELSE state
    /\ owner' = FALSE
    /\ UNCHANGED <<route, unconfirmed, attempts, pc, engine, reply, shown, exposed, overdue>>

DeadlinePasses ==
    /\ ~overdue
    /\ overdue' = TRUE
    /\ UNCHANGED <<route, state, unconfirmed, attempts, pc, owner, engine, reply, shown, exposed, alive>>

\* Section 7.4: a stale `sending` becomes `delivered`, `unconfirmed`; overdue pending or held expire.
Sweep ==
    \/ /\ state = "sending" /\ pc \in {"claimed", "written", "failed", "dead"}
       /\ state' = "delivered" /\ unconfirmed' = TRUE /\ owner' = FALSE
       /\ pc' = IF pc = "dead" THEN "idle" ELSE pc
       /\ UNCHANGED <<route, attempts, engine, reply, shown, exposed, overdue, alive>>
    \/ /\ overdue /\ state \in {"pending", "held"}
       /\ state' = "expired"
       /\ UNCHANGED <<route, unconfirmed, attempts, pc, owner, engine, reply, shown, exposed, overdue, alive>>

Next ==
    \/ Claim \/ Write \/ WriteFails \/ RecordAnswer \/ RecordLost \/ RecordFailure \/ Crash
    \/ EngineReleases \/ HookAnnounces \/ InboxRead \/ ReadById \/ Ack \/ SessionEnds
    \/ DeadlinePasses
    \/ Sweep

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
(* Properties (section 15.0). *)

\* A non-bridge route puts the text in front of the model at most once.
AtMostOnce == shown <= 1

\* A native body is never shown by a read before Claude Code accepted it.
NoEarlyExposure == ~exposed

\* A line that may have reached the engine is never recorded as not pushed.
NeverLooksUnpushed == (route = "native" /\ engine # "none") => state # "pending"

\* Final states are never left.
FinalStable == [][state \in Final => state' = state]_vars

\* Nothing moves an overdue pending or held delivery to acted (section 4.10).
DeadlineFirst == [][(overdue /\ state \in {"pending", "held"}) => state' # "acted"]_vars

=============================================================================
