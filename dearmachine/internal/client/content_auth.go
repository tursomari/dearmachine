package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
)

type authenticatedAttachment struct {
	size   int64
	digest [sha256.Size]byte
}

type authenticatedMessageContent struct {
	fingerprint string
	attachments map[string]authenticatedAttachment
}

const maxAuthenticatedMessages = 2048

// Include HTML in authentication caches without changing durable guest-work
// fingerprints used by existing approval and restart records.
func authenticatedContentFingerprint(message Message) string {
	sum := sha256.Sum256([]byte(messageFingerprint(message) + "\x00" + message.RawHTML))
	return hex.EncodeToString(sum[:])
}

// normalizedMIMEBody is also used for provider reports. Provider-generated
// previews and extracted replies are never instruction or correlation evidence.
func normalizedMIMEBody(text, html string) (body, rawBody string, references []string) {
	rawBody = text
	if strings.TrimSpace(rawBody) == "" {
		rawBody = htmlToText(html)
	}
	body, references = stripConversationFooters(rawBody)
	references = mergeConversationReferences(references, conversationReferencesInBodies(text, htmlToText(html)))
	return
}

// bindVerifiedMIME compares all content used for execution or control decisions
// with locally decoded signed MIME, before publishing authentication evidence.
// Only CRLF/LF representation is normalized; other whitespace is significant.
func bindVerifiedMIME(message Message, content verifiedMIMEContent) (authenticatedMessageContent, error) {
	body, rawBody, references := normalizedMIMEBody(content.bodyText, content.bodyHTML)
	if !mimeTextMatchesReport(body, message.Body) ||
		!mimeTextMatchesReport(rawBody, message.RawBody) ||
		!mimeTextMatchesReport(content.bodyHTML, message.RawHTML) ||
		!slices.Equal(references, message.ConversationReferences) ||
		len(content.attachments) != len(message.Attachments) {
		return authenticatedMessageContent{}, ErrMessageUnauthenticated
	}
	parts := make(map[string]verifiedMIMEAttachment, len(content.attachments))
	for _, part := range content.attachments {
		if _, exists := parts[part.filename]; exists {
			return authenticatedMessageContent{}, ErrMessageUnauthenticated
		}
		parts[part.filename] = part
	}
	verified := authenticatedMessageContent{fingerprint: authenticatedContentFingerprint(message), attachments: make(map[string]authenticatedAttachment)}
	for _, ref := range message.Attachments {
		part, exists := parts[ref.Filename]
		_, duplicate := verified.attachments[ref.AttachmentID]
		if !exists || ref.AttachmentID == "" || duplicate || ref.SizeBytes != int64(len(part.data)) ||
			(ref.ContentType != "" && !strings.EqualFold(ref.ContentType, part.contentType)) {
			return authenticatedMessageContent{}, ErrMessageUnauthenticated
		}
		verified.attachments[ref.AttachmentID] = authenticatedAttachment{size: int64(len(part.data)), digest: sha256.Sum256(part.data)}
		delete(parts, ref.Filename)
	}
	return verified, nil
}

// Caller holds the adapter's authMu. Never replace evidence for an existing ID.
func rememberAuthenticatedContent(cache *map[string]authenticatedMessageContent, id string, verified authenticatedMessageContent) error {
	if previous, exists := (*cache)[id]; exists {
		if previous.fingerprint != verified.fingerprint || !maps.Equal(previous.attachments, verified.attachments) {
			return ErrMessageUnauthenticated
		}
		return nil
	}
	if *cache == nil {
		*cache = make(map[string]authenticatedMessageContent)
	}
	if len(*cache) >= maxAuthenticatedMessages {
		for key := range *cache {
			delete(*cache, key)
			break
		}
	}
	(*cache)[id] = verified
	return nil
}

func (a authenticatedAttachment) matches(data []byte) bool {
	return int64(len(data)) == a.size && sha256.Sum256(data) == a.digest
}

// Sent receipts have no inbound DKIM evidence. Their files can only be checked
// against the exact durable outgoing bytes; this capability never returns
// unverified bytes to an agent or to inbound attachment staging.
type receiptAttachmentVerifier interface {
	verifyReceiptAttachment(context.Context, string, []byte) (bool, error)
}

func matchesReceiptAttachment(ctx context.Context, transport Transport, id string, expected []byte) (bool, error) {
	if verifier, ok := transport.(receiptAttachmentVerifier); ok {
		return verifier.verifyReceiptAttachment(ctx, id, expected)
	}
	data, err := transport.FetchAttachment(ctx, id, int64(len(expected))+1)
	return err == nil && bytes.Equal(data, expected), err
}

func (t *retryTransport) verifyReceiptAttachment(ctx context.Context, id string, expected []byte) (matches bool, err error) {
	err = t.retry(ctx, "receipt attachment", func() error {
		matches, err = matchesReceiptAttachment(ctx, t.Transport, id, expected)
		return err
	})
	return
}
