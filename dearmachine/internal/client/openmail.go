package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	openMailAPIBaseURL = "https://api.openmail.sh"
	openMailPageSize   = 100
)

var _ Transport = (*OpenMailTransport)(nil)

// OpenMailTransport adapts OpenMail's REST API to the DearMachine transport
// contract. OpenMail read state belongs to a thread, rather than an individual
// message, so MarkProcessed acknowledges the message's complete thread.
type OpenMailTransport struct {
	baseURL       *url.URL
	apiKey        string
	inbox         string
	httpClient    *http.Client
	allowMutation bool

	resolveMu       sync.Mutex
	resolvedInboxID string
	resolvedAddress string
	authLookupTXT   func(context.Context, string) ([]string, error)
	authMu          sync.Mutex
	authenticated   map[string]openMailAuthenticatedMessage
}

type openMailTransportConfig struct {
	BaseURL       string
	APIKey        string
	Inbox         string
	HTTPClient    *http.Client
	AllowMutation bool
}

// NewOpenMailTransport constructs the production OpenMail adapter. The API
// host is deliberately fixed to OpenMail's documented api.openmail.sh host.
func NewOpenMailTransport(inboxID string) (*OpenMailTransport, error) {
	apiKey, err := loadOpenMailCredential()
	if err != nil {
		return nil, err
	}
	return newOpenMailTransport(openMailTransportConfig{
		BaseURL:       openMailAPIBaseURL,
		APIKey:        apiKey,
		Inbox:         inboxID,
		HTTPClient:    http.DefaultClient,
		AllowMutation: true,
	})
}

// ProvisionOpenMailInbox creates an inbox using OpenMail's account API.
func ProvisionOpenMailInbox(ctx context.Context) (Inbox, error) {
	apiKey, err := loadOpenMailCredential()
	if err != nil {
		return Inbox{}, err
	}
	return provisionOpenMailInbox(ctx, openMailTransportConfig{
		BaseURL: openMailAPIBaseURL, APIKey: apiKey, HTTPClient: http.DefaultClient,
	})
}

func provisionOpenMailInbox(ctx context.Context, config openMailTransportConfig) (Inbox, error) {
	config.Inbox = "provisioning"
	transport, err := newOpenMailTransport(config)
	if err != nil {
		return Inbox{}, err
	}
	var inbox openMailInbox
	if err := transport.mutateJSON(ctx, http.MethodPost, "/v1/inboxes", struct{}{}, &inbox); err != nil {
		return Inbox{}, fmt.Errorf("create OpenMail inbox: %w", err)
	}
	address, valid := canonicalOpenMailAddress(inbox.Address)
	if strings.TrimSpace(inbox.ID) == "" || !valid {
		return Inbox{}, fmt.Errorf("create OpenMail inbox: provider returned incomplete inbox metadata")
	}
	return Inbox{Transport: "openmail", ProviderID: strings.TrimSpace(inbox.ID), Address: address}, nil
}

func InspectOpenMailInbox(ctx context.Context, selection string) (Inbox, error) {
	transport, err := NewOpenMailTransport(selection)
	if err != nil {
		return Inbox{}, err
	}
	providerID, err := transport.inboxID(ctx)
	if err != nil {
		return Inbox{}, err
	}
	return Inbox{Transport: "openmail", ProviderID: providerID, Address: transport.resolvedAddress}, nil
}

func newOpenMailTransport(config openMailTransportConfig) (*OpenMailTransport, error) {
	if strings.TrimSpace(config.Inbox) == "" {
		return nil, fmt.Errorf("OpenMail inbox ID or address is required")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("OpenMail API key is required")
	}
	baseURL, err := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" || baseURL.User != nil {
		return nil, fmt.Errorf("invalid OpenMail API base URL")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OpenMailTransport{
		baseURL:       baseURL,
		apiKey:        strings.TrimSpace(config.APIKey),
		inbox:         strings.TrimSpace(config.Inbox),
		httpClient:    httpClient,
		allowMutation: config.AllowMutation,
	}, nil
}

