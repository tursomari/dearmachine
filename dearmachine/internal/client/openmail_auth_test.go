package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func openMailEvidenceFixture(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var headers [][]string
	for _, line := range strings.Split(strings.SplitN(string(raw), "\r\n\r\n", 2)[0], "\r\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			headers[len(headers)-1][1] += "\n" + line
		} else {
			name, value, ok := strings.Cut(line, ":")
			if !ok {
				t.Fatal("invalid fixture header")
			}
			headers = append(headers, []string{name, strings.TrimLeft(value, " \t")})
		}
	}
	list, err := json.Marshal(headers)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(map[string]string{"message-headers": string(list)})
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestOpenMailAuthenticationBindsSignedRecord(t *testing.T) {
	for _, mode := range []string{"valid-reply", "valid-7bit", "valid-8bit", "forged-from", "changed-to", "changed-cc", "changed-body", "changed-parent", "changed-references", "changed-rfc-id", "changed-polled-body", "changed-polled-thread", "wrong-inbox", "missing-inbox", "outbound", "missing-evidence", "fake-verdict", "duplicate-header", "injected-header", "unsigned-parent", "html", "attachment", "multipart", "quoted-printable", "dns-failure", "provider-failure"} {
		t.Run(mode, func(t *testing.T) {
			message := signedFixtureMessage()
			var keys []string
			if mode == "unsigned-parent" {
				keys = []string{"From", "To", "Cc", "Message-ID", "Subject", "Date", "MIME-Version", "Content-Type", "References"}
			}
			var extra []string
			if mode == "valid-7bit" {
				extra = []string{"Content-Transfer-Encoding: 7bit"}
			}
			if mode == "valid-8bit" {
				message.Body = "Signed UTF-8 café"
				extra = []string{"Content-Transfer-Encoding: 8bit"}
			}
			raw, lookup := signedMailFixture(t, message, "sender.test", keys, extra...)
			source := openMailMessage{ID: "opaque-provider-id", InboxID: "inbox", RFCMessageID: message.MessageID, ThreadID: message.ThreadID,
				Direction: "inbound", FromAddr: message.From, ToAddr: strings.Join(message.To, ", "), HeaderTo: strings.Join(message.To, ", "),
				CC: message.CC, Subject: message.Subject, BodyText: message.Body + "\n", InReplyTo: message.InReplyTo, References: message.References,
				Raw: openMailEvidenceFixture(t, raw)}
			switch mode {
			case "forged-from":
				source.FromAddr = "intruder@sender.test"
				source.Raw = openMailEvidenceFixture(t, []byte(strings.ReplaceAll(string(raw), message.From, source.FromAddr)))
			case "changed-to":
				source.HeaderTo += ", added@other.test"
			case "changed-cc":
				source.CC = []string{"added@other.test"}
			case "changed-body":
				source.BodyText += "Injected instruction"
			case "changed-parent":
				source.InReplyTo = "<other@receiver.test>"
			case "changed-references":
				source.References = []string{"<other@receiver.test>"}
			case "changed-rfc-id":
				source.RFCMessageID = "<other@sender.test>"
			case "wrong-inbox":
				source.InboxID = "different-inbox"
			case "missing-inbox":
				source.InboxID = ""
			case "outbound":
				source.Direction = "outbound"
			case "missing-evidence":
				source.Raw = nil
			case "fake-verdict":
				source.Raw = json.RawMessage(`{"message-headers":"[[\"Authentication-Results\",\"dkim=pass; spf=pass\"]]"}`)
			case "duplicate-header":
				source.Raw = openMailEvidenceFixture(t, append([]byte("From: "+message.From+"\r\n"), raw...))
			case "injected-header":
				h, _ := json.Marshal([][]string{{"From", message.From + "\nTo: injected@other.test"}})
				source.Raw, _ = json.Marshal(map[string]string{"message-headers": string(h)})
			case "html":
				source.BodyHTML = "<p>Another instruction</p>"
			case "attachment":
				source.Attachments = []openMailAttachment{{Filename: "payload.txt"}}
			case "multipart":
				source.Raw = openMailEvidenceFixture(t, []byte(strings.Replace(string(raw), "text/plain; charset=utf-8", "multipart/mixed; boundary=fixture", 1)))
			case "quoted-printable":
				source.Raw = openMailEvidenceFixture(t, append([]byte("Content-Transfer-Encoding: quoted-printable\r\n"), raw...))
			case "dns-failure":
				lookup = func(context.Context, string) ([]string, error) { return nil, errors.New("unavailable") }
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/inboxes/inbox/messages" {
					t.Error("unexpected authentication request")
					http.NotFound(w, r)
					return
				}
				if mode == "provider-failure" {
					http.Error(w, "private provider response", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(openMailList[openMailMessage]{Data: []openMailMessage{source}, Total: 1})
			}))
			defer server.Close()
			transport, err := newOpenMailTransport(openMailTransportConfig{BaseURL: server.URL, APIKey: "fixture-key", Inbox: "inbox", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			transport.resolvedInboxID = "inbox"
			transport.resolvedAddress = message.To[0]
			transport.authLookupTXT = lookup
			expected := transport.normalize(source, false)
			if mode == "changed-polled-body" {
				expected.Body += " stale altered content"
			}
			if mode == "changed-polled-thread" {
				expected.ThreadID = "other-thread"
			}
			err = transport.AuthenticateMessage(context.Background(), expected)
			if strings.HasPrefix(mode, "valid-") {
				if err != nil {
					t.Fatalf("signed reply rejected: %v", err)
				}
				if expected.MessageID != "opaque-provider-id" || expected.InReplyTo != message.InReplyTo {
					t.Fatal("provider ID or reply correlation lost")
				}
			} else if err == nil {
				t.Fatal("unsafe evidence accepted")
			}
			if err != nil && strings.Contains(err.Error(), "private provider") {
				t.Fatal("provider error leaked")
			}
		})
	}
}
