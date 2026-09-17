package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- JSON decoding -----------------------------------------------------

func TestOpenMailMessageDecodesRawURLField(t *testing.T) {
	var message openMailMessage
	if err := json.Unmarshal([]byte(`{"id":"m1","rawUrl":"/v1/messages/m1/raw"}`), &message); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if message.RawURL != "/v1/messages/m1/raw" {
		t.Fatalf("RawURL = %q", message.RawURL)
	}
}

func TestOpenMailMessageDecodesWithoutRawURLField(t *testing.T) {
	var message openMailMessage
	if err := json.Unmarshal([]byte(`{"id":"m1"}`), &message); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if message.RawURL != "" {
		t.Fatalf("RawURL = %q, want empty", message.RawURL)
	}
}

// --- fetching ------------------------------------------------------------

func TestOpenMailFetchRawMessage(t *testing.T) {
	const body = "raw-mime-bytes"
	outsideHit := false
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outsideHit = true
		w.Write([]byte("must never be fetched"))
	}))
	defer outside.Close()

	for _, tc := range []struct {
		name                string
		messageID           string
		rawURL              string
		handler             func(w http.ResponseWriter, r *http.Request)
		wantErr             string
		wantUnauthenticated bool
	}{
		{
			name:      "success",
			messageID: "m1",
			rawURL:    "/v1/messages/m1/raw",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Error("raw fetch missing bearer credential")
				}
				if r.Method != http.MethodGet {
					t.Errorf("raw fetch method = %s", r.Method)
				}
				if r.URL.Path != "/v1/messages/m1/raw" {
					t.Errorf("raw fetch path = %s, want deterministic /v1/messages/{id}/raw", r.URL.Path)
				}
				io.WriteString(w, body)
			},
		},
		{
			// The rawUrl value is provider-controlled and must never be used to
			// build the request. Pointing it at an attacker host must not steer
			// the fetch there; the request still goes to the configured host at
			// the deterministic path derived from the message ID.
			name:      "raw-url-value-is-ignored-even-if-attacker-controlled",
			messageID: "m1",
			rawURL:    outside.URL + "/raw?redirect=me",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, body)
			},
		},
		{
			name:                "missing-raw-url",
			messageID:           "m1",
			rawURL:              "",
			wantUnauthenticated: true,
		},
		{
			name:                "missing-message-id",
			messageID:           "",
			rawURL:              "present",
			wantUnauthenticated: true,
		},
		{
			name:      "non-200-status",
			messageID: "m1",
			rawURL:    "present",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "private detail", http.StatusInternalServerError)
			},
			wantErr: "HTTP 500",
		},
		{
			name:      "redirect-not-followed",
			messageID: "m1",
			rawURL:    "present",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
			},
			wantErr: "HTTP 302",
		},
		{
			name:      "empty-body",
			messageID: "m1",
			rawURL:    "present",
			handler: func(w http.ResponseWriter, r *http.Request) {
			},
			wantUnauthenticated: true,
		},
		{
			name:      "oversized-body",
			messageID: "m1",
			rawURL:    "present",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.CopyN(w, zeroReader{}, maxAuthenticationMessageBytes+1)
			},
			wantUnauthenticated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var main *httptest.Server
			main = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.handler != nil {
					tc.handler(w, r)
					return
				}
				http.NotFound(w, r)
			}))
			defer main.Close()
			transport, err := newOpenMailTransport(openMailTransportConfig{
				BaseURL: main.URL, APIKey: "fixture-key", Inbox: "inbox", HTTPClient: main.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := transport.fetchOpenMailRawMessage(context.Background(), openMailMessage{ID: tc.messageID, RawURL: tc.rawURL})
			switch {
			case tc.wantErr == "" && !tc.wantUnauthenticated:
				if err != nil {
					t.Fatalf("fetchOpenMailRawMessage: %v", err)
				}
				if string(data) != body {
					t.Fatalf("fetchOpenMailRawMessage data = %q, want %q", data, body)
				}
			case tc.wantUnauthenticated:
				if !errors.Is(err, ErrMessageUnauthenticated) {
					t.Fatalf("fetchOpenMailRawMessage error = %v, want ErrMessageUnauthenticated", err)
				}
			default:
				if err == nil {
					t.Fatal("fetchOpenMailRawMessage accepted invalid evidence")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("fetchOpenMailRawMessage error = %v, want containing %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "private detail") {
					t.Fatal("fetch error leaked provider response body")
				}
			}
			if outsideHit {
				t.Fatal("provider-supplied rawUrl value redirected the authenticated fetch")
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// --- MIME parsing and byte integrity -------------------------------------

func TestParseVerifiedOpenMailRawMIME(t *testing.T) {
	base := "From: a@sender.test\r\nTo: b@receiver.test\r\nMessage-ID: <1@sender.test>\r\nSubject: s\r\nMIME-Version: 1.0\r\n"

	t.Run("single-part-7bit", func(t *testing.T) {
		raw := base + "Content-Type: text/plain; charset=utf-8\r\n\r\nHello world\r\n"
		content, err := parseVerifiedOpenMailRawMIME([]byte(raw))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if content.bodyText != "Hello world\r\n" {
			t.Fatalf("bodyText = %q", content.bodyText)
		}
	})

	t.Run("single-part-base64", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte("Encoded body"))
		raw := base + "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n"
		content, err := parseVerifiedOpenMailRawMIME([]byte(raw))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if content.bodyText != "Encoded body" {
			t.Fatalf("bodyText = %q", content.bodyText)
		}
	})

	t.Run("single-part-quoted-printable", func(t *testing.T) {
		raw := base + "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nCaf=C3=A9\r\n"
		content, err := parseVerifiedOpenMailRawMIME([]byte(raw))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if content.bodyText != "Café\r\n" {
			t.Fatalf("bodyText = %q", content.bodyText)
		}
	})

	t.Run("single-part-html", func(t *testing.T) {
		raw := base + "Content-Type: text/html; charset=utf-8\r\n\r\n<p>Hi</p>\r\n"
		content, err := parseVerifiedOpenMailRawMIME([]byte(raw))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if content.bodyHTML != "<p>Hi</p>\r\n" {
			t.Fatalf("bodyHTML = %q", content.bodyHTML)
		}
	})

	t.Run("multipart-alternative-and-attachment-byte-integrity", func(t *testing.T) {
		attachmentBytes := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 'h', 'i', 0x00}
		raw := rawOpenMailMultipartMessage(t, rawOpenMailMultipartSpec{
			headers:        base,
			boundary:       "OUTER",
			plainBody:      "Plain part",
			htmlBody:       "<p>HTML part</p>",
			attachmentName: "payload.bin", attachmentType: "application/octet-stream", attachmentBytes: attachmentBytes,
		})
		content, err := parseVerifiedOpenMailRawMIME(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		// multipart.Reader strips the CRLF immediately preceding the next
		// boundary delimiter, unlike a top-level single-part body.
		if content.bodyText != "Plain part" || content.bodyHTML != "<p>HTML part</p>" {
			t.Fatalf("content = %+v", content)
		}
		if len(content.attachments) != 1 {
			t.Fatalf("attachments = %+v", content.attachments)
		}
		got := content.attachments[0]
		if got.filename != "payload.bin" || got.contentType != "application/octet-stream" || !bytes.Equal(got.data, attachmentBytes) {
			t.Fatalf("attachment = %+v, want bytes %x", got, attachmentBytes)
		}
	})

	t.Run("missing-boundary", func(t *testing.T) {
		raw := base + "Content-Type: multipart/mixed\r\n\r\nbody\r\n"
		if _, err := parseVerifiedOpenMailRawMIME([]byte(raw)); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("missing boundary err = %v", err)
		}
	})

	t.Run("malformed-multipart-body", func(t *testing.T) {
		raw := base + "Content-Type: multipart/mixed; boundary=X\r\n\r\nnot actually multipart content at all"
		if _, err := parseVerifiedOpenMailRawMIME([]byte(raw)); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("malformed multipart err = %v", err)
		}
	})

	t.Run("unsupported-transfer-encoding", func(t *testing.T) {
		raw := base + "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: x-custom\r\n\r\nbody\r\n"
		if _, err := parseVerifiedOpenMailRawMIME([]byte(raw)); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("unsupported encoding err = %v", err)
		}
	})

	t.Run("unsupported-charset", func(t *testing.T) {
		raw := base + "Content-Type: text/plain; charset=iso-8859-1\r\n\r\nbody\r\n"
		if _, err := parseVerifiedOpenMailRawMIME([]byte(raw)); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("unsupported charset err = %v", err)
		}
	})

	t.Run("invalid-utf8-claimed", func(t *testing.T) {
		raw := append([]byte(base+"Content-Type: text/plain; charset=utf-8\r\n\r\n"), 0xFF, 0xFE)
		if _, err := parseVerifiedOpenMailRawMIME(raw); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("invalid utf-8 err = %v", err)
		}
	})

	t.Run("unnamed-non-text-part-rejected", func(t *testing.T) {
		raw := rawOpenMailMultipartMessage(t, rawOpenMailMultipartSpec{
			headers: base, boundary: "OUTER", plainBody: "text",
			extraPart: "Content-Type: application/octet-stream\r\n\r\nunnamed binary\r\n",
		})
		if _, err := parseVerifiedOpenMailRawMIME(raw); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("unnamed part err = %v", err)
		}
	})

	t.Run("malformed-content-type", func(t *testing.T) {
		raw := base + "Content-Type: ;;;not-valid\r\n\r\nbody\r\n"
		if _, err := parseVerifiedOpenMailRawMIME([]byte(raw)); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("malformed content-type err = %v", err)
		}
	})
}

