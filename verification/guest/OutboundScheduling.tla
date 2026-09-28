------------------------ MODULE OutboundScheduling ------------------------
EXTENDS Naturals, TLC
CONSTANTS BusyKind, Reliable, AllowRevoke, Mutation
VARIABLES busy, state, stage, approval, eligible, attempts, submittedEligible, polled
vars == <<busy, state, stage, approval, eligible, attempts, submittedEligible, polled>>
Bug(s) == Mutation = s

\* One already-issued immutable preview and one authenticated, correctly bound
\* owner Yes are assumed from Outbound. This model isolates scheduler progress;
\* parsing, guest generations, provider requests and lock fairness are trusted.
Init ==
  /\ busy = BusyKind
  /\ state = "pending" /\ stage = "poll" /\ approval = FALSE
  /\ eligible = TRUE /\ attempts = 0 /\ submittedEligible = FALSE
  /\ polled = FALSE

\* PollAndClaim handles the owner's decision before its outbound pass. The
\* execution lane may remain occupied forever: finishing it has no fairness.
Poll ==
  /\ stage = "poll"
  /\ approval' = TRUE
  /\ state' = IF state = "pending" THEN "approved" ELSE state
  /\ stage' = "outbound" /\ polled' = TRUE
  /\ UNCHANGED <<busy, eligible, attempts, submittedEligible>>

\* Returning to polling abstracts a completed pass even if there was nothing
\* to submit. The old code omitted this pass whenever dispatch or maintenance
\* was still running, although it continued to consume owner decisions.
Drain ==
  /\ stage = "outbound"
  /\ LET canDrain == ~Bug("WaitForIdle") \/ busy = "idle"
         submit == canDrain /\
           (state = "approved" \/ (Bug("RetryUncertain") /\ state = "sending")) /\
           (eligible \/ Bug("SkipEligibility"))
     IN /\ attempts' = attempts + IF submit THEN 1 ELSE 0
        /\ submittedEligible' = IF submit THEN eligible ELSE submittedEligible
        /\ state' = IF submit THEN (IF Reliable THEN "sent" ELSE "sending")
                     ELSE IF canDrain /\ state = "approved" /\ ~eligible
                          THEN "redraft" ELSE state
  /\ stage' = "poll"
  /\ UNCHANGED <<busy, approval, eligible, polled>>

\* A poll failure does not manufacture an approval or start a send. Conditional
\* progress below assumes polls and the outbox pass eventually complete.
PollFailure ==
  /\ stage = "poll"
  /\ UNCHANGED vars
FinishBusy ==
  /\ busy # "idle" /\ busy' = "idle"
  /\ UNCHANGED <<state, stage, approval, eligible, attempts, submittedEligible, polled>>
Revoke ==
  /\ AllowRevoke /\ eligible /\ eligible' = FALSE
  /\ UNCHANGED <<busy, state, stage, approval, attempts, submittedEligible, polled>>
\* Restarts preserve the outbox; interruption before a pass must not erase
\* recorded approval. Fair Drain requires eventual opportunity to finish a pass.
Restart ==
  /\ UNCHANGED vars

Next == Poll \/ Drain \/ PollFailure \/ FinishBusy \/ Revoke \/ Restart
TypeOK ==
  /\ busy \in {"idle", "worker", "maintenance"}
  /\ state \in {"pending", "approved", "sending", "sent", "redraft"}
  /\ stage \in {"poll", "outbound"}
  /\ approval \in BOOLEAN /\ eligible \in BOOLEAN
  /\ submittedEligible \in BOOLEAN /\ polled \in BOOLEAN
  /\ attempts \in 0..2
ApprovalRequired == attempts > 0 => approval
AtMostOneAttempt == attempts <= 1
EligibilityAtSubmission == attempts > 0 => submittedEligible
NoSubmissionWhileBusy == ~(attempts = 1 /\ busy = BusyKind /\ BusyKind # "idle")
NoUncertainWhileBusy == ~(state = "sending" /\ busy = BusyKind /\ BusyKind # "idle")
NoRevokedRedraft == state # "redraft"
\* With stable eligible recipients and a responsive reliable provider, a fair
\* poll + outbox pass must submit even if an unrelated execution never ends.
Progress == (Reliable /\ ~AllowRevoke) => <>(state = "sent")
Spec == Init /\ [][Next]_vars /\ WF_vars(Poll) /\ WF_vars(Drain)
=============================================================================
