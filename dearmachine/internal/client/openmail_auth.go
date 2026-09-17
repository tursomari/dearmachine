package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
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
		messageFingerprint(transport.normalize(source, false)) != messageFingerprint(message) {
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
	content, err := parseVerifiedOpenMailRawMIME(raw)
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
func (transport *OpenMailTransport) rememberOpenMailAuthenticatedContent(message Message, content openMailRawContent) error {
	verified := openMailAuthenticatedMessage{
		fingerprint: messageFingerprint(message),
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

type openMailRawAttachment struct {
	filename    string
	contentType string
	data        []byte
}

type openMailRawContent struct {
	bodyText    string
	bodyHTML    string
	attachments []openMailRawAttachment
}

// parseVerifiedOpenMailRawMIME parses raw bytes that have already passed DKIM
// verification. It supports single-part and (nested) multipart messages, and
// fails closed on any structure or encoding it cannot decode losslessly.
func parseVerifiedOpenMailRawMIME(raw []byte) (openMailRawContent, error) {
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return openMailRawContent{}, ErrMessageUnauthenticated
	}
	var content openMailRawContent
	if err := collectOpenMailMIMEParts(textproto.MIMEHeader(parsed.Header), parsed.Body, 0, &content); err != nil {
		return openMailRawContent{}, err
	}
	return content, nil
}

func collectOpenMailMIMEParts(header textproto.MIMEHeader, body io.Reader, depth int, content *openMailRawContent) error {
	if depth > 8 {
		return ErrMessageUnauthenticated
	}
	contentType := header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		if strings.TrimSpace(contentType) != "" {
			return ErrMessageUnauthenticated
		}
		mediaType, params = "text/plain", map[string]string{"charset": "us-ascii"}
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return ErrMessageUnauthenticated
		}
		reader := multipart.NewReader(body, boundary)
		parts := 0
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return ErrMessageUnauthenticated
			}
			parts++
			if parts > 64 {
				return ErrMessageUnauthenticated
			}
			if err := collectOpenMailMIMEParts(part.Header, part, depth+1, content); err != nil {
				return err
			}
		}
		return nil
	}
	decoded, err := decodeOpenMailMIMEPart(header, body)
	if err != nil {
		return err
	}
	if len(decoded) > maxAuthenticationMessageBytes {
		return ErrMessageUnauthenticated
	}
	filename := openMailPartFilename(header, params)
	disposition, _, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	switch {
	case disposition != "attachment" && filename == "" && mediaType == "text/plain":
		text, err := decodeOpenMailPlainText(decoded, params["charset"])
		if err != nil {
			return err
		}
		content.bodyText += text
	case disposition != "attachment" && filename == "" && mediaType == "text/html":
		text, err := decodeOpenMailPlainText(decoded, params["charset"])
		if err != nil {
			return err
		}
		content.bodyHTML += text
	default:
		// Anything else must be identifiable as an attachment by name so it can
		// be bound to the API-reported attachment metadata below. An unnamed,
		// non-text part cannot be correlated safely, so it is rejected.
		if filename == "" {
			return ErrMessageUnauthenticated
		}
		content.attachments = append(content.attachments, openMailRawAttachment{
			filename: filename, contentType: mediaType, data: decoded,
		})
	}
	return nil
}

func openMailPartFilename(header textproto.MIMEHeader, contentTypeParams map[string]string) string {
	if _, dispositionParams, err := mime.ParseMediaType(header.Get("Content-Disposition")); err == nil {
		if name := strings.TrimSpace(dispositionParams["filename"]); name != "" {
			return name
		}
	}
	return strings.TrimSpace(contentTypeParams["name"])
}

// decodeOpenMailMIMEPart reverses Content-Transfer-Encoding to recover the
// original bytes exactly, so downloaded attachments can be checked against
// digests of the signed content rather than just provider-reported sizes.
func decodeOpenMailMIMEPart(header textproto.MIMEHeader, body io.Reader) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
	case "", "7bit", "8bit", "binary":
		return io.ReadAll(io.LimitReader(body, maxAuthenticationMessageBytes+1))
	case "base64":
		return io.ReadAll(io.LimitReader(base64.NewDecoder(base64.StdEncoding, body), maxAuthenticationMessageBytes+1))
	case "quoted-printable":
		return io.ReadAll(io.LimitReader(quotedprintable.NewReader(body), maxAuthenticationMessageBytes+1))
	default:
		return nil, ErrMessageUnauthenticated
	}
}

// decodeOpenMailPlainText only accepts unencoded UTF-8 or US-ASCII text, the
// only charsets this adapter can decode losslessly without an external
// charset conversion dependency. Anything else fails closed.
func decodeOpenMailPlainText(data []byte, charset string) (string, error) {
	charset = strings.ToLower(strings.TrimSpace(charset))
	if charset != "" && charset != "utf-8" && charset != "us-ascii" {
		return "", ErrMessageUnauthenticated
	}
	if charset == "utf-8" {
		if !utf8.Valid(data) {
			return "", ErrMessageUnauthenticated
		}
	} else {
		for _, b := range data {
			if b > 127 {
				return "", ErrMessageUnauthenticated
			}
		}
	}
	return string(data), nil
}

// verifyOpenMailRawContentMatchesReport binds the DKIM-verified raw MIME to
// the metadata OpenMail's JSON API reported. Without this check, a signed
// envelope for one message could be paired with a different message's
// API-reported body or attachments.
func verifyOpenMailRawContentMatchesReport(content openMailRawContent, source openMailMessage) error {
	if !openMailTextMatchesReport(content.bodyText, source.BodyText) ||
		!openMailTextMatchesReport(content.bodyHTML, source.BodyHTML) {
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

func normalizeOpenMailLineEndings(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

// openMailTextMatchesReport compares decoded, DKIM-verified MIME text with
// OpenMail's JSON report. Only CRLF/LF representation differences are normalized;
// all content whitespace, including every terminal newline, must match.
func openMailTextMatchesReport(raw, reported string) bool {
	return normalizeOpenMailLineEndings(raw) == normalizeOpenMailLineEndings(reported)
}
