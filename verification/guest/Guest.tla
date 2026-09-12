----------------------------- MODULE Guest -----------------------------
EXTENDS Naturals, FiniteSets, TLC
CONSTANTS Pairs, Inboxes, Guests, Threads, Evidence, MaxGeneration,
          KnownOnly, StaleDecision, ReplayInvitation, DeleteUnowned, Sparse
Primary(S) == CHOOSE x \in S: TRUE
AllKeys == Pairs \X Inboxes \X Guests \X Threads
Keys == IF Sparse THEN {k \in AllKeys:
           (k[1] = Primary(Pairs) /\ k[2] = Primary(Inboxes) /\
            k[3] = Primary(Guests) /\ k[4] = Primary(Threads)) \/
           (k[1] # Primary(Pairs) /\ k[2] # Primary(Inboxes) /\
            k[3] # Primary(Guests) /\ k[4] # Primary(Threads))}
        ELSE AllKeys
VARIABLES grant, seen, work, remote, owned, intent, violation, permanent
vars == <<grant, seen, work, remote, owned, intent, violation, permanent>>
EmptyGrant == [active |-> FALSE, generation |-> 0]
EmptyWork == [generation |-> 0, authenticated |-> FALSE, approved |-> FALSE]
Init == /\ grant = [k \in Keys |-> EmptyGrant]
        /\ seen = [k \in Keys |-> {}]
        /\ work = [k \in Keys |-> EmptyWork]
        /\ remote \in SUBSET (Inboxes \X Guests)
        /\ permanent \in SUBSET (Inboxes \X Guests)
        /\ owned = {}
        /\ intent = {}
        /\ violation = FALSE
Entry(k) == <<k[2], k[3]>>
Needed(e) == e \in permanent \/ (\E k \in Keys: Entry(k) = e /\ grant[k].active)
Invite(k, m, explicit, authenticated, visible, paired) ==
  /\ authenticated /\ visible /\ ~paired
  /\ explicit \/ m \notin seen[k] \/ ReplayInvitation
  /\ explicit \/ grant[k].active \/ grant[k].generation = 0 \/ ReplayInvitation
  /\ grant[k].active \/ grant[k].generation < MaxGeneration
  /\ grant' = [grant EXCEPT ![k] =
       [active |-> TRUE, generation |-> IF @.active THEN @.generation ELSE @.generation + 1]]
  /\ seen' = [seen EXCEPT ![k] = @ \cup {m}]
  /\ intent' = intent \cup {Entry(k)}
  /\ violation' = (violation \/
       (~explicit /\ m \in seen[k] /\ ~grant[k].active))
  /\ UNCHANGED <<work, remote, owned, permanent>>
Revoke(k) ==
  /\ grant[k].active
  /\ grant' = [grant EXCEPT ![k].active = FALSE]
  /\ intent' = intent \cup {Entry(k)}
  /\ UNCHANGED <<seen, work, remote, owned, violation, permanent>>
Receive(k, authenticated, visible) ==
  /\ authenticated /\ visible /\ (grant[k].active \/ KnownOnly)
  /\ work[k].generation = 0
  /\ work' = [work EXCEPT ![k] =
       [generation |-> grant[k].generation, authenticated |-> TRUE, approved |-> FALSE]]
  /\ violation' = (violation \/ ~grant[k].active)
  /\ UNCHANGED <<grant, seen, remote, owned, intent, permanent>>
Approve(k, controller) ==
  /\ controller /\ work[k].authenticated
  /\ grant[k].active /\ work[k].generation = grant[k].generation
  /\ work' = [work EXCEPT ![k].approved = TRUE]
  /\ UNCHANGED <<grant, seen, remote, owned, intent, violation, permanent>>
Start(k) ==
  /\ work[k].approved /\ work[k].authenticated
  /\ grant[k].active
  /\ StaleDecision \/ work[k].generation = grant[k].generation
  /\ violation' = (violation \/ work[k].generation # grant[k].generation)
  /\ work' = [work EXCEPT ![k] = EmptyWork]
  /\ UNCHANGED <<grant, seen, remote, owned, intent, permanent>>
\* Remote operations are separate from committed local intent. A lost add
\* response preserves uncertain ownership; an inspection cannot invent it.
Add(e, response) ==
  /\ e \in intent /\ Needed(e) /\ e \notin remote
  /\ remote' = remote \cup {e}
  /\ owned' = IF response THEN owned \cup {e} ELSE owned
  /\ UNCHANGED <<grant, seen, work, intent, violation, permanent>>
Remove(e) ==
  /\ e \in intent /\ ~Needed(e) /\ e \in remote
  /\ e \in owned \/ DeleteUnowned
  /\ remote' = remote \ {e}
  /\ owned' = owned \ {e}
  /\ violation' = (violation \/ e \notin owned)
  /\ UNCHANGED <<grant, seen, work, intent, permanent>>
Inspect(e) ==
  /\ e \in intent
  /\ (Needed(e) /\ e \in remote) \/ (~Needed(e) /\ e \notin owned)
  /\ intent' = intent \ {e}
  /\ UNCHANGED <<grant, seen, work, remote, owned, violation, permanent>>
Restart == UNCHANGED vars
Next == (\E k \in Keys:
           (\E m \in Evidence, explicit, authenticated, visible, paired \in BOOLEAN:
              Invite(k, m, explicit, authenticated, visible, paired))
           \/ Revoke(k) \/ (\E a, v \in BOOLEAN: Receive(k, a, v))
           \/ (\E c \in BOOLEAN: Approve(k, c)) \/ Start(k))
        \/ (\E e \in Inboxes \X Guests:
              (\E response \in BOOLEAN: Add(e, response)) \/ Remove(e) \/ Inspect(e))
        \/ Restart
Safety == ~violation
TypeOK == /\ owned \subseteq remote
          /\ intent \subseteq Inboxes \X Guests
          /\ \A k \in Keys: grant[k].generation \in 0..MaxGeneration
Spec == Init /\ [][Next]_vars
=============================================================================
