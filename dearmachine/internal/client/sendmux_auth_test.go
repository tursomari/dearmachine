package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sendmux.ai/go/mailbox"
)

func TestSendmuxFullHeadersPreserveSignedReplyIDs(t *testing.T) {
	content := mailbox.MailboxMessageContentResponseData{Headers: mailbox.MailboxContentHeaders{Full: []mailbox.MailboxContentHeadersFullItem{
		{Name: "Message-ID", Value: " \t<request@sender.test>"},
		{Name: "In-Reply-To", Value: " <prompt@receiver.test> "},
		{Name: "References", Value: " <root@receiver.test>\r\n\t<prompt@receiver.test> "},
	}}}
	raw := sendmuxRawFromSDK(mailbox.MailboxMessage{FolderIds: []string{"folder-inbox"}}, content)
	if sendmuxRFCMessageID(raw) != "<request@sender.test>" || raw.InReplyTo != "<prompt@receiver.test>" ||
		strings.Join(raw.References, " ") != "<root@receiver.test> <prompt@receiver.test>" || len(raw.FolderIDs) != 1 {
		t.Fatalf("full header mapping: %+v", raw)
	}
	content.Headers.Full = append(content.Headers.Full, mailbox.MailboxContentHeadersFullItem{Name: "message-id", Value: "<duplicate@sender.test>"})
	if sendmuxRFCMessageID(sendmuxRawFromSDK(mailbox.MailboxMessage{}, content)) != "" {
		t.Fatal("duplicate Message-ID accepted")
	}
}

