----------------------------- MODULE ChatSend -----------------------------
(***************************************************************************)
(* The journalled send of the agent chat (docs/design/chat.md, sections   *)
(* 7.2 to 7.4): one send and its retry with the same idempotency key, a    *)
(* process crash or a machine crash at every step (a machine crash loses a *)
(* commit record whose folder could not be fsynced), recovery before       *)
(* anything is served, the pusher, sessions registering near the store's   *)
(* limit, and the sweep of leftovers. The rules are those of               *)
(* internal/sessionchat/deliverystate (Recover, Served, ResolveRetry,      *)
(* SendCharge); its journal test explores the same world in Go. Each       *)
(* constant named after a rule replays it as the spec stated it before     *)
(* this model, and set TRUE yields the counterexample in README.md.       *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
    Limit, Core, Derived, SessionCharge, MaxSessions,  \* charges, in units
    RecoveryIgnoresSync,   \* section 7.2 before: recovery rolls a written commit forward unsynced
    DerivedChargedLate,    \* section 7.4 before: markers and the idempotency entry charged at step 4
    RetryIgnoresEntry,     \* section 7.2 before: a retry with no idempotency entry is a fresh send
    ChargeNotReserved,     \* section 7.4 before: a kept entry holds only the charges of its files
    RivalIgnoresEntry      \* review round 1: another response or the deadline closes a request a kept response names

Messages == {1, 2}   \* 1: the send; 2: its retry, if it became a fresh send
None == 0

VARIABLES
    msg,          \* per message: journal, files, commit, followOn, derived, markers, pushed
    idempotency,  \* the message the idempotency entry names, or None
    attempt,      \* the attempt in progress or last made
    phase,        \* where the attempt in progress is; "idle" when no process holds the lock
    answer,       \* what each attempt was answered
    sessions,
    request,         \* the request this send answers: "open", "answered", "expired"
    requestChanges   \* times the request left open, or was answered again

vars == <<msg, idempotency, attempt, phase, answer, sessions, request, requestChanges>>
RequestVars == <<request, requestChanges>>

Used ==
    LET charge(m) ==
          IF msg[m].journal /\ ~DerivedChargedLate /\ ~ChargeNotReserved THEN Core + Derived
          ELSE (IF msg[m].files THEN Core ELSE 0)
               + (IF msg[m].derived /\ msg[m].markers THEN Derived ELSE 0)
    IN sessions * SessionCharge + charge(1) + charge(2)

Admit(charge) == Used + charge <= Limit

\* deliverystate.Recover, Served and ResolveRetry.
Recover(commit, synced) ==
    CASE commit = "none"                  -> "roll-back"
      [] commit = "durable" \/ synced     -> "roll-forward"
      [] OTHER                            -> "keep"

Served(m) ==
    IF RecoveryIgnoresSync THEN msg[m].commit # "none" /\ ~msg[m].journal
    ELSE msg[m].commit = "durable" /\ ~msg[m].journal

\* No process holds the lock, and recovery has done what it can: only entries it must keep remain.
LockFree ==
    /\ phase = "idle"
    /\ \A m \in Messages : msg[m].journal => (~RecoveryIgnoresSync /\ msg[m].commit = "written")

Init ==
    /\ msg = [m \in Messages |-> [journal |-> FALSE, files |-> FALSE, commit |-> "none",
                                  followOn |-> FALSE, derived |-> FALSE, markers |-> TRUE,
                                  pushed |-> 0]]
    /\ idempotency = None /\ attempt = 0 /\ phase = "idle"
    /\ answer = [m \in Messages |-> "none"]
    /\ sessions = 0
    /\ request = "open" /\ requestChanges = 0

\* A send, or its retry with the same key after an uncertain or lost answer.
SendAttempt ==
    /\ LockFree
    /\ \/ attempt = 0
       \/ attempt = 1 /\ answer[1] \in {"uncertain", "crashed"}
    /\ LET slot == attempt + 1
           kept == msg[1].journal /\ ~RecoveryIgnoresSync /\ ~RetryIgnoresEntry
           charge == IF DerivedChargedLate THEN Core ELSE Core + Derived
       IN CASE idempotency # None ->
                 /\ attempt' = slot /\ answer' = [answer EXCEPT ![slot] = "duplicate"]
                 /\ UNCHANGED <<msg, phase>>
            [] kept ->
                 /\ answer' = [answer EXCEPT ![1] = "uncertain"]
                 /\ UNCHANGED <<msg, phase, attempt>>
            [] request # "open" ->
                 /\ attempt' = slot /\ answer' = [answer EXCEPT ![slot] = "closed"]
                 /\ UNCHANGED <<msg, phase>>
            [] ~Admit(charge) ->
                 /\ attempt' = slot /\ answer' = [answer EXCEPT ![slot] = "capacity"]
                 /\ UNCHANGED <<msg, phase>>
            [] OTHER ->
                 /\ attempt' = slot /\ phase' = "journalled"
                 /\ msg' = [msg EXCEPT ![slot].journal = TRUE]
                 /\ UNCHANGED answer
    /\ UNCHANGED <<idempotency, sessions>> /\ UNCHANGED RequestVars

\* The steps of section 7.3 under the lock.
Proceed ==
    LET m == attempt IN
    /\ phase # "idle"
    /\ CASE phase = "journalled" ->
              /\ msg' = [msg EXCEPT ![m].files = TRUE] /\ phase' = "files"
              /\ UNCHANGED <<answer, idempotency>>
         [] phase = "files" ->
              /\ msg' = [msg EXCEPT ![m].commit = "written"] /\ phase' = "committed"
              /\ UNCHANGED <<answer, idempotency>>
         [] phase = "committed" ->
              \/ /\ msg' = [msg EXCEPT ![m].commit = "durable"] /\ phase' = "follow"
                 /\ UNCHANGED <<answer, idempotency>>
              \/ /\ UNCHANGED <<msg, idempotency>>
                 /\ IF RecoveryIgnoresSync THEN phase' = "follow" /\ UNCHANGED answer
                    ELSE phase' = "idle" /\ answer' = [answer EXCEPT ![m] = "uncertain"]
         [] phase = "follow" ->
              /\ msg' = [msg EXCEPT ![m].followOn = TRUE] /\ phase' = "derived"
              /\ UNCHANGED <<answer, idempotency>>
         [] phase = "derived" ->
              LET fits == ~DerivedChargedLate \/ Admit(Derived) IN
              /\ msg' = [msg EXCEPT ![m].derived = TRUE, ![m].markers = fits]
              /\ idempotency' = IF fits THEN m ELSE idempotency
              /\ phase' = "done" /\ UNCHANGED answer
         [] phase = "done" ->
              /\ IF msg[m].commit = "durable"
                 THEN msg' = [msg EXCEPT ![m].journal = FALSE] /\ answer' = [answer EXCEPT ![m] = "accepted"]
                 ELSE UNCHANGED msg /\ answer' = [answer EXCEPT ![m] = "uncertain"]
              /\ phase' = "idle" /\ UNCHANGED idempotency
    /\ UNCHANGED <<attempt, sessions>>
    /\ IF phase = "follow" THEN request' = "answered" /\ requestChanges' = requestChanges + 1
       ELSE UNCHANGED RequestVars

ProcessCrashes ==
    /\ phase # "idle"
    /\ phase' = "idle" /\ answer' = [answer EXCEPT ![attempt] = "crashed"]
    /\ UNCHANGED <<msg, idempotency, attempt, sessions>> /\ UNCHANGED RequestVars

\* Recovery, under the lock, before anything else.
Recovers ==
    /\ phase = "idle"
    /\ \E m \in Messages, synced \in BOOLEAN :
         /\ msg[m].journal
         /\ LET decision == IF RecoveryIgnoresSync /\ Recover(msg[m].commit, synced) = "keep"
                            THEN "roll-forward" ELSE Recover(msg[m].commit, synced)
                fits == ~DerivedChargedLate \/ Admit(Derived)
            IN CASE decision = "roll-back" ->
                      /\ msg' = [msg EXCEPT ![m].files = FALSE, ![m].journal = FALSE]
                      /\ idempotency' = IF ~RecoveryIgnoresSync /\ idempotency = m THEN None ELSE idempotency
                      /\ UNCHANGED RequestVars
                 [] decision = "roll-forward" ->
                      /\ msg' = [msg EXCEPT
                                   ![m].commit = IF synced /\ ~RecoveryIgnoresSync THEN "durable" ELSE @,
                                   ![m].followOn = TRUE,
                                   ![m].markers = IF msg[m].derived THEN @ ELSE fits,
                                   ![m].derived = TRUE,
                                   ![m].journal = FALSE]
                      /\ idempotency' = IF ~msg[m].derived /\ fits THEN m ELSE idempotency
                      /\ IF msg[m].followOn THEN UNCHANGED RequestVars
                         ELSE request' = "answered" /\ requestChanges' = requestChanges + 1
                 [] OTHER -> FALSE
    /\ UNCHANGED <<attempt, phase, answer, sessions>>

Pushes ==
    /\ LockFree
    /\ \E m \in Messages : Served(m) /\ msg[m].pushed = 0 /\ msg' = [msg EXCEPT ![m].pushed = 1]
    /\ UNCHANGED <<idempotency, attempt, phase, answer, sessions>> /\ UNCHANGED RequestVars

SweepsLeftovers ==
    /\ LockFree
    /\ \E m \in Messages :
         /\ ~msg[m].journal /\ msg[m].commit = "none" /\ msg[m].files
         /\ msg' = [msg EXCEPT ![m].files = FALSE, ![m].derived = FALSE]
    /\ UNCHANGED <<idempotency, attempt, phase, answer, sessions>> /\ UNCHANGED RequestVars

SessionRegisters ==
    /\ LockFree /\ sessions < MaxSessions /\ Admit(SessionCharge)
    /\ sessions' = sessions + 1
    /\ UNCHANGED <<msg, idempotency, attempt, phase, answer>> /\ UNCHANGED RequestVars

\* A machine crash: what was never fsynced is gone, and so is the process holding the lock.
MachineCrashes ==
    /\ \/ \E m \in Messages : msg[m].commit = "written"
       \/ phase # "idle"
    /\ msg' = [m \in Messages |-> IF msg[m].commit = "written" THEN [msg[m] EXCEPT !.commit = "none"]
                                  ELSE msg[m]]
    /\ IF phase # "idle" THEN phase' = "idle" /\ answer' = [answer EXCEPT ![attempt] = "crashed"]
       ELSE UNCHANGED <<phase, answer>>
    /\ UNCHANGED <<idempotency, attempt, sessions>> /\ UNCHANGED RequestVars

