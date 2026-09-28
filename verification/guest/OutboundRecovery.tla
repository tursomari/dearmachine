------------------------ MODULE OutboundRecovery ------------------------
EXTENDS Naturals, FiniteSets, Sequences, TLC
CONSTANTS Records, Kind, VerifiedWindow, DecisionCase, Isolation, MaxAttempts, PhaseFlow, Mutation
\* Companion to Outbound, not a runtime refinement. Its issued-preview and
\* exact-approval contract is assumed at entry and after ApproveRedraft.
\* PhaseFlow also follows preview reconciliation into submission at SAME revision.
\* Provider acceptance is hidden from the client. A typed rejection means NO
\* mutating API call occurred; an absent receipt never supplies that evidence.
\* VerifiedWindow is an external provider assumption, FALSE by default.
\* Time advances in retry-budget units; retries must precede their durable deadline.
\* MaxAttempts matches the runtime budget; two revisions bound redraft exploration.
\* One time unit abstracts the durable 15-minute budget; time is bounded at 2.
Bug(s) == Mutation = s
VARIABLES work, now, eligible, notices, consumed, deferred, polled
vars == <<work, now, eligible, notices, consumed, deferred, polled>>
HeldKey(r, rev, op) == <<"held", r, rev, op>>
InvalidKey == <<"invalid", "consumed-message-id">>
NoticeKeys == {HeldKey(r, rev, op): r \in Records, rev \in 1..2, op \in {"preview", "submission"}} \cup {InvalidKey}
Owner == "controlling-owner"
EmptyNotice == [reserved |-> FALSE, finished |-> FALSE, attempts |-> 0,
                to |-> {}, status |-> "none", eligible |-> TRUE, identity |-> <<>>]
EmptyWork == [operation |-> IF PhaseFlow THEN "preview" ELSE Kind,
  previewHeld |-> FALSE, previewReconciled |-> FALSE, transientSeen |-> FALSE,
  resumePhase |-> "ready", pendingTransientSeen |-> FALSE,
  phase |-> "ready", revision |-> 1, snapshot |-> TRUE,
  approved |-> 1, attempts |-> <<>>, accepted |-> 0, known |-> FALSE,
  evidence |-> "none", deadline |-> 0, reason |-> "", fault |-> FALSE]
Init ==
  /\ now = 0 /\ eligible = TRUE /\ consumed = FALSE /\ deferred = FALSE /\ polled = FALSE
  /\ notices = [n \in NoticeKeys |-> EmptyNotice]
  /\ work = [r \in Records |-> IF Isolation /\ r = "broken"
       THEN [EmptyWork EXCEPT !.phase = "held", !.reason = "record failure",
                              !.fault = TRUE] ELSE EmptyWork]
Payload(r) == <<r, work[r].revision, work[r].snapshot, work[r].operation>>
Key(r) == IF work[r].operation = "preview" THEN <<r, work[r].revision, "preview">> ELSE <<r, "submission">>
Count(r) == Cardinality({i \in 1..Len(work[r].attempts):
                         work[r].attempts[i].revision = work[r].revision /\
                         work[r].attempts[i].operation = work[r].operation})
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
  /\ work[r].operation = "preview" \/ work[r].approved = work[r].revision
  /\ Count(r) = 0 \/ Permit(r) \/ Bug("BlindRetry") \/ Bug("AbsentMeansRejected")
  /\ ~Isolation \/ outcome = "ok"
  /\ LET retry == Count(r) > 0
         dedup == VerifiedWindow /\ Within(r) /\ work[r].accepted > 0
         accepts == outcome \in {"ok", "lost"}
         event == [operation |-> work[r].operation, key |-> Key(r), payload |-> IF Bug("MutableRequest") /\ retry THEN <<"changed">> ELSE Payload(r), time |-> now,
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
  /\ ~work[r].fault
  /\ work[r].phase = "uncertain" \/ (work[r].phase = "held" /\ Count(r) > 0)
  /\ result = "matched" => work[r].accepted > 0
  /\ work' = [work EXCEPT
       ![r].previewReconciled = @ \/ (work[r].operation = "preview" /\
         work[r].previewHeld /\ result = "matched"),
       ![r].previewHeld = @ \/ (work[r].operation = "preview" /\
         result \in {"ambiguous", "lookup-failed"}),
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
  /\ work' = [work EXCEPT ![r].phase = "held", ![r].reason = "unsafe or exhausted",
       ![r].previewHeld = @ \/ (work[r].operation = "preview" /\ Count(r) > 0)]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>

