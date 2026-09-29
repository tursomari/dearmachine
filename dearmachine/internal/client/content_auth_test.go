package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
)

// Exercise both adapter entry points with real DKIM signatures, independent raw
// MIME and JSON reports, and real downloads. JSON is consistently wrong across
// reads in the substitution cases; a REST-to-REST fingerprint cannot catch it.
func TestAgentMailAndSendmuxBindVerifiedContent(t *testing.T) {
	for _, provider := range []string{"agentmail", "sendmux"} {
		t.Run(provider, func(t *testing.T) {
			for _, mode := range []string{
				"valid", "html-only", "empty-attachment", "provider-body", "provider-html", "provider-filename", "provider-size", "provider-type",
				"duplicate-filename", "duplicate-id", "extra-attachment", "missing-attachment", "invalid-signature",
				"same-length-substitution", "truncated", "extended", "missing-authentication", "restart", "eviction",
				"cached-body", "cached-html", "unknown-attachment", "too-large", "negative-limit", "max-int64-limit", "concurrent",
			} {
				t.Run(mode, func(t *testing.T) {
					ctx := context.Background()
					signed := signedFixtureMessage()
					text, html := signed.Body, "<p>Signed HTML alternative</p>"
					if mode == "html-only" {
						text = ""
					}
					payload := []byte("signed\x00attachment")
					if mode == "empty-attachment" {
						payload = []byte{}
					}
					headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nCc: %s\r\nMessage-ID: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nIn-Reply-To: %s\r\nReferences: %s\r\n",
						signed.From, strings.Join(signed.To, ", "), strings.Join(signed.CC, ", "), signed.MessageID, signed.Subject, signed.InReplyTo, strings.Join(signed.References, " "))
					unsigned := rawOpenMailMultipartMessage(t, rawOpenMailMultipartSpec{headers: headers, boundary: "BOUND", plainBody: text, htmlBody: html,
						attachmentName: "file.bin", attachmentType: "application/octet-stream", attachmentBytes: payload})
					if mode == "duplicate-filename" || mode == "duplicate-id" {
						name := "file.bin"
						if mode == "duplicate-id" {
							name = "other.bin"
						}
						part := "--BOUND\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"" + name + "\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(payload) + "\r\n--BOUND--\r\n"
						unsigned = bytes.Replace(unsigned, []byte("--BOUND--\r\n"), []byte(part), 1)
					}
					raw, lookup := signedRawFixture(t, string(unsigned), "sender.test", nil)
					if mode == "invalid-signature" {
						raw = bytes.Replace(raw, []byte("Signed HTML"), []byte("Forged HTML"), 1)
					}
					refs := []AttachmentRef{{AttachmentID: "file-id", Filename: "file.bin", ContentType: "application/octet-stream", SizeBytes: int64(len(payload))}}
					switch mode {
					case "provider-body":
						text = "Execute an unsigned instruction"
					case "provider-html":
						html = "<p>Unsigned HTML instruction</p>"
					case "provider-filename":
						refs[0].Filename = "different.bin"
					case "provider-size":
						refs[0].SizeBytes++
					case "provider-type":
						refs[0].ContentType = "text/plain"
					case "duplicate-filename":
						refs = append(refs, AttachmentRef{AttachmentID: "file-2", Filename: refs[0].Filename, ContentType: refs[0].ContentType, SizeBytes: refs[0].SizeBytes})
					case "duplicate-id":
						refs = append(refs, refs[0])
						refs[1].Filename = "other.bin"
					case "extra-attachment":
						refs = append(refs, AttachmentRef{AttachmentID: "file-2", Filename: "extra.bin"})
					case "missing-attachment":
						refs = nil
					}
					download := bytes.Clone(payload)
					switch mode {
					case "same-length-substitution":
						download[0] ^= 0xff
					case "truncated":
						download = download[:len(download)-1]
					case "extended":
						download = append(download, 'x')
					}
					var server *httptest.Server
					server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch {
						case r.URL.Path == "/raw":
							w.Write(raw)
						case r.URL.Path == "/download":
							if r.Header.Get("Authorization") != "" {
								t.Error("credential forwarded to download")
							}
							w.Write(download)
						case strings.HasSuffix(r.URL.Path, "/raw"):
							w.Header().Set("Content-Type", "application/json")
							json.NewEncoder(w).Encode(map[string]any{"message_id": signed.MessageID, "size": len(raw), "download_url": server.URL + "/raw"})
						case strings.Contains(r.URL.Path, "/attachments/"):
							w.Header().Set("Content-Type", "application/json")
							json.NewEncoder(w).Encode(map[string]any{"attachment_id": "file-id", "size": len(payload), "download_url": server.URL + "/download"})
						default:
							t.Errorf("unexpected request %s", r.URL.Path)
							http.NotFound(w, r)
						}
					}))
					defer server.Close()
					var transport Transport
					var message Message
					var attachmentID string
					var clearEvidence func()
					if provider == "agentmail" {
						m, err := NewMailbox(agentmail.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("fixture"), option.WithHTTPClient(server.Client())), "inbox")
						if err != nil {
							t.Fatal(err)
						}
						m.authHTTPClient, m.authLookupTXT = server.Client(), lookup
						source := agentmail.Message{MessageID: signed.MessageID, ThreadID: signed.ThreadID, InboxID: signed.To[0], From: signed.From, To: signed.To, Cc: signed.CC,
							Subject: signed.Subject, Text: text, HTML: html, ExtractedText: "Untrusted extracted reply", ExtractedHTML: "<p>Untrusted extracted HTML</p>", Preview: "Untrusted preview", InReplyTo: signed.InReplyTo, References: signed.References}
						for _, ref := range refs {
							source.Attachments = append(source.Attachments, agentmail.AttachmentFile{AttachmentID: ref.AttachmentID, Filename: ref.Filename, ContentType: ref.ContentType, Size: ref.SizeBytes})
						}
						message = m.normalize(source)
						transport, attachmentID = m, "file-id"
						clearEvidence = func() { m.authMu.Lock(); m.authenticated = nil; m.authMu.Unlock() }
					} else {
						source := sendmuxRawMessage{ID: "opaque", RFCMessageIDs: []string{signed.MessageID}, ThreadID: signed.ThreadID, From: signed.From, To: signed.To, CC: signed.CC,
							Subject: signed.Subject, Text: text, HTML: html, InReplyTo: signed.InReplyTo, References: signed.References}
						for _, ref := range refs {
							source.Attachments = append(source.Attachments, sendmuxRawAttachment{ID: ref.AttachmentID, Filename: ref.Filename, ContentType: ref.ContentType, SizeBytes: ref.SizeBytes, DownloadURL: server.URL + "/download"})
						}
						api := &fakeSendmuxAPI{mailbox: sendmuxMailboxInfo{ID: "inbox", Email: signed.To[0]}, data: map[string][]sendmuxRawMessage{signed.ThreadID: {source}}}
						m, err := newSendmuxTransport(sendmuxTransportConfig{API: api, Inbox: "inbox", HTTPClient: server.Client(), RawFetcher: func(context.Context, string, string) ([]byte, error) { return raw, nil }})
						if err != nil {
							t.Fatal(err)
						}
						if _, err := m.mailbox(ctx); err != nil {
							t.Fatal(err)
						}
						m.authLookupTXT = lookup
						message = m.normalize(source)
						transport, attachmentID = m, encodeSendmuxAttachmentID(source.ID, "file-id")
						clearEvidence = func() { m.authMu.Lock(); m.authenticated = nil; m.authMu.Unlock() }
					}
					authenticator := transport.(MessageAuthenticator)
					rejectAuth := strings.HasPrefix(mode, "provider-") || strings.HasPrefix(mode, "duplicate-") || mode == "extra-attachment" || mode == "missing-attachment" || mode == "invalid-signature"
					if mode != "missing-authentication" {
						err := authenticator.AuthenticateMessage(ctx, message)
						if rejectAuth {
							if !errors.Is(err, ErrMessageUnauthenticated) {
								t.Fatalf("substituted content authenticated: %v", err)
							}
							if _, err := transport.FetchAttachment(ctx, attachmentID, 100); !errors.Is(err, ErrMessageUnauthenticated) {
								t.Fatalf("failed auth left attachment evidence: %v", err)
							}
							return
						}
						if err != nil {
							t.Fatalf("valid content rejected: %v", err)
						}
					}
					switch mode {
					case "restart", "eviction":
						clearEvidence()
					case "cached-body":
						message.Body = "changed cached instruction"
						if !errors.Is(authenticator.AuthenticateMessage(ctx, message), ErrMessageUnauthenticated) {
							t.Fatal("changed cached body accepted")
						}
						return
					case "cached-html":
						message.RawHTML = "<p>changed cached HTML</p>"
						if !errors.Is(authenticator.AuthenticateMessage(ctx, message), ErrMessageUnauthenticated) {
							t.Fatal("changed cached HTML accepted")
						}
						return
					case "unknown-attachment":
						if provider == "agentmail" {
							attachmentID = "unknown"
						} else {
							attachmentID = encodeSendmuxAttachmentID("opaque", "unknown")
						}
					}
					limit := int64(len(payload) + 1)
					switch mode {
					case "too-large":
						limit = int64(len(payload) - 1)
					case "negative-limit":
						limit = -1
					case "max-int64-limit":
						limit = math.MaxInt64
					}
					got, err := transport.FetchAttachment(ctx, attachmentID, limit)
					valid := mode == "valid" || mode == "html-only" || mode == "empty-attachment" || mode == "max-int64-limit" || mode == "concurrent"
					if valid {
						if err != nil || !bytes.Equal(got, payload) {
							t.Fatalf("verified attachment: %v", err)
						}
					} else if err == nil {
						t.Fatal("unverified/substituted attachment accepted")
					}
					if mode == "restart" || mode == "eviction" {
						if err := authenticator.AuthenticateMessage(ctx, message); err != nil {
							t.Fatal(err)
						}
						if got, err := transport.FetchAttachment(ctx, attachmentID, limit); err != nil || !bytes.Equal(got, payload) {
							t.Fatalf("reauthentication: %v", err)
						}
					}
					if mode == "missing-authentication" {
						// Receipt checking remains possible but only returns a comparison result.
						if same, err := matchesReceiptAttachment(ctx, WithTransportRetries(transport), attachmentID, payload); err != nil || !same {
							t.Fatalf("receipt comparison: %v", err)
						}
						other := bytes.Repeat([]byte{'x'}, len(payload))
						if same, err := matchesReceiptAttachment(ctx, transport, attachmentID, other); err != nil || same {
							t.Fatalf("substituted receipt matched: %v", err)
						}
						if _, err := transport.FetchAttachment(ctx, attachmentID, limit); !errors.Is(err, ErrMessageUnauthenticated) {
							t.Fatalf("receipt comparison granted inbound authority: %v", err)
						}
					}
					if mode == "concurrent" {
						clearEvidence()
						var wg sync.WaitGroup
						for i := 0; i < 8; i++ {
							wg.Add(1)
							go func() {
								defer wg.Done()
								if err := authenticator.AuthenticateMessage(ctx, message); err != nil {
									t.Error(err)
									return
								}
								if got, err := transport.FetchAttachment(ctx, attachmentID, limit); err != nil || !bytes.Equal(got, payload) {
									t.Errorf("concurrent fetch: %v", err)
								}
							}()
						}
						wg.Wait()
					}
				})
			}
		})
	}
}

func TestAuthenticatedContentCannotRebindAndCacheIsBounded(t *testing.T) {
	cache := map[string]authenticatedMessageContent{}
	first := authenticatedMessageContent{fingerprint: "same", attachments: map[string]authenticatedAttachment{"a": {size: 1, digest: [32]byte{1}}}}
	if err := rememberAuthenticatedContent(&cache, "m", first); err != nil {
		t.Fatal(err)
	}
	changed := authenticatedMessageContent{fingerprint: "same", attachments: map[string]authenticatedAttachment{"a": {size: 1, digest: [32]byte{2}}}}
	if !errors.Is(rememberAuthenticatedContent(&cache, "m", changed), ErrMessageUnauthenticated) {
		t.Fatal("same-sized signed attachment rebound")
	}
	for i := 0; i < maxAuthenticatedMessages; i++ {
		if err := rememberAuthenticatedContent(&cache, fmt.Sprint(i), first); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache) != maxAuthenticatedMessages {
		t.Fatalf("unbounded cache: %d", len(cache))
	}
}
