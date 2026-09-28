---------------------------- MODULE Outbound ----------------------------
EXTENDS Naturals, FiniteSets, TLC
CONSTANTS Scopes, GuestMessages, OwnerMessages, Guests, MaxRevision,
          MaxGeneration, Mutation
\* Proposed sending contract, NOT a refinement of the current client.
\* Scope is an exact (owner pair, provider inbox, provider thread). Accept
\* abstracts authenticated instruction acceptance; execution is upstream.
\* Guest execution approval NEVER supplies an outbound decision.
Requests == Scopes \X (GuestMessages \cup OwnerMessages)
Owner(k) == <<"owner", k>>
Guest(k, g) == <<"guest", k, g>>
OwnerOnly(k) == {Owner(k)}
Bug(name) == Mutation = name
VARIABLES grants, work
vars == <<grants, work>>
EmptyGenerations == [g \in Guests |-> 0]
EmptyGrants == [g \in Guests |-> [active |-> FALSE, generation |-> 0]]
EmptyDraft == [body |-> 0, files |-> 0, recipients |-> {},
               generations |-> EmptyGenerations, banner |-> FALSE]
EmptyDecision(r) == [by |-> Owner(r[1]), authenticated |-> FALSE, value |-> "none",
                     request |-> r, revision |-> 0]
EmptyWork(r) == [phase |-> "empty", revision |-> 0, visible |-> {},
  acceptedGenerations |-> EmptyGenerations,
  draft |-> EmptyDraft, preview |-> EmptyDraft, previewTo |-> {},
  previewGrants |-> EmptyGrants, issuedPreviews |-> {},
  priorApprovedRevision |-> 0, priorRecipients |-> {},
  approved |-> EmptyDraft, approvedRevision |-> 0, decision |-> EmptyDecision(r),
  submitted |-> EmptyDraft, firstSubmission |-> EmptyDraft,
  submissionGrants |-> EmptyGrants, eligibleAtSubmit |-> {}]
Init == /\ grants = [k \in Scopes |-> [g \in Guests |->
                                    [active |-> TRUE, generation |-> 1]]]
        /\ work = [r \in Requests |-> EmptyWork(r)]
Eligible(r) == {g \in work[r].visible:
  grants[r[1]][g].active /\ work[r].acceptedGenerations[g] > 0 /\
  grants[r[1]][g].generation = work[r].acceptedGenerations[g]}
Recipients(r) == OwnerOnly(r[1]) \cup {Guest(r[1], g): g \in Eligible(r)}
Shared(r, draft) == draft.recipients # OwnerOnly(r[1])
Current(r) == work[r].draft.recipients = Recipients(r)
WithoutBanner(draft) == [draft EXCEPT !.banner = FALSE]
ExactApproval(r) == /\ work[r].approved = work[r].draft
                    /\ work[r].approvedRevision = work[r].revision

Accept(r, visible) ==
  /\ work[r].phase = "empty"
  /\ work' = [work EXCEPT ![r].phase = "accepted", ![r].visible = visible,
       ![r].acceptedGenerations = [g \in Guests |->
         IF g \in visible /\ grants[r[1]][g].active
         THEN grants[r[1]][g].generation ELSE 0]]
  /\ UNCHANGED grants
Prepare(r) ==
  /\ work[r].phase = "accepted"
  /\ work' = [work EXCEPT ![r].phase = "ready", ![r].revision = 1,
       ![r].draft = [body |-> 1, files |-> 1, recipients |-> Recipients(r),
                     generations |-> work[r].acceptedGenerations,
                     banner |-> FALSE]]
  /\ UNCHANGED grants

\* A private preview has the same body, attachments, recipients and generation
\* binding as the draft. Only its banner differs. Check eligibility at preview
\* creation. This abstracts successful issuance plus durable history recording,
\* not arrival/read receipts. Failed sends must not create issued records.
\* Retain the observed grants so later revocation does not rewrite
\* history. An already-stale draft must be redrafted before it is previewed.
Preview(r) ==
  /\ work[r].phase = "ready" /\ Shared(r, work[r].draft)
  /\ Current(r) \/ Bug("StalePreview")
  /\ LET payload == [work[r].draft EXCEPT
          !.banner = ~Bug("MissingBanner"),
          !.files = IF Bug("IncompletePreview") THEN 0 ELSE @]
         recipients == IF Bug("PublicPreview") THEN work[r].draft.recipients
                       ELSE OwnerOnly(r[1])
     IN work' = [work EXCEPT ![r].phase = "pending",
          ![r].preview = payload, ![r].previewTo = recipients,
          ![r].previewGrants = grants[r[1]],
          ![r].issuedPreviews = @ \cup {
            [revision |-> work[r].revision, payload |-> payload, to |-> recipients]}]
  /\ UNCHANGED grants