\* Only proof of no possible acceptance permits replacing a failed request.
\* A safely replaced revision starts a new budget; same-revision retries and
\* restarts cannot refresh it. Submission keys stay stable even across revision.
Redraft(r) ==
  /\ ~PhaseFlow
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
       THEN [n \in NoticeKeys |-> [notices[n] EXCEPT !.reserved = FALSE]]
       ELSE [n \in NoticeKeys |-> IF n # InvalidKey /\
         ((Bug("ForgetPreviewNotice") /\ n[4] = "preview") \/
          (Bug("ForgetSubmissionNotice") /\ n[4] = "submission"))
         THEN [notices[n] EXCEPT !.reserved = FALSE] ELSE notices[n]]
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
\* Generic pre-send and waiting-owner failures are visible but not notice-eligible.
\* Preview "done" is the pending owner-decision state in PhaseFlow; recovery
\* restores it without redrafting, resending, or inventing owner approval.
TransientFailure(r) ==
  /\ ~Isolation
  /\ (work[r].phase = "ready" /\ Count(r) = 0) \/
       (PhaseFlow /\ work[r].operation = "preview" /\ work[r].phase = "done")
  /\ work' = [work EXCEPT ![r].phase = "held", ![r].fault = TRUE,
       ![r].reason = "non-sending transient", ![r].transientSeen = TRUE,
       ![r].resumePhase = work[r].phase,
       ![r].pendingTransientSeen = @ \/ work[r].phase = "done"]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>
RecoverTransient(r) ==
  /\ ~Isolation /\ work[r].fault
  /\ work' = [work EXCEPT ![r].phase = work[r].resumePhase, ![r].fault = FALSE, ![r].reason = ""]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>
\* Approval is abstracted under Outbound's exact issued-preview contract.
\* This resets submission's budget, not revision or durable notice reservations.
BeginSubmission(r) ==
  /\ PhaseFlow /\ work[r].operation = "preview" /\ work[r].phase = "done"
  /\ work' = [work EXCEPT ![r].operation = "submission", ![r].phase = "ready",
       ![r].accepted = 0, ![r].known = FALSE, ![r].evidence = "none",
       ![r].deadline = 0, ![r].reason = ""]
  /\ UNCHANGED <<now, eligible, notices, consumed, deferred, polled>>
SendingHold(n) ==
  /\ work[n[2]].phase = "held" /\ ~work[n[2]].fault
  /\ work[n[2]].revision = n[3] /\ work[n[2]].operation = n[4]
  /\ Count(n[2]) > 0
Needed(n) == IF n = InvalidKey THEN consumed ELSE SendingHold(n)
NoticeKey(n) == IF Bug("SharedPhaseNotice") /\ n # InvalidKey
               THEN HeldKey(n[2], n[3], "preview") ELSE n
Reserve(n) ==
  /\ Needed(n) \/ ((Bug("NonSendingNotice") \/
       (Bug("PendingTransientNotice") /\ n # InvalidKey /\
        work[n[2]].fault /\ work[n[2]].resumePhase = "done")) /\ n # InvalidKey /\
       work[n[2]].phase = "held" /\ work[n[2]].revision = n[3] /\
       work[n[2]].operation = n[4])
  /\ ~notices[IF Bug("SuppressPhaseNotice") /\ n # InvalidKey
               THEN HeldKey(n[2], n[3], "preview") ELSE n].reserved
  /\ notices' = [notices EXCEPT ![NoticeKey(n)].reserved = TRUE,
       ![NoticeKey(n)].finished = FALSE, ![NoticeKey(n)].eligible = Needed(n),
       ![NoticeKey(n)].identity = n,
       ![NoticeKey(n)].status = "attempt pending or interrupted"]
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
              Reconcile(r, result)) \/ Hold(r) \/ Redraft(r) \/ ApproveRedraft(r) \/
           TransientFailure(r) \/ RecoverTransient(r) \/ BeginSubmission(r)) \/
        (\E n \in NoticeKeys: Reserve(n) \/ InterruptNotice(n) \/ (\E ok \in BOOLEAN: SendNotice(n, ok))) \/
        Revoke \/ Tick \/ Restart \/ ConsumeInvalid \/ DeferPremature \/ Poll

TypeOK ==
  /\ now \in 0..2 /\ eligible \in BOOLEAN /\ consumed \in BOOLEAN /\ deferred \in BOOLEAN /\ polled \in BOOLEAN
  /\ DecisionCase \in {"malformed", "stale", "ambiguous", "premature", "bare"}
  /\ PhaseFlow \in BOOLEAN
  /\ Kind \in {"preview", "submission"} /\ VerifiedWindow \in BOOLEAN
  /\ \A r \in Records:
       /\ work[r].phase \in {"ready", "checked", "uncertain", "held", "done", "approval"}
       /\ work[r].operation \in {"preview", "submission"}
       /\ work[r].revision \in 1..2 /\ work[r].approved \in 0..2
       /\ Count(r) \in 0..MaxAttempts /\ work[r].accepted \in 0..MaxAttempts
       /\ work[r].snapshot \in BOOLEAN /\ work[r].known \in BOOLEAN
  /\ \A n \in NoticeKeys: notices[n].attempts \in 0..2