func loadOpenMailCredential() (string, error) {
	if credential := strings.TrimSpace(os.Getenv("OPENMAIL_API_KEY")); credential != "" {
		return credential, nil
	}
	credentialPath := strings.TrimSpace(os.Getenv("OPENMAIL_API_KEY_FILE"))
	usingDefault := credentialPath == ""
	if usingDefault {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("OPENMAIL_API_KEY or OPENMAIL_API_KEY_FILE is required: resolve default credential path: %w", err)
		}
		credentialPath = filepath.Join(home, ".config", "dearmachine", "openmail-api-key")
	}
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		if usingDefault && errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("OPENMAIL_API_KEY or OPENMAIL_API_KEY_FILE is required (optional default: $HOME/.config/dearmachine/openmail-api-key)")
		}
		return "", fmt.Errorf("read OPENMAIL_API_KEY_FILE: %w", err)
	}
	credential := strings.TrimRight(string(contents), "\r\n")
	if strings.TrimSpace(credential) == "" {
		return "", fmt.Errorf("OPENMAIL_API_KEY_FILE is empty")
	}
	if strings.ContainsAny(credential, "\r\n") {
		return "", fmt.Errorf("OPENMAIL_API_KEY_FILE must contain exactly one line")
	}
	return credential, nil
}

type openMailInbox struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}

type openMailPolicyRule struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Direction string `json:"direction"`
}

type openMailPolicyMode struct {
	Mode      string `json:"mode"`
	Direction string `json:"direction"`
}

type openMailThread struct {
	ID            string    `json:"id"`
	Subject       string    `json:"subject"`
	IsRead        bool      `json:"isRead"`
	LastMessageAt time.Time `json:"lastMessageAt"`
	CreatedAt     time.Time `json:"createdAt"`
}

type openMailAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type openMailMessage struct {
	InboxID      string   `json:"inboxId"`
	RFCMessageID string   `json:"rfcMessageId"`
	InReplyTo    string   `json:"inReplyTo"`
	References   []string `json:"references"`
	// RawURL is only a presence signal that GET /v1/messages/{id}/raw serves
	// this message's exact received bytes, before OpenMail's own MIME
	// parsing. Its value is provider-controlled and is never used to build a
	// request; see fetchOpenMailRawMessage.
	RawURL       string               `json:"rawUrl"`
	ID           string               `json:"id"`
	ThreadID     string               `json:"threadId"`
	Direction    string               `json:"direction"`
	FromAddr     string               `json:"fromAddr"`
	ToAddr       string               `json:"toAddr"`
	HeaderTo     string               `json:"headerTo"`
	DeliveryRole string               `json:"deliveryRole"`
	CC           []string             `json:"cc"`
	BCC          []string             `json:"bcc"`
	Subject      string               `json:"subject"`
	BodyText     string               `json:"bodyText"`
	BodyHTML     string               `json:"bodyHtml"`
	Attachments  []openMailAttachment `json:"attachments"`
	Status       string               `json:"status"`
	CreatedAt    time.Time            `json:"createdAt"`
}

type openMailList[T any] struct {
	Data  []T `json:"data"`
	Total int `json:"total"`
}

type openMailThreadMessages struct {
	ThreadID string            `json:"threadId"`
	Subject  string            `json:"subject"`
	IsRead   bool              `json:"isRead"`
	Data     []openMailMessage `json:"data"`
}

func (transport *OpenMailTransport) Poll(ctx context.Context) ([]Message, error) {
	inboxID, err := transport.inboxID(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []Message
	for offset := 0; ; {
		query := url.Values{
			"limit":   {strconv.Itoa(openMailPageSize)},
			"offset":  {strconv.Itoa(offset)},
			"is_read": {"false"},
		}
		var page openMailList[openMailThread]
		if err := transport.getJSON(
			ctx,
			"/v1/inboxes/"+url.PathEscape(inboxID)+"/threads?"+query.Encode(),
			&page,
		); err != nil {
			return nil, fmt.Errorf("list unread OpenMail threads: %w", err)
		}
		for _, summary := range page.Data {
			thread, err := transport.rawThread(ctx, summary.ID)
			if err != nil {
				return nil, err
			}
			sort.SliceStable(thread.Data, func(left, right int) bool {
				return thread.Data[left].CreatedAt.Before(thread.Data[right].CreatedAt)
			})
			if len(thread.Data) == 0 {
				continue
			}
			latest := thread.Data[len(thread.Data)-1]
			if latest.Direction != "inbound" {
				continue
			}
			candidates = append(candidates, transport.normalize(latest, thread.IsRead))
		}
		offset += len(page.Data)
		if len(page.Data) == 0 || (page.Total > 0 && offset >= page.Total) || len(page.Data) < openMailPageSize {
			break
		}
	}
	return finalizePolledMessages(candidates), nil
}

func (transport *OpenMailTransport) Thread(ctx context.Context, threadID string) ([]Message, error) {
	thread, err := transport.rawThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(thread.Data))
	for _, message := range thread.Data {
		messages = append(messages, transport.normalize(message, thread.IsRead))
	}
	sortMessages(messages)
	return messages, nil
}

