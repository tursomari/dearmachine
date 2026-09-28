------------------------ MODULE OutboundRecovery ------------------------
EXTENDS Naturals, FiniteSets, Sequences, TLC
CONSTANTS Records, Kind, VerifiedWindow, DecisionCase, Isolation, MaxAttempts, Mutation
\* Companion to Outbound, not a runtime refinement. Its issued-preview and
\* exact-approval contract is assumed at entry and after ApproveRedraft.
\* Each record is one frozen preview OR submission operation. Run both kinds.
\* Provider acceptance is hidden from the client. A typed rejection means NO
\* mutating API call occurred; an absent receipt never supplies that evidence.
\* VerifiedWindow is an external provider assumption, FALSE by default.
\* Time advances in retry-budget units; retries must precede their durable deadline.
\* MaxAttempts matches the runtime budget; two revisions bound redraft exploration.
\* One time unit abstracts the durable 15-minute budget; time is bounded at 2.
Bug(s) == Mutation = s
VARIABLES work, now, eligible, notices, consumed, deferred, polled
vars == <<work, now, eligible, notices, consumed, deferred, polled>>
HeldKey(r, rev) == <<"held", r, rev>>
InvalidKey == <<"invalid", "consumed-message-id">>
NoticeKeys == {HeldKey(r, rev): r \in Records, rev \in 1..2} \cup {InvalidKey}
Owner == "controlling-owner"
EmptyNotice == [reserved |-> FALSE, finished |-> FALSE, attempts |-> 0,
                to |-> {}, status |-> "none"]
EmptyWork == [phase |-> "ready", revision |-> 1, snapshot |-> TRUE,
  approved |-> 1, attempts |-> <<>>, accepted |-> 0, known |-> FALSE,
  evidence |-> "none", deadline |-> 0, reason |-> "", fault |-> FALSE]
Init ==
  /\ now = 0 /\ eligible = TRUE /\ consumed = FALSE /\ deferred = FALSE /\ polled = FALSE
  /\ notices = [n \in NoticeKeys |-> EmptyNotice]
  /\ work = [r \in Records |-> IF Isolation /\ r = "broken"
       THEN [EmptyWork EXCEPT !.phase = "held", !.reason = "record failure",
                              !.fault = TRUE] ELSE EmptyWork]
Payload(r) == <<r, work[r].revision, work[r].snapshot, Kind>>
Key(r) == IF Kind = "preview" THEN <<r, work[r].revision, Kind>> ELSE <<r, Kind>>
Count(r) == Cardinality({i \in 1..Len(work[r].attempts):
                         work[r].attempts[i].revision = work[r].revision})
Within(r) == Count(r) = 0 \/ now < work[r].deadline
Current(r) == work[r].snapshot = eligible
Permit(r) == work[r].evidence = "rejected" \/ (VerifiedWindow /\ Within(r))
Blocked == Bug("GlobalFailure") /\ \E r \in Records: work[r].fault

\* Each Attempt atomically rechecks grants under the existing DB write lock,
\* records the attempt, and calls the provider. Revocation cannot interleave.
\* "typed" certifies no mutating call; "lost" accepted but response lost;
\* "unknown" may have failed before acceptance but supplies no proof of that.
Attempt(r, outcome) ==
  /\ ~Blocked /\ ~work[r].fault
  /\ work[r].phase \in {"ready", "checked"} \/
       (Bug("BlindRetry") /\ work[r].phase = "uncertain")
  /\ Count(r) < MaxAttempts /\ (Within(r) \/ Bug("ExpiredRetry"))
  /\ Current(r) \/ Bug("SkipGrantCheck")
  /\ Kind = "preview" \/ work[r].approved = work[r].revision
  /\ Count(r) = 0 \/ Permit(r) \/ Bug("BlindRetry") \/ Bug("AbsentMeansRejected")
  /\ ~Isolation \/ outcome = "ok"
  /\ LET retry == Count(r) > 0
         dedup == VerifiedWindow /\ Within(r) /\ work[r].accepted > 0
         accepts == outcome \in {"ok", "lost"}
         event == [key |-> Key(r), payload |-> IF Bug("MutableRequest") /\ retry THEN <<"changed">> ELSE Payload(r), time |-> now,
           deadline |-> IF Count(r) = 0 THEN now + 1 ELSE work[r].deadline, revision |-> work[r].revision,
           snapshot |-> work[r].snapshot, observed |-> eligible,
           evidence |-> work[r].evidence, reconciled |-> work[r].phase = "checked",
           prior |-> work[r].accepted, typed |-> outcome = "typed"]
     IN work' = [work EXCEPT
       ![r].attempts = Append(@, event),
       ![r].deadline = event.deadline,
       ![r].accepted = @ + IF accepts /\ ~dedup THEN 1 ELSE 0,
       ![r].known = outcome = "typed" /\ (Count(r) = 0 \/ work[r].known),
       ![r].phase = IF outcome = "ok" THEN "done" ELSE "uncertain",
       ![r].evidence = "none"]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>