func TestSendmuxMultipartAuthenticationProtectsAttachmentBytes(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(fmt.Sprint(tampered), func(t *testing.T) {
			message := signedFixtureMessage()
			message.CC, message.InReplyTo, message.References = nil, "", nil
			wire := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=fixture\r\n\r\n--fixture\r\nContent-Type: text/plain\r\n\r\n%s\r\n--fixture\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=fixture.txt\r\nContent-Transfer-Encoding: base64\r\n\r\nc3ludGhldGlj\r\n--fixture--\r\n", message.From, message.To[0], message.Subject, message.MessageID, message.Body)
			raw, lookup := signedRawFixture(t, wire, "sender.test", nil)
			if tampered {
				raw = bytes.ReplaceAll(raw, []byte("c3ludGhldGlj"), []byte("YXR0YWNrZXI="))
			}
			api := &fakeSendmuxAPI{mailbox: sendmuxMailboxInfo{ID: "inbox", Email: message.To[0]}}
			source := sendmuxRawMessage{ID: "opaque", ThreadID: message.ThreadID, From: message.From, To: message.To, Subject: message.Subject, Text: message.Body,
				RFCMessageIDs: []string{message.MessageID}, Attachments: []sendmuxRawAttachment{{ID: "attachment", Filename: "fixture.txt", SizeBytes: 9, ContentType: "application/octet-stream"}}}
			api.data = map[string][]sendmuxRawMessage{message.ThreadID: {source}}
			transport, err := newSendmuxTransport(sendmuxTransportConfig{API: api, Inbox: "inbox", RawFetcher: func(ctx context.Context, address, id string) ([]byte, error) {
				conn, done := sendmuxIMAPFixture(t, "valid", raw, id)
				defer func() { <-done }()
				return fetchSendmuxIMAP(ctx, conn, address, "fixture-credential", id)
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := transport.mailbox(context.Background()); err != nil {
				t.Fatal(err)
			}
			transport.authLookupTXT = lookup
			err = transport.AuthenticateMessage(context.Background(), transport.normalize(source))
			if (err == nil) == tampered {
				t.Fatalf("attachment tampered=%v: %v", tampered, err)
			}
		})
	}
}

func TestSendmuxForgedInboxFromCannotBecomeSentMail(t *testing.T) {
	transport := &SendmuxTransport{resolved: sendmuxMailboxInfo{ID: "inbox", Email: "machine@receiver.test", SentFolderIDs: []string{"folder-sent"}}}
	for _, folders := range [][]string{nil, {"folder-inbox"}, {"folder-sent"}} {
		message := transport.normalize(sendmuxRawMessage{From: "machine@receiver.test", FolderIDs: folders, Keywords: []string{"sent", "outbound"}})
		want := sendmuxInSentFolder(folders, transport.resolved.SentFolderIDs)
		if containsFold(message.Labels, "sent") != want || containsFold(message.Labels, "outbound") != want {
			t.Fatal("From or keyword bypassed Sent-folder evidence")
		}
	}
}

func TestSendmuxAuthenticationBindsRESTRecordToSignedIMAPMessage(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-inbox", "hidden-recipient", "wrong-rest-id", "ambiguous-rfc-id", "missing-rfc-id", "changed-body", "changed-thread", "changed-attachment", "wrong-imap-id", "changed-cc", "tampered-body", "provider-error", "imap-error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			signed := signedFixtureMessage()
			raw, lookup := signedMailFixture(t, signed, "sender.test", nil)
			source := sendmuxRawMessage{ID: "opaque-request", RFCMessageIDs: []string{" \t" + signed.MessageID}, ThreadID: signed.ThreadID,
				From: signed.From, To: signed.To, CC: signed.CC, Subject: signed.Subject, Text: signed.Body + "\r\n",
				InReplyTo: signed.InReplyTo, References: signed.References}
			api := &fakeSendmuxAPI{mailbox: sendmuxMailboxInfo{ID: "inbox", Email: signed.Delivery.Recipient}, data: map[string][]sendmuxRawMessage{signed.ThreadID: {source}}}
			calls := 0
			transport, err := newSendmuxTransport(sendmuxTransportConfig{API: api, Inbox: "inbox", RawFetcher: func(ctx context.Context, address, id string) ([]byte, error) {
				calls++
				if address != signed.Delivery.Recipient || id != signed.MessageID {
					t.Error("IMAP evidence crossed mailbox or RFC ID scope")
				}
				if mode == "imap-error" {
					return nil, errors.New("private IMAP response")
				}
				if mode == "cancelled" {
					return nil, ctx.Err()
				}
				return raw, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := transport.mailbox(context.Background()); err != nil {
				t.Fatal(err)
			}
			transport.authLookupTXT = lookup
			message := transport.normalize(source)
			switch mode {
			case "wrong-inbox":
				message.Delivery.InboxID = "other"
			case "hidden-recipient":
				message.To = nil
			case "wrong-rest-id":
				source.ID = "other"
			case "ambiguous-rfc-id":
				source.RFCMessageIDs = append(source.RFCMessageIDs, signed.MessageID)
			case "missing-rfc-id":
				source.RFCMessageIDs = nil
			case "changed-body":
				source.Text = "different request"
			case "changed-thread":
				source.ThreadID = "other"
			case "changed-attachment":
				source.Attachments = []sendmuxRawAttachment{{ID: "injected", Filename: "instructions.txt"}}
			case "wrong-imap-id":
				raw = bytes.ReplaceAll(raw, []byte(signed.MessageID), []byte("<other@sender.test>"))
			case "changed-cc":
				raw = bytes.ReplaceAll(raw, []byte(signed.CC[0]), []byte("attacker@other.test"))
			case "tampered-body":
				raw = bytes.ReplaceAll(raw, []byte(signed.Body), []byte("different request"))
			case "provider-error":
				api.data = nil
			}
			if mode != "provider-error" {
				api.data[signed.ThreadID] = []sendmuxRawMessage{source}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			err = transport.AuthenticateMessage(ctx, message)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("authentication %s: %v", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("leaked provider error")
			}
			if mode != "valid" {
				return
			}
			if err := transport.AuthenticateMessage(ctx, message); err != nil || calls != 1 {
				t.Fatalf("exact fingerprint cache: %v, calls %d", err, calls)
			}
			message.Body = "changed cached request"
			if !errors.Is(transport.AuthenticateMessage(ctx, message), ErrMessageUnauthenticated) {
				t.Fatal("cache accepted changed request")
			}
		})
	}
}

func TestSendmuxRFCMessageIDsRemainSeparateFromProviderIDs(t *testing.T) {
	transport := &SendmuxTransport{resolved: sendmuxMailboxInfo{ID: "inbox", Email: "machine@receiver.test", SentFolderIDs: []string{"folder-sent"}}}
	prompt := transport.normalize(sendmuxRawMessage{ID: "opaque-prompt", FolderIDs: []string{"folder-sent"}, ThreadID: "thread", From: "machine@receiver.test", RFCMessageIDs: []string{" \t<prompt@receiver.test> "}})
	approval := transport.normalize(sendmuxRawMessage{ID: "opaque-approval", ThreadID: "thread", From: "owner@sender.test", To: []string{"machine@receiver.test"}, RFCMessageIDs: []string{"<approval@sender.test>"}, InReplyTo: "<prompt@receiver.test>"})
	mapped, err := resolveControlReferences(approval, []Message{prompt, approval})
	if err != nil || mapped.InReplyTo != prompt.MessageID || mapped.MessageID != "opaque-approval" || approval.InReplyTo != "<prompt@receiver.test>" {
		t.Fatalf("signed approval reference did not resolve exact provider receipt: %+v, %v", mapped, err)
	}
	for _, ids := range [][]string{nil, {""}, {"opaque-id"}, {"<a@test>", "<b@test>"}, {"<a@test>\r\nInjected: yes"}} {
		if sendmuxRFCMessageID(sendmuxRawMessage{RFCMessageIDs: ids}) != "" {
			t.Fatalf("accepted malformed/ambiguous RFC IDs: %q", ids)
		}
	}
}
