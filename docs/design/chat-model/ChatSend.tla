----------------------------- MODULE ChatSend -----------------------------
(***************************************************************************)
(* The journalled send of the agent chat (docs/design/chat.md, sections   *)
(* 7.2 to 7.4): one response (a send that answers a request) and its retry *)
(* with the same idempotency key, a process crash or a machine crash at    *)
(* every step (a machine crash loses a commit record whose folder could    *)
(* not be fsynced), recovery before anything is served, the store stopped  *)
(* while an entry cannot be resolved, the pusher, sessions registering     *)
(* near the store's limit, another response or the deadline closing the    *)
(* request, the sweep of leftovers, and retention removing the message in  *)
(* two steps a crash can cut apart. The rules are those of                 *)
(* internal/sessionchat/deliverystate (Recover, Served, Stopped,           *)
(* ResolveRetry, ResponseAdmitted, SendCharge); its journal test explores  *)
(* the same world in Go. Each constant named after a rule replays it as    *)
(* the spec stated it before; set TRUE, it yields the counterexample in    *)
(* README.md.                                                              *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
    Limit, Core, Derived, SessionCharge, MaxSessions,  \* charges, in units
    RecoveryIgnoresSync,    \* section 7.2 before: recovery rolls a written commit forward unsynced
    DerivedChargedLate,     \* section 7.4 before: markers and the idempotency entry charged at step 4
    KeptEntryLetsOthersGo,  \* review rounds 1 and 2: operations go on beside an entry recovery keeps
    DuplicateTrustsEntry,   \* review round 2: a retry trusts the idempotency entry without its commit
    CleanupBeforeUnlink     \* review round 3: retention removes files before the commit's unlink is fsynced

Messages == {1, 2}   \* 1: the send; 2: its retry, if it became a fresh send
None == 0

VARIABLES
    msg,             \* per message: journal, files, commit, followOn, derived, markers, pushed, retired
    idempotency,     \* the message the idempotency entry names, or None
    attempt,         \* the attempt in progress or last made
    phase,           \* where the attempt in progress is; "idle" when no process holds the lock
    answer,          \* what each attempt was answered
    sessions,
    request,         \* the request this send answers: "open", "answered", "expired"
    requestChanges,  \* times the request left open, or was answered again
    phantom,         \* a retry was told `duplicate` of a message that does not exist
    forgotten        \* retention removed the message, so its key is no longer remembered

vars == <<msg, idempotency, attempt, phase, answer, sessions, request, requestChanges, phantom, forgotten>>
Rest == <<sessions, phantom, forgotten>>

Used ==
    LET charge(m) == (IF msg[m].files THEN Core ELSE 0)
                     + (IF msg[m].derived /\ msg[m].markers THEN Derived ELSE 0)
    IN sessions * SessionCharge + charge(1) + charge(2)

Admit(charge) == Used + charge <= Limit

\* deliverystate.Recover and Served.
Recover(commit, synced) ==
    CASE commit = "none"              -> "roll-back"
      [] commit = "durable" \/ synced -> "roll-forward"
      [] OTHER                        -> "keep"

Served(m) ==
    /\ ~msg[m].unlinking
    /\ IF RecoveryIgnoresSync THEN msg[m].commit # "none" /\ ~msg[m].journal
       ELSE msg[m].commit = "durable" /\ ~msg[m].journal

\* No process holds the lock, recovery has resolved what it can, and no entry it had to keep
\* stops the store (deliverystate.Stopped); the old rule let operations go on beside a kept entry.
LockFree ==
    /\ phase = "idle"
    /\ \A m \in Messages : msg[m].journal => (~RecoveryIgnoresSync /\ msg[m].commit = "written")
    /\ KeptEntryLetsOthersGo \/ \A m \in Messages : ~msg[m].journal

AnswerRequest ==
    /\ request' = "answered" /\ requestChanges' = requestChanges + 1

Init ==
    /\ msg = [m \in Messages |-> [journal |-> FALSE, files |-> FALSE, commit |-> "none",
                                  followOn |-> FALSE, derived |-> FALSE, markers |-> TRUE,
                                  pushed |-> 0, retired |-> FALSE, unlinking |-> FALSE]]
    /\ idempotency = None /\ attempt = 0 /\ phase = "idle"
    /\ answer = [m \in Messages |-> "none"]
    /\ sessions = 0
    /\ request = "open" /\ requestChanges = 0
    /\ phantom = FALSE /\ forgotten = FALSE

\* A response, or its retry with the same key after an uncertain or lost answer.
SendAttempt ==
    /\ LockFree
    /\ \/ attempt = 0
       \/ attempt = 1 /\ answer[1] \in {"uncertain", "crashed"}
    /\ LET slot      == attempt + 1
           committed == idempotency # None /\ msg[idempotency].commit # "none"
                        /\ ~msg[idempotency].unlinking
           duplicate == idempotency # None /\ (committed \/ DuplicateTrustsEntry)
           charge    == IF DerivedChargedLate THEN Core ELSE Core + Derived
       IN /\ attempt' = slot
          /\ CASE duplicate ->
                    /\ answer' = [answer EXCEPT ![slot] = "duplicate"]
                    /\ phantom' = (phantom \/ ~committed)
                    /\ UNCHANGED <<msg, phase, idempotency>>
               [] request # "open" ->
                    /\ answer' = [answer EXCEPT ![slot] = "closed"]
                    /\ UNCHANGED <<msg, phase, idempotency, phantom>>
               [] ~Admit(charge) ->
                    /\ answer' = [answer EXCEPT ![slot] = "capacity"]
                    /\ UNCHANGED <<msg, phase, idempotency, phantom>>
               [] OTHER ->
                    \* a stale entry (its message is gone) is removed and the send is fresh
                    /\ idempotency' = None
                    /\ phase' = "journalled"
                    /\ msg' = [msg EXCEPT ![slot].journal = TRUE]
                    /\ UNCHANGED <<answer, phantom>>
    /\ UNCHANGED <<sessions, forgotten, request, requestChanges>>

\* The steps of section 7.3 under the lock.
Proceed ==
    LET m == attempt IN
    /\ phase # "idle"
    /\ CASE phase = "journalled" ->
              /\ msg' = [msg EXCEPT ![m].files = TRUE] /\ phase' = "files"
              /\ UNCHANGED <<answer, idempotency, request, requestChanges>>
         [] phase = "files" ->
              /\ msg' = [msg EXCEPT ![m].commit = "written"] /\ phase' = "committed"
              /\ UNCHANGED <<answer, idempotency, request, requestChanges>>
         [] phase = "committed" ->
              /\ UNCHANGED <<idempotency, request, requestChanges>>
              /\ \/ /\ msg' = [msg EXCEPT ![m].commit = "durable"] /\ phase' = "follow"
                    /\ UNCHANGED answer
                 \/ /\ UNCHANGED msg
                    /\ IF RecoveryIgnoresSync THEN phase' = "follow" /\ UNCHANGED answer
                       ELSE phase' = "idle" /\ answer' = [answer EXCEPT ![m] = "uncertain"]
         [] phase = "follow" ->
              /\ msg' = [msg EXCEPT ![m].followOn = TRUE] /\ phase' = "derived"
              /\ AnswerRequest
              /\ UNCHANGED <<answer, idempotency>>
         [] phase = "derived" ->
              LET fits == ~DerivedChargedLate \/ Admit(Derived) IN
              /\ msg' = [msg EXCEPT ![m].derived = TRUE, ![m].markers = fits]
              /\ idempotency' = IF fits THEN m ELSE idempotency
              /\ phase' = "done"
              /\ UNCHANGED <<answer, request, requestChanges>>
         [] phase = "done" ->
              /\ IF msg[m].commit = "durable"
                 THEN msg' = [msg EXCEPT ![m].journal = FALSE] /\ answer' = [answer EXCEPT ![m] = "accepted"]
                 ELSE UNCHANGED msg /\ answer' = [answer EXCEPT ![m] = "uncertain"]
              /\ phase' = "idle"
              /\ UNCHANGED <<idempotency, request, requestChanges>>
    /\ UNCHANGED <<attempt>> /\ UNCHANGED Rest

ProcessCrashes ==
    /\ phase # "idle"
    /\ phase' = "idle" /\ answer' = [answer EXCEPT ![attempt] = "crashed"]
    /\ UNCHANGED <<msg, idempotency, attempt, request, requestChanges>> /\ UNCHANGED Rest

\* Recovery, under the lock, before anything else; an entry it must keep stops the store.
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
                      /\ UNCHANGED <<request, requestChanges>>
                 [] decision = "roll-forward" ->
                      /\ msg' = [msg EXCEPT
                                   ![m].commit = IF synced /\ ~RecoveryIgnoresSync THEN "durable" ELSE @,
                                   ![m].followOn = TRUE,
                                   ![m].markers = IF msg[m].derived THEN @ ELSE fits,
                                   ![m].derived = TRUE,
                                   ![m].journal = FALSE]
                      /\ idempotency' = IF ~msg[m].derived /\ fits THEN m ELSE idempotency
                      /\ IF msg[m].followOn THEN UNCHANGED <<request, requestChanges>> ELSE AnswerRequest
                 [] OTHER -> FALSE
    /\ UNCHANGED <<attempt, phase, answer>> /\ UNCHANGED Rest

Pushes ==
    /\ LockFree
    /\ \E m \in Messages : Served(m) /\ msg[m].pushed = 0 /\ msg' = [msg EXCEPT ![m].pushed = 1]
    /\ UNCHANGED <<idempotency, attempt, phase, answer, request, requestChanges>> /\ UNCHANGED Rest

SweepsLeftovers ==
    /\ LockFree
    /\ \E m \in Messages :
         /\ ~msg[m].journal /\ msg[m].commit = "none" /\ msg[m].files /\ ~msg[m].retired
         /\ msg' = [msg EXCEPT ![m].files = FALSE, ![m].derived = FALSE]
    /\ UNCHANGED <<idempotency, attempt, phase, answer, request, requestChanges>> /\ UNCHANGED Rest

\* Retention removes a done message: it unlinks the commit record and fsyncs its folder, then
\* removes the rest and the idempotency entry; a crash may come between any two.
RetentionUnlinks ==
    /\ LockFree
    /\ \E m \in Messages :
         /\ ~msg[m].journal /\ msg[m].commit = "durable" /\ msg[m].pushed = 1 /\ ~msg[m].unlinking
         /\ msg' = [msg EXCEPT ![m].unlinking = TRUE]
    /\ forgotten' = TRUE
    /\ UNCHANGED <<idempotency, attempt, phase, answer, request, requestChanges, sessions, phantom>>

RetentionSyncs ==
    /\ LockFree
    /\ \E m \in Messages :
         /\ msg[m].unlinking
         /\ msg' = [msg EXCEPT ![m].unlinking = FALSE, ![m].commit = "none", ![m].retired = TRUE]
    /\ UNCHANGED <<idempotency, attempt, phase, answer, request, requestChanges>> /\ UNCHANGED Rest

RetentionRemovesRest ==
    /\ LockFree
    /\ \E m \in Messages :
         /\ msg[m].retired \/ (CleanupBeforeUnlink /\ msg[m].unlinking)
         /\ msg[m].files
         /\ msg' = [msg EXCEPT ![m].files = FALSE, ![m].derived = FALSE]
         /\ idempotency' = IF idempotency = m THEN None ELSE idempotency
    /\ UNCHANGED <<attempt, phase, answer, request, requestChanges>> /\ UNCHANGED Rest

\* Another response (with another key) answers the request, or its deadline closes it.
RequestClosesElsewhere ==
    /\ LockFree /\ request = "open"
    /\ request' \in {"answered", "expired"} /\ requestChanges' = requestChanges + 1
    /\ UNCHANGED <<msg, idempotency, attempt, phase, answer>> /\ UNCHANGED Rest

SessionRegisters ==
    /\ LockFree /\ sessions < MaxSessions /\ Admit(SessionCharge)
    /\ sessions' = sessions + 1
    /\ UNCHANGED <<msg, idempotency, attempt, phase, answer, request, requestChanges, phantom, forgotten>>

\* A machine crash: what was never fsynced is gone, and so is the process holding the lock.
MachineCrashes ==
    /\ \/ \E m \in Messages : msg[m].commit = "written" \/ msg[m].unlinking
       \/ phase # "idle"
    /\ msg' = [m \in Messages |-> [msg[m] EXCEPT
                 !.commit = IF msg[m].commit = "written" THEN "none" ELSE @,
                 !.unlinking = FALSE]]   \* an unsynced unlink is undone
    /\ IF phase # "idle" THEN phase' = "idle" /\ answer' = [answer EXCEPT ![attempt] = "crashed"]
       ELSE UNCHANGED <<phase, answer>>
    /\ UNCHANGED <<idempotency, attempt, request, requestChanges>> /\ UNCHANGED Rest