\* Lookup is mandatory even after typed rejection. No receipt is not rejection.
\* Mismatched/ambiguous receipts and lookup errors hold only this record.
\* Held attempts can still reconcile a receipt later; holding never resends.
Reconcile(r, result) ==
  /\ work[r].phase = "uncertain" \/ (work[r].phase = "held" /\ Count(r) > 0)
  /\ result = "matched" => work[r].accepted > 0
  /\ work' = [work EXCEPT
       ![r].phase = IF result = "matched" THEN "done"
                   ELSE IF result \in {"ambiguous", "lookup-failed"} THEN "held"
                   ELSE "checked",
       ![r].evidence = IF result = "absent" /\ work[r].known
                       THEN "rejected" ELSE result,
       ![r].reason = IF result \in {"ambiguous", "lookup-failed"} THEN result ELSE ""]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>
Hold(r) ==
  /\ work[r].phase \in {"ready", "checked"}
  /\ ~Within(r) \/ Count(r) = MaxAttempts \/
       (Count(r) > 0 /\ ~Permit(r)) \/ ~Current(r)
  /\ work' = [work EXCEPT ![r].phase = "held", ![r].reason = "unsafe or exhausted"]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>

\* Only proof of no possible acceptance permits replacing a failed request.
\* A safely replaced revision starts a new budget; same-revision retries and
\* restarts cannot refresh it. Submission keys stay stable even across revision.
Redraft(r) ==
  /\ work[r].phase = "checked" /\ ~Current(r) /\ work[r].revision = 1
  /\ work[r].evidence = "rejected" \/ Bug("RedraftUnknown")
  /\ Count(r) < MaxAttempts /\ Within(r)
  /\ work' = [work EXCEPT ![r].revision = 2, ![r].snapshot = eligible,
       ![r].approved = 0, ![r].deadline = 0, ![r].phase = "approval"]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>
ApproveRedraft(r) ==
  /\ work[r].phase = "approval"
  /\ work' = [work EXCEPT ![r].approved = work[r].revision, ![r].phase = "checked"]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>
Revoke == /\ ~Isolation /\ eligible /\ eligible' = FALSE
          /\ UNCHANGED <<work, now, notices, consumed, deferred, polled>>
Tick == /\ ~Isolation /\ now < 2 /\ now' = now + 1
        /\ UNCHANGED <<work, eligible, notices, consumed, deferred, polled>>
\* Correct restart is a stutter; pending reservation is durable, not retried.
Restart ==
  /\ work' = IF Bug("RenewDeadline")
       THEN [r \in Records |-> IF Count(r) > 0
         THEN [work[r] EXCEPT !.deadline = now + 1] ELSE work[r]] ELSE work
  /\ notices' = IF Bug("ForgetNotice")
       THEN [n \in NoticeKeys |-> [notices[n] EXCEPT !.reserved = FALSE]] ELSE notices
  /\ UNCHANGED <<now, eligible, consumed, deferred, polled>>

\* Invalid consumed owner controls: malformed, stale, ambiguous,
\* or unbound bare yes/no. DecisionCase labels these equivalent abstract inputs;
\* parsing/authentication/consumption classification remains a runtime obligation.
\* None is an approval; message-ID dedup survives restart. Premature controls
\* are deferred for later handling, never consumed or answered as invalid here.
ConsumeInvalid ==
  /\ DecisionCase # "premature" \/ Bug("ConsumePremature")
  /\ ~consumed /\ consumed' = TRUE
  /\ work' = IF Bug("FeedbackApproves") THEN
       [r \in Records |-> [work[r] EXCEPT !.approved = 2]] ELSE work
  /\ UNCHANGED <<now, eligible, notices, deferred, polled>>
