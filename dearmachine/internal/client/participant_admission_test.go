package client

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParticipantInstructionRequiresPrivateCorrelatedApproval(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-shared")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")

	const secret = "PARTICIPANT_SECRET_DO_NOT_PERSIST"
	participant := Message{
		MessageID: "participant-1", ThreadID: "thread-shared",
		From: "Guest <guest@example.test>", To: []string{pair.UserEmail}, CC: []string{inbox.Address},
		Delivery: MessageDelivery{
			InboxID: inbox.ProviderID, Recipient: inbox.Address,
			Role: DeliveryRoleCC, ReadState: MessageReadStateUnread,
		},
		Subject: "Re: shared work", Body: secret,
		Timestamp: time.Date(2026, 9, 11, 20, 45, 0, 0, time.UTC),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "1" {
		t.Fatalf("unapproved participant reached agent: run count=%q", got)
	}
	replies := raw.sentReplies()
	if len(replies) != 2 {
		t.Fatalf("reply count = %d, want controller answer and approval", len(replies))
	}
	approval := replies[1]
	if approval.MessageID != participant.MessageID || !equalFoldSlice(approval.To, []string{pair.UserEmail}) ||
		len(approval.CC) != 0 || len(approval.BCC) != 0 || approval.IncludeQuotedContent {
		t.Fatalf("approval recipient isolation = %+v", approval)
	}
	for _, choice := range []string{"Yes", "No", "Other"} {
		if !strings.Contains(approval.Text, choice) {
			t.Errorf("approval omitted %q: %q", choice, approval.Text)
		}
	}
	for _, forbidden := range []string{"Cancel", secret} {
		if strings.Contains(approval.Text, forbidden) {
			t.Errorf("approval exposed forbidden content %q: %q", forbidden, approval.Text)
		}
	}
	assertBodyAbsentFromApplicationState(t, rig, secret)
	assertNoPendingParticipantWork(t, rig, participant.MessageID)

	yes := Message{
		MessageID: "controller-yes", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
		InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(yes.ThreadID, append(raw.thread(yes.ThreadID), yes))
	raw.setPoll([]Message{participant, yes}) // duplicate request delivery is intentional
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "2" {
		t.Fatalf("correlated Yes run count = %q, want 2", got)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, secret) || strings.Contains(prompt, "controller-yes") || strings.Contains(prompt, approval.Text) {
		t.Fatalf("approved prompt did not isolate the frozen instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, "lower authority") || !strings.Contains(prompt, pair.UserEmail) {
		t.Fatalf("approved prompt omitted controlling-participant precedence:\n%s", prompt)
	}
	assertBodyAbsentFromApplicationState(t, rig, approval.Text)
}

func TestParticipantControlAcceptsOnlyExactChoices(t *testing.T) {
	tests := []string{"yes", "Yes.", "Yes please", "Skip", ""}
	for _, body := range tests {
		name := body
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			rig, raw, router, pair, inbox := newParticipantTestRig(t)
			establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-exact")
			seedAdmittedParticipants(t, rig.store, "guest@example.test")
			participant := Message{
				MessageID: "guest-exact", ThreadID: "thread-exact",
				From: "guest@example.test", To: []string{inbox.Address}, Body: "HELD_EXACT_BODY",
				Timestamp: time.Now().UTC(),
			}
			raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
			raw.setPoll([]Message{participant})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			approval := raw.sentReplies()[1]

			control := Message{
				MessageID: "invalid-control", ThreadID: participant.ThreadID,
				From: pair.UserEmail, To: []string{inbox.Address}, Body: body,
				InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute),
			}
			raw.setThread(control.ThreadID, append(raw.thread(control.ThreadID), control))
			raw.setPoll([]Message{control})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)

			if got := rig.capture("count"); got != "1" {
				t.Fatalf("non-exact control %q reached agent: run count=%q", body, got)
			}
			request, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
			if err != nil || !found || request.State != participantAwaitingDecision {
				t.Fatalf("request after non-exact control = %+v, found=%v, err=%v", request, found, err)
			}
			assertNoPendingParticipantWork(t, rig, participant.MessageID)

			retryPrompt := raw.sentReplies()[2]
			yes := Message{
				MessageID: "valid-after-invalid", ThreadID: participant.ThreadID,
				From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
				InReplyTo: retryPrompt.ReceiptID, References: []string{approval.ReceiptID, retryPrompt.ReceiptID}, Timestamp: control.Timestamp.Add(time.Minute),
			}
			raw.setThread(yes.ThreadID, append(raw.thread(yes.ThreadID), yes))
			raw.setPoll([]Message{yes})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			if got := rig.capture("count"); got != "2" {
				t.Fatalf("exact retry after %q run count=%q, want 2", body, got)
			}
			if prompt := rig.capture("text-2"); !strings.Contains(prompt, participant.Body) || strings.Contains(prompt, "valid-after-invalid") {
				t.Fatalf("exact retry after %q did not release only guest request:\n%s", body, prompt)
			}
		})
	}
}

