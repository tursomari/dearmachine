package client

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"unicode/utf8"
)

type verifiedMIMEAttachment struct {
	filename    string
	contentType string
	data        []byte
}

type verifiedMIMEContent struct {
	bodyText    string
	bodyHTML    string
	attachments []verifiedMIMEAttachment
}

// parseVerifiedMIME parses raw bytes that have already passed DKIM
// verification. It supports single-part and (nested) multipart messages, and
// fails closed on any structure or encoding it cannot decode losslessly.
func parseVerifiedMIME(raw []byte) (verifiedMIMEContent, error) {
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return verifiedMIMEContent{}, ErrMessageUnauthenticated
	}
	var content verifiedMIMEContent
	if err := collectVerifiedMIMEParts(textproto.MIMEHeader(parsed.Header), parsed.Body, 0, &content); err != nil {
		return verifiedMIMEContent{}, err
	}
	return content, nil
}

func collectVerifiedMIMEParts(header textproto.MIMEHeader, body io.Reader, depth int, content *verifiedMIMEContent) error {
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
			if err := collectVerifiedMIMEParts(part.Header, part, depth+1, content); err != nil {
				return err
			}
		}
		return nil
	}
	decoded, err := decodeVerifiedMIMEPart(header, body)
	if err != nil {
		return err
	}
	if len(decoded) > maxAuthenticationMessageBytes {
		return ErrMessageUnauthenticated
	}
	filename := verifiedMIMEFilename(header, params)
	disposition, _, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	switch {
	case disposition != "attachment" && filename == "" && mediaType == "text/plain":
		text, err := decodeVerifiedMIMEText(decoded, params["charset"])
		if err != nil {
			return err
		}
		content.bodyText += text
	case disposition != "attachment" && filename == "" && mediaType == "text/html":
		text, err := decodeVerifiedMIMEText(decoded, params["charset"])
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
		content.attachments = append(content.attachments, verifiedMIMEAttachment{
			filename: filename, contentType: mediaType, data: decoded,
		})
	}
	return nil
}

func verifiedMIMEFilename(header textproto.MIMEHeader, contentTypeParams map[string]string) string {
	if _, dispositionParams, err := mime.ParseMediaType(header.Get("Content-Disposition")); err == nil {
		if name := strings.TrimSpace(dispositionParams["filename"]); name != "" {
			return name
		}
	}
	return strings.TrimSpace(contentTypeParams["name"])
}

// decodeVerifiedMIMEPart reverses Content-Transfer-Encoding to recover the
// original bytes exactly, so downloaded attachments can be checked against
// digests of the signed content rather than just provider-reported sizes.
func decodeVerifiedMIMEPart(header textproto.MIMEHeader, body io.Reader) ([]byte, error) {
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

// decodeVerifiedMIMEText only accepts unencoded UTF-8 or US-ASCII text, the
// only charsets these adapters can decode losslessly without an external
// charset conversion dependency. Anything else fails closed.
func decodeVerifiedMIMEText(data []byte, charset string) (string, error) {
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

func normalizeMIMELineEndings(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

// mimeTextMatchesReport compares decoded, DKIM-verified MIME text with
// the provider report. Only CRLF/LF representation differences are normalized;
// all content whitespace, including every terminal newline, must match.
func mimeTextMatchesReport(raw, reported string) bool {
	return normalizeMIMELineEndings(raw) == normalizeMIMELineEndings(reported)
}