DeferPremature ==
  /\ DecisionCase = "premature" /\ ~deferred /\ deferred' = TRUE
  /\ UNCHANGED <<work, now, eligible, notices, consumed, polled>>
Needed(n) == IF n = InvalidKey THEN consumed
             ELSE work[n[2]].phase = "held" /\ work[n[2]].revision = n[3]
Reserve(n) ==
  /\ Needed(n) /\ ~notices[n].reserved
  /\ notices' = [notices EXCEPT ![n].reserved = TRUE, ![n].finished = FALSE,
       ![n].status = "attempt pending or interrupted"]
  /\ UNCHANGED <<work, now, eligible, consumed, deferred, polled>>
SendNotice(n, ok) ==
  /\ notices[n].reserved /\ ~notices[n].finished
  /\ notices' = [notices EXCEPT ![n].finished = TRUE,
       ![n].attempts = @ + 1,
       ![n].to = IF Bug("PublicNotice") THEN {Owner, "guest"} ELSE {Owner},
       ![n].status = IF ok THEN "sent" ELSE "failed"]
  /\ UNCHANGED <<work, now, eligible, consumed, deferred, polled>>
\* A crash after reservation may prevent the provider call altogether. Recovery
\* leaves its status uncertain and must not reuse that reserved notice identity.
InterruptNotice(n) ==
  /\ notices[n].reserved /\ ~notices[n].finished
  /\ notices' = [notices EXCEPT ![n].finished = TRUE]
  /\ UNCHANGED <<work, now, eligible, consumed, deferred, polled>>
Poll == /\ ~Blocked /\ ~polled /\ polled' = TRUE
        /\ UNCHANGED <<work, now, eligible, notices, consumed, deferred>>
Next == (\E r \in Records:
           (\E outcome \in {"ok", "lost", "typed", "unknown"}: Attempt(r, outcome)) \/
           (\E result \in {"matched", "absent", "ambiguous", "lookup-failed"}:
              Reconcile(r, result)) \/ Hold(r) \/ Redraft(r) \/ ApproveRedraft(r)) \/
        (\E n \in NoticeKeys: Reserve(n) \/ InterruptNotice(n) \/ (\E ok \in BOOLEAN: SendNotice(n, ok))) \/
        Revoke \/ Tick \/ Restart \/ ConsumeInvalid \/ DeferPremature \/ Poll

TypeOK ==
  /\ now \in 0..2 /\ eligible \in BOOLEAN /\ consumed \in BOOLEAN /\ deferred \in BOOLEAN /\ polled \in BOOLEAN
  /\ DecisionCase \in {"malformed", "stale", "ambiguous", "premature", "bare"}
  /\ Kind \in {"preview", "submission"} /\ VerifiedWindow \in BOOLEAN
  /\ \A r \in Records:
       /\ work[r].phase \in {"ready", "checked", "uncertain", "held", "done", "approval"}
       /\ work[r].revision \in 1..2 /\ work[r].approved \in 0..2
       /\ Count(r) \in 0..MaxAttempts /\ work[r].accepted \in 0..MaxAttempts
       /\ work[r].snapshot \in BOOLEAN /\ work[r].known \in BOOLEAN
  /\ \A n \in NoticeKeys: notices[n].attempts \in 0..2
AtMostOneAcceptance == \A r \in Records: work[r].accepted <= 1
DurableDeadline == \A r \in Records:
  IF Count(r) = 0 THEN work[r].deadline = 0 ELSE
    \A i \in 1..Len(work[r].attempts):
      work[r].attempts[i].revision = work[r].revision =>
        work[r].deadline = work[r].attempts[i].deadline