func TestParticipantDecisionsRequireControllerAndOriginalThreadCorrelation(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-correlation")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	participant := Message{
		MessageID: "guest-correlation", ThreadID: "thread-correlation",
		From: "guest@example.test", To: []string{inbox.Address}, Body: "HELD_CORRELATION_BODY",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	approval := raw.sentReplies()[1]

	guestDecision := Message{
		MessageID: "guest-false-yes", ThreadID: participant.ThreadID,
		From: "guest@example.test", To: []string{inbox.Address}, Body: "Yes",
		InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(guestDecision.ThreadID, append(raw.thread(guestDecision.ThreadID), guestDecision))
	raw.setPoll([]Message{guestDecision})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	wrongThread := Message{
		MessageID: "controller-wrong-thread", ThreadID: "thread-wrong",
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
		InReplyTo: approval.ReceiptID, Timestamp: guestDecision.Timestamp.Add(time.Minute),
	}
	raw.setThread(wrongThread.ThreadID, []Message{wrongThread})
	raw.setPoll([]Message{wrongThread})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "1" {
		t.Fatalf("uncorrelated decision reached agent: run count=%q", got)
	}
	request, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
	if err != nil || !found || request.State != participantAwaitingDecision {
		t.Fatalf("original request after false decisions = %+v, found=%v, err=%v", request, found, err)
	}
	if falseRequest, falseFound, err := rig.store.ParticipantRequestByMessage(guestDecision.MessageID); err != nil || falseFound {
		t.Fatalf("guest-authored control became an approvable request = %+v, found=%v, err=%v", falseRequest, falseFound, err)
	}
	if got := len(raw.sentReplies()); got != 2 {
		t.Fatalf("guest-authored control sent another approval: total replies=%d, want 2", got)
	}
	assertNoPendingParticipantWork(t, rig, participant.MessageID)
}

func TestParticipantApprovalReleasesOnlyFrozenRequestAndIsolatesLateArrival(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-late-arrival")
	seedAdmittedParticipants(t, rig.store, "first-guest@example.test", "second-guest@example.test")
	first := Message{
		MessageID: "guest-frozen", ThreadID: "thread-late-arrival",
		From: "first-guest@example.test", To: []string{inbox.Address}, Body: "FROZEN_REQUEST_ONLY",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(first.ThreadID, append(raw.thread(first.ThreadID), first))
	raw.setPoll([]Message{first})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	firstApproval := raw.sentReplies()[1]

	late := Message{
		MessageID: "guest-late", ThreadID: first.ThreadID,
		From: "second-guest@example.test", To: []string{inbox.Address}, Body: "LATE_REQUEST_MUST_REMAIN_HELD",
		Timestamp: first.Timestamp.Add(time.Minute),
	}
	raw.setThread(late.ThreadID, append(raw.thread(late.ThreadID), late))
	raw.setPoll([]Message{late})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	lateApproval := raw.sentReplies()[2]

	yes := Message{
		MessageID: "controller-frozen-yes", ThreadID: first.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
		InReplyTo: firstApproval.ReceiptID, Timestamp: late.Timestamp.Add(time.Minute),
	}
	raw.setThread(yes.ThreadID, append(raw.thread(yes.ThreadID), yes))
	raw.setPoll([]Message{yes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "2" {
		t.Fatalf("frozen approval run count=%q, want 2", got)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, first.Body) || strings.Contains(prompt, late.Body) {
		t.Fatalf("frozen approval crossed its request boundary:\n%s", prompt)
	}
	lateRequest, found, err := rig.store.ParticipantRequestByMessage(late.MessageID)
	if err != nil || !found || lateRequest.State != participantAwaitingDecision {
		t.Fatalf("late request after earlier Yes = %+v, found=%v, err=%v", lateRequest, found, err)
	}
	assertNoPendingParticipantWork(t, rig, late.MessageID)
	assertBodyAbsentFromApplicationState(t, rig, late.Body)

	no := Message{
		MessageID: "controller-late-no", ThreadID: late.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "No",
		InReplyTo: lateApproval.ReceiptID, Timestamp: yes.Timestamp.Add(time.Minute),
	}
	raw.setThread(no.ThreadID, append(raw.thread(no.ThreadID), no))
	raw.setPoll([]Message{no})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("late No changed execution count=%q", got)
	}
	assertBodyAbsentFromApplicationState(t, rig, late.Body)
}

func TestParticipantDecisionRequiresReplyCorrelationEvenWithSingleActiveRequest(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-only-correlation")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	participant := Message{
		MessageID: "guest-thread-only", ThreadID: "thread-only-correlation",
		From: "guest@example.test", To: []string{inbox.Address}, Body: "THREAD_ONLY_APPROVED_BODY",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	yes := Message{
		MessageID: "controller-thread-only-yes", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
		Timestamp: participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(yes.ThreadID, append(raw.thread(yes.ThreadID), yes))
	raw.setPoll([]Message{yes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("uncorrelated controller instruction run count=%q, want 2", got)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "\nYes\n") || strings.Contains(prompt, participant.Body) {
		t.Fatalf("uncorrelated Yes did not remain an ordinary controller instruction:\n%s", prompt)
	}
	request, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
	if err != nil || !found || request.State != participantInvalidated {
		t.Fatalf("uncorrelated Yes did not invalidate guest request = %+v, found=%v, err=%v", request, found, err)
	}
}

func TestParticipantAmbiguousReferencesResolveNoRequest(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-ambiguous")
	seedAdmittedParticipants(t, rig.store, "first@example.test", "second@example.test")
	first := Message{
		MessageID: "guest-ambiguous-first", ThreadID: "thread-ambiguous",
		From: "first@example.test", To: []string{inbox.Address}, Body: "AMBIGUOUS_FIRST_HELD",
		Timestamp: time.Now().UTC(),
	}
	second := Message{
		MessageID: "guest-ambiguous-second", ThreadID: first.ThreadID,
		From: "second@example.test", To: []string{inbox.Address}, Body: "AMBIGUOUS_SECOND_HELD",
		Timestamp: first.Timestamp.Add(time.Minute),
	}
	for _, participant := range []Message{first, second} {
		raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
		raw.setPoll([]Message{participant})
		router.lastPoll = time.Time{}
		mustProcess(t, rig)
	}
	replies := raw.sentReplies()
	ambiguous := Message{
		MessageID: "controller-ambiguous-yes", ThreadID: first.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
		References: []string{replies[1].ReceiptID, replies[2].ReceiptID},
		Timestamp:  second.Timestamp.Add(time.Minute),
	}
	raw.setThread(ambiguous.ThreadID, append(raw.thread(ambiguous.ThreadID), ambiguous))
	raw.setPoll([]Message{ambiguous})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "1" {
		t.Fatalf("ambiguous approval reached agent: run count=%q", got)
	}
	for _, participant := range []Message{first, second} {
		request, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
		if err != nil || !found || request.State != participantAwaitingDecision {
			t.Fatalf("ambiguous request %s = %+v, found=%v, err=%v", participant.MessageID, request, found, err)
		}
		assertNoPendingParticipantWork(t, rig, participant.MessageID)
		assertBodyAbsentFromApplicationState(t, rig, participant.Body)
	}
	seen, err := rig.store.Seen(ambiguous.MessageID)
	if err != nil || !seen {
		t.Fatalf("ambiguous control tombstone = %v, %v", seen, err)
	}
}

func TestParticipantDecisionAndDeliveryDuplicatesAreIdempotent(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-duplicates")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	participant := Message{
		MessageID: "guest-duplicate", ThreadID: "thread-duplicates",
		From: "guest@example.test", To: []string{inbox.Address}, Body: "RUN_ONCE_GUEST_BODY",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant, participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	approval := raw.sentReplies()[1]
	if got := len(raw.sentReplies()); got != 2 {
		t.Fatalf("duplicate guest delivery sent %d total replies, want 2", got)
	}

	yes := Message{
		MessageID: "controller-duplicate-yes", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
		InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(yes.ThreadID, append(raw.thread(yes.ThreadID), yes))
	raw.setPoll([]Message{yes, yes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	raw.setPoll([]Message{participant, yes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "2" {
		t.Fatalf("duplicate decision or delivery reran instruction: run count=%q", got)
	}
}

func TestParticipantApprovalRequestRecoversWithoutBodyPersistenceOrDuplicatePrompt(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-restart")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	const secret = "RESTART_SECRET_MUST_STAY_PROVIDER_SIDE"
	participant := Message{
		MessageID: "guest-restart", ThreadID: "thread-restart",
		From: "guest@example.test", To: []string{inbox.Address}, Body: secret,
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	approval := raw.sentReplies()[1]
	assertBodyAbsentFromApplicationState(t, rig, secret)

	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(rig.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rig.store = store
	rig.app, err = New(
		rig.app.transport, store, rig.app.runner, nil, 3, time.Minute,
		log.New(io.Discard, "", 0), false, "", TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if got := len(raw.sentReplies()); got != 2 {
		t.Fatalf("restart resent approval: total replies=%d", got)
	}
	assertBodyAbsentFromApplicationState(t, rig, secret)
	assertNoPendingParticipantWork(t, rig, participant.MessageID)

	yes := Message{
		MessageID: "controller-restart-yes", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes",
		InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(yes.ThreadID, append(raw.thread(yes.ThreadID), yes))
	raw.setPoll([]Message{yes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("recovered approval run count=%q, want 2", got)
	}
}

func TestParticipantApprovalRecoversReceiptSentBeforePromptCommit(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-receipt-recovery")
	participant := Message{
		MessageID: "guest-receipt-recovery", ThreadID: "thread-receipt-recovery",
		From: "guest@example.test", To: []string{inbox.Address}, Body: "RECEIPT_RECOVERY_HELD",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	request, _, err := rig.store.BeginParticipantRequest(participant, pair.UserEmail)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := rig.app.transport.Reply(
		context.Background(), participant.MessageID,
		participantPrivatePayload(participantApprovalPrompt(request), pair.UserEmail),
		controlIdempotencyKey("participant-approval", participant.MessageID),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the provider accepted the private reply but before
	// SetParticipantPrompt committed its receipt and correlation metadata.
	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(rig.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rig.store = store
	rig.app, err = New(
		rig.app.transport, store, rig.app.runner, nil, 3, time.Minute,
		log.New(io.Discard, "", 0), false, "", TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := len(raw.sentReplies()); got != 2 {
		t.Fatalf("receipt recovery resent approval: total replies=%d", got)
	}
	recovered, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
	if err != nil || !found || recovered.PromptMessageID != receipt || recovered.State != participantAwaitingDecision {
		t.Fatalf("recovered approval = %+v, found=%v, err=%v", recovered, found, err)
	}
	assertBodyAbsentFromApplicationState(t, rig, participant.Body)
	assertNoPendingParticipantWork(t, rig, participant.MessageID)
}

func TestParticipantControllerAuthorityRecoversAfterClaimBeforeMetadata(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-authority-recovery")
	controller := Message{
		MessageID: "controller-authority-recovery", ThreadID: "thread-authority-recovery",
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "RECOVER_WITH_CONTROLLER_AUTHORITY",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(controller.ThreadID, append(raw.thread(controller.ThreadID), controller))
	pending, existed, err := rig.store.BeginMessage(controller.MessageID, controller.ThreadID, TierPlain)
	if err != nil || existed || pending.Authority != "" {
		t.Fatalf("pre-crash claim = %+v, existed=%v, err=%v", pending, existed, err)
	}

	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(rig.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rig.store = store
	rig.app, err = New(
		rig.app.transport, store, rig.app.runner, nil, 3, time.Minute,
		log.New(io.Discard, "", 0), false, "", TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw.setPoll(nil)
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "2" {
		t.Fatalf("controller recovery run count=%q, want 2", got)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "RECOVER_WITH_CONTROLLER_AUTHORITY") ||
		!strings.Contains(prompt, "Authority: highest") ||
		!strings.Contains(prompt, pair.UserEmail) {
		t.Fatalf("recovered controller prompt omitted authority:\n%s", prompt)
	}
}

func TestParticipantApprovedInstructionRecoveryDoesNotTreatApprovalAsFinalReply(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-approved-recovery")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	participant := Message{
		MessageID: "guest-approved-recovery", ThreadID: "thread-approved-recovery",
		From: "guest@example.test", To: []string{inbox.Address}, Body: "APPROVED_RECOVERY_BODY",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	request, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
	if err != nil || !found {
		t.Fatalf("ParticipantRequestByMessage = %+v, %v, %v", request, found, err)
	}
	if _, err := rig.store.MaterializeParticipantExecution(
		request, participant.MessageID, "controller-approved-recovery",
		participantResolvedYes, false,
	); err != nil {
		t.Fatal(err)
	}

	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(rig.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rig.store = store
	rig.app, err = New(
		rig.app.transport, store, rig.app.runner, nil, 3, time.Minute,
		log.New(io.Discard, "", 0), false, "", TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw.setPoll(nil)
	router.lastPoll = time.Time{}
	mustProcess(t, rig)

	if got := rig.capture("count"); got != "2" {
		t.Fatalf("approved recovery mistook control mail for final reply: run count=%q", got)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, participant.Body) || !strings.Contains(prompt, "Authority: lower authority") {
		t.Fatalf("approved recovery prompt:\n%s", prompt)
	}
}

func TestParticipantApprovalPromptRetriesAfterSendFailure(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-send-retry")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	participant := Message{
		MessageID: "guest-send-retry", ThreadID: "thread-send-retry",
		From: "guest@example.test", To: []string{inbox.Address}, Body: "HELD_SEND_RETRY_BODY",
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	raw.errors.Reply = errors.New("injected approval send failure")
	router.lastPoll = time.Time{}
	if err := rig.app.ProcessOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "injected approval send failure") {
		t.Fatalf("first ProcessOnce error = %v", err)
	}
	request, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
	if err != nil || !found || request.State != participantAwaitingDecision || request.PromptMessageID != "" {
		t.Fatalf("request after failed send = %+v, found=%v, err=%v", request, found, err)
	}
	assertNoPendingParticipantWork(t, rig, participant.MessageID)

	raw.errors.Reply = nil
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if got := len(raw.sentReplies()); got != 2 {
		t.Fatalf("approval retry total replies=%d, want controller answer plus approval", got)
	}
	approval := raw.sentReplies()[1]
	if !equalFoldSlice(approval.To, []string{pair.UserEmail}) {
		t.Fatalf("retried approval recipients = %v", approval.To)
	}
}

func TestParticipantNoDisposesBodyAndOtherRunsOnlyControllerReplacement(t *testing.T) {
	for _, decision := range []string{"No", "Other"} {
		t.Run(decision, func(t *testing.T) {
			rig, raw, router, pair, inbox := newParticipantTestRig(t)
			establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-"+strings.ToLower(decision))
			seedAdmittedParticipants(t, rig.store, "guest@example.test")
			secret := "REJECTED_" + strings.ToUpper(decision) + "_BODY"
			participant := Message{MessageID: "guest-" + strings.ToLower(decision), ThreadID: "thread-" + strings.ToLower(decision), From: "guest@example.test", To: []string{inbox.Address}, Body: secret, Timestamp: time.Now().UTC()}
			raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
			raw.setPoll([]Message{participant})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			approval := raw.sentReplies()[1]

			control := Message{MessageID: "control-" + strings.ToLower(decision), ThreadID: participant.ThreadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: decision, InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute)}
			raw.setThread(control.ThreadID, append(raw.thread(control.ThreadID), control))
			raw.setPoll([]Message{control})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)

			if decision == "No" {
				if got := rig.capture("count"); got != "1" {
					t.Fatalf("No reached agent: run count=%q", got)
				}
				assertBodyAbsentFromApplicationState(t, rig, secret)
				return
			}

			replacementPrompt := raw.sentReplies()[2]
			if !equalFoldSlice(replacementPrompt.To, []string{pair.UserEmail}) || strings.Contains(replacementPrompt.Text, secret) {
				t.Fatalf("replacement request was not private: %+v", replacementPrompt)
			}
			staleYes := Message{MessageID: "stale-original-yes", ThreadID: participant.ThreadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes", InReplyTo: approval.ReceiptID, Timestamp: control.Timestamp.Add(time.Minute)}
			raw.setThread(staleYes.ThreadID, append(raw.thread(staleYes.ThreadID), staleYes))
			raw.setPoll([]Message{staleYes})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			if got := rig.capture("count"); got != "1" {
				t.Fatalf("stale Yes became replacement work: run count=%q", got)
			}
			emptyReplacement := Message{MessageID: "empty-replacement", ThreadID: participant.ThreadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: "> " + secret, InReplyTo: replacementPrompt.ReceiptID, Timestamp: control.Timestamp.Add(time.Minute)}
			raw.setThread(emptyReplacement.ThreadID, append(raw.thread(emptyReplacement.ThreadID), emptyReplacement))
			raw.setPoll([]Message{emptyReplacement})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)
			if got := rig.capture("count"); got != "1" {
				t.Fatalf("empty replacement reached agent: run count=%q", got)
			}
			replacementPrompt = raw.sentReplies()[3]
			if !equalFoldSlice(replacementPrompt.To, []string{pair.UserEmail}) || strings.Contains(replacementPrompt.Text, secret) {
				t.Fatalf("replacement retry was not private: %+v", replacementPrompt)
			}

			replacement := Message{MessageID: "replacement", ThreadID: participant.ThreadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: "CONTROLLER_REPLACEMENT_ONLY\n\n> " + secret, InReplyTo: replacementPrompt.ReceiptID, References: []string{approval.ReceiptID, replacementPrompt.ReceiptID}, Timestamp: emptyReplacement.Timestamp.Add(time.Minute)}
			raw.setThread(replacement.ThreadID, append(raw.thread(replacement.ThreadID), replacement))
			raw.setPoll([]Message{replacement})
			router.lastPoll = time.Time{}
			mustProcess(t, rig)

			if got := rig.capture("count"); got != "2" {
				t.Fatalf("replacement run count=%q, want 2", got)
			}
			prompt := rig.capture("text-2")
			if !strings.Contains(prompt, "CONTROLLER_REPLACEMENT_ONLY") || strings.Contains(prompt, secret) {
				t.Fatalf("replacement prompt leaked rejected participant body:\n%s", prompt)
			}
		})
	}
}

func TestParticipantReplacementRecoverySanitizesProviderRefetch(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-replacement-recovery")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	const secret = "RECOVERY_REJECTED_GUEST_BODY"
	participant := Message{
		MessageID: "guest-recovery-other", ThreadID: "thread-replacement-recovery",
		From: "guest@example.test", To: []string{inbox.Address}, Body: secret,
		Timestamp: time.Now().UTC(),
	}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	approval := raw.sentReplies()[1]
	other := Message{
		MessageID: "controller-recovery-other", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "Other",
		InReplyTo: approval.ReceiptID, Timestamp: participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(other.ThreadID, append(raw.thread(other.ThreadID), other))
	raw.setPoll([]Message{other})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	replacementPrompt := raw.sentReplies()[2]
	replacement := Message{
		MessageID: "controller-recovery-replacement", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address},
		Body:      "RECOVERED_CONTROLLER_REPLACEMENT\n\n> " + secret,
		InReplyTo: replacementPrompt.ReceiptID, Timestamp: other.Timestamp.Add(time.Minute),
	}
	raw.setThread(replacement.ThreadID, append(raw.thread(replacement.ThreadID), replacement))
	raw.setPoll(nil)
	request, found, err := rig.store.ParticipantRequestByMessage(participant.MessageID)
	if err != nil || !found {
		t.Fatalf("ParticipantRequestByMessage = %+v, %v, %v", request, found, err)
	}
	if _, err := rig.store.MaterializeParticipantExecution(
		request, replacement.MessageID, "", participantResolvedOther, true,
	); err != nil {
		t.Fatalf("MaterializeParticipantExecution: %v", err)
	}

	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(rig.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rig.store = store
	rig.app, err = New(
		rig.app.transport, store, rig.app.runner, nil, 3, time.Minute,
		log.New(io.Discard, "", 0), false, "", TierPlain,
	)
	if err != nil {
		t.Fatal(err)
	}
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("replacement recovery run count=%q, want 2", got)
	}
	prompt := rig.capture("text-2")
	if !strings.Contains(prompt, "RECOVERED_CONTROLLER_REPLACEMENT") || strings.Contains(prompt, secret) {
		t.Fatalf("recovered replacement prompt leaked rejected content:\n%s", prompt)
	}
}

func TestOrdinaryControllerInstructionInvalidatesOlderParticipantRequest(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread-precedence")
	seedAdmittedParticipants(t, rig.store, "guest@example.test")
	participant := Message{MessageID: "guest-old", ThreadID: "thread-precedence", From: "guest@example.test", To: []string{inbox.Address}, Body: "STALE_GUEST_BODY", Timestamp: time.Now().UTC()}
	raw.setThread(participant.ThreadID, append(raw.thread(participant.ThreadID), participant))
	raw.setPoll([]Message{participant})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	approval := raw.sentReplies()[1]

	controller := Message{
		MessageID: "controller-new", ThreadID: participant.ThreadID,
		From: pair.UserEmail, To: []string{inbox.Address}, Body: "CONTROLLER_WINS",
		InReplyTo:  "controller-root-thread-precedence",
		References: []string{approval.ReceiptID},
		Timestamp:  participant.Timestamp.Add(time.Minute),
	}
	raw.setThread(controller.ThreadID, append(raw.thread(controller.ThreadID), controller))
	raw.setPoll([]Message{controller})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if prompt := rig.capture("text-2"); !strings.Contains(prompt, "CONTROLLER_WINS") || strings.Contains(prompt, "STALE_GUEST_BODY") {
		t.Fatalf("controller precedence prompt:\n%s", prompt)
	}

	lateYes := Message{MessageID: "late-yes", ThreadID: participant.ThreadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes", InReplyTo: approval.ReceiptID, Timestamp: controller.Timestamp.Add(time.Minute)}
	raw.setThread(lateYes.ThreadID, append(raw.thread(lateYes.ThreadID), lateYes))
	raw.setPoll([]Message{lateYes})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	if got := rig.capture("count"); got != "2" {
		t.Fatalf("late Yes released invalidated body: run count=%q", got)
	}
}

func newParticipantTestRig(t *testing.T) (*testRig, *fakeTransport, *InboxRouter, Pair, Inbox) {
	t.Helper()
	raw := newFakeTransport()
	inbox := Inbox{ID: testInboxUUID, Transport: "test", ProviderID: "provider", Address: "machine@example.test"}
	pair := Pair{ID: testPairAUUID, UserEmail: "controller@example.test", InboxID: inbox.ID}
	router, err := NewInboxRouter(raw, inbox, []Pair{pair}, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := router.Endpoint(pair.ID)
	if err != nil {
		t.Fatal(err)
	}
	return newTestRigTransport(t, TierPlain, endpoint, "test-model"), raw, router, pair, inbox
}

func establishParticipantThread(t *testing.T, rig *testRig, raw *fakeTransport, router *InboxRouter, pair Pair, inbox Inbox, threadID string) {
	t.Helper()
	controller := Message{MessageID: "controller-root-" + threadID, ThreadID: threadID, From: pair.UserEmail, To: []string{inbox.Address}, Body: "Establish controlled work.", Timestamp: time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)}
	raw.setThread(threadID, []Message{controller})
	raw.setPoll([]Message{controller})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
}

func seedAdmittedParticipants(t *testing.T, store *Store, addresses ...string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, address := range addresses {
		canonical, err := canonicalMessageAddress(address)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(
			`INSERT INTO admitted_participants
			     (participant_address, trusted, created_at, updated_at)
			 VALUES (?, 0, ?, ?)`,
			canonical, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fakeTransport) thread(threadID string) []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneMessages(f.threads[threadID])
}

func (f *fakeTransport) sentReplies() []fakeTransportReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeTransportReply(nil), f.replies...)
}

func equalFoldSlice(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if !strings.EqualFold(got[index], want[index]) {
			return false
		}
	}
	return true
}

func assertBodyAbsentFromApplicationState(t *testing.T, rig *testRig, body string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		contents, err := os.ReadFile(rig.dbPath + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), body) {
			t.Fatalf("unapproved body persisted in %s", filepath.Base(rig.dbPath+suffix))
		}
	}
	if entries, _ := os.ReadDir(rig.captureDir); len(entries) > 0 {
		for _, entry := range entries {
			contents, _ := os.ReadFile(filepath.Join(rig.captureDir, entry.Name()))
			if strings.Contains(string(contents), body) {
				t.Fatalf("unapproved body reached agent capture %s", entry.Name())
			}
		}
	}
	err := filepath.WalkDir(rig.app.runner.projectDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), body) {
			t.Fatalf("unapproved body persisted in session/project file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertNoPendingParticipantWork(t *testing.T, rig *testRig, messageID string) {
	t.Helper()
	pending, found, err := rig.store.PendingByID(messageID)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("unapproved participant has executable pending work: %+v", pending)
	}
	var count int
	if err := rig.store.db.QueryRow(
		`SELECT COUNT(*) FROM pending_messages WHERE message_id = ?`,
		messageID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pending row count for unapproved participant = %d", count)
	}
	seen, err := rig.store.Seen(messageID)
	if err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("content-free participant request was not retained for idempotency")
	}
}
