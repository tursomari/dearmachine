package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// OpenMail currently exposes an ordered header list, but only decoded MIME
// bodies. Reconstruct only the lossless plain-text subset and require the
// sender's signature to verify it. A provider verdict is never authority.
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
		messageFingerprint(transport.normalize(source, false)) != messageFingerprint(message) {
		return ErrMessageUnauthenticated
	}
	raw, err := openMailSignedPlainText(source)
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
	return verifySignedMessage(ctx, raw, expected, lookup)
}

func openMailSignedPlainText(source openMailMessage) ([]byte, error) {
	if len(source.Raw) == 0 || len(source.Raw) > maxAuthenticationMessageBytes ||
		len(source.BodyText) > maxAuthenticationMessageBytes || source.RFCMessageID == "" {
		return nil, ErrMessageUnauthenticated
	}
	if len(source.Attachments) != 0 || source.BodyHTML != "" || !utf8.ValidString(source.BodyText) {
		return nil, ErrMessageUnauthenticated
	}
	var evidence struct {
		Headers string `json:"message-headers"`
	}
	var headers [][]string
	if json.Unmarshal(source.Raw, &evidence) != nil || json.Unmarshal([]byte(evidence.Headers), &headers) != nil ||
		len(headers) == 0 || len(headers) > 256 {
		return nil, ErrMessageUnauthenticated
	}
	var data bytes.Buffer
	for _, h := range headers {
		if len(h) != 2 || h[0] == "" {
			return nil, ErrMessageUnauthenticated
		}
		for _, c := range h[0] {
			if c < 33 || c > 126 || c == ':' {
				return nil, ErrMessageUnauthenticated
			}
		}
		value := strings.ReplaceAll(h[1], "\r\n", "\n")
		for i, c := range value {
			if (c < 32 && c != '\n' && c != '\t') || c == 127 || (c == '\n' && (i+1 == len(value) || (value[i+1] != ' ' && value[i+1] != '\t'))) {
				return nil, ErrMessageUnauthenticated
			}
		}
		data.WriteString(h[0] + ": " + strings.ReplaceAll(value, "\n", "\r\n") + "\r\n")
	}
	data.WriteString("\r\n")
	parsed, err := mail.ReadMessage(bytes.NewReader(data.Bytes()))
	if err != nil {
		return nil, ErrMessageUnauthenticated
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	charset := strings.ToLower(params["charset"])
	encoding := strings.ToLower(strings.TrimSpace(parsed.Header.Get("Content-Transfer-Encoding")))
	if err != nil || mediaType != "text/plain" || len(params) > 1 ||
		(len(params) == 1 && charset == "") || (charset != "" && charset != "utf-8" && charset != "us-ascii") ||
		(encoding != "" && encoding != "7bit" && encoding != "8bit") {
		return nil, ErrMessageUnauthenticated
	}
	if charset != "utf-8" || encoding == "" || encoding == "7bit" {
		for _, c := range source.BodyText {
			if c > 127 {
				return nil, ErrMessageUnauthenticated
			}
		}
	}
	body := strings.ReplaceAll(source.BodyText, "\r\n", "\n")
	if strings.ContainsRune(body, '\r') {
		return nil, ErrMessageUnauthenticated
	}
	data.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	if data.Len() > maxAuthenticationMessageBytes {
		return nil, ErrMessageUnauthenticated
	}
	return data.Bytes(), nil
}
