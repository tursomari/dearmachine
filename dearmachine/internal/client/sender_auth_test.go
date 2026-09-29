package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
	"github.com/emersion/go-msgauth/dkim"
)

func signedMailFixture(t *testing.T, message Message, domain string, signedHeaders []string, extraHeaders ...string) ([]byte, func(context.Context, string) ([]string, error)) {
	t.Helper()
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nMessage-ID: %s\r\nSubject: %s\r\nDate: Sat, 12 Sep 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n", message.From, strings.Join(message.To, ", "), message.MessageID, message.Subject)
	if len(message.CC) > 0 {
		raw += "Cc: " + strings.Join(message.CC, ", ") + "\r\n"
	}
	if message.InReplyTo != "" {
		raw += "In-Reply-To: " + message.InReplyTo + "\r\n"
	}
	if len(message.References) > 0 {
		raw += "References: " + strings.Join(message.References, " ") + "\r\n"
	}
	for _, header := range extraHeaders {
		raw += header + "\r\n"
	}
	raw += "\r\n" + message.Body + "\r\n"
	return signedRawFixture(t, raw, domain, signedHeaders)
}

func signedRawFixture(t *testing.T, raw, domain string, signedHeaders []string) ([]byte, func(context.Context, string) ([]string, error)) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var signed bytes.Buffer
	if err := dkim.Sign(&signed, strings.NewReader(raw), &dkim.SignOptions{Domain: domain, Selector: "fixture", Signer: private, HeaderKeys: signedHeaders}); err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, name string) ([]string, error) {
		if name != "fixture._domainkey."+domain {
			return nil, errors.New("unknown fixture signing key")
		}
		return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(public)}, nil
	}
	return signed.Bytes(), lookup
}

func signedFixtureMessage() Message {
	return Message{MessageID: "<request@sender.test>", ThreadID: "thread", From: "owner@sender.test",
		To: []string{"machine@receiver.test"}, CC: []string{"guest@other.test"}, Subject: "A signed request", Body: "Handle this request",
		Delivery:  MessageDelivery{InboxID: "inbox", Recipient: "machine@receiver.test"},
		InReplyTo: "<parent@receiver.test>", References: []string{"<root@receiver.test>", "<parent@receiver.test>"}}
}

func TestSenderSignatureBindsAuthorRecipientsAndApprovalCorrelation(t *testing.T) {
	for _, mode := range []string{"valid", "valid-content-headers", "spoof-from", "unsigned-cc", "unsigned-parent", "unsigned-mime", "unsigned-content-disposition", "unsigned-content-length", "body-tampering", "wrong-domain", "subdomain", "duplicate-from", "duplicate-to", "changed-api-cc", "changed-api-parent", "changed-api-id", "fake-authentication-results", "missing-signature", "dns-failure"} {
		t.Run(mode, func(t *testing.T) {
			message := signedFixtureMessage()
			domain := "sender.test"
			var headers []string
			if strings.HasPrefix(mode, "unsigned-") {
				headers = []string{"From", "To", "Message-ID", "Subject", "Date", "MIME-Version", "Content-Type", "Cc", "In-Reply-To", "References"}
				omit := map[string]string{"unsigned-cc": "Cc", "unsigned-parent": "In-Reply-To", "unsigned-mime": "Content-Type"}[mode]
				kept := headers[:0]
				for _, field := range headers {
					if field != omit {
						kept = append(kept, field)
					}
				}
				headers = kept
			}
			if mode == "wrong-domain" {
				domain = "attacker.test"
			} else if mode == "subdomain" {
				domain = "sub.sender.test"
			}
			var extraHeaders []string
			if mode == "valid-content-headers" {
				extraHeaders = []string{"Content-Disposition: inline", "Content-X-Extension: signed", fmt.Sprintf("Content-Length: %d", len(message.Body)+2)}
			}
			raw, lookup := signedMailFixture(t, message, domain, headers, extraHeaders...)
			switch mode {
			case "unsigned-content-disposition":
				raw = append([]byte("Content-Disposition: attachment; filename=changed.txt\r\n"), raw...)
			case "unsigned-content-length":
				raw = append([]byte("Content-Length: 0\r\n"), raw...)
			case "spoof-from":
				raw = bytes.ReplaceAll(raw, []byte("owner@sender.test"), []byte("other@sender.test"))
				message.From = "other@sender.test"
			case "body-tampering":
				raw = bytes.ReplaceAll(raw, []byte(message.Body), []byte("Changed instruction"))
			case "duplicate-from":
				raw = append([]byte("From: owner@sender.test\r\n"), raw...)
			case "duplicate-to":
				raw = append([]byte("To: machine@receiver.test\r\n"), raw...)
			case "changed-api-cc":
				message.CC = []string{"attacker@other.test"}
			case "changed-api-parent":
				message.InReplyTo = "<another-approval@receiver.test>"
			case "changed-api-id":
				message.MessageID = "<replayed-as-new@sender.test>"
			case "fake-authentication-results", "missing-signature":
				// An attacker-controlled verdict is no substitute for a signature.
				raw = regexp.MustCompile(`(?im)^DKIM-Signature:[^\r\n]*\r\n(?:[ \t][^\r\n]*\r\n)*`).ReplaceAll(raw, nil)
				if mode == "fake-authentication-results" {
					raw = append([]byte("Authentication-Results: receiver.test; dkim=pass; dmarc=pass\r\n"), raw...)
				}
			case "dns-failure":
				lookup = func(context.Context, string) ([]string, error) { return nil, errors.New("DNS unavailable") }
			}
			err := verifySignedMessage(context.Background(), raw, message, lookup)
			if (err == nil) != (mode == "valid" || mode == "valid-content-headers") {
				t.Fatalf("signature acceptance for %s: %v", mode, err)
			}
		})
	}
}