AtMostOneAcceptance == \A r \in Records: work[r].accepted <= 1
DurableDeadline == \A r \in Records:
  IF Count(r) = 0 THEN work[r].deadline = 0 ELSE
    \A i \in 1..Len(work[r].attempts):
      work[r].attempts[i].revision = work[r].revision /\
      work[r].attempts[i].operation = work[r].operation =>
        work[r].deadline = work[r].attempts[i].deadline
Range(s) == {s[i]: i \in 1..Len(s)}
BoundedAttempts == \A r \in Records: \A a \in Range(work[r].attempts): a.time < a.deadline
\* Range explicitly avoids enumerating unbounded Seq for type checking.
RetryEvidence == \A r \in Records: \A i \in 2..Len(work[r].attempts):
  LET a == work[r].attempts[i] IN
    a.revision = work[r].attempts[i-1].revision /\
    a.operation = work[r].attempts[i-1].operation =>
    /\ a.reconciled
    /\ a.evidence = "rejected" \/ (VerifiedWindow /\ a.time < a.deadline)
FreshEligibility == \A r \in Records: \A a \in Range(work[r].attempts):
  a.snapshot = a.observed
FrozenRequest == \A r \in Records: \A a \in Range(work[r].attempts):
  /\ a.key = IF a.operation = "preview" THEN <<r, a.revision, a.operation>> ELSE <<r, a.operation>>
  /\ a.payload = <<r, a.revision, a.snapshot, a.operation>>
SafeRedraft == \A r \in Records: work[r].revision = 2 =>
  /\ Len(work[r].attempts) > 0
  /\ \A a \in Range(work[r].attempts): a.revision = 1 => a.typed
NoticeEligibility == \A n \in NoticeKeys: notices[n].reserved => notices[n].eligible
NoticePhaseIdentity == \A n \in NoticeKeys:
  notices[n].reserved => notices[n].identity = n
\* Every unreserved sending hold can reserve its own phase even if the other
\* phase has already reserved, failed, completed, or interrupted its notice.
NoticeAvailability == \A n \in NoticeKeys:
  Needed(n) /\ ~notices[n].reserved => ENABLED Reserve(n)
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
TwoPhaseNotices(r) ==
  /\ work[r].revision = 1 /\ work[r].operation = "submission"
  /\ work[r].previewReconciled /\ work[r].phase = "held"
  /\ notices[HeldKey(r, 1, "preview")].attempts = 1
  /\ notices[HeldKey(r, 1, "submission")].attempts = 1
NoTwoPhaseNotices == ~\E r \in Records: TwoPhaseNotices(r)
NoFailedPreviewThenSubmissionNotice == ~\E r \in Records:
  TwoPhaseNotices(r) /\ notices[HeldKey(r, 1, "preview")].status = "failed"
NoTransientThenTwoPhaseNotices == ~\E r \in Records:
  TwoPhaseNotices(r) /\ work[r].transientSeen
NoInterruptedPreviewThenSubmissionNotice == ~\E r \in Records:
  /\ work[r].revision = 1 /\ work[r].operation = "submission"
  /\ work[r].previewReconciled /\ work[r].phase = "held"
  /\ notices[HeldKey(r, 1, "preview")].reserved
  /\ notices[HeldKey(r, 1, "preview")].finished
  /\ notices[HeldKey(r, 1, "preview")].attempts = 0
  /\ notices[HeldKey(r, 1, "submission")].attempts = 1
NoPendingTransientThenSubmissionNotice == ~\E r \in Records:
  /\ work[r].pendingTransientSeen /\ work[r].revision = 1
  /\ work[r].operation = "submission" /\ work[r].phase = "held"
  /\ ~work[r].previewHeld
  /\ ~notices[HeldKey(r, 1, "preview")].reserved
  /\ notices[HeldKey(r, 1, "preview")].attempts = 0
  /\ notices[HeldKey(r, 1, "submission")].attempts = 1
NoTransientHold == ~\E r \in Records: work[r].fault /\ work[r].transientSeen
NoTypedRecovery == ~\E r \in Records:
  work[r].phase = "done" /\ Count(r) = 2 /\ work[r].attempts[1].typed
NoLostReconciliation == ~\E r \in Records:
  work[r].phase = "done" /\ Count(r) = 1 /\ work[r].evidence = "matched"
NoLateReceipt == ~\E r \in Records:
  work[r].phase = "done" /\ work[r].evidence = "matched" /\
  notices[HeldKey(r, work[r].revision, work[r].operation)].attempts = 1
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
  notices[HeldKey(r, work[r].revision, work[r].operation)].status = "failed"
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
