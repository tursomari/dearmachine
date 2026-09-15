------------------------- MODULE Participation -------------------------
EXTENDS Naturals, FiniteSets, TLC
CONSTANTS Scopes, GuestMessages, OwnerMessages, Evidence, MaxGeneration,
          Mutation
\* A scope is the exact (owner pair, provider inbox, guest, provider thread).
\* Sender evidence and outer-header visibility are trusted input facts, never
\* inferred from From, quoted text, thread membership, or provider allowlists.
Requests == Scopes \X (GuestMessages \cup OwnerMessages)
IsGuest(r) == r[2] \in GuestMessages
Owner(k) == <<"owner", k>>
Guest(k) == <<"guest", k>>
OwnerOnly(k) == {Owner(k)}
Shared(k) == {Owner(k), Guest(k)}
Bug(name) == Mutation = name
VARIABLES grant, seen, work, violation
vars == <<grant, seen, work, violation>>
EmptyGrant == [active |-> FALSE, generation |-> 0, exception |-> 0, warned |-> 0]
\* stage: 0 absent, 1 awaiting decision (owner: ready), 2 approved,
\*        3 started, 4 submitted. Requests are immutable and never reused.
EmptyWork == [stage |-> 0, generation |-> 0, visible |-> FALSE,
              envelope |-> {}, verified |-> FALSE]
Init == /\ grant = [k \in Scopes |-> EmptyGrant]
        /\ seen = [k \in Scopes |-> {}]
        /\ work = [r \in Requests |-> EmptyWork]
        /\ violation = FALSE

Invite(k, evidence, authenticatedOwner, outerVisible, reinvite) ==
  /\ authenticatedOwner \/ Bug("ForgedInvitation")
  /\ outerVisible
  /\ reinvite \/ evidence \notin seen[k] \/ Bug("ReplayInvitation")
  \* Revocation leaves a tombstone. A fresh ordinary reply-all is insufficient;
  \* an explicit, fresh owner reinvitation is required after revocation.
  /\ grant[k].generation = 0 \/ grant[k].active \/ reinvite
       \/ Bug("ImplicitReinvite") \/ Bug("ReplayInvitation")
  /\ grant[k].active \/ grant[k].generation < MaxGeneration
  /\ grant' = [grant EXCEPT ![k] = [active |-> TRUE,
       generation |-> IF @.active THEN @.generation ELSE @.generation + 1,
       exception |-> @.exception, warned |-> @.warned]]
  /\ seen' = [seen EXCEPT ![k] = @ \cup {evidence}]
  /\ violation' = (violation \/ ~authenticatedOwner
       \/ (evidence \in seen[k] /\ ~grant[k].active /\ ~reinvite)
       \/ (~grant[k].active /\ grant[k].generation > 0 /\ ~reinvite))
  /\ UNCHANGED work

Revoke(k, tokenScope, tokenGeneration, authenticatedOwner) ==
  /\ authenticatedOwner \/ Bug("ForgedRevocation")
  /\ tokenScope = k \/ Bug("WrongRevocationScope")
  /\ tokenGeneration = grant[k].generation
  /\ grant[k].active
  /\ grant' = [grant EXCEPT ![k].active = FALSE]
  /\ violation' = (violation \/ ~authenticatedOwner \/ tokenScope # k)
  /\ UNCHANGED <<seen, work>>

\* Exception state never substitutes for authenticated owners. Warning tokens
\* and the accepted risk bind a current scope and generation; removal makes
\* that generation ineligible. A held message remains an immutable identity.
ExceptionValid(k) == grant[k].active /\ grant[k].exception > 0 /\
  (grant[k].exception = grant[k].generation \/ Bug("StaleException"))
WarnGuest(r, visible, deliveredScope) ==
  /\ IsGuest(r) /\ work[r].stage = 0
  /\ visible /\ deliveredScope = r[1] /\ grant[r[1]].active
  /\ ~ExceptionValid(r[1])
  /\ grant' = [grant EXCEPT ![r[1]].warned = grant[r[1]].generation]
  /\ work' = [work EXCEPT ![r] = [stage |-> 5,
       generation |-> grant[r[1]].generation, visible |-> TRUE,
       envelope |-> {}, verified |-> FALSE]]
  /\ UNCHANGED <<seen, violation>>