\* Parsing is abstracted to yes/no/other after case/whitespace and quoted-history
\* handling. No, other, guest instruction approval, or silence is NEVER a Yes.
\* Record the actual evidence even when a mutation incorrectly grants approval.
Decide(r, tokenRequest, tokenRevision, ownerScope, authenticated, value) ==
  /\ work[r].phase = "pending" \/
       (Bug("ApproveUnseen") /\ work[r].phase = "ready" /\ Shared(r, work[r].draft))
  /\ authenticated \/ Bug("ForgedOwner")
  /\ ownerScope = r[1] \/ Bug("WrongOwner")
  /\ tokenRequest = r \/ Bug("WrongRequest") \/ Bug("WrongScopeRequest")
  /\ ~Bug("WrongRequest") \/ tokenRequest[1] = r[1]
  /\ ~Bug("WrongScopeRequest") \/ tokenRequest[1] # r[1]
  /\ (\E preview \in work[tokenRequest].issuedPreviews:
        preview.revision = tokenRevision) \/ Bug("ApproveUnseen")
  /\ tokenRevision = work[r].revision \/ Bug("StaleDecision")
  /\ value \in {"yes", "no"} \/ Bug("InvalidDecision")
  /\ LET yes == value = "yes" \/
                    (Bug("InvalidDecision") /\ value = "other") \/
                    (Bug("NoMeansYes") /\ value = "no")
     IN work' = [work EXCEPT
          ![r].decision = [by |-> Owner(ownerScope), authenticated |-> authenticated,
                            value |-> value, request |-> tokenRequest,
                            revision |-> tokenRevision],
          ![r].phase = IF yes THEN "approved" ELSE "rejected",
          ![r].approved = IF yes THEN
            IF Bug("ApproveUnseen") THEN work[r].draft ELSE WithoutBanner(work[r].preview)
            ELSE EmptyDraft,
          \* A stale-token bug deliberately authorizes the CURRENT revision.
          \* ExactApproval alone cannot catch it; decision history must do so.
          ![r].approvedRevision = IF yes THEN work[r].revision ELSE 0]
  /\ UNCHANGED grants

\* Any draft/recipient change invalidates approval. If guests remain, another
\* preview and Yes are required. No is terminal for this proposal. Reaching
\* MaxRevision may leave work permanently unsent; no liveness claim is made.
Redraft(r) ==
  /\ work[r].phase \in {"ready", "pending", "approved"}
  /\ work[r].revision < MaxRevision
  /\ work' = [work EXCEPT ![r].revision = @ + 1,
       ![r].phase = "ready", ![r].preview = EmptyDraft,
       ![r].previewTo = {}, ![r].previewGrants = EmptyGrants,
       ![r].priorApprovedRevision = work[r].approvedRevision,
       ![r].priorRecipients = work[r].draft.recipients,
       ![r].approved = EmptyDraft, ![r].approvedRevision = 0,
       ![r].decision = EmptyDecision(r),
       ![r].draft = [body |-> work[r].revision + 1,
                     files |-> work[r].revision + 1, recipients |-> Recipients(r),
                     generations |-> work[r].acceptedGenerations,
                     banner |-> FALSE]]
  /\ UNCHANGED grants
Revoke(k, g) ==
  /\ grants[k][g].active
  /\ grants' = [grants EXCEPT ![k][g].active = FALSE]
  /\ UNCHANGED work
Reinvite(k, g) ==
  /\ ~grants[k][g].active /\ grants[k][g].generation < MaxGeneration
  /\ grants' = [grants EXCEPT ![k][g].active = TRUE,
                              ![k][g].generation = @ + 1]
  /\ UNCHANGED work

Submit(r) ==
  /\ work[r].phase \in {"ready", "approved"}
       \/ (Bug("SendRejected") /\ work[r].phase = "rejected")
  /\ Current(r) \/ Bug("IgnoreRevocation") \/
       (Bug("StaleGeneration") /\ work[r].draft.recipients =
         OwnerOnly(r[1]) \cup {Guest(r[1], g):
           g \in {v \in work[r].visible: grants[r[1]][v].active}})
  /\ ~Shared(r, work[r].draft) \/
       (work[r].phase = "approved" /\ ExactApproval(r)) \/
       Bug("BypassApproval") \/
       (Bug("SendRejected") /\ work[r].phase = "rejected") \/
       (Bug("ReuseReplyApproval") /\ \E other \in Requests:
          other # r /\ work[other].phase = "approved")
  /\ LET sent == IF Bug("ChangeBody") /\ work[r].phase = "approved" THEN
                      [work[r].draft EXCEPT !.body = 0]
                  ELSE IF Bug("ChangeAttachments") /\ work[r].phase = "approved" THEN
                      [work[r].draft EXCEPT !.files = 0]
                  ELSE IF Bug("AddRecipient") /\ work[r].phase = "approved" THEN
                      [work[r].draft EXCEPT !.recipients = @ \cup
                        {Guest(r[1], g): g \in Guests}]
                  ELSE IF Bug("BannerInRelease") /\ work[r].phase = "approved"
                       THEN work[r].preview ELSE work[r].draft
     IN work' = [work EXCEPT ![r].phase = "submitted", ![r].submitted = sent,
          ![r].firstSubmission = sent, ![r].submissionGrants = grants[r[1]],
          ![r].eligibleAtSubmit = Eligible(r)]
  /\ UNCHANGED grants

