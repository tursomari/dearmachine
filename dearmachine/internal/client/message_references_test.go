package client

import (
	"context"
	"testing"
	"time"
)

func TestInternetReferencesResolveOnlyScopedOutboundPrompts(t *testing.T) {
	control := Message{MessageID: "control", RFCMessageID: "<control@example.test>", ThreadID: "thread", InReplyTo: "<prompt@example.test>", References: []string{"<prompt@example.test>"}, Delivery: MessageDelivery{InboxID: "inbox", Recipient: "machine@example.test"}}
	prompt := Message{MessageID: "opaque-prompt", RFCMessageID: control.InReplyTo, ThreadID: control.ThreadID, From: control.Delivery.Recipient, Labels: []string{"sent"}, Delivery: control.Delivery}
	for _, mode := range []string{"valid", "other-thread", "other-inbox", "wrong-sender", "inbound", "opaque-injection", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			c, p := control, prompt
			switch mode {
			case "other-thread":
				p.ThreadID = "another-thread"
			case "other-inbox":
				p.Delivery.InboxID = "another-inbox"
			case "wrong-sender":
				p.From = "guest@example.test"
			case "inbound":
				p.Labels = []string{"inbound"}
			case "opaque-injection":
				c.InReplyTo = p.MessageID
				c.References = []string{p.MessageID}
			}
			thread := []Message{p}
			if mode == "ambiguous" {
				duplicate := p
				duplicate.MessageID = "other-prompt"
				thread = append(thread, duplicate)
			}
			result, err := resolveControlReferences(c, thread)
			if mode == "ambiguous" {
				if err == nil {
					t.Fatal("ambiguous reference accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "valid" {
				if result.InReplyTo != prompt.MessageID || len(result.References) != 1 {
					t.Fatal("signed reference was not resolved")
				}
			} else if result.InReplyTo != "" || len(result.References) != 0 {
				t.Fatal("unscoped reference resolved")
			}
			if c.InReplyTo != control.InReplyTo && mode != "opaque-injection" {
				t.Fatal("signed input modified")
			}
		})
	}
}