AcceptException(k, tokenScope, epoch, authenticatedOwner) ==
  /\ authenticatedOwner \/ Bug("ForgedException")
  /\ tokenScope = k \/ Bug("WrongExceptionScope")
  /\ epoch = grant[tokenScope].warned /\ epoch > 0
  /\ grant[k].active
  /\ epoch = grant[k].generation \/ Bug("StaleExceptionToken")
  /\ grant' = [grant EXCEPT ![k].exception = grant[k].generation]
  /\ work' = IF Bug("ExceptionApproves")
       THEN [r \in Requests |-> IF r[1] = k /\ work[r].stage = 5
             THEN [work[r] EXCEPT !.stage = 2] ELSE work[r]] ELSE work
  /\ violation' = (violation \/ ~authenticatedOwner \/ tokenScope # k
       \/ epoch # grant[k].generation \/ work' # work)
  /\ UNCHANGED seen
ReleaseHeld(r) ==
  /\ work[r].stage = 5 /\ ExceptionValid(r[1])
  /\ work[r].generation = grant[r[1]].generation
  /\ work' = [work EXCEPT ![r].stage = 1]
  /\ violation' = (violation \/ grant[r[1]].exception # grant[r[1]].generation)
  /\ UNCHANGED <<grant, seen>>

ReceiveGuest(r, authenticatedGuest, visible, deliveredScope) ==
  /\ IsGuest(r) /\ work[r].stage = 0
  /\ authenticatedGuest \/ ExceptionValid(r[1]) \/ Bug("ForgedGuest")
  /\ deliveredScope = r[1] \/ Bug("WrongDeliveryScope")
  /\ visible /\ grant[r[1]].active
  /\ work' = [work EXCEPT ![r] = [stage |-> 1,
       generation |-> grant[r[1]].generation, visible |-> TRUE,
       envelope |-> {}, verified |-> authenticatedGuest]]
  \* Acceptance creates exactly one private approval prompt. Its correlation
  \* token binds r (scope + provider message ID) and this grant generation.
  /\ LET prompt == IF Bug("PublicApproval") THEN Shared(r[1])
                   ELSE OwnerOnly(r[1])
     IN violation' = (violation \/ (~authenticatedGuest /\ ~ExceptionValid(r[1]))
          \/ deliveredScope # r[1] \/ prompt # OwnerOnly(r[1]))
  /\ UNCHANGED <<grant, seen>>

ReceiveOwner(r, authenticatedOwner, guestVisible) ==
  /\ ~IsGuest(r) /\ work[r].stage = 0
  /\ authenticatedOwner \/ Bug("ForgedOwner")
  \* Initial visible invitation persistence precedes instruction acceptance.
  \* A revoked grant instead stays revoked on an ordinary visible reply.
  /\ ~guestVisible \/ grant[r[1]].generation > 0
  /\ work' = [work EXCEPT ![r] = [stage |-> 1,
       generation |-> grant[r[1]].generation, visible |-> guestVisible,
       envelope |-> {}, verified |-> authenticatedOwner]]
  /\ grant' = IF Bug("OmissionRevokes") /\ ~guestVisible
       THEN [grant EXCEPT ![r[1]].active = FALSE] ELSE grant
  /\ violation' = (violation \/ ~authenticatedOwner \/ grant' # grant)
  /\ UNCHANGED seen

Approve(r, tokenRequest, tokenGeneration, authenticatedOwner) ==
  /\ IsGuest(r) /\ work[r].stage = 1
  /\ authenticatedOwner \/ Bug("ForgedApproval")
  /\ IsGuest(tokenRequest) /\ work[tokenRequest].stage > 0
  /\ tokenRequest = r \/ Bug("WrongApprovalRequest")
  /\ tokenGeneration = work[tokenRequest].generation
  /\ grant[r[1]].active
  /\ work[r].generation = grant[r[1]].generation
  /\ tokenGeneration = work[r].generation
  /\ work' = [work EXCEPT ![r].stage = 2]
  /\ violation' = (violation \/ ~authenticatedOwner \/ tokenRequest # r
       \/ tokenGeneration # work[r].generation)
  /\ UNCHANGED <<grant, seen>>