\* First submission is irreversible. Provider idempotency/receipt lookup and
\* durable writes are assumptions here. OutboundRecovery separately checks
\* bounded recovery/feedback; the two models are not a composed refinement.
\* Retain the
\* actual first-submission payload and eligibility even after revocation.
Retry(r) ==
  /\ work[r].phase = "submitted"
  /\ work' = [work EXCEPT ![r].submitted =
       IF Bug("MutableRetry") THEN [@ EXCEPT !.recipients = Recipients(r)] ELSE @]
  /\ UNCHANGED grants
\* Correct restart is a stutter: persistence is explicitly assumed. The mutant
\* tests that recovery cannot invent approval without a recorded owner decision.
Restart ==
  /\ work' = IF Bug("RestartApproves") THEN
       [r \in Requests |-> IF work[r].phase = "pending"
          THEN [work[r] EXCEPT !.phase = "approved",
                 !.approved = work[r].draft, !.approvedRevision = work[r].revision]
          ELSE work[r]] ELSE work
  /\ UNCHANGED grants
Next == (\E r \in Requests:
           (\E visible \in SUBSET Guests: Accept(r, visible)) \/
           Prepare(r) \/ Preview(r) \/ Redraft(r) \/ Submit(r) \/ Retry(r) \/
           (\E tokenRequest \in Requests, tokenRevision \in 1..MaxRevision,
               ownerScope \in Scopes, authenticated \in BOOLEAN,
               value \in {"yes", "no", "other"}:
              Decide(r, tokenRequest, tokenRevision, ownerScope, authenticated, value))) \/
        (\E k \in Scopes, g \in Guests: Revoke(k, g) \/ Reinvite(k, g)) \/ Restart

\* Independent state properties: no action writes a self-reported violation.
PreviewPrivacy == \A r \in Requests:
  work[r].preview # EmptyDraft =>
    /\ work[r].previewTo = OwnerOnly(r[1])
    /\ work[r].preview.banner
    /\ WithoutBanner(work[r].preview) = work[r].draft
\* Evaluate historical grant evidence, never current grants: a revocation after
\* a legitimate preview/submission must not retroactively falsify its invariant.
EligibleSnapshot(r, payload, observed) ==
  /\ Owner(r[1]) \in payload.recipients
  /\ payload.recipients \subseteq OwnerOnly(r[1]) \cup
       {Guest(r[1], g): g \in work[r].visible}
  /\ \A g \in Guests: Guest(r[1], g) \in payload.recipients =>
       /\ observed[g].active
       /\ work[r].acceptedGenerations[g] > 0
       /\ payload.generations[g] = work[r].acceptedGenerations[g]
       /\ observed[g].generation = payload.generations[g]
PreviewEligibility == \A r \in Requests:
  work[r].preview # EmptyDraft =>
    EligibleSnapshot(r, work[r].preview, work[r].previewGrants)
ValidDecision(r) ==
  /\ work[r].decision.value \in {"yes", "no"}
  /\ work[r].decision.authenticated
  /\ work[r].decision.by = Owner(r[1])
  /\ work[r].decision.request = r
  /\ work[r].decision.revision = work[r].revision
ApprovalEvidence == \A r \in Requests:
  /\ work[r].decision.value # "none" => ValidDecision(r)
  /\ (work[r].phase = "approved" \/
       (work[r].phase = "submitted" /\ Shared(r, work[r].submitted))) =>
        /\ work[r].decision.value = "yes"
        /\ ValidDecision(r)
\* Authority must trace to an actual issued outbound preview, not merely a
\* well-formed Yes with matching request/revision fields. History survives
\* redrafting; an old payload/revision cannot establish approval of a new one.
ApprovedWhatWasPreviewed == \A r \in Requests:
  (work[r].phase = "approved" \/
   (work[r].phase = "submitted" /\ Shared(r, work[r].submitted))) =>
    [revision |-> work[r].decision.revision,
     payload |-> [work[r].draft EXCEPT !.banner = TRUE],
     to |-> OwnerOnly(r[1])] \in work[r].issuedPreviews
ReleaseWithoutBanner == \A r \in Requests:
  work[r].phase = "submitted" => ~work[r].submitted.banner