func TestParticipantApprovalWithInternetMessageIDs(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
	guest := Message{MessageID: "guest-request", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: "Execute only after approval", Timestamp: time.Now().UTC()}
	raw.setThread("thread", append(raw.thread("thread"), guest))
	raw.setPoll([]Message{guest})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	replies := raw.sentReplies()
	prompt := replies[len(replies)-1]
	thread := raw.thread("thread")
	found := false
	for i := range thread {
		if thread[i].MessageID == prompt.ReceiptID {
			thread[i].RFCMessageID = "<prompt@receiver.test>"
			thread[i].Delivery = MessageDelivery{InboxID: inbox.ProviderID, Recipient: inbox.Address}
			found = true
		}
	}
	if !found {
		t.Fatal("approval prompt absent")
	}
	control := Message{MessageID: "approval", RFCMessageID: "<approval@sender.test>", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes", InReplyTo: "<prompt@receiver.test>", Timestamp: guest.Timestamp.Add(time.Minute)}
	raw.setThread("thread", append(thread, control))
	raw.setPoll([]Message{control})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	request, ok, err := rig.store.ParticipantRequestByMessage(guest.MessageID)
	if err != nil || !ok || request.State != participantResolvedYes {
		t.Fatalf("approval did not authorize exact request: state=%s err=%v", request.State, err)
	}
	if rig.capture("count") != "2" {
		t.Fatal("approval did not release exactly one guest execution")
	}
	if _, err = rig.app.participantControlReferences(context.Background(), control); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalReferenceRequiresAuthenticatedOwnerAndPrivateScopedPrompt(t *testing.T) {
	token := "0123456789abcdef0123456789abcdef"
	control := Message{authenticated: true, From: "owner@example.test", MessageID: "control", RFCMessageID: "<control@example.test>", ThreadID: "thread", RawBody: "Yes\n\n> " + approvalReferencePrefix + token, Delivery: MessageDelivery{InboxID: "inbox", Recipient: "machine@example.test"}}
	prompt := Message{From: control.Delivery.Recipient, To: []string{control.From}, MessageID: "prompt", RFCMessageID: "<original@example.test>", ThreadID: "thread", Body: approvalReferencePrefix + token, Labels: []string{"sent"}, Delivery: control.Delivery}
	for _, mode := range []string{"valid", "unsigned", "other-owner", "other-inbox", "other-thread", "shared", "inbound", "wrong-token", "preview-forged", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			c, p := control, prompt
			switch mode {
			case "unsigned":
				c.authenticated = false
			case "other-owner":
				c.From = "guest@example.test"
			case "other-inbox":
				p.Delivery.InboxID = "elsewhere"
			case "other-thread":
				p.ThreadID = "elsewhere"
			case "shared":
				p.CC = []string{"guest@example.test"}
			case "inbound":
				p.Labels = []string{"inbound"}
			case "preview-forged":
				p.Body = "> " + approvalReferencePrefix + token + "\n\n" + approvalReferencePrefix + "fedcba9876543210fedcba9876543210"
			case "wrong-token":
				c.RawBody = "Yes\n" + approvalReferencePrefix + "fedcba9876543210fedcba9876543210"
			}
			thread := []Message{p}
			if mode == "duplicate" {
				p.MessageID = "duplicate"
				thread = append(thread, p)
			}
			result, err := resolveControlReferences(c, thread)
			if mode == "duplicate" {
				if err == nil {
					t.Fatal("ambiguous reference accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "valid" {
				if len(result.References) != 1 || result.References[0] != "prompt" {
					t.Fatal("private prompt was not resolved")
				}
			} else if len(result.References) != 0 {
				t.Fatal("untrusted approval reference accepted")
			}
		})
	}
	if got := authoredControlBody(control); got != "Yes" {
		t.Fatalf("quoted control body: %q", got)
	}
	control.RawBody = "Yes\n\n" + approvalReferencePrefix + token
	if got := authoredControlBody(control); got != "Yes" {
		t.Fatalf("copied control body: %q", got)
	}
}

func TestParticipantApprovalWithAuthenticatedQuotedReference(t *testing.T) {
	rig, raw, router, pair, inbox := newParticipantTestRig(t)
	establishParticipantThread(t, rig, raw, router, pair, inbox, "thread")
	guest := Message{MessageID: "guest-request", ThreadID: "thread", From: "guest@example.test", To: []string{inbox.Address, pair.UserEmail}, Body: "Execute only after approval", Timestamp: time.Now().UTC()}
	raw.setThread("thread", append(raw.thread("thread"), guest))
	raw.setPoll([]Message{guest})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	replies := raw.sentReplies()
	prompt := replies[len(replies)-1]
	thread := raw.thread("thread")
	found := false
	for i := range thread {
		if thread[i].MessageID == prompt.ReceiptID {
			thread[i].RFCMessageID = "<original@receiver.test>"
			thread[i].RawBody = approvalReferencePrefix + "0123456789abcdef0123456789abcdef"
			thread[i].Delivery = MessageDelivery{InboxID: inbox.ProviderID, Recipient: inbox.Address}
			found = true
		}
	}
	if !found {
		t.Fatal("approval prompt absent")
	}
	control := Message{MessageID: "approval", RFCMessageID: "<approval@sender.test>", ThreadID: "thread", From: pair.UserEmail, To: []string{inbox.Address}, Body: "Yes\n\n> " + approvalReferencePrefix + "0123456789abcdef0123456789abcdef", InReplyTo: "<rewritten@relay.test>", Timestamp: guest.Timestamp.Add(time.Minute)}
	raw.setThread("thread", append(thread, control))
	raw.setPoll([]Message{control})
	router.lastPoll = time.Time{}
	mustProcess(t, rig)
	request, ok, err := rig.store.ParticipantRequestByMessage(guest.MessageID)
	if err != nil || !ok || request.State != participantResolvedYes {
		t.Fatalf("approval did not authorize exact request: state=%s err=%v", request.State, err)
	}
	if rig.capture("count") != "2" {
		t.Fatal("approval did not release exactly one guest execution")
	}
	if _, err = rig.app.participantControlReferences(context.Background(), control); err != nil {
		t.Fatal(err)
	}
}