type rawOpenMailMultipartSpec struct {
	headers                        string
	boundary                       string
	plainBody, htmlBody            string
	attachmentName, attachmentType string
	attachmentBytes                []byte
	extraPart                      string
}

func rawOpenMailMultipartMessage(t *testing.T, spec rawOpenMailMultipartSpec) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(spec.headers)
	b.WriteString("Content-Type: multipart/mixed; boundary=" + spec.boundary + "\r\n\r\n")
	b.WriteString("--" + spec.boundary + "\r\n")
	if spec.htmlBody != "" {
		b.WriteString("Content-Type: multipart/alternative; boundary=" + spec.boundary + "ALT\r\n\r\n")
		b.WriteString("--" + spec.boundary + "ALT\r\n")
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n" + spec.plainBody + "\r\n")
		b.WriteString("--" + spec.boundary + "ALT\r\n")
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n" + spec.htmlBody + "\r\n")
		b.WriteString("--" + spec.boundary + "ALT--\r\n")
	} else {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n" + spec.plainBody + "\r\n")
	}
	if spec.attachmentName != "" {
		b.WriteString("--" + spec.boundary + "\r\n")
		b.WriteString(fmt.Sprintf("Content-Type: %s\r\nContent-Disposition: attachment; filename=%q\r\nContent-Transfer-Encoding: base64\r\n\r\n",
			spec.attachmentType, spec.attachmentName))
		b.WriteString(base64.StdEncoding.EncodeToString(spec.attachmentBytes) + "\r\n")
	}
	if spec.extraPart != "" {
		b.WriteString("--" + spec.boundary + "\r\n")
		b.WriteString(spec.extraPart)
	}
	b.WriteString("--" + spec.boundary + "--\r\n")
	return []byte(b.String())
}

