------------------------- MODULE Replacement -------------------------
EXTENDS Naturals, TLC

\* One owner replacement, after an authenticated Other decision. Provider
\* identity and authorization are assumptions supplied by Participation.tla.
\* SQLite transactions are atomic steps; crash can occur between any steps.
CONSTANTS Fixture, Mutation
VARIABLES decision, pending, processed, online, recovering, sent,
          receiptKnown, deliveries, sequence, completed, unread, ranSavedResult
vars == <<decision, pending, processed, online, recovering, sent,
          receiptKnown, deliveries, sequence, completed, unread, ranSavedResult>>

Legacy == Fixture \in {"LegacyUnsent", "LegacySent"}
Conflict == Fixture = "Conflict"
Init ==
 /\ decision = IF Fixture = "Clean" THEN "decision" ELSE "resolved_other"
 /\ pending = IF Fixture = "Clean" THEN "none" ELSE "result_ready"
 /\ processed = IF Legacy THEN "empty_control"
                 ELSE IF Conflict THEN "foreign_receipt" ELSE "none"
 /\ online = TRUE
 /\ recovering = (Fixture # "Clean")
 /\ sent = (Fixture = "LegacySent")
 /\ receiptKnown = FALSE
 /\ deliveries = IF sent THEN 1 ELSE 0
 /\ sequence = 0
 /\ completed = FALSE
 /\ unread = TRUE
 /\ ranSavedResult = FALSE

Other ==
 /\ online /\ ~recovering /\ decision = "decision"
 /\ decision' = "replacement"
 /\ UNCHANGED <<pending, processed, online, recovering, sent, receiptKnown,
                deliveries, sequence, completed, unread, ranSavedResult>>

Claim ==
 /\ online /\ ~recovering /\ decision = "replacement"
 /\ decision' = "resolved_other" /\ pending' = "received"
 /\ UNCHANGED <<processed, online, recovering, sent, receiptKnown,
                deliveries, sequence, completed, unread, ranSavedResult>>

Start ==
 /\ online /\ ~recovering /\ pending = "received"
 /\ pending' = "running"
 /\ UNCHANGED <<decision, processed, online, recovering, sent, receiptKnown,
                deliveries, sequence, completed, unread, ranSavedResult>>

Result ==
 /\ online /\ ~recovering /\ pending = "running"
 /\ pending' = "result_ready"
 /\ UNCHANGED <<decision, processed, online, recovering, sent, receiptKnown,
                deliveries, sequence, completed, unread, ranSavedResult>>

\* Fixed pollAndClaim leaves all durable pending work to dispatch/recovery.
\* The mutation is the observed stale-control write on a replacement repoll.
Poll ==
 /\ online /\ unread /\ decision = "resolved_other"
 /\ IF pending # "none"
       THEN /\ processed' = IF Mutation = "RepollPending"
                               THEN "empty_control" ELSE processed
            /\ unread' = IF Mutation = "RepollPending" THEN FALSE ELSE unread
       ELSE /\ UNCHANGED processed /\ unread' = FALSE
 /\ UNCHANGED <<decision, pending, online, recovering, sent, receiptKnown,
                deliveries, sequence, completed, ranSavedResult>>

Crash ==
 /\ online /\ online' = FALSE /\ recovering' = TRUE /\ receiptKnown' = FALSE
 /\ UNCHANGED <<decision, pending, processed, sent, deliveries, sequence,
                completed, unread, ranSavedResult>>
Boot ==
 /\ ~online /\ online' = TRUE
 /\ UNCHANGED <<decision, pending, processed, recovering, sent, receiptKnown,
                deliveries, sequence, completed, unread, ranSavedResult>>
Recover ==
 /\ online /\ recovering
 /\ recovering' = FALSE
 /\ receiptKnown' = IF Mutation = "ForgetReceipt" THEN FALSE ELSE sent
 /\ ranSavedResult' = (ranSavedResult \/
                       (Mutation = "RunSavedResult" /\ pending = "result_ready"))
 /\ UNCHANGED <<decision, pending, processed, online, sent, deliveries,
                sequence, completed, unread>>

\* Delivery may succeed with its response lost. Receipt lookup on restart
\* must then find the provider's durable, matching owner-only answer.
Submit(lost) ==
 /\ online /\ ~recovering /\ pending = "result_ready" /\ ~receiptKnown
 /\ deliveries < 2
 /\ sent' = TRUE /\ deliveries' = deliveries + 1
 /\ receiptKnown' = ~lost /\ online' = ~lost /\ recovering' = lost
 /\ UNCHANGED <<decision, pending, processed, sequence, completed, unread,
                ranSavedResult>>

Complete ==
 /\ online /\ ~recovering /\ pending = "result_ready" /\ receiptKnown
 /\ (processed = "none" \/
       (processed = "empty_control" /\ Mutation # "NoLegacyRepair") \/
       Mutation = "LooseRepair")
 /\ pending' = "none" /\ processed' = "answer"
 /\ sequence' = sequence + 1 /\ completed' = TRUE
 /\ UNCHANGED <<decision, online, recovering, sent, receiptKnown, deliveries,
                unread, ranSavedResult>>

Ack ==
 /\ online /\ completed /\ unread /\ unread' = FALSE
 /\ UNCHANGED <<decision, pending, processed, online, recovering, sent,
                receiptKnown, deliveries, sequence, completed, ranSavedResult>>

Next == Other \/ Claim \/ Start \/ Result \/ Poll \/ Crash \/ Boot \/ Recover
        \/ Submit(TRUE) \/ Submit(FALSE) \/ Complete \/ Ack
Spec == Init /\ [][Next]_vars
        /\ WF_vars(Other) /\ WF_vars(Claim) /\ WF_vars(Start) /\ WF_vars(Result)
        /\ WF_vars(Boot) /\ WF_vars(Recover) /\ WF_vars(Submit(FALSE))
        /\ WF_vars(Complete) /\ WF_vars(Ack)

TypeOK ==
 /\ decision \in {"decision", "replacement", "resolved_other"}
 /\ pending \in {"none", "received", "running", "result_ready"}
 /\ processed \in {"none", "empty_control", "foreign_receipt", "answer"}
 /\ online \in BOOLEAN /\ recovering \in BOOLEAN /\ sent \in BOOLEAN
 /\ receiptKnown \in BOOLEAN /\ completed \in BOOLEAN /\ unread \in BOOLEAN
 /\ ranSavedResult \in BOOLEAN /\ deliveries \in 0..2 /\ sequence \in 0..1
Safety ==
 /\ deliveries <= 1 /\ ~ranSavedResult
 /\ (Fixture = "Clean" /\ pending # "none" => processed = "none")
 /\ (Conflict => processed = "foreign_receipt" /\ ~completed)
 /\ (completed <=> sequence = 1)
 /\ (completed => pending = "none" /\ processed = "answer" /\ sent)
\* Progress requires eventual uptime, fair work and reliable receipt lookup.
\* Foreign receipt/thread conflicts deliberately fail closed, not complete.
Progress == (<>[]online /\ ~Conflict) => <>(completed /\ ~unread)
=============================================================================