\* Another response (with another key), or the request's deadline, closes the request: refused
\* (store_busy, or waiting) while a kept journal entry may still answer it (ResponseAdmitted).
RequestClosesElsewhere ==
    /\ LockFree /\ request = "open"
    /\ RivalIgnoresEntry \/ ~\E m \in Messages : msg[m].journal
    /\ request' \in {"answered", "expired"} /\ requestChanges' = requestChanges + 1
    /\ UNCHANGED <<msg, idempotency, attempt, phase, answer, sessions>>

Next ==
    \/ SendAttempt \/ Proceed \/ ProcessCrashes \/ Recovers \/ Pushes \/ SweepsLeftovers
    \/ SessionRegisters \/ MachineCrashes \/ RequestClosesElsewhere

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
(* Properties. *)

TypeOK ==
    /\ \A m \in Messages : msg[m].commit \in {"none", "written", "durable"} /\ msg[m].pushed \in 0..1
    /\ idempotency \in {None} \cup Messages /\ attempt \in 0..2 /\ sessions \in 0..MaxSessions

\* The charges never pass the write limit, so the last MiB stays for journal and temporary files.
Capacity == Used <= Limit

\* One send, retried with its key, is pushed at most once.
AtMostOncePerKey == msg[1].pushed + msg[2].pushed <= 1

\* A `duplicate` answer names a message that exists.
DuplicateIsReal == \A m \in Messages : idempotency = m => (msg[m].commit # "none" \/ msg[m].journal)

\* A subscription, or a request's answer, written by a send stands only for a message that exists
\* (so a rollback leaves the earlier subscription, and an unanswered request, untouched).
FollowOnAgrees == \A m \in Messages : msg[m].followOn => (msg[m].commit # "none" \/ msg[m].journal)

\* A committed send keeps every marker it was admitted with.
MarkersComplete == \A m \in Messages : msg[m].derived => msg[m].markers

\* A request and its response agree: the request is answered or closed once, never overwritten.
RequestAgrees == requestChanges <= 1

\* `accepted` is said only of a send no crash can undo.
AcceptedIsDurable == \A m \in Messages : answer[m] = "accepted" => msg[m].commit = "durable"
=============================================================================