// --- cross-checking parsed content against reported metadata -------------

func TestVerifyOpenMailRawContentMatchesReport(t *testing.T) {
	baseSource := openMailMessage{BodyText: "hello\n", Attachments: []openMailAttachment{
		{Filename: "a.txt", ContentType: "text/plain", SizeBytes: 3},
	}}
	baseContent := openMailRawContent{bodyText: "hello\n", attachments: []openMailRawAttachment{
		{filename: "a.txt", contentType: "text/plain", data: []byte("abc")},
	}}

	if err := verifyOpenMailRawContentMatchesReport(baseContent, baseSource); err != nil {
		t.Fatalf("matching content rejected: %v", err)
	}

	t.Run("crlf-normalized-body-matches", func(t *testing.T) {
		content := baseContent
		content.bodyText = "hello\r\n"
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); err != nil {
			t.Fatalf("CRLF-normalized body rejected: %v", err)
		}
	})

	t.Run("raw-trailing-newline-rejected", func(t *testing.T) {
		// A single-part MIME body can retain a final line break that the
		// JSON report omits.
		source := baseSource
		source.BodyText = "hello"
		if err := verifyOpenMailRawContentMatchesReport(baseContent, source); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("raw body with extra trailing newline error = %v, want ErrMessageUnauthenticated", err)
		}
	})

	t.Run("reported-trailing-newline-rejected", func(t *testing.T) {
		// Reproduces the former provider discrepancy, which is now rejected.
		content := baseContent
		content.bodyText = "hello"
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("reported body with extra trailing newline error = %v, want ErrMessageUnauthenticated", err)
		}
	})

	t.Run("raw-trailing-crlf-rejected", func(t *testing.T) {
		source := baseSource
		source.BodyText = "hello"
		content := baseContent
		content.bodyText = "hello\r\n"
		if err := verifyOpenMailRawContentMatchesReport(content, source); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("CRLF trailing newline discrepancy error = %v, want ErrMessageUnauthenticated", err)
		}
	})

	t.Run("body-mismatch", func(t *testing.T) {
		content := baseContent
		content.bodyText = "goodbye\n"
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("body mismatch err = %v", err)
		}
	})

	t.Run("body-mismatch-beyond-trailing-newline", func(t *testing.T) {
		// Every content newline must match; do not trim trailing whitespace.
		source := baseSource
		source.BodyText = "hello"
		content := baseContent
		content.bodyText = "hello\n\n"
		if err := verifyOpenMailRawContentMatchesReport(content, source); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("double trailing newline mismatch err = %v", err)
		}
	})

	t.Run("body-substitution-not-tolerated", func(t *testing.T) {
		// Content changes are rejected independently of line-ending format.
		content := baseContent
		content.bodyText = "hellx"
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("substituted body err = %v", err)
		}
	})

	t.Run("html-mismatch", func(t *testing.T) {
		source := baseSource
		source.BodyHTML = "<p>a</p>"
		if err := verifyOpenMailRawContentMatchesReport(baseContent, source); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("html mismatch err = %v", err)
		}
	})

	t.Run("html-trailing-newline-rejected", func(t *testing.T) {
		content := baseContent
		content.bodyHTML = "<p>a</p>\n"
		source := baseSource
		source.BodyHTML = "<p>a</p>"
		if err := verifyOpenMailRawContentMatchesReport(content, source); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("html trailing newline discrepancy error = %v, want ErrMessageUnauthenticated", err)
		}
	})

	t.Run("attachment-count-mismatch", func(t *testing.T) {
		content := baseContent
		content.attachments = append(content.attachments, openMailRawAttachment{filename: "b.txt", data: []byte("x")})
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("attachment count mismatch err = %v", err)
		}
	})

	t.Run("attachment-size-mismatch", func(t *testing.T) {
		content := baseContent
		content.attachments = []openMailRawAttachment{{filename: "a.txt", contentType: "text/plain", data: []byte("abcd")}}
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("attachment size mismatch err = %v", err)
		}
	})

	t.Run("attachment-filename-mismatch", func(t *testing.T) {
		content := baseContent
		content.attachments = []openMailRawAttachment{{filename: "other.txt", contentType: "text/plain", data: []byte("abc")}}
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("attachment filename mismatch err = %v", err)
		}
	})

	t.Run("attachment-content-type-mismatch", func(t *testing.T) {
		content := baseContent
		content.attachments = []openMailRawAttachment{{filename: "a.txt", contentType: "application/octet-stream", data: []byte("abc")}}
		if err := verifyOpenMailRawContentMatchesReport(content, baseSource); !errors.Is(err, ErrMessageUnauthenticated) {
			t.Fatalf("attachment content-type mismatch err = %v", err)
		}
	})

	t.Run("attachment-content-type-ignored-when-unreported", func(t *testing.T) {
		source := baseSource
		source.Attachments = []openMailAttachment{{Filename: "a.txt", SizeBytes: 3}}
		if err := verifyOpenMailRawContentMatchesReport(baseContent, source); err != nil {
			t.Fatalf("unreported content-type rejected: %v", err)
		}
	})
}

