package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/emersion/go-msgauth/dkim"
)

const maxAuthenticationMessageBytes = 32 << 20

// SenderAuthenticationStatus describes the shipped evidence path, without
// loading credentials or contacting a provider.
func SenderAuthenticationStatus(transport string) string {
	if transport == "agentmail" {
		return "enabled: locally verified exact-domain DKIM, signed author and routing headers; trusts the sender domain's mailbox controls"
	}
	if transport == "openmail" {
		return "limited: locally verified exact-domain DKIM for unencoded plain text; HTML, multipart, attachments and unsupported evidence are rejected"
	}
	return "disabled: transport lacks supported sender evidence; inbound work is rejected"
}

// AuthenticateMessage verifies the raw message, not an Authentication-Results
// header or a generic provider verdict. The trust contract delegates mailbox
// assertions to the exact From domain's signing authority. It does not prove
// which human operated an account, or protect against that operator's abuse.
func (m *Mailbox) AuthenticateMessage(ctx context.Context, message Message) error {
	if message.MessageID == "" || message.Delivery.InboxID != m.inboxID ||
		!visibleRecipient(message, message.Delivery.Recipient) || containsFold(message.Labels, "unauthenticated") {
		return ErrMessageUnauthenticated
	}
	fingerprint := messageFingerprint(message)
	m.authMu.Lock()
	previous := m.authenticated[message.MessageID]
	m.authMu.Unlock()
	if previous != "" && previous != fingerprint {
		return ErrMessageUnauthenticated
	}
	if previous == fingerprint {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := m.client.Inboxes.Messages.GetRaw(ctx, message.MessageID, agentmail.InboxMessageGetRawParams{InboxID: m.inboxID})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Do not log provider response bodies or presigned download URLs.
		var apiError *agentmail.Error
		if errors.As(err, &apiError) {
			return fmt.Errorf("get raw authentication evidence: HTTP %d", apiError.StatusCode)
		}
		return errors.New("get raw authentication evidence: provider request failed")
	}
	if raw.MessageID != message.MessageID || raw.Size < 1 || raw.Size > maxAuthenticationMessageBytes {
		return ErrMessageUnauthenticated
	}
	u, err := url.Parse(raw.DownloadURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ErrMessageUnauthenticated
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrMessageUnauthenticated
	}
	client := m.authHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Copy the client so injected transports remain usable without mutating
	// shared redirect policy. No API authorization header reaches the blob URL.
	download := *client
	download.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("invalid raw message redirect")
		}
		return nil
	}
	response, err := download.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("download authentication evidence failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download authentication evidence: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAuthenticationMessageBytes+1))
	if err != nil {
		return errors.New("read authentication evidence failed")
	}
	if len(data) > maxAuthenticationMessageBytes || int64(len(data)) != raw.Size {
		return ErrMessageUnauthenticated
	}
	lookup := m.authLookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	if err := verifySignedMessage(ctx, data, message, lookup); err != nil {
		return err
	}
	m.authMu.Lock()
	if m.authenticated == nil || len(m.authenticated) >= 2048 {
		m.authenticated = map[string]string{}
	}
	m.authenticated[message.MessageID] = fingerprint
	m.authMu.Unlock()
	return nil
}

// Authorization-relevant fields must be covered together by one valid
// signature. Requiring every present field also rejects injected unsigned CC,
// approval parents, and MIME interpretation headers. Duplicate fields are
// rejected to avoid differences between parser/header-selection conventions.
var authenticatedHeaderFields = []string{
	"From", "To", "Cc", "Message-Id", "Subject", "Date", "In-Reply-To", "References", "Reply-To",
	"Mime-Version", "Content-Type", "Content-Transfer-Encoding",
}

func verifySignedMessage(ctx context.Context, raw []byte, expected Message, lookup func(context.Context, string) ([]string, error)) error {
	if len(raw) == 0 || len(raw) > maxAuthenticationMessageBytes {
		return ErrMessageUnauthenticated
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ErrMessageUnauthenticated
	}
	fields := append([]string(nil), authenticatedHeaderFields...)
	for field := range parsed.Header {
		// Bind all MIME metadata, including disposition, attachment names and
		// extension fields that a provider's MIME parser may interpret.
		if strings.HasPrefix(field, "Content-") && !containsFold(fields, field) {
			fields = append(fields, field)
		}
	}
	for _, field := range fields {
		if len(parsed.Header[field]) > 1 {
			return ErrMessageUnauthenticated
		}
	}
	for _, required := range []string{"From", "To", "Message-Id"} {
		if len(parsed.Header[required]) != 1 || parsed.Header.Get(required) == "" {
			return ErrMessageUnauthenticated
		}
	}
	from, err := canonicalMessageAddress(parsed.Header.Get("From"))
	wantFrom, wantErr := canonicalMessageAddress(expected.From)
	if err != nil || wantErr != nil || from != wantFrom || strings.TrimSpace(parsed.Header.Get("Message-Id")) != expected.MessageID {
		return ErrMessageUnauthenticated
	}
	domain := from[strings.LastIndexByte(from, '@')+1:]
	for field, want := range map[string][]string{"To": expected.To, "Cc": expected.CC} {
		var addresses []string
		if value := parsed.Header.Get(field); value != "" {
			values, err := mail.ParseAddressList(value)
			if err != nil {
				return ErrMessageUnauthenticated
			}
			for _, address := range values {
				addresses = append(addresses, address.Address)
			}
		}
		if !sameRecipientSet(addresses, want) {
			return ErrMessageUnauthenticated
		}
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != expected.Subject || strings.TrimSpace(parsed.Header.Get("In-Reply-To")) != expected.InReplyTo ||
		!slices.Equal(strings.Fields(parsed.Header.Get("References")), expected.References) {
		return ErrMessageUnauthenticated
	}
	results, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
		MaxVerifications: 5,
		LookupTXT:        func(name string) ([]string, error) { return lookup(ctx, name) },
	})
	if err != nil {
		return ErrMessageUnauthenticated
	}
	for _, result := range results {
		if result.Err != nil || !strings.EqualFold(result.Domain, domain) {
			continue
		}
		covered := true
		for _, field := range fields {
			if len(parsed.Header[field]) > 0 && !containsFold(result.HeaderKeys, field) {
				covered = false
				break
			}
		}
		if covered {
			return nil
		}
	}
	return ErrMessageUnauthenticated
}

func (*SendmuxTransport) AuthenticateMessage(context.Context, Message) error {
	return ErrSenderAttributionUnsupported
}