func (transport *OpenMailTransport) rawThread(ctx context.Context, threadID string) (openMailThreadMessages, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return openMailThreadMessages{}, fmt.Errorf("OpenMail thread ID is required")
	}
	if _, err := transport.inboxID(ctx); err != nil {
		return openMailThreadMessages{}, err
	}
	var thread openMailThreadMessages
	if err := transport.getJSON(
		ctx,
		"/v1/threads/"+url.PathEscape(threadID)+"/messages",
		&thread,
	); err != nil {
		return openMailThreadMessages{}, fmt.Errorf("get OpenMail thread %s: %w", threadID, err)
	}
	return thread, nil
}

func (transport *OpenMailTransport) Message(ctx context.Context, messageID string) (Message, error) {
	raw, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return Message{}, err
	}
	thread, err := transport.rawThread(ctx, raw.ThreadID)
	if err != nil {
		return Message{}, err
	}
	return transport.normalize(raw, thread.IsRead), nil
}

func (transport *OpenMailTransport) rawMessage(ctx context.Context, messageID string) (openMailMessage, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return openMailMessage{}, fmt.Errorf("OpenMail message ID is required")
	}
	inboxID, err := transport.inboxID(ctx)
	if err != nil {
		return openMailMessage{}, err
	}
	for offset := 0; ; {
		query := url.Values{
			"limit":  {strconv.Itoa(openMailPageSize)},
			"offset": {strconv.Itoa(offset)},
		}
		var page openMailList[openMailMessage]
		if err := transport.getJSON(
			ctx,
			"/v1/inboxes/"+url.PathEscape(inboxID)+"/messages?"+query.Encode(),
			&page,
		); err != nil {
			return openMailMessage{}, fmt.Errorf("list OpenMail messages: %w", err)
		}
		for _, message := range page.Data {
			if message.ID == messageID {
				return message, nil
			}
		}
		offset += len(page.Data)
		if len(page.Data) == 0 || (page.Total > 0 && offset >= page.Total) || len(page.Data) < openMailPageSize {
			break
		}
	}
	return openMailMessage{}, fmt.Errorf("OpenMail message %s was not found", messageID)
}

func (transport *OpenMailTransport) Reply(
	ctx context.Context,
	messageID string,
	payload ReplyPayload,
	idempotencyKey string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", beforeReplySubmission(err)
	}
	if err := transport.requireMutationOptIn("reply"); err != nil {
		return "", beforeReplySubmission(err)
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return "", beforeReplySubmission(fmt.Errorf("reply to OpenMail message %s: idempotency key is required", messageID))
	}
	inbound, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return "", beforeReplySubmission(err)
	}
	if inbound.Direction != "inbound" {
		return "", beforeReplySubmission(fmt.Errorf("OpenMail message %s is not an allowed inbound user turn", messageID))
	}
	recipient, ok := canonicalOpenMailAddress(inbound.FromAddr)
	if !ok {
		return "", beforeReplySubmission(fmt.Errorf("OpenMail message %s has no reply recipient", messageID))
	}
	if len(payload.To) > 0 {
		if len(payload.To) != 1 {
			return "", beforeReplySubmission(fmt.Errorf("reply to OpenMail message %s: exactly one primary recipient is required", messageID))
		}
		var valid bool
		recipient, valid = canonicalOpenMailAddress(payload.To[0])
		if !valid {
			return "", beforeReplySubmission(fmt.Errorf("reply to OpenMail message %s: primary recipient is invalid", messageID))
		}
	}
	if len(payload.BCC) > 0 {
		return "", beforeReplySubmission(fmt.Errorf("OpenMail reply does not support BCC"))
	}
	body := payload.Text
	if body == "" {
		body = payload.HTML
	}
	if body == "" {
		return "", beforeReplySubmission(fmt.Errorf("reply to OpenMail message %s: body is required", messageID))
	}
	inboxID, err := transport.inboxID(ctx)
	if err != nil {
		return "", beforeReplySubmission(err)
	}
	request, err := transport.replyRequest(
		ctx,
		inboxID,
		recipient,
		inbound.ThreadID,
		body,
		payload,
		idempotencyKey,
	)
	if err != nil {
		return "", beforeReplySubmission(fmt.Errorf("prepare OpenMail reply to message %s: %w", messageID, err))
	}
	// All work above is read-only preparation; no send has been attempted.
	if err := ctx.Err(); err != nil {
		return "", beforeReplySubmission(err)
	}
	var receipt struct {
		MessageID string `json:"messageId"`
		ThreadID  string `json:"threadId"`
		Status    string `json:"status"`
	}
	// This nonempty send body must not be replayed by net/http after a lost response.
	request.GetBody = nil
	if err := transport.doJSON(request, &receipt); err != nil {
		return "", fmt.Errorf("reply to OpenMail message %s: %w", messageID, err)
	}
	if receipt.MessageID == "" {
		return "", fmt.Errorf("reply to OpenMail message %s returned no receipt", messageID)
	}
	return receipt.MessageID, nil
}