// --- end-to-end authentication over the rawUrl evidence path --------------

func TestOpenMailAuthenticationBindsSignedRawMessage(t *testing.T) {
	for _, mode := range []string{
		"valid-plain", "valid-7bit", "valid-8bit", "valid-multipart-attachment", "valid-html-alternative",
		"valid-raw-url-value-ignored", "single-part-omitted-newline",
		"multipart-reported-newline", "html-reported-newline",
		"reported-two-newlines", "reported-trailing-space", "raw-body-tampered",
		"forged-from", "changed-to", "changed-cc", "changed-body", "changed-parent", "changed-references",
		"changed-rfc-id", "wrong-inbox", "missing-inbox", "outbound",
		"missing-raw-url", "fetch-error", "attachment-size-tampered",
		"dns-failure", "provider-failure",
	} {
		t.Run(mode, func(t *testing.T) {
			message := signedFixtureMessage()
			var extra []string
			if mode == "valid-7bit" {
				extra = []string{"Content-Transfer-Encoding: 7bit"}
			}
			if mode == "valid-8bit" {
				message.Body = "Signed UTF-8 café"
				extra = []string{"Content-Transfer-Encoding: 8bit"}
			}

			var raw []byte
			var lookup func(context.Context, string) ([]string, error)
			var attachments []openMailAttachment
			switch mode {
			case "valid-multipart-attachment", "multipart-reported-newline":
				attachmentBytes := []byte("attachment payload bytes")
				headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nCc: %s\r\nMessage-ID: %s\r\nSubject: %s\r\nDate: Sat, 12 Sep 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nIn-Reply-To: %s\r\nReferences: %s\r\n",
					message.From, strings.Join(message.To, ", "), strings.Join(message.CC, ", "), message.MessageID, message.Subject,
					message.InReplyTo, strings.Join(message.References, " "))
				unsigned := rawOpenMailMultipartMessage(t, rawOpenMailMultipartSpec{
					headers: headers, boundary: "BOUNDARY", plainBody: message.Body,
					attachmentName: "file.bin", attachmentType: "application/octet-stream", attachmentBytes: attachmentBytes,
				})
				raw, lookup = signedRawFixture(t, string(unsigned), "sender.test", nil)
				attachments = []openMailAttachment{{Filename: "file.bin", ContentType: "application/octet-stream", SizeBytes: int64(len(attachmentBytes))}}
			case "valid-html-alternative", "html-reported-newline":
				headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nCc: %s\r\nMessage-ID: %s\r\nSubject: %s\r\nDate: Sat, 12 Sep 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nIn-Reply-To: %s\r\nReferences: %s\r\n",
					message.From, strings.Join(message.To, ", "), strings.Join(message.CC, ", "), message.MessageID, message.Subject,
					message.InReplyTo, strings.Join(message.References, " "))
				unsigned := rawOpenMailMultipartMessage(t, rawOpenMailMultipartSpec{
					headers: headers, boundary: "BOUNDARY", plainBody: message.Body, htmlBody: "<p>" + message.Body + "</p>",
				})
				raw, lookup = signedRawFixture(t, string(unsigned), "sender.test", nil)
			default:
				raw, lookup = signedMailFixture(t, message, "sender.test", nil, extra...)
			}

			// A single-part body reader includes the trailing CRLF that precedes
			// end-of-message, but multipart.Reader strips the CRLF immediately
			// before a boundary delimiter, so multipart bodies have none.
			bodyText := message.Body + "\n"
			if mode == "valid-multipart-attachment" || mode == "valid-html-alternative" || mode == "multipart-reported-newline" || mode == "html-reported-newline" {
				bodyText = message.Body
			}
			source := openMailMessage{
				ID: "opaque-provider-id", InboxID: "inbox", RFCMessageID: message.MessageID, ThreadID: message.ThreadID,
				Direction: "inbound", FromAddr: message.From, ToAddr: strings.Join(message.To, ", "), HeaderTo: strings.Join(message.To, ", "),
				CC: message.CC, Subject: message.Subject, BodyText: bodyText, InReplyTo: message.InReplyTo, References: message.References,
				Attachments: attachments, RawURL: "present",
			}
			if mode == "valid-html-alternative" || mode == "html-reported-newline" {
				source.BodyHTML = "<p>" + message.Body + "</p>"
			}

			switch mode {
			case "single-part-omitted-newline":
				source.BodyText = message.Body
			case "multipart-reported-newline":
				source.BodyText += "\n"
			case "html-reported-newline":
				source.BodyHTML += "\n"
			case "reported-two-newlines":
				source.BodyText += "\n\n"
			case "reported-trailing-space":
				source.BodyText += " "
			case "raw-body-tampered":
				// Keep the API report consistent with the altered MIME body.
				// Only checking the original DKIM signature can reject this.
				raw = bytes.Replace(raw, []byte(message.Body), []byte("Injected instruction"), 1)
				source.BodyText = "Injected instruction"
			case "forged-from":
				source.FromAddr = "intruder@sender.test"
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
			case "missing-raw-url":
				source.RawURL = ""
			case "valid-raw-url-value-ignored":
				// The value is provider-controlled and must never steer the
				// fetch; only its presence matters. If the implementation ever
				// used this value to build the request, it would try to reach an
				// unroutable host instead of the deterministic API endpoint below.
				source.RawURL = "https://attacker.invalid/raw"
			case "attachment-size-tampered":
				source.Attachments = []openMailAttachment{{Filename: "file.bin", ContentType: "application/octet-stream", SizeBytes: 999}}
			case "dns-failure":
				lookup = func(context.Context, string) ([]string, error) { return nil, errors.New("unavailable") }
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/inboxes/inbox/messages":
					if mode == "provider-failure" {
						http.Error(w, "private provider response", 500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(openMailList[openMailMessage]{Data: []openMailMessage{source}, Total: 1})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/"+source.ID+"/raw":
					if mode == "fetch-error" {
						http.Error(w, "private raw error", 500)
						return
					}
					w.Write(raw)
				default:
					t.Errorf("unexpected authentication request: %s %s", r.Method, r.URL.String())
					http.NotFound(w, r)
				}
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
			err = transport.AuthenticateMessage(context.Background(), expected)
			if strings.HasPrefix(mode, "valid-") {
				if err != nil {
					t.Fatalf("signed message rejected: %v", err)
				}
				if expected.MessageID != "opaque-provider-id" || expected.InReplyTo != message.InReplyTo {
					t.Fatal("provider ID or reply correlation lost")
				}
			} else if err == nil {
				t.Fatal("unsafe evidence accepted")
			}
			if err != nil && (strings.Contains(err.Error(), "private provider") || strings.Contains(err.Error(), "private raw error")) {
				t.Fatal("provider error leaked")
			}
		})
	}
}