Start(r) ==
  /\ IF IsGuest(r)
       THEN /\ work[r].stage = 2 \/
                  (Bug("ReuseApproval") /\ work[r].stage = 1 /\
                   \E other \in Requests: IsGuest(other) /\
                       other # r /\ work[other].stage >= 2)
            /\ grant[r[1]].active \/ Bug("StartRevoked")
            /\ work[r].verified \/ ExceptionValid(r[1])
            /\ work[r].generation = grant[r[1]].generation
                 \/ Bug("StaleExecution")
       ELSE work[r].stage = 1
  /\ work' = [work EXCEPT ![r].stage = 3]
  /\ violation' = (violation \/ (IsGuest(r) /\
       (work[r].stage # 2 \/ ~grant[r[1]].active \/
        (~work[r].verified /\ grant[r[1]].exception # grant[r[1]].generation) \/
        work[r].generation # grant[r[1]].generation)))
  /\ UNCHANGED <<grant, seen>>

Recipients(r) ==
  IF work[r].visible /\ grant[r[1]].active /\
     work[r].generation = grant[r[1]].generation
  THEN Shared(r[1]) ELSE OwnerOnly(r[1])
Submit(r) ==
  /\ work[r].stage = 3
  /\ LET selected == IF Bug("HistoricalRecipients") /\ grant[r[1]].active
                       THEN Shared(r[1]) ELSE Recipients(r)
     IN /\ work' = [work EXCEPT ![r].stage = 4, ![r].envelope = selected]
        /\ violation' = (violation \/ selected # Recipients(r))
  /\ UNCHANGED <<grant, seen>>
Retry(r) ==
  /\ work[r].stage = 4
  /\ LET selected == IF Bug("MutableRetry") THEN Recipients(r)
                       ELSE work[r].envelope
     IN /\ work' = [work EXCEPT ![r].envelope = selected]
        /\ violation' = (violation \/ selected # work[r].envelope)
  /\ UNCHANGED <<grant, seen>>
Restart == UNCHANGED vars
Next == (\E k \in Scopes:
           (\E auth, visible, explicit \in BOOLEAN, evidence \in Evidence:
              Invite(k, evidence, auth, visible, explicit)) \/
           (\E tokenScope \in Scopes, epoch \in 0..MaxGeneration,
               auth \in BOOLEAN: Revoke(k, tokenScope, epoch, auth) \/
                                  AcceptException(k, tokenScope, epoch, auth)))
        \/ (\E r \in Requests:
           (\E auth, visible \in BOOLEAN, deliveredScope \in Scopes:
              ReceiveGuest(r, auth, visible, deliveredScope) \/
              WarnGuest(r, visible, deliveredScope)) \/
           (\E auth, visible \in BOOLEAN: ReceiveOwner(r, auth, visible)) \/
           (\E tokenRequest \in Requests, epoch \in 0..MaxGeneration,
               auth \in BOOLEAN: Approve(r, tokenRequest, epoch, auth)) \/
           ReleaseHeld(r) \/ Start(r) \/ Submit(r) \/ Retry(r))
        \/ Restart
Safety == ~violation
AnswerPrivacy == \A r \in Requests: work[r].stage = 4 =>
  /\ Owner(r[1]) \in work[r].envelope
  /\ work[r].envelope \subseteq Shared(r[1])
  /\ ~work[r].visible => work[r].envelope = OwnerOnly(r[1])
TypeOK == /\ grant \in [Scopes -> [active : BOOLEAN,
                                   generation : 0..MaxGeneration, exception : 0..MaxGeneration,
                                   warned : 0..MaxGeneration]]
          /\ seen \in [Scopes -> SUBSET Evidence]
          /\ work \in [Requests -> [stage : 0..5,
                generation : 0..MaxGeneration, visible : BOOLEAN, verified : BOOLEAN,
                envelope : SUBSET ({Owner(k): k \in Scopes} \cup
                                  {Guest(k): k \in Scopes})]]
          /\ violation \in BOOLEAN
\* Expected-failing reachability checks prevent a vacuous safety result from
\* a model that rejects every request or never sends a shared/private answer.
NoRepeatedAnswers == ~\E k \in Scopes:
  \A m \in GuestMessages: work[<<k, m>>].stage = 4 /\
                          work[<<k, m>>].envelope = Shared(k)
NoPrivateContinuation == ~\E r, g \in Requests:
  ~IsGuest(r) /\ IsGuest(g) /\ r[1] = g[1] /\ grant[r[1]].active /\
  ~work[r].visible /\ work[r].stage = 4 /\
  work[r].envelope = OwnerOnly(r[1]) /\ work[g].stage = 4 /\
  work[g].envelope = Shared(g[1])
NoReinvitation == ~\E r \in Requests: IsGuest(r) /\
  work[r].generation = 2 /\ work[r].stage = 4 /\
  work[r].envelope = Shared(r[1])
NoUnverifiedAnswers == ~\E k \in Scopes:
  \A m \in GuestMessages: work[<<k,m>>].stage = 4 /\
    ~work[<<k,m>>].verified /\ work[<<k,m>>].envelope = Shared(k)
Spec == Init /\ [][Next]_vars
=============================================================================