RetryIdentity == \A r \in Requests:
  work[r].phase = "submitted" => work[r].submitted = work[r].firstSubmission
ApprovedDisclosure == \A r \in Requests:
  work[r].phase = "submitted" =>
    /\ work[r].submitted = work[r].draft
    /\ Shared(r, work[r].submitted) => ExactApproval(r)
SubmissionEligibility == \A r \in Requests:
  work[r].phase = "submitted" =>
    /\ EligibleSnapshot(r, work[r].submitted, work[r].submissionGrants)
    /\ work[r].eligibleAtSubmit = {g \in work[r].visible:
         work[r].submissionGrants[g].active /\ work[r].acceptedGenerations[g] > 0 /\
         work[r].submissionGrants[g].generation = work[r].acceptedGenerations[g]}
    /\ work[r].submitted.recipients = OwnerOnly(r[1]) \cup
         {Guest(r[1], g): g \in work[r].eligibleAtSubmit}
Addresses == {Owner(k): k \in Scopes} \cup
             {Guest(k, g): k \in Scopes, g \in Guests}
Generations == [Guests -> 0..MaxGeneration]
GrantSnapshots == [Guests -> [active : BOOLEAN, generation : 0..MaxGeneration]]
Drafts == [body : 0..MaxRevision, files : 0..MaxRevision,
           recipients : SUBSET Addresses, generations : Generations, banner : BOOLEAN]
IssuedPreviews == [revision : 1..MaxRevision, payload : Drafts,
                   to : SUBSET Addresses]
Decisions == [by : {Owner(k): k \in Scopes},
              authenticated : BOOLEAN, value : {"none", "yes", "no", "other"},
              request : Requests, revision : 0..MaxRevision]
TypeOK ==
  /\ grants \in [Scopes -> [Guests ->
        [active : BOOLEAN, generation : 1..MaxGeneration]]]
  /\ work \in [Requests -> [
       phase : {"empty", "accepted", "ready", "pending", "approved", "rejected", "submitted"},
       revision : 0..MaxRevision, visible : SUBSET Guests,
       acceptedGenerations : Generations,
       draft : Drafts, preview : Drafts, approved : Drafts, submitted : Drafts,
       firstSubmission : Drafts, previewTo : SUBSET Addresses,
       previewGrants : GrantSnapshots, issuedPreviews : SUBSET IssuedPreviews,
       submissionGrants : GrantSnapshots, eligibleAtSubmit : SUBSET Guests,
       priorApprovedRevision : 0..MaxRevision, priorRecipients : SUBSET Addresses,
       approvedRevision : 0..MaxRevision, decision : Decisions]]
\* Expected-failing invariants require useful behavior, not just refusal.
NoSharedOwner == ~\E r \in Requests: r[2] \in OwnerMessages /\
  work[r].phase = "submitted" /\ Shared(r, work[r].submitted)
NoSharedGuest == ~\E r \in Requests: r[2] \in GuestMessages /\
  work[r].phase = "submitted" /\ Shared(r, work[r].submitted)
NoPrivate == ~\E r \in Requests: work[r].phase = "submitted" /\
  work[r].submitted.recipients = OwnerOnly(r[1]) /\ work[r].issuedPreviews = {}
NoRejection == ~\E r \in Requests: work[r].phase = "rejected"
NoReapprovedDraft == ~\E r \in Requests: work[r].phase = "submitted" /\
  work[r].revision = 2 /\ work[r].priorApprovedRevision = 1 /\
  Shared(r, work[r].submitted)
NoRepeatedAnswers == ~(\A r \in Requests: work[r].phase = "submitted" /\
  Shared(r, work[r].submitted))
NoMultiGuest == ~\E r \in Requests: work[r].phase = "submitted" /\
  Cardinality(work[r].submitted.recipients) = 3
NoIndependentAnswer == ~\E r, other \in Requests:
  r # other /\ work[r].phase = "pending" /\
  work[other].phase = "submitted" /\ Shared(other, work[other].submitted)
NoReducedRecipients == ~\E r \in Requests:
  work[r].phase = "submitted" /\ work[r].revision = 2 /\
  work[r].visible = Guests /\ Cardinality(Guests) = 2 /\
  work[r].priorApprovedRevision = 1 /\ Cardinality(work[r].priorRecipients) = 3 /\
  Cardinality(work[r].submitted.recipients) = 2
NoReinvitedAnswer == ~\E r \in Requests, g \in Guests:
  work[r].phase = "submitted" /\ Guest(r[1], g) \in work[r].submitted.recipients /\
  work[r].acceptedGenerations[g] = 2
NoBoundedHold == ~\E r \in Requests:
  work[r].phase = "approved" /\ work[r].revision = MaxRevision /\ ~Current(r)
Spec == Init /\ [][Next]_vars
=============================================================================