func TestOpenMailBodyReportCanonicalization(t *testing.T) {
	for _, tc := range []struct {
		name, raw, reported string
		matches             bool
	}{
		{"empty", "", "", true},
		{"identical", "first\nsecond", "first\nsecond", true},
		{"mixed-line-endings", "first\r\nsecond\nthird\r\n", "first\nsecond\r\nthird\n", true},
		{"raw-final-crlf", "body\r\n", "body", false},
		{"report-final-crlf", "body", "body\r\n", false},
		{"empty-raw-final-newline", "\r\n", "", false},
		{"empty-report-final-newline", "", "\n", false},
		{"existing-final-newline", "body\r\n\r\n", "body\n", false},
		{"preserve-identical-whitespace", " \tbody\t \r\n\r\n", " \tbody\t \n\n", true},
		{"two-raw-newlines", "body\r\n\r\n", "body", false},
		{"two-report-newlines", "body", "body\n\n", false},
		{"empty-two-newlines", "", "\n\n", false},
		{"leading-newline", "\nbody", "body", false},
		{"interior-blank-line", "first\n\nsecond", "first\nsecond", false},
		{"interior-space", "first  second", "first second", false},
		{"terminal-space", "body ", "body", false},
		{"terminal-tab", "body\t\n", "body\n", false},
		{"terminal-bare-cr", "body\r", "body", false},
		{"interior-bare-cr", "first\rsecond", "first\nsecond", false},
		{"unicode-line-separator", "body\u2028", "body", false},
		{"substitution-and-newline", "good\n", "evil", false},
	} {
		for _, field := range []string{"text", "html"} {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				var content openMailRawContent
				var source openMailMessage
				if field == "text" {
					content.bodyText, source.BodyText = tc.raw, tc.reported
				} else {
					content.bodyHTML, source.BodyHTML = tc.raw, tc.reported
				}
				err := verifyOpenMailRawContentMatchesReport(content, source)
				if tc.matches && err != nil {
					t.Fatalf("equivalent body rejected: %v", err)
				}
				if !tc.matches && !errors.Is(err, ErrMessageUnauthenticated) {
					t.Fatalf("changed body error = %v, want ErrMessageUnauthenticated", err)
				}
			})
		}
	}
}

