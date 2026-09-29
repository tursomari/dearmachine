package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenMail exposes an authenticated raw-message endpoint referenced by each
// message's rawUrl. Only those bytes, exactly as OpenMail received them
// before its own MIME parsing, carry the sender domain's DKIM signature. A
// provider verdict, or any content OpenMail reports outside those bytes, is
// never authority.
func (transport *OpenMailTransport) AuthenticateMessage(ctx context.Context, message Message) error {
	if message.MessageID == "" || containsFold(message.Labels, "unauthenticated") {
		return ErrMessageUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	inboxID, err := transport.inboxID(ctx)
	if err != nil {
		return errors.New("resolve OpenMail authentication inbox failed")
	}
	if message.Delivery.InboxID != inboxID || message.Delivery.Recipient != transport.resolvedAddress ||
		!visibleRecipient(message, transport.resolvedAddress) {
		return ErrMessageUnauthenticated
	}
	source, err := transport.rawMessage(ctx, message.MessageID)
	if err != nil {
		return errors.New("get OpenMail authentication evidence failed")
	}
	if source.InboxID != inboxID || source.ID != message.MessageID || source.Direction != "inbound" ||
		source.RFCMessageID == "" ||
		authenticatedContentFingerprint(transport.normalize(source, false)) != authenticatedContentFingerprint(message) {
		return ErrMessageUnauthenticated
	}
	raw, err := transport.fetchOpenMailRawMessage(ctx, source)
	if err != nil {
		return err
	}
	// Keep the provider's opaque ID for all API operations and persisted work.
	// Only signature comparison uses the Internet Message-ID of this same record.
	expected := message
	expected.MessageID = source.RFCMessageID
	lookup := transport.authLookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	if err := verifySignedMessage(ctx, raw, expected, lookup); err != nil {
		return err
	}
	content, err := parseVerifiedMIME(raw)
	if err != nil {
		return err
	}
	// The signature covers only the raw bytes. Bind them to what the API
	// reported and what normalize() already folded into the fingerprint above,
	// so a provider cannot pair a genuinely signed envelope with substituted
	// body or attachment content.
	if err := verifyOpenMailRawContentMatchesReport(content, source); err != nil {
		return err
	}
	return transport.rememberOpenMailAuthenticatedContent(message, content)
}

type openMailAuthenticatedAttachment struct {
	size   int64
	digest [sha256.Size]byte
}

type openMailAuthenticatedMessage struct {
	fingerprint string
	attachments map[string]openMailAuthenticatedAttachment
}

const maxOpenMailAuthenticatedMessages = 2048

// Retain only digests of decoded, verified MIME parts, never entire messages
// or attachment bodies. Entries are immutable once published under authMu.
func (transport *OpenMailTransport) rememberOpenMailAuthenticatedContent(message Message, content verifiedMIMEContent) error {
	verified := openMailAuthenticatedMessage{
		fingerprint: authenticatedContentFingerprint(message),
		attachments: make(map[string]openMailAuthenticatedAttachment, len(content.attachments)),
	}
	for _, attachment := range content.attachments {
		if _, exists := verified.attachments[attachment.filename]; exists {
			// The download API identifies a part by filename, so repeated
			// filenames cannot be bound to an unambiguous attachment identity.
			return ErrMessageUnauthenticated
		}
		verified.attachments[attachment.filename] = openMailAuthenticatedAttachment{
			size: int64(len(attachment.data)), digest: sha256.Sum256(attachment.data),
		}
	}
	transport.authMu.Lock()
	defer transport.authMu.Unlock()
	if previous, exists := transport.authenticated[message.MessageID]; exists {
		if previous.fingerprint != verified.fingerprint || !maps.Equal(previous.attachments, verified.attachments) {
			return ErrMessageUnauthenticated
		}
		return nil
	}
	if transport.authenticated == nil {
		transport.authenticated = make(map[string]openMailAuthenticatedMessage)
	}
	if len(transport.authenticated) >= maxOpenMailAuthenticatedMessages {
		// Bound memory. An evicted message must be authenticated again before
		// downloading; missing evidence never enables an unchecked download.
		for id := range transport.authenticated {
			delete(transport.authenticated, id)
			break
		}
	}
	transport.authenticated[message.MessageID] = verified
	return nil
}

// fetchOpenMailRawMessage retrieves the exact bytes OpenMail received for a
// message. rawUrl is provider-controlled and untrusted, so its value is never
// used to build the request: it is only a presence signal that raw evidence
// exists for this message. The request path is always constructed from the
// configured API base URL and the message's own opaque ID, so a compromised
// or malformed rawUrl value can never redirect the authenticated request off
// the configured OpenMail API host.
func (transport *OpenMailTransport) fetchOpenMailRawMessage(ctx context.Context, source openMailMessage) ([]byte, error) {
	if strings.TrimSpace(source.RawURL) == "" {
		return nil, ErrMessageUnauthenticated
	}
	messageID := strings.TrimSpace(source.ID)
	if messageID == "" {
		return nil, ErrMessageUnauthenticated
	}
	request, err := transport.request(ctx, http.MethodGet, "/v1/messages/"+url.PathEscape(messageID)+"/raw", nil)
	if err != nil {
		return nil, ErrMessageUnauthenticated
	}
	client := *transport.httpClient
	// The raw endpoint lives on the trusted API host itself, but a redirect
	// could still retarget the credential elsewhere. Never follow one.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("fetch OpenMail raw message failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch OpenMail raw message: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAuthenticationMessageBytes+1))
	if err != nil {
		return nil, errors.New("read OpenMail raw message failed")
	}
	if len(data) == 0 || len(data) > maxAuthenticationMessageBytes {
		return nil, ErrMessageUnauthenticated
	}
	return data, nil
}

// verifyOpenMailRawContentMatchesReport binds the DKIM-verified raw MIME to
// the metadata OpenMail's JSON API reported. Without this check, a signed
// envelope for one message could be paired with a different message's
// API-reported body or attachments.
func verifyOpenMailRawContentMatchesReport(content verifiedMIMEContent, source openMailMessage) error {
	if !mimeTextMatchesReport(content.bodyText, source.BodyText) ||
		!mimeTextMatchesReport(content.bodyHTML, source.BodyHTML) {
		return ErrMessageUnauthenticated
	}
	if len(content.attachments) != len(source.Attachments) {
		return ErrMessageUnauthenticated
	}
	matched := make([]bool, len(content.attachments))
	for _, want := range source.Attachments {
		found := false
		for index, got := range content.attachments {
			if matched[index] || got.filename != want.Filename || int64(len(got.data)) != want.SizeBytes {
				continue
			}
			if want.ContentType != "" && !strings.EqualFold(got.contentType, want.ContentType) {
				continue
			}
			matched[index] = true
			found = true
			break
		}
		if !found {
			return ErrMessageUnauthenticated
		}
	}
	return nil
}
