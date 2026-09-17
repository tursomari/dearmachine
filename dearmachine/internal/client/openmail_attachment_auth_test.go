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
)

func TestOpenMailVerifiedAttachmentDownload(t *testing.T) {
	for _, mode := range []string{
		"valid", "same-length-substitution", "truncated", "extended", "too-large", "negative-limit",
		"missing-authentication", "invalid-signature", "duplicate-filename", "metadata-changed",
		"restart", "eviction", "reauthentication", "concurrent", "signed-replacement",
		"quoted-printable-attachment", "7bit-attachment", "inline-related", "multiple-attachments",
		"swapped-attachments", "unknown-attachment", "empty-attachment", "max-int64-limit",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			message := signedFixtureMessage()
			payload := []byte{0, 1, 2, 127, 128, 254, 255, '\r', '\n'}
			if mode == "7bit-attachment" || mode == "quoted-printable-attachment" {
				payload = []byte("first\r\nsecond\r\n")
			} else if mode == "empty-attachment" {
				payload = []byte{}
			}
			headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nCc: %s\r\nMessage-ID: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nIn-Reply-To: %s\r\nReferences: %s\r\n",
				message.From, strings.Join(message.To, ", "), strings.Join(message.CC, ", "), message.MessageID, message.Subject,
				message.InReplyTo, strings.Join(message.References, " "))
			unsigned := rawOpenMailMultipartMessage(t, rawOpenMailMultipartSpec{
				headers: headers, boundary: "OUTER", plainBody: message.Body, htmlBody: "<p>HTML</p>",
				attachmentName: "file.bin", attachmentType: "application/octet-stream", attachmentBytes: payload,
			})
			source := openMailMessage{
				ID: "provider-id", InboxID: "inbox", RFCMessageID: message.MessageID, ThreadID: message.ThreadID,
				Direction: "inbound", FromAddr: message.From, ToAddr: strings.Join(message.To, ", "),
				CC: message.CC, Subject: message.Subject, BodyText: message.Body, BodyHTML: "<p>HTML</p>",
				InReplyTo: message.InReplyTo, References: message.References, RawURL: "present",
				Attachments: []openMailAttachment{{Filename: "file.bin", ContentType: "application/octet-stream", SizeBytes: int64(len(payload))}},
			}
			switch mode {
			case "7bit-attachment", "quoted-printable-attachment":
				encoding, wire := "7bit", string(payload)
				if mode == "quoted-printable-attachment" {
					encoding, wire = "quoted-printable", "fir=73t\r\nsecond\r\n"
				}
				unsigned = bytes.Replace(unsigned, []byte("Content-Transfer-Encoding: base64\r\n\r\n"+base64.StdEncoding.EncodeToString(payload)), []byte("Content-Transfer-Encoding: "+encoding+"\r\n\r\n"+wire), 1)
			case "inline-related":
				unsigned = bytes.Replace(unsigned, []byte("multipart/mixed"), []byte("multipart/related"), 1)
				unsigned = bytes.Replace(unsigned, []byte("Content-Disposition: attachment"), []byte("Content-Disposition: inline"), 1)
			}
			secondPayload := bytes.Repeat([]byte("X"), len(payload))
			if mode == "multiple-attachments" || mode == "swapped-attachments" {
				part := "--OUTER\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"other.bin\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(secondPayload) + "\r\n--OUTER--\r\n"
				unsigned = bytes.Replace(unsigned, []byte("--OUTER--\r\n"), []byte(part), 1)
				// Deliberately reverse JSON order; binding is by unique filename.
				source.Attachments = append([]openMailAttachment{{Filename: "other.bin", ContentType: "application/octet-stream", SizeBytes: int64(len(secondPayload))}}, source.Attachments...)
			}
			if mode == "duplicate-filename" {
				part := "--OUTER\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"file.bin\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(payload) + "\r\n--OUTER--\r\n"
				unsigned = bytes.Replace(unsigned, []byte("--OUTER--\r\n"), []byte(part), 1)
				source.Attachments = append(source.Attachments, source.Attachments[0])
			}
			raw, lookup := signedRawFixture(t, string(unsigned), "sender.test", nil)
			if mode == "invalid-signature" {
				raw = bytes.Replace(raw, []byte(base64.StdEncoding.EncodeToString(payload)), []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("X"), len(payload)))), 1)
			}
			download := bytes.Clone(payload)
			switch mode {
			case "same-length-substitution":
				download[0] ^= 0xff
			case "truncated":
				download = download[:len(download)-1]
			case "extended":
				download = append(download, 'x')
			case "swapped-attachments":
				download = secondPayload
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/inboxes/inbox/messages":
					_ = json.NewEncoder(w).Encode(openMailList[openMailMessage]{Data: []openMailMessage{source}, Total: 1})
				case "/v1/messages/provider-id/raw":
					_, _ = w.Write(raw)
				case "/v1/attachments/provider-id/file.bin":
					_, _ = w.Write(download)
				case "/v1/attachments/provider-id/other.bin":
					_, _ = w.Write(secondPayload)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			newTransport := func() *OpenMailTransport {
				transport, err := newOpenMailTransport(openMailTransportConfig{BaseURL: server.URL, APIKey: "fixture-key", Inbox: "inbox", HTTPClient: server.Client()})
				if err != nil {
					t.Fatal(err)
				}
				transport.resolvedInboxID, transport.resolvedAddress = "inbox", message.To[0]
				transport.authLookupTXT = lookup
				return transport
			}
			transport := newTransport()
			expected := transport.normalize(source, false)
			id := encodeOpenMailAttachmentID(source.ID, "file.bin")
			if mode != "missing-authentication" {
				err := transport.AuthenticateMessage(ctx, expected)
				if mode == "invalid-signature" || mode == "duplicate-filename" {
					if !errors.Is(err, ErrMessageUnauthenticated) {
						t.Fatalf("unsafe authentication: %v", err)
					}
				} else if err != nil {
					t.Fatalf("valid authentication: %v", err)
				}
			}
			switch mode {
			case "metadata-changed":
				source.BodyText += "changed"
			case "unknown-attachment":
				id = encodeOpenMailAttachmentID(source.ID, "unknown.bin")
			case "restart":
				transport = newTransport()
			case "eviction":
				// Check the cache bound without depending on map eviction order.
				for i := 0; i < maxOpenMailAuthenticatedMessages; i++ {
					m := expected
					m.MessageID = fmt.Sprintf("other-%d", i)
					if err := transport.rememberOpenMailAuthenticatedContent(m, openMailRawContent{}); err != nil {
						t.Fatal(err)
					}
				}
				if len(transport.authenticated) != maxOpenMailAuthenticatedMessages {
					t.Fatal("authentication cache is not bounded")
				}
				// Exercise loss of this message's evidence independently of
				// which entry the bounded cache selected above.
				delete(transport.authenticated, expected.MessageID)
			case "signed-replacement":
				// Even a second validly signed message with the same public
				// metadata must not overwrite the first attachment binding.
				replacement := bytes.Replace(unsigned, []byte(base64.StdEncoding.EncodeToString(payload)), []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("X"), len(payload)))), 1)
				var replacementLookup func(context.Context, string) ([]string, error)
				raw, replacementLookup = signedRawFixture(t, string(replacement), "sender.test", nil)
				transport.authLookupTXT = replacementLookup
				if err := transport.AuthenticateMessage(ctx, expected); !errors.Is(err, ErrMessageUnauthenticated) {
					t.Fatalf("rebound signed attachment: %v", err)
				}
			case "reauthentication":
				if err := transport.AuthenticateMessage(ctx, expected); err != nil {
					t.Fatal(err)
				}
			case "concurrent":
				var wg sync.WaitGroup
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if err := transport.AuthenticateMessage(ctx, expected); err != nil {
							t.Error(err)
						}
						got, err := transport.FetchAttachment(ctx, id, int64(len(payload)))
						if err != nil || !bytes.Equal(got, payload) {
							t.Errorf("concurrent verified download: %v", err)
						}
					}()
				}
				wg.Wait()
			}
			maxBytes := int64(len(payload) + 1)
			if mode == "too-large" {
				maxBytes = int64(len(payload) - 1)
			} else if mode == "negative-limit" {
				maxBytes = -1
			} else if mode == "empty-attachment" {
				maxBytes = 0
			} else if mode == "max-int64-limit" {
				maxBytes = math.MaxInt64
			}
			got, err := transport.FetchAttachment(ctx, id, maxBytes)
			switch mode {
			case "valid", "reauthentication", "concurrent", "signed-replacement",
				"quoted-printable-attachment", "7bit-attachment", "inline-related", "multiple-attachments", "empty-attachment", "max-int64-limit":
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("verified download differs: %v", err)
				}
			case "too-large":
				if !errors.Is(err, ErrAttachmentTooLarge) || got != nil {
					t.Fatalf("size limit: %v", err)
				}
			case "negative-limit":
				if err == nil || got != nil {
					t.Fatal("negative limit accepted")
				}
			default:
				if !errors.Is(err, ErrMessageUnauthenticated) || got != nil {
					t.Fatalf("unverified bytes exposed: %v", err)
				}
			}
			if mode == "multiple-attachments" {
				got, err := transport.FetchAttachment(ctx, encodeOpenMailAttachmentID(source.ID, "other.bin"), int64(len(secondPayload)))
				if err != nil || !bytes.Equal(got, secondPayload) {
					t.Fatalf("second attachment download differs: %v", err)
				}
			}
			if mode == "restart" || mode == "eviction" {
				if err := transport.AuthenticateMessage(ctx, expected); err != nil {
					t.Fatal(err)
				}
				got, err := transport.FetchAttachment(ctx, id, int64(len(payload)))
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("download after renewed authentication: %v", err)
				}
			}
		})
	}
}
