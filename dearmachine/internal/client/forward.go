package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"path/filepath"
	"strings"

	"golang.org/x/net/html/charset"
)

const maxForwardMIMEDepth = 5

func forwardedConversationReferences(
	ctx context.Context,
	transport Transport,
	message Message,
) ([]string, error) {
	body := message.RawBody
	if body == "" {
		body = message.Body
	}
	references := inlineForwardReferences(body)
	for _, attachment := range message.Attachments {
		if !isEMLAttachment(attachment) {
			continue
		}
		contents, err := transport.FetchAttachment(
			ctx,
			attachment.AttachmentID,
			DefaultAttachmentLimits().MaxFileBytes,
		)
		if err != nil {
			return nil, fmt.Errorf("inspect forwarded email %s: %w", attachment.Filename, err)
		}
		bodies, err := embeddedMessageBodies(contents, 0)
		if err != nil {
			return nil, fmt.Errorf("parse forwarded email %s: %w", attachment.Filename, err)
		}
		for _, embedded := range bodies {
			_, found := stripConversationFooters(embedded)
			references = mergeConversationReferences(references, found)
		}
	}
	return references, nil
}

func isEMLAttachment(attachment AttachmentRef) bool {
	mediaType, _, _ := mime.ParseMediaType(attachment.ContentType)
	return strings.EqualFold(mediaType, "message/rfc822") ||
		strings.EqualFold(filepath.Ext(strings.TrimSpace(attachment.Filename)), ".eml")
}

func inlineForwardReferences(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	var references []string
	for index, line := range lines {
		session, found := strings.CutPrefix(footerLine(line), "session ")
		if !found || !isCanonicalConversationReference(session) {
			continue
		}
		if emailQuoteDepth(line) > 0 || forwardContextBefore(lines, index) {
			references = mergeConversationReferences(references, []string{session})
		}
	}
	return references
}

func hasForwardStructure(message Message) bool {
	for _, attachment := range message.Attachments {
		if isEMLAttachment(attachment) {
			return true
		}
	}
	body := message.RawBody
	if body == "" {
		body = message.Body
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for index, raw := range lines {
		line := strings.ToLower(strings.TrimSpace(stripEmailQuotePrefix(raw)))
		if strings.Contains(line, "forwarded message") ||
			strings.Contains(line, "begin forwarded") ||
			strings.Contains(line, "original message") ||
			outlookReplyHeader(lines, index) {
			return true
		}
	}
	return false
}

func prepareForwardForkMessage(message Message) Message {
	body := message.RawBody
	if body == "" {
		body = message.Body
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	for index, raw := range lines {
		line := strings.ToLower(strings.TrimSpace(stripEmailQuotePrefix(raw)))
		boundary := strings.Contains(line, "forwarded message") ||
			strings.Contains(line, "begin forwarded") ||
			strings.Contains(line, "original message") ||
			outlookReplyHeader(lines, index)
		if !boundary {
			continue
		}
		contribution := strings.TrimSpace(strings.Join(lines[:index], "\n"))
		if contribution != "" {
			message.Body = contribution
			message.ConversationReferences = nil
			return message
		}
	}
	message.Body = body
	message, _ = prepareInboundMessage(message)
	return message
}

func forwardContextBefore(lines []string, referenceLine int) bool {
	start := referenceLine - 60
	if start < 0 {
		start = 0
	}
	headers := map[string]bool{}
	for _, raw := range lines[start:referenceLine] {
		line := strings.ToLower(strings.TrimSpace(stripEmailQuotePrefix(raw)))
		if strings.Contains(line, "forwarded message") ||
			strings.Contains(line, "begin forwarded") ||
			strings.Contains(line, "original message") {
			return true
		}
		for _, field := range []string{"from:", "to:", "date:", "sent:", "subject:"} {
			if strings.HasPrefix(line, field) {
				headers[field] = true
			}
		}
	}
	return headers["from:"] && headers["to:"] && headers["subject:"] &&
		(headers["date:"] || headers["sent:"])
}

func embeddedMessageBodies(raw []byte, depth int) ([]string, error) {
	if depth >= maxForwardMIMEDepth {
		return nil, fmt.Errorf("nested message depth exceeds %d", maxForwardMIMEDepth)
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return mimeEntityBodies(message.Header, message.Body, depth)
}

type mimeHeader interface {
	Get(string) string
}

func mimeEntityBodies(header mimeHeader, body io.Reader, depth int) ([]string, error) {
	mediaType, parameters, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	decoded := decodeTransferEncoding(body, header.Get("Content-Transfer-Encoding"))
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		boundary := parameters["boundary"]
		if boundary == "" {
			return nil, fmt.Errorf("multipart message has no boundary")
		}
		reader := multipart.NewReader(decoded, boundary)
		var bodies []string
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			found, err := mimeEntityBodies(part.Header, part, depth)
			part.Close()
			if err != nil {
				return nil, err
			}
			bodies = append(bodies, found...)
		}
		return bodies, nil
	}
	contents, err := io.ReadAll(decoded)
	if err != nil {
		return nil, err
	}
	if label := strings.TrimSpace(parameters["charset"]); label != "" && !strings.EqualFold(label, "utf-8") {
		reader, err := charset.NewReaderLabel(label, bytes.NewReader(contents))
		if err != nil {
			return nil, fmt.Errorf("decode charset %s: %w", label, err)
		}
		contents, err = io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
	}
	switch strings.ToLower(mediaType) {
	case "text/plain":
		return []string{string(contents)}, nil
	case "text/html":
		return []string{htmlToText(string(contents))}, nil
	case "message/rfc822":
		return embeddedMessageBodies(contents, depth+1)
	default:
		return nil, nil
	}
}

func decodeTransferEncoding(body io.Reader, encoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}