Next ==
    \/ SendAttempt \/ Proceed \/ ProcessCrashes \/ Recovers \/ Pushes \/ SweepsLeftovers
    \/ RetentionUnlinks \/ RetentionSyncs \/ RetentionRemovesRest \/ RequestClosesElsewhere
    \/ SessionRegisters \/ MachineCrashes

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
(* Properties. *)

TypeOK ==
    /\ \A m \in Messages : msg[m].commit \in {"none", "written", "durable"} /\ msg[m].pushed \in 0..1
    /\ idempotency \in {None} \cup Messages /\ attempt \in 0..2 /\ sessions \in 0..MaxSessions

\* The charges never pass the write limit, so the last MiB stays for journal and temporary files.
Capacity == Used <= Limit

\* A request and its response agree: the request is answered or closed once, never overwritten.
RequestAgrees == requestChanges <= 1

\* A `duplicate` names a message that exists.
DuplicateIsReal == ~phantom

\* One send, retried with its key, is pushed at most once while its key is remembered.
AtMostOncePerKey == forgotten \/ msg[1].pushed + msg[2].pushed <= 1

\* An answer written by a response stands only for a message that exists (so a rollback leaves the
\* request as it was).
FollowOnAgrees ==
    \A m \in Messages : msg[m].followOn => (msg[m].commit # "none" \/ msg[m].journal \/ msg[m].retired)

\* A commit record never stands for files that are gone.
CommitHasFiles == \A m \in Messages : (msg[m].commit # "none" /\ ~msg[m].unlinking) => msg[m].files

\* A committed send keeps every marker it was admitted with.
MarkersComplete == \A m \in Messages : msg[m].derived => msg[m].markers

\* `accepted` is said only of a send no crash can undo.
AcceptedIsDurable ==
    \A m \in Messages : answer[m] = "accepted" => (msg[m].commit = "durable" \/ msg[m].retired)
=============================================================================