func TestAgentMailAuthenticationUsesScopedRawBytesAndCachesOnlyExactContent(t *testing.T) {
	message := signedFixtureMessage()
	raw, lookup := signedMailFixture(t, message, "sender.test", nil)
	message.RawBody = message.Body + "\r\n"
	var endpoint *httptest.Server
	requests := 0
	endpoint = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/blob" {
			if r.Header.Get("Authorization") != "" {
				t.Error("API credential forwarded to the blob host")
			}
			w.Write(raw)
			return
		}
		if r.URL.Path != "/v0/inboxes/inbox/messages/"+message.MessageID+"/raw" {
			t.Errorf("wrong evidence scope: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"message_id": message.MessageID, "size": len(raw), "download_url": endpoint.URL + "/blob?credential=private-download-reference"})
	}))
	defer endpoint.Close()
	m, err := NewMailbox(agentmail.NewClient(option.WithBaseURL(endpoint.URL), option.WithAPIKey("fixture-credential"), option.WithHTTPClient(endpoint.Client())), "inbox")
	if err != nil {
		t.Fatal(err)
	}
	m.authHTTPClient, m.authLookupTXT = endpoint.Client(), lookup
	for i := 0; i < 2; i++ {
		if err := m.AuthenticateMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 2 {
		t.Fatalf("authentication cache repeated network calls: %d", requests)
	}
	message.Body = "Changed provider content under the same ID"
	if !errors.Is(m.AuthenticateMessage(context.Background(), message), ErrMessageUnauthenticated) {
		t.Fatal("cached signature authenticated substituted normalized content")
	}
	message = signedFixtureMessage()
	message.Delivery.InboxID = "another-inbox"
	if !errors.Is(m.AuthenticateMessage(context.Background(), message), ErrMessageUnauthenticated) {
		t.Fatal("cached evidence crossed inbox scope")
	}
}

func TestUnsupportedProviderAuthenticationFailsClosed(t *testing.T) {
	for _, adapter := range []MessageAuthenticator{&SendmuxTransport{}} {
		if err := adapter.AuthenticateMessage(context.Background(), signedFixtureMessage()); !errors.Is(err, ErrSenderAttributionUnsupported) {
			t.Fatalf("unsupported evidence was accepted: %v", err)
		}
	}
}

func TestAgentMailAuthenticationRejectsBadRawEvidenceWithoutLeakingDownloadURL(t *testing.T) {
	for _, mode := range []string{"wrong-id", "oversize", "empty", "http-url", "url-userinfo", "wrong-size", "body-tampered", "blob-error", "redirect-http", "api-error"} {
		t.Run(mode, func(t *testing.T) {
			message := signedFixtureMessage()
			raw, lookup := signedMailFixture(t, message, "sender.test", nil)
			const secretReference = "private-download-reference"
			var endpoint *httptest.Server
			endpoint = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/blob" {
					switch mode {
					case "blob-error":
						http.Error(w, secretReference, http.StatusForbidden)
						return
					case "redirect-http":
						http.Redirect(w, r, "http://invalid.test/"+secretReference, http.StatusFound)
						return
					case "body-tampered":
						raw = bytes.ReplaceAll(raw, []byte(message.Body), []byte("Changed body"))
					}
					w.Write(raw)
					return
				}
				if mode == "api-error" {
					http.Error(w, secretReference, http.StatusForbidden)
					return
				}
				id, size, downloadURL := message.MessageID, len(raw), endpoint.URL+"/blob?secret="+secretReference
				switch mode {
				case "wrong-id":
					id = "<other@sender.test>"
				case "oversize":
					size = maxAuthenticationMessageBytes + 1
				case "empty":
					size = 0
				case "wrong-size":
					size++
				case "http-url":
					downloadURL = "http://invalid.test/" + secretReference
				case "url-userinfo":
					downloadURL = "https://user:" + secretReference + "@invalid.test/blob"
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"message_id": id, "size": size, "download_url": downloadURL})
			}))
			defer endpoint.Close()
			m, err := NewMailbox(agentmail.NewClient(option.WithBaseURL(endpoint.URL), option.WithAPIKey("fixture"), option.WithHTTPClient(endpoint.Client())), "inbox")
			if err != nil {
				t.Fatal(err)
			}
			m.authHTTPClient, m.authLookupTXT = endpoint.Client(), lookup
			err = m.AuthenticateMessage(context.Background(), message)
			if err == nil {
				t.Fatal("invalid raw evidence was accepted")
			}
			if strings.Contains(err.Error(), secretReference) || strings.Contains(err.Error(), endpoint.URL) {
				t.Fatal("authentication error exposed raw download credentials")
			}
			if len(m.authenticated) != 0 {
				t.Fatal("failed authentication was positively cached")
			}
		})
	}
}

func TestSenderAuthenticationDNSOutageIsNotAnIdentityRejection(t *testing.T) {
	m := signedFixtureMessage()
	raw, _ := signedMailFixture(t, m, "sender.test", authenticatedHeaderFields)
	err := verifySignedMessage(context.Background(), raw, m, func(context.Context, string) ([]string, error) { return nil, context.DeadlineExceeded })
	if err == nil || errors.Is(err, ErrMessageUnauthenticated) || !strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatalf("DNS outage reported as spoofed sender: %v", err)
	}
}