func (transport *OpenMailTransport) replyRequest(
	ctx context.Context,
	inboxID, recipient, threadID, body string,
	payload ReplyPayload,
	idempotencyKey string,
) (*http.Request, error) {
	target := transport.endpoint("/v1/inboxes/" + url.PathEscape(inboxID) + "/send")
	if len(payload.Files) == 0 {
		requestBody := struct {
			To           string   `json:"to"`
			CC           []string `json:"cc,omitempty"`
			Body         string   `json:"body"`
			BodyHTML     string   `json:"bodyHtml,omitempty"`
			ThreadID     string   `json:"threadId"`
			IncludeQuote bool     `json:"includeQuote"`
		}{
			To:           recipient,
			CC:           append([]string(nil), payload.CC...),
			Body:         body,
			BodyHTML:     payload.HTML,
			ThreadID:     threadID,
			IncludeQuote: payload.IncludeQuotedContent,
		}
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", idempotencyKey)
		transport.authorize(request)
		return request, nil
	}
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	fields := map[string]string{
		"to":           recipient,
		"body":         body,
		"threadId":     threadID,
		"includeQuote": strconv.FormatBool(payload.IncludeQuotedContent),
	}
	if payload.HTML != "" {
		fields["bodyHtml"] = payload.HTML
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, err
		}
	}
	for _, address := range payload.CC {
		if err := writer.WriteField("cc", address); err != nil {
			return nil, err
		}
	}
	for _, file := range payload.Files {
		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Disposition", fmt.Sprintf(
			`form-data; name="attachments"; filename="%s"`,
			escapeMultipartFilename(file.Filename),
		))
		if file.ContentType != "" {
			partHeader.Set("Content-Type", file.ContentType)
		}
		part, err := writer.CreatePart(partHeader)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(file.Contents); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, &encoded)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Idempotency-Key", idempotencyKey)
	transport.authorize(request)
	return request, nil
}

func escapeMultipartFilename(filename string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, `\"`, "\r", "", "\n", "").Replace(filename)
}

func (transport *OpenMailTransport) ReplyReceipt(
	ctx context.Context,
	message Message,
	recipient string,
) (string, bool, error) {
	messages, err := transport.Thread(ctx, message.ThreadID)
	if err != nil {
		return "", false, err
	}
	foundInbound := false
	for _, candidate := range messages {
		if candidate.MessageID == message.MessageID {
			foundInbound = true
			continue
		}
		if foundInbound && containsFold(candidate.Labels, "sent") &&
			containsMessageAddress(candidate.To, replyReceiptRecipient(message, recipient)) {
			return candidate.MessageID, true, nil
		}
	}
	return "", false, nil
}

func (transport *OpenMailTransport) MarkProcessed(ctx context.Context, messageID string) error {
	if err := transport.requireMutationOptIn("mark processed"); err != nil {
		return err
	}
	message, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return err
	}
	if message.Direction != "inbound" {
		return fmt.Errorf("OpenMail message %s is not an allowed inbound user turn", messageID)
	}
	encoded, err := json.Marshal(struct {
		IsRead bool `json:"is_read"`
	}{IsRead: true})
	if err != nil {
		return err
	}
	request, err := transport.request(
		ctx,
		http.MethodPatch,
		"/v1/threads/"+url.PathEscape(message.ThreadID),
		bytes.NewReader(encoded),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	var response struct {
		OK bool `json:"ok"`
	}
	if err := transport.doJSON(request, &response); err != nil {
		return fmt.Errorf("mark OpenMail message %s processed: %w", messageID, err)
	}
	if !response.OK {
		return fmt.Errorf("mark OpenMail message %s processed: update was not acknowledged", messageID)
	}
	return nil
}