func TestOpenMailMIMEBodyTrailingNewlines(t *testing.T) {
	// Include the delimiter CRLF separately from body content. In particular,
	// it must not erase a content newline in base64 or quoted-printable parts.
	for _, encoding := range []string{"7bit", "base64", "quoted-printable"} {
		for _, multipartBody := range []bool{false, true} {
			for _, suffix := range []string{"", "\r\n", "\r\n\r\n"} {
				name := fmt.Sprintf("%s/multipart=%t/trailing=%d", encoding, multipartBody, len(suffix)/2)
				t.Run(name, func(t *testing.T) {
					body := "first\r\nsecond" + suffix
					wireBody := body
					switch encoding {
					case "base64":
						wireBody = base64.StdEncoding.EncodeToString([]byte(body)) + "\r\n"
					case "quoted-printable":
						wireBody = "fir=73t\r\nsec=\r\nond" + suffix
					}
					part := "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: " + encoding + "\r\n\r\n" + wireBody
					raw := part
					if multipartBody {
						raw = "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + part + "\r\n--B--\r\n"
					}
					content, err := parseVerifiedOpenMailRawMIME([]byte(raw))
					if err != nil {
						t.Fatal(err)
					}
					if content.bodyText != body {
						t.Fatalf("decoded body = %q, want %q", content.bodyText, body)
					}
					report := normalizeOpenMailLineEndings(body)
					if err := verifyOpenMailRawContentMatchesReport(content, openMailMessage{BodyText: report}); err != nil {
						t.Fatalf("matching decoded body rejected: %v", err)
					}
					if err := verifyOpenMailRawContentMatchesReport(content, openMailMessage{BodyText: report + "\n"}); !errors.Is(err, ErrMessageUnauthenticated) {
						t.Fatalf("one extra reported LF: %v", err)
					}
				})
			}
		}
	}
}