Range(s) == {s[i]: i \in 1..Len(s)}
BoundedAttempts == \A r \in Records: \A a \in Range(work[r].attempts): a.time < a.deadline
\* Range explicitly avoids enumerating unbounded Seq for type checking.
RetryEvidence == \A r \in Records: \A i \in 2..Len(work[r].attempts):
  LET a == work[r].attempts[i] IN
    a.revision = work[r].attempts[i-1].revision =>
    /\ a.reconciled
    /\ a.evidence = "rejected" \/ (VerifiedWindow /\ a.time < a.deadline)
FreshEligibility == \A r \in Records: \A a \in Range(work[r].attempts):
  a.snapshot = a.observed
FrozenRequest == \A r \in Records: \A a \in Range(work[r].attempts):
  /\ a.key = IF Kind = "preview" THEN <<r, a.revision, Kind>> ELSE <<r, Kind>>
  /\ a.payload = <<r, a.revision, a.snapshot, Kind>>
SafeRedraft == \A r \in Records: work[r].revision = 2 =>
  /\ Len(work[r].attempts) > 0
  /\ \A a \in Range(work[r].attempts): a.revision = 1 => a.typed
NoticePrivacy == \A n \in NoticeKeys:
  notices[n].attempts > 0 => notices[n].to = {Owner}
NoticeAtMostOnce == \A n \in NoticeKeys: notices[n].attempts <= 1
NoticeStatus == \A n \in NoticeKeys:
  notices[n].reserved => notices[n].status \in
    {"attempt pending or interrupted", "sent", "failed"}
HoldVisible == \A r \in Records: work[r].phase = "held" => work[r].reason # ""
PrematureDeferred == DecisionCase = "premature" =>
  /\ ~consumed /\ ~notices[InvalidKey].reserved /\ notices[InvalidKey].attempts = 0
NoPrematureDeferral == ~deferred
NoInventedApproval == \A r \in Records: work[r].approved <= work[r].revision

\* Expected-failing reachability checks run with every safety invariant enabled.
NoTypedRecovery == ~\E r \in Records:
  work[r].phase = "done" /\ Count(r) = 2 /\ work[r].attempts[1].typed
NoLostReconciliation == ~\E r \in Records:
  work[r].phase = "done" /\ Count(r) = 1 /\ work[r].evidence = "matched"
NoLateReceipt == ~\E r \in Records:
  work[r].phase = "done" /\ work[r].evidence = "matched" /\
  notices[HeldKey(r, work[r].revision)].attempts = 1
NoUnknownHold == ~\E r \in Records:
  work[r].phase = "held" /\ Count(r) = 1 /\ ~work[r].known /\ work[r].evidence = "absent"
NoSafeRedraft == ~\E r \in Records:
  work[r].phase = "done" /\ work[r].revision = 2
NoExhaustedHold == ~\E r \in Records:
  work[r].phase = "held" /\ Count(r) = MaxAttempts
NoExpiredHold == ~\E r \in Records:
  work[r].phase = "held" /\ ~Within(r) /\ work[r].known
NoRevokedUnknownHold == ~\E r \in Records:
  work[r].phase = "held" /\ ~eligible /\ work[r].accepted = 1 /\
  work[r].evidence = "absent"
NoHeldNoticeFailure == ~\E r \in Records:
  notices[HeldKey(r, work[r].revision)].status = "failed"
NoInterruptedNotice == ~\E n \in NoticeKeys:
  notices[n].reserved /\ notices[n].finished /\ notices[n].attempts = 0
NoInvalidFeedback == notices[InvalidKey].attempts = 0
NoGuaranteedRecovery == ~\E r \in Records:
  work[r].phase = "done" /\ Count(r) = 2 /\ work[r].attempts[2].prior = 1
NoIsolation == ~(polled /\ \E r \in Records:
  ~work[r].fault /\ work[r].phase = "done")
\* Isolation fixture removes clock/revocation/provider nondeterminism, leaving
\* a permanently broken record. Fair scheduling must still poll and send the
\* healthy record. This is not a promise of delivery under an unreliable API.
Progress == <>polled /\ (\A r \in Records: ~work[r].fault => <>(work[r].phase = "done"))
Spec == Init /\ [][Next]_vars
IsolationSpec == Spec /\ WF_vars(Poll) /\ (\A r \in Records: WF_vars(Attempt(r, "ok")))
=============================================================================