func (transport *OpenMailTransport) FetchAttachment(
	ctx context.Context,
	attachmentID string,
	maxBytes int64,
) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("fetch OpenMail attachment: maximum size must not be negative")
	}
	messageID, filename, err := decodeOpenMailAttachmentID(attachmentID)
	if err != nil {
		return nil, err
	}
	transport.authMu.Lock()
	verified := transport.authenticated[messageID]
	attachment, authenticated := verified.attachments[filename]
	transport.authMu.Unlock()
	// Never fall back to provider metadata when authentication has not run
	// (including after restart or cache eviction). The router authenticates
	// messages again before exposing them for attachment staging.
	if !authenticated {
		return nil, ErrMessageUnauthenticated
	}
	if attachment.size > maxBytes {
		return nil, ErrAttachmentTooLarge
	}
	message, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if messageFingerprint(transport.normalize(message, false)) != verified.fingerprint {
		return nil, ErrMessageUnauthenticated
	}
	found := false
	for _, attachment := range message.Attachments {
		if attachment.Filename == filename {
			found = true
			if attachment.SizeBytes > maxBytes {
				return nil, fmt.Errorf("fetch OpenMail attachment %s: %w", attachmentID, ErrAttachmentTooLarge)
			}
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("OpenMail attachment %s was not found on its message", attachmentID)
	}
	request, err := transport.request(
		ctx,
		http.MethodGet,
		"/v1/attachments/"+url.PathEscape(messageID)+"/"+url.PathEscape(filename),
		nil,
	)
	if err != nil {
		return nil, err
	}
	client := *transport.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("get OpenMail attachment %s: %w", attachmentID, err)
	}
	if response.StatusCode == http.StatusFound || response.StatusCode == http.StatusTemporaryRedirect {
		location := response.Header.Get("Location")
		response.Body.Close()
		downloadURL, err := request.URL.Parse(location)
		if err != nil || !sameOrigin(transport.baseURL, downloadURL) {
			return nil, fmt.Errorf("get OpenMail attachment %s: refused redirect outside configured OpenMail API host", attachmentID)
		}
		download, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("prepare OpenMail attachment %s download: %w", attachmentID, err)
		}
		response, err = client.Do(download)
		if err != nil {
			return nil, fmt.Errorf("download OpenMail attachment %s: %w", attachmentID, err)
		}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, openMailStatusError(response)
	}
	if response.ContentLength > maxBytes {
		return nil, fmt.Errorf("download OpenMail attachment %s: %w", attachmentID, ErrAttachmentTooLarge)
	}
	// The authenticated MIME size also bounds reads when callers supply a
	// much larger limit (or the largest possible int64).
	contents, err := io.ReadAll(io.LimitReader(response.Body, attachment.size+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenMail attachment %s: %w", attachmentID, err)
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf("download OpenMail attachment %s: %w", attachmentID, ErrAttachmentTooLarge)
	}
	if int64(len(contents)) != attachment.size || sha256.Sum256(contents) != attachment.digest {
		return nil, ErrMessageUnauthenticated
	}
	return contents, nil
}

