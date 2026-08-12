package client

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestFakeTransportContractPollAndThreadReturnNormalizedMessages(t *testing.T) {
	transport := newFakeTransportFixture()
	messages, err := transport.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Timestamp.IsZero() == false ||
		messages[0].CreatedAt.IsZero() || messages[0].Body == "" ||
		len(messages[0].Attachments) != 1 ||
		messages[0].Attachments[0].AttachmentID != "attachment-1" {
		t.Fatalf("Poll = %+v", messages)
	}
	thread, err := transport.Thread(context.Background(), "thread-1")
	if err != nil || len(thread) != 1 || thread[0].MessageID != "inbound-1" {
		t.Fatalf("Thread = %+v, %v", thread, err)
	}
}

func TestFakeTransportContractReplyPreservesIdempotencyKey(t *testing.T) {
	transport := newFakeTransportFixture()
	receiptID, err := transport.Reply(
		context.Background(),
		"inbound-1",
		"completed",
		"idempotency-1",
	)
	if err != nil || receiptID != "outbound-1" {
		t.Fatalf("Reply = %q, %v", receiptID, err)
	}
	if len(transport.replies) != 1 ||
		transport.replies[0].IdempotencyKey != "idempotency-1" ||
		transport.replies[0].Text != "completed" {
		t.Fatalf("reply log = %+v", transport.replies)
	}
}

func TestFakeTransportContractReplyReceiptFindsFirstOutboundMatch(t *testing.T) {
	transport := newFakeTransportFixture()
	inbound, err := transport.Message(context.Background(), "inbound-1")
	if err != nil {
		t.Fatal(err)
	}
	transport.setThread("thread-1", []Message{
		inbound,
		{MessageID: "unrelated", ThreadID: "thread-1", InReplyTo: "other", Labels: []string{"sent"}},
		{MessageID: "first", ThreadID: "thread-1", InReplyTo: "inbound-1", Labels: []string{"sent"}},
		{MessageID: "second", ThreadID: "thread-1", InReplyTo: "inbound-1", To: []string{inbound.From}},
	})

	receiptID, found, err := transport.ReplyReceipt(context.Background(), inbound)
	if err != nil || !found || receiptID != "first" {
		t.Fatalf("ReplyReceipt = %q, %v, %v", receiptID, found, err)
	}
}

func TestFakeTransportContractMarkProcessedAppliesReadState(t *testing.T) {
	transport := newFakeTransportFixture()
	if err := transport.MarkProcessed(context.Background(), "inbound-1"); err != nil {
		t.Fatal(err)
	}
	message, err := transport.Message(context.Background(), "inbound-1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(transport.processed, []string{"inbound-1"}) ||
		containsFold(message.Labels, "unread") || !containsFold(message.Labels, "read") {
		t.Fatalf("processed = %v, labels = %v", transport.processed, message.Labels)
	}
}

func TestFakeTransportContractFetchAttachmentReturnsBytesAndErrors(t *testing.T) {
	transport := newFakeTransportFixture()
	contents, err := transport.FetchAttachment(context.Background(), "attachment-1")
	if err != nil || string(contents) != "payload" {
		t.Fatalf("FetchAttachment = %q, %v", contents, err)
	}

	want := errors.New("injected attachment failure")
	transport.attachmentErrors["attachment-2"] = want
	if _, err := transport.FetchAttachment(context.Background(), "attachment-2"); !errors.Is(err, want) {
		t.Fatalf("FetchAttachment error = %v, want %v", err, want)
	}
}