func (transport *OpenMailTransport) normalize(message openMailMessage, isRead bool) Message {
	from := message.FromAddr
	if address, ok := canonicalOpenMailAddress(from); ok {
		from = address
	}
	to := openMailAddresses(message.ToAddr)
	if message.Direction == "inbound" && strings.TrimSpace(message.HeaderTo) != "" {
		to = openMailAddresses(message.HeaderTo)
	}
	labels := []string{message.Direction}
	if message.Direction == "outbound" {
		labels = append(labels, "sent")
	}
	if isRead {
		labels = append(labels, "read")
	} else {
		labels = append(labels, "unread")
	}
	attachments := make([]AttachmentRef, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		attachments = append(attachments, AttachmentRef{
			AttachmentID: encodeOpenMailAttachmentID(message.ID, attachment.Filename),
			Filename:     attachment.Filename,
			ContentType:  attachment.ContentType,
			SizeBytes:    attachment.SizeBytes,
		})
	}
	body := message.BodyText
	if strings.TrimSpace(body) == "" {
		body = htmlToText(message.BodyHTML)
	}
	rawBody := body
	body, bodyReferences := stripConversationFooters(body)
	conversationReferences := mergeConversationReferences(
		bodyReferences,
		conversationReferencesInBodies(message.BodyText, htmlToText(message.BodyHTML)),
	)
	readState := MessageReadStateUnread
	if isRead {
		readState = MessageReadStateRead
	}
	return Message{
		MessageID:    message.ID,
		RFCMessageID: message.RFCMessageID,
		InReplyTo:    message.InReplyTo,
		References:   append([]string(nil), message.References...),
		ThreadID:     message.ThreadID,
		From:         from,
		To:           to,
		CC:           append([]string(nil), message.CC...),
		BCC:          append([]string(nil), message.BCC...),
		Delivery: normalizeMessageDelivery(
			transport.resolvedInboxID, transport.resolvedAddress,
			to, message.CC, message.BCC, message.DeliveryRole, readState,
		),
		Timestamp:              message.CreatedAt,
		CreatedAt:              message.CreatedAt,
		Subject:                message.Subject,
		Body:                   body,
		RawBody:                rawBody,
		RawHTML:                message.BodyHTML,
		ConversationReferences: conversationReferences,
		Labels:                 labels,
		Attachments:            attachments,
	}
}

func (transport *OpenMailTransport) requireMutationOptIn(operation string) error {
	if transport.allowMutation {
		return nil
	}
	return fmt.Errorf("OpenMail %s is disabled by adapter configuration", operation)
}

func (transport *OpenMailTransport) inboxID(ctx context.Context) (string, error) {
	transport.resolveMu.Lock()
	defer transport.resolveMu.Unlock()
	if transport.resolvedInboxID != "" {
		return transport.resolvedInboxID, nil
	}
	if !strings.Contains(transport.inbox, "@") {
		var inbox openMailInbox
		if err := transport.getJSON(
			ctx,
			"/v1/inboxes/"+url.PathEscape(transport.inbox),
			&inbox,
		); err != nil {
			return "", fmt.Errorf("resolve OpenMail inbox ID: %w", err)
		}
		address, ok := canonicalOpenMailAddress(inbox.Address)
		if !ok || inbox.ID == "" {
			return "", fmt.Errorf("resolve OpenMail inbox ID: API returned incomplete inbox metadata")
		}
		transport.resolvedInboxID = inbox.ID
		transport.resolvedAddress = address
		return transport.resolvedInboxID, nil
	}
	want, ok := canonicalOpenMailAddress(transport.inbox)
	if !ok {
		return "", fmt.Errorf("invalid OpenMail inbox address")
	}
	for offset := 0; ; {
		query := url.Values{
			"limit":  {strconv.Itoa(openMailPageSize)},
			"offset": {strconv.Itoa(offset)},
		}
		var page openMailList[openMailInbox]
		if err := transport.getJSON(ctx, "/v1/inboxes?"+query.Encode(), &page); err != nil {
			return "", fmt.Errorf("resolve OpenMail inbox address: %w", err)
		}
		for _, inbox := range page.Data {
			address, valid := canonicalOpenMailAddress(inbox.Address)
			if valid && strings.EqualFold(address, want) {
				transport.resolvedInboxID = inbox.ID
				transport.resolvedAddress = address
				return transport.resolvedInboxID, nil
			}
		}
		offset += len(page.Data)
		if len(page.Data) == 0 || (page.Total > 0 && offset >= page.Total) || len(page.Data) < openMailPageSize {
			break
		}
	}
	return "", fmt.Errorf("OpenMail inbox address %s was not found", transport.inbox)
}

func (transport *OpenMailTransport) getJSON(ctx context.Context, path string, target any) error {
	request, err := transport.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return transport.doJSON(request, target)
}

// AuthorizePair applies the same exact-address allowlist policy for every
// OpenMail pair. The local router remains a second fail-closed boundary.
func (transport *OpenMailTransport) AuthorizePair(ctx context.Context, email string) error {
	address, err := canonicalMessageAddress(email)
	if err != nil {
		return fmt.Errorf("authorize OpenMail pair: %w", err)
	}
	inboxID, err := transport.inboxID(ctx)
	if err != nil {
		return err
	}
	path := "/v1/policy/rules?inboxId=" + url.QueryEscape(inboxID)
	for _, direction := range []string{"inbound", "outbound"} {
		rule := openMailPolicyRule{Type: "allow", Value: address, Direction: direction}
		if err := transport.mutateJSON(ctx, http.MethodPost, path, rule, &struct{}{}); err != nil {
			var statusErr *openMailAPIError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusConflict || statusErr.Code != "rule_exists" {
				return fmt.Errorf("authorize OpenMail %s policy: %w", direction, err)
			}
		}
	}
	path = "/v1/policy/mode?inboxId=" + url.QueryEscape(inboxID)
	for _, direction := range []string{"inbound", "outbound"} {
		mode := openMailPolicyMode{Mode: "allowlist", Direction: direction}
		if err := transport.mutateJSON(ctx, http.MethodPut, path, mode, &struct{}{}); err != nil {
			return fmt.Errorf("enable OpenMail %s allowlist: %w", direction, err)
		}
	}
	return nil
}

func (transport *OpenMailTransport) mutateJSON(ctx context.Context, method, path string, body, target any) error {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(body); err != nil {
		return err
	}
	request, err := transport.request(ctx, method, path, &encoded)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	return transport.doJSON(request, target)
}

func (transport *OpenMailTransport) request(
	ctx context.Context,
	method, path string,
	body io.Reader,
) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, transport.endpoint(path), body)
	if err != nil {
		return nil, err
	}
	transport.authorize(request)
	return request, nil
}

func (transport *OpenMailTransport) endpoint(path string) string {
	return strings.TrimRight(transport.baseURL.String(), "/") + "/" + strings.TrimLeft(path, "/")
}

func (transport *OpenMailTransport) authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+transport.apiKey)
}

func (transport *OpenMailTransport) doJSON(request *http.Request, target any) error {
	client := *transport.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openMailStatusError(response)
	}
	if response.StatusCode == http.StatusNoContent && target == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode OpenMail response: %w", err)
	}
	return nil
}

type openMailAPIError struct {
	StatusCode int
	Status     string
	Code       string
	Message    string
}

func (err *openMailAPIError) Error() string {
	detail := strings.TrimSpace(err.Code)
	if message := strings.TrimSpace(err.Message); message != "" {
		if detail != "" {
			detail += ": "
		}
		detail += message
	}
	if detail == "" {
		return fmt.Sprintf("OpenMail API returned %s", err.Status)
	}
	return fmt.Sprintf("OpenMail API returned %s (%s)", err.Status, detail)
}

func openMailStatusError(response *http.Response) error {
	var apiError struct {
		Code    string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&apiError)
	return &openMailAPIError{
		StatusCode: response.StatusCode, Status: response.Status,
		Code: strings.TrimSpace(apiError.Code), Message: strings.TrimSpace(apiError.Message),
	}
}

func encodeOpenMailAttachmentID(messageID, filename string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(messageID)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(filename))
}

func decodeOpenMailAttachmentID(attachmentID string) (string, string, error) {
	left, right, found := strings.Cut(attachmentID, ".")
	if !found || left == "" || right == "" {
		return "", "", fmt.Errorf("invalid OpenMail attachment ID")
	}
	messageID, err := base64.RawURLEncoding.DecodeString(left)
	if err != nil {
		return "", "", fmt.Errorf("invalid OpenMail attachment ID")
	}
	filename, err := base64.RawURLEncoding.DecodeString(right)
	if err != nil || len(messageID) == 0 || len(filename) == 0 {
		return "", "", fmt.Errorf("invalid OpenMail attachment ID")
	}
	return string(messageID), string(filename), nil
}

func canonicalOpenMailAddress(raw string) (string, bool) {
	address, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(address.Address) == "" {
		return "", false
	}
	return strings.ToLower(address.Address), true
}

func openMailAddresses(raw string) []string {
	addresses, err := mail.ParseAddressList(raw)
	if err != nil {
		if address, ok := canonicalOpenMailAddress(raw); ok {
			return []string{address}
		}
		return nil
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, strings.ToLower(address.Address))
	}
	return result
}

func appendUniqueAddresses(existing []string, raw ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(raw))
	for _, address := range existing {
		seen[strings.ToLower(address)] = struct{}{}
	}
	for _, value := range raw {
		for _, address := range openMailAddresses(value) {
			if _, ok := seen[address]; ok {
				continue
			}
			seen[address] = struct{}{}
			existing = append(existing, address)
		}
	}
	return existing
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}
