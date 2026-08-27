package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sendmux.ai/go/core"
	"sendmux.ai/go/mailbox"
	"sendmux.ai/go/sending"
)

const sendmuxPageSize = 100

var _ Transport = (*SendmuxTransport)(nil)

type SendmuxTransport struct {
	api              sendmuxTransportAPI
	outbound         sendmuxOutboundAPI
	inbox            string
	httpClient       *http.Client
	allowMutation    bool
	allowInsecureURL bool

	resolveMu sync.Mutex
	resolved  sendmuxMailboxInfo
}

type sendmuxTransportConfig struct {
	API              sendmuxTransportAPI
	Outbound         sendmuxOutboundAPI
	Inbox            string
	HTTPClient       *http.Client
	AllowMutation    bool
	AllowInsecureURL bool
}

type sendmuxTransportAPI interface {
	ResolveMailbox(context.Context, string) (sendmuxMailboxInfo, error)
	ListUnread(context.Context, string) ([]string, error)
	Message(context.Context, string, string) (sendmuxRawMessage, error)
	Thread(context.Context, string, string) ([]sendmuxRawMessage, error)
	Send(context.Context, string, sendmuxSendRequest, string) (string, error)
	MarkSeen(context.Context, string, string) error
}

// sendmuxOutboundAPI is deliberately separate from the mailbox API: Sendmux
// issues send credentials independently of mailbox credentials.
type sendmuxOutboundAPI interface {
	Send(context.Context, string, sendmuxSendRequest, string) (string, error)
}

type sendmuxMailboxInfo struct {
	ID     string
	Email  string
	Status string
}

type sendmuxRawAttachment struct {
	ID          string
	Filename    string
	ContentType string
	SizeBytes   int64
	DownloadURL string
}

type sendmuxRawMessage struct {
	ID            string
	ThreadID      string
	From          string
	To            []string
	CC            []string
	BCC           []string
	ReplyTo       []string
	Subject       string
	Text          string
	HTML          string
	ReceivedAt    time.Time
	SentAt        time.Time
	Seen          bool
	Keywords      []string
	Attachments   []sendmuxRawAttachment
	InReplyTo     string
	References    []string
	RFCMessageIDs []string
}

type sendmuxSendFile struct {
	Filename    string
	ContentType string
	Content     string
}

type sendmuxSendRequest struct {
	ReplyToMessageID string
	To               []string
	Subject          string
	Text             string
	HTML             string
	Files            []sendmuxSendFile
	CustomHeaders    map[string]string
}

func NewSendmuxTransport(inboxID string) (*SendmuxTransport, error) {
	apiKey, err := loadSendmuxCredential()
	if err != nil {
		return nil, err
	}
	api, err := newSendmuxSDKAPI(apiKey)
	if err != nil {
		return nil, fmt.Errorf("create Sendmux mailbox client: %w", err)
	}
	config := sendmuxTransportConfig{
		API: api, Inbox: inboxID, HTTPClient: http.DefaultClient,
		AllowMutation: os.Getenv("DEARMACHINE_LIVE_SENDMUX") == "1" &&
			os.Getenv("DEARMACHINE_LIVE_SENDMUX_APPLY") == "1",
	}
	if sendAPIKey, configured, err := loadSendmuxSendCredential(); err != nil {
		return nil, err
	} else if configured {
		outbound, err := newSendmuxSDKSendingAPI(sendAPIKey)
		if err != nil {
			return nil, fmt.Errorf("create Sendmux sending client: %w", err)
		}
		config.Outbound = outbound
	}
	return newSendmuxTransport(config)
}

func newSendmuxTransport(config sendmuxTransportConfig) (*SendmuxTransport, error) {
	if strings.TrimSpace(config.Inbox) == "" {
		return nil, fmt.Errorf("Sendmux mailbox ID or address is required")
	}
	if config.API == nil {
		return nil, fmt.Errorf("Sendmux mailbox API is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &SendmuxTransport{
		api: config.API, outbound: config.Outbound, inbox: strings.TrimSpace(config.Inbox), httpClient: httpClient,
		allowMutation: config.AllowMutation, allowInsecureURL: config.AllowInsecureURL,
	}, nil
}

func loadSendmuxCredential() (string, error) {
	if credential := strings.TrimSpace(os.Getenv("SENDMUX_MAILBOX_API_KEY")); credential != "" {
		return credential, nil
	}
	credentialPath := strings.TrimSpace(os.Getenv("SENDMUX_MAILBOX_API_KEY_FILE"))
	usingDefault := credentialPath == ""
	if usingDefault {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("SENDMUX_MAILBOX_API_KEY or SENDMUX_MAILBOX_API_KEY_FILE is required: resolve default credential path: %w", err)
		}
		credentialPath = filepath.Join(home, ".config", "dearmachine", "sendmux-api-key")
	}
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		if usingDefault && errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("SENDMUX_MAILBOX_API_KEY or SENDMUX_MAILBOX_API_KEY_FILE is required (optional default: $HOME/.config/dearmachine/sendmux-api-key)")
		}
		return "", fmt.Errorf("read SENDMUX_MAILBOX_API_KEY_FILE: %w", err)
	}
	credential := strings.TrimRight(string(contents), "\r\n")
	if strings.TrimSpace(credential) == "" {
		return "", fmt.Errorf("SENDMUX_MAILBOX_API_KEY_FILE is empty")
	}
	if strings.ContainsAny(credential, "\r\n") {
		return "", fmt.Errorf("SENDMUX_MAILBOX_API_KEY_FILE must contain exactly one line")
	}
	return credential, nil
}

// loadSendmuxSendCredential reads an explicitly configured Sending API key.
// Keeping it optional preserves mailbox-key-only deployments, while avoiding
// accidentally treating a mailbox credential as a send credential.
func loadSendmuxSendCredential() (credential string, configured bool, err error) {
	if credential = strings.TrimSpace(os.Getenv("SENDMUX_SEND_API_KEY")); credential != "" {
		return credential, true, nil
	}
	credentialPath := strings.TrimSpace(os.Getenv("SENDMUX_SEND_API_KEY_FILE"))
	if credentialPath == "" {
		return "", false, nil
	}
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		return "", true, fmt.Errorf("read SENDMUX_SEND_API_KEY_FILE: %w", err)
	}
	credential = strings.TrimRight(string(contents), "\r\n")
	if strings.TrimSpace(credential) == "" {
		return "", true, fmt.Errorf("SENDMUX_SEND_API_KEY_FILE is empty")
	}
	if strings.ContainsAny(credential, "\r\n") {
		return "", true, fmt.Errorf("SENDMUX_SEND_API_KEY_FILE must contain exactly one line")
	}
	return credential, true, nil
}

func (transport *SendmuxTransport) Poll(ctx context.Context) ([]Message, error) {
	mailboxInfo, err := transport.mailbox(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := transport.api.ListUnread(ctx, mailboxInfo.ID)
	if err != nil {
		return nil, fmt.Errorf("list unread Sendmux messages: %w", err)
	}
	messages := make([]Message, 0, len(ids))
	for _, id := range ids {
		raw, err := transport.api.Message(ctx, mailboxInfo.ID, id)
		if err != nil {
			return nil, fmt.Errorf("get Sendmux message %s: %w", id, err)
		}
		messages = append(messages, transport.normalize(raw))
	}
	sortMessages(messages)
	return messages, nil
}

func (transport *SendmuxTransport) Thread(ctx context.Context, threadID string) ([]Message, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("Sendmux thread ID is required")
	}
	mailboxInfo, err := transport.mailbox(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := transport.api.Thread(ctx, mailboxInfo.ID, threadID)
	if err != nil {
		return nil, fmt.Errorf("get Sendmux thread %s: %w", threadID, err)
	}
	messages := make([]Message, 0, len(raw))
	for _, message := range raw {
		messages = append(messages, transport.normalize(message))
	}
	sortMessages(messages)
	return messages, nil
}

func (transport *SendmuxTransport) Message(ctx context.Context, messageID string) (Message, error) {
	raw, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return Message{}, err
	}
	return transport.normalize(raw), nil
}

func (transport *SendmuxTransport) rawMessage(ctx context.Context, messageID string) (sendmuxRawMessage, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return sendmuxRawMessage{}, fmt.Errorf("Sendmux message ID is required")
	}
	mailboxInfo, err := transport.mailbox(ctx)
	if err != nil {
		return sendmuxRawMessage{}, err
	}
	raw, err := transport.api.Message(ctx, mailboxInfo.ID, messageID)
	if err != nil {
		return sendmuxRawMessage{}, fmt.Errorf("get Sendmux message %s: %w", messageID, err)
	}
	return raw, nil
}

func (transport *SendmuxTransport) Reply(ctx context.Context, messageID string, payload ReplyPayload, idempotencyKey string) (string, error) {
	if err := transport.requireMutationOptIn("reply"); err != nil {
		return "", err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return "", fmt.Errorf("reply to Sendmux message %s: idempotency key is required", messageID)
	}
	if len(idempotencyKey) > 255 {
		return "", fmt.Errorf("reply to Sendmux message %s: idempotency key exceeds 255 characters", messageID)
	}
	inbound, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return "", err
	}
	mailboxInfo, err := transport.mailbox(ctx)
	if err != nil {
		return "", err
	}
	recipients := inbound.ReplyTo
	if len(recipients) == 0 {
		recipients = []string{inbound.From}
	}
	request := sendmuxSendRequest{
		ReplyToMessageID: messageID,
		To:               append([]string(nil), recipients...),
		Subject:          sendmuxReplySubject(inbound.Subject),
		Text:             payload.Text,
		HTML:             sendmuxReplyHTML(payload.Text, payload.HTML),
	}
	if strings.TrimSpace(request.Text) == "" && strings.TrimSpace(request.HTML) == "" {
		return "", fmt.Errorf("reply to Sendmux message %s: body is required", messageID)
	}
	for _, file := range payload.Files {
		request.Files = append(request.Files, sendmuxSendFile{
			Filename: file.Filename, ContentType: file.ContentType,
			Content: base64.StdEncoding.EncodeToString(file.Contents),
		})
	}
	var receipt string
	if transport.outbound != nil {
		receipt, err = transport.outbound.Send(ctx, mailboxInfo.Email, request, idempotencyKey)
	} else {
		receipt, err = transport.api.Send(ctx, mailboxInfo.ID, request, idempotencyKey)
	}
	if err != nil {
		return "", fmt.Errorf("reply to Sendmux message %s: %w", messageID, err)
	}
	if strings.TrimSpace(receipt) == "" {
		return "", fmt.Errorf("reply to Sendmux message %s returned no receipt", messageID)
	}
	return receipt, nil
}

// sendmuxReplyHTML ensures the Sending API's required html_body is present
// when Dear Machine produces a plain-text answer.
func sendmuxReplyHTML(text, html string) string {
	if strings.TrimSpace(html) != "" {
		return html
	}
	return "<pre>" + stdhtml.EscapeString(text) + "</pre>"
}

func (transport *SendmuxTransport) ReplyReceipt(ctx context.Context, message Message) (string, bool, error) {
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
		if foundInbound && containsFold(candidate.Labels, "sent") && containsFold(candidate.To, message.From) {
			return candidate.MessageID, true, nil
		}
	}
	return "", false, nil
}

func (transport *SendmuxTransport) MarkProcessed(ctx context.Context, messageID string) error {
	if err := transport.requireMutationOptIn("mark processed"); err != nil {
		return err
	}
	_, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return err
	}
	mailboxInfo, err := transport.mailbox(ctx)
	if err != nil {
		return err
	}
	if err := transport.api.MarkSeen(ctx, mailboxInfo.ID, messageID); err != nil {
		return fmt.Errorf("mark Sendmux message %s processed: %w", messageID, err)
	}
	return nil
}

func (transport *SendmuxTransport) FetchAttachment(ctx context.Context, attachmentID string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("fetch Sendmux attachment: maximum size must not be negative")
	}
	messageID, rawAttachmentID, err := decodeSendmuxAttachmentID(attachmentID)
	if err != nil {
		return nil, err
	}
	message, err := transport.rawMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	var attachment *sendmuxRawAttachment
	for index := range message.Attachments {
		if message.Attachments[index].ID == rawAttachmentID {
			attachment = &message.Attachments[index]
			break
		}
	}
	if attachment == nil || strings.TrimSpace(attachment.DownloadURL) == "" {
		return nil, fmt.Errorf("Sendmux attachment %s was not found on its message", attachmentID)
	}
	if attachment.SizeBytes > maxBytes {
		return nil, fmt.Errorf("fetch Sendmux attachment %s: %w", attachmentID, ErrAttachmentTooLarge)
	}
	downloadURL, err := url.Parse(attachment.DownloadURL)
	if err != nil || downloadURL.User != nil || downloadURL.Host == "" ||
		(downloadURL.Scheme != "https" && !(transport.allowInsecureURL && downloadURL.Scheme == "http")) {
		return nil, fmt.Errorf("fetch Sendmux attachment %s: invalid download URL", attachmentID)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("prepare Sendmux attachment %s download: %w", attachmentID, err)
	}
	client := *transport.httpClient
	client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		if request.URL.Scheme != "https" && !(transport.allowInsecureURL && request.URL.Scheme == "http") {
			return fmt.Errorf("refused non-HTTPS Sendmux attachment redirect")
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download Sendmux attachment %s: %w", attachmentID, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("download Sendmux attachment %s: server returned %s", attachmentID, response.Status)
	}
	if response.ContentLength > maxBytes {
		return nil, fmt.Errorf("download Sendmux attachment %s: %w", attachmentID, ErrAttachmentTooLarge)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Sendmux attachment %s: %w", attachmentID, err)
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf("download Sendmux attachment %s: %w", attachmentID, ErrAttachmentTooLarge)
	}
	return contents, nil
}

func (transport *SendmuxTransport) normalize(raw sendmuxRawMessage) Message {
	body := raw.Text
	if strings.TrimSpace(body) == "" {
		body = htmlToText(raw.HTML)
	}
	body, bodyReferences := stripConversationFooters(body)
	conversationReferences := mergeConversationReferences(
		bodyReferences,
		conversationReferencesInBodies(raw.Text, htmlToText(raw.HTML)),
	)
	labels := append([]string(nil), raw.Keywords...)
	mailboxAddress := strings.ToLower(transport.resolved.Email)
	if strings.EqualFold(raw.From, mailboxAddress) {
		labels = append(labels, "outbound", "sent")
	} else {
		labels = append(labels, "inbound")
	}
	if raw.Seen {
		labels = append(labels, "read")
	} else {
		labels = append(labels, "unread")
	}
	to := append([]string(nil), raw.To...)
	to = appendUniqueStringsFold(to, raw.CC...)
	attachments := make([]AttachmentRef, 0, len(raw.Attachments))
	for _, attachment := range raw.Attachments {
		attachments = append(attachments, AttachmentRef{
			AttachmentID: encodeSendmuxAttachmentID(raw.ID, attachment.ID),
			Filename:     attachment.Filename, ContentType: attachment.ContentType,
			SizeBytes: attachment.SizeBytes,
		})
	}
	return Message{
		MessageID: raw.ID, ThreadID: raw.ThreadID, From: strings.ToLower(raw.From), To: to,
		Timestamp: firstNonZeroTime(raw.ReceivedAt, raw.SentAt), CreatedAt: firstNonZeroTime(raw.ReceivedAt, raw.SentAt),
		Subject: raw.Subject, Body: body, InReplyTo: raw.InReplyTo,
		References:             append([]string(nil), raw.References...),
		ConversationReferences: conversationReferences, Labels: labels, Attachments: attachments,
	}
}

func (transport *SendmuxTransport) requireMutationOptIn(operation string) error {
	if transport.allowMutation {
		return nil
	}
	return fmt.Errorf("Sendmux %s is inspect-only; set both DEARMACHINE_LIVE_SENDMUX=1 and DEARMACHINE_LIVE_SENDMUX_APPLY=1 to allow live mutations", operation)
}

func (transport *SendmuxTransport) mailbox(ctx context.Context) (sendmuxMailboxInfo, error) {
	transport.resolveMu.Lock()
	defer transport.resolveMu.Unlock()
	if transport.resolved.ID != "" {
		return transport.resolved, nil
	}
	resolved, err := transport.api.ResolveMailbox(ctx, transport.inbox)
	if err != nil {
		return sendmuxMailboxInfo{}, fmt.Errorf("resolve Sendmux mailbox: %w", err)
	}
	address, err := mail.ParseAddress(strings.TrimSpace(resolved.Email))
	if err != nil || strings.TrimSpace(resolved.ID) == "" || strings.TrimSpace(address.Address) == "" {
		return sendmuxMailboxInfo{}, fmt.Errorf("resolve Sendmux mailbox: API returned incomplete mailbox metadata")
	}
	if resolved.Status != "" && !strings.EqualFold(resolved.Status, "active") {
		return sendmuxMailboxInfo{}, fmt.Errorf("resolve Sendmux mailbox: mailbox status is %s", resolved.Status)
	}
	if strings.Contains(transport.inbox, "@") && !strings.EqualFold(transport.inbox, address.Address) {
		return sendmuxMailboxInfo{}, fmt.Errorf("resolve Sendmux mailbox: credential grants %s, not %s", address.Address, transport.inbox)
	}
	resolved.Email = strings.ToLower(address.Address)
	transport.resolved = resolved
	return resolved, nil
}

func encodeSendmuxAttachmentID(messageID, attachmentID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(messageID)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(attachmentID))
}

func decodeSendmuxAttachmentID(value string) (string, string, error) {
	left, right, found := strings.Cut(value, ".")
	if !found || left == "" || right == "" {
		return "", "", fmt.Errorf("invalid Sendmux attachment ID")
	}
	messageID, err := base64.RawURLEncoding.DecodeString(left)
	if err != nil {
		return "", "", fmt.Errorf("invalid Sendmux attachment ID")
	}
	attachmentID, err := base64.RawURLEncoding.DecodeString(right)
	if err != nil || len(messageID) == 0 || len(attachmentID) == 0 {
		return "", "", fmt.Errorf("invalid Sendmux attachment ID")
	}
	return string(messageID), string(attachmentID), nil
}

func sendmuxReplySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if len(subject) >= 3 && strings.EqualFold(subject[:3], "re:") {
		return subject
	}
	return "Re: " + subject
}

func lastNonEmpty(values []string) string {
	for index := len(values) - 1; index >= 0; index-- {
		if value := strings.TrimSpace(values[index]); value != "" {
			return value
		}
	}
	return ""
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func appendUniqueStringsFold(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[strings.ToLower(value)] = struct{}{}
	}
	for _, value := range additions {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok || value == "" {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, value)
	}
	return values
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

type sendmuxSDKAPI struct {
	client *mailbox.Client
}

type sendmuxSDKSendingAPI struct {
	client *sending.Client
}

func newSendmuxSDKAPI(apiKey string) (*sendmuxSDKAPI, error) {
	client, err := mailbox.New(apiKey, mailbox.WithRetryOptions(core.RetryOptions{MaxAttempts: 1}))
	if err != nil {
		return nil, err
	}
	return &sendmuxSDKAPI{client: client}, nil
}

func newSendmuxSDKSendingAPI(apiKey string) (*sendmuxSDKSendingAPI, error) {
	client, err := sending.New(apiKey, sending.WithRetryOptions(core.RetryOptions{MaxAttempts: 1}))
	if err != nil {
		return nil, err
	}
	return &sendmuxSDKSendingAPI{client: client}, nil
}

func (api *sendmuxSDKAPI) ResolveMailbox(ctx context.Context, inbox string) (sendmuxMailboxInfo, error) {
	params := mailbox.MailboxGetMeParams{}
	if !strings.Contains(inbox, "@") {
		params.MailboxID = mailbox.NewOptString(inbox)
	}
	response, err := api.client.MailboxGetMe(ctx, params)
	if err != nil {
		return sendmuxMailboxInfo{}, err
	}
	success, ok := response.(*mailbox.MailboxMeItemResponseHeaders)
	if !ok {
		return sendmuxMailboxInfo{}, sendmuxUnexpectedResponse("get mailbox", response)
	}
	data := success.Response.GetData()
	return sendmuxMailboxInfo{ID: data.GetID(), Email: data.GetEmail(), Status: data.GetStatus()}, nil
}

func (api *sendmuxSDKAPI) ListUnread(ctx context.Context, mailboxID string) ([]string, error) {
	var ids []string
	cursor := ""
	for {
		params := mailbox.MailboxListMessagesParams{
			Limit: mailbox.NewOptInt(sendmuxPageSize), IsUnread: mailbox.NewOptBool(true),
			MailboxID: mailbox.NewOptString(mailboxID),
		}
		if cursor != "" {
			params.Cursor = mailbox.NewOptString(cursor)
		}
		response, err := api.client.MailboxListMessages(ctx, params)
		if err != nil {
			return nil, err
		}
		page, ok := response.(*mailbox.MailboxMessageSummaryCursorListResponse)
		if !ok {
			return nil, sendmuxUnexpectedResponse("list messages", response)
		}
		for _, message := range page.GetData() {
			ids = append(ids, message.GetID())
		}
		pagination := page.GetPagination()
		next, hasNext := pagination.GetNextCursor().Get()
		if !pagination.GetHasMore() || !hasNext || next == "" {
			break
		}
		if next == cursor {
			return nil, fmt.Errorf("Sendmux message pagination repeated cursor %q", next)
		}
		cursor = next
	}
	return ids, nil
}

func (api *sendmuxSDKAPI) Message(ctx context.Context, mailboxID, messageID string) (sendmuxRawMessage, error) {
	detailResponse, err := api.client.MailboxGetMessage(ctx, mailbox.MailboxGetMessageParams{
		MessageID: messageID, MailboxID: mailbox.NewOptString(mailboxID),
	})
	if err != nil {
		return sendmuxRawMessage{}, err
	}
	detail, ok := detailResponse.(*mailbox.MailboxMessageDetailResponse)
	if !ok {
		return sendmuxRawMessage{}, sendmuxUnexpectedResponse("get message", detailResponse)
	}
	contentResponse, err := api.client.MailboxListContent(ctx, mailbox.MailboxListContentParams{
		MessageID: messageID, MailboxID: mailbox.NewOptString(mailboxID),
		Part:               mailbox.NewOptMailboxListContentPart(mailbox.MailboxListContentPartAuto),
		IncludeHTML:        mailbox.NewOptBool(true),
		IncludeHeaders:     mailbox.NewOptMailboxListContentIncludeHeaders(mailbox.MailboxListContentIncludeHeadersSelected),
		IncludeAttachments: mailbox.NewOptMailboxListContentIncludeAttachments(mailbox.MailboxListContentIncludeAttachmentsMetadata),
	})
	if err != nil {
		return sendmuxRawMessage{}, err
	}
	contentEnvelope, ok := contentResponse.(*mailbox.MailboxMessageContentResponse)
	if !ok {
		return sendmuxRawMessage{}, sendmuxUnexpectedResponse("get message content", contentResponse)
	}
	content, ok := contentEnvelope.GetData().Get()
	if !ok {
		return sendmuxRawMessage{}, fmt.Errorf("Sendmux message %s returned no content", messageID)
	}
	return sendmuxRawFromSDK(detail.GetData(), content), nil
}

func (api *sendmuxSDKAPI) Thread(ctx context.Context, mailboxID, threadID string) ([]sendmuxRawMessage, error) {
	var messages []sendmuxRawMessage
	cursor := ""
	for {
		params := mailbox.MailboxListThreadMessagesParams{
			ThreadID: threadID, Limit: mailbox.NewOptInt(sendmuxPageSize),
			MailboxID: mailbox.NewOptString(mailboxID),
		}
		if cursor != "" {
			params.Cursor = mailbox.NewOptString(cursor)
		}
		response, err := api.client.MailboxListThreadMessages(ctx, params)
		if err != nil {
			return nil, err
		}
		page, ok := response.(*mailbox.MailboxMessageSummaryCursorListResponse)
		if !ok {
			return nil, sendmuxUnexpectedResponse("list thread messages", response)
		}
		for _, summary := range page.GetData() {
			message, err := api.Message(ctx, mailboxID, summary.GetID())
			if err != nil {
				return nil, err
			}
			messages = append(messages, message)
		}
		pagination := page.GetPagination()
		next, hasNext := pagination.GetNextCursor().Get()
		if !pagination.GetHasMore() || !hasNext || next == "" {
			break
		}
		if next == cursor {
			return nil, fmt.Errorf("Sendmux thread pagination repeated cursor %q", next)
		}
		cursor = next
	}
	return messages, nil
}

func (api *sendmuxSDKAPI) Send(ctx context.Context, mailboxID string, request sendmuxSendRequest, idempotencyKey string) (string, error) {
	body := mailbox.SendMailboxMessageBody{
		To: mailboxAddresses(request.To), Subject: request.Subject,
	}
	if len(request.CustomHeaders) > 0 {
		body.CustomHeaders = mailbox.NewOptSendMailboxMessageBodyCustomHeaders(mailbox.SendMailboxMessageBodyCustomHeaders(request.CustomHeaders))
	}
	if request.Text != "" {
		body.TextBody = mailbox.NewOptString(request.Text)
	}
	if request.HTML != "" {
		body.HTMLBody = mailbox.NewOptString(request.HTML)
	}
	for _, file := range request.Files {
		body.Attachments = append(body.Attachments, mailbox.SendMailboxMessageBodyAttachmentsItem{
			Filename: file.Filename, ContentType: file.ContentType, Content: mailbox.NewOptString(file.Content),
		})
	}
	response, err := api.client.MailboxSendMessage(ctx, mailbox.NewOptSendMailboxMessageBody(body), mailbox.MailboxSendMessageParams{
		IdempotencyKey: mailbox.IdempotencyKey(idempotencyKey), MailboxID: mailbox.NewOptString(mailboxID),
	})
	if err != nil {
		return "", err
	}
	success, ok := response.(*mailbox.MailboxSendResultResponse)
	if !ok {
		return "", sendmuxUnexpectedResponse("send message", response)
	}
	data := success.GetData()
	return data.GetMessageID(), nil
}

func (api *sendmuxSDKSendingAPI) Send(ctx context.Context, from string, request sendmuxSendRequest, idempotencyKey string) (string, error) {
	if len(request.To) != 1 {
		return "", fmt.Errorf("Sendmux Sending API requires exactly one reply recipient")
	}
	body := sending.EmailSendRequest{
		From:     sending.Address{Email: from},
		To:       sending.EmailSendRequestTo{Email: request.To[0]},
		Subject:  request.Subject,
		HTMLBody: request.HTML,
	}
	if request.Text != "" {
		body.TextBody = sending.NewOptString(request.Text)
	}
	if len(request.CustomHeaders) > 0 {
		body.CustomHeaders = sending.NewOptEmailSendRequestCustomHeaders(sending.EmailSendRequestCustomHeaders(request.CustomHeaders))
	}
	for _, file := range request.Files {
		attachment := sending.Attachment{Filename: file.Filename, Content: file.Content}
		attachment.Type = sending.NewOptString(file.ContentType)
		body.Attachments = append(body.Attachments, attachment)
	}
	response, err := api.client.SendingSendEmail(ctx, &body, sending.SendingSendEmailParams{IdempotencyKey: sending.IdempotencyKey(idempotencyKey)})
	if err != nil {
		return "", err
	}
	success, ok := response.(*sending.SendSuccessResponse)
	if !ok {
		return "", sendmuxUnexpectedResponse("send message", response)
	}
	data := success.GetData()
	if receipt := strings.TrimSpace(data.GetMessageID()); receipt != "" {
		return receipt, nil
	}
	return "", nil
}

func (api *sendmuxSDKAPI) MarkSeen(ctx context.Context, mailboxID, messageID string) error {
	response, err := api.client.MailboxUpdateMessage(ctx, mailbox.NewOptPatchMailboxMessageBody(mailbox.PatchMailboxMessageBody{
		Seen: mailbox.NewOptBool(true),
	}), mailbox.MailboxUpdateMessageParams{MessageID: messageID, MailboxID: mailbox.NewOptString(mailboxID)})
	if err != nil {
		return err
	}
	if _, ok := response.(*mailbox.MailboxMessageDetailResponse); !ok {
		return sendmuxUnexpectedResponse("update message", response)
	}
	return nil
}

func sendmuxRawFromSDK(detail mailbox.MailboxMessage, content mailbox.MailboxMessageContentResponseData) sendmuxRawMessage {
	participants := content.GetParticipants()
	from := ""
	if address, ok := participants.GetFrom().Get(); ok {
		from = strings.ToLower(address.GetEmail())
	}
	headers := content.GetHeaders()
	var inReplyTo string
	var references, messageIDs []string
	if selected, ok := headers.GetSelected().Get(); ok {
		inReplyTo, _ = selected.GetInReplyTo().Get()
		references = append([]string(nil), selected.GetReferences()...)
		messageIDs = append([]string(nil), selected.GetMessageID()...)
	}
	body := content.GetBody()
	textBody, _ := body.GetText().Get()
	htmlBody, _ := body.GetHTML().Get()
	dates := content.GetDates()
	receivedAt := parseSendmuxTime(dates.GetReceivedAt())
	sentAt := parseSendmuxTime(dates.GetSentAt())
	flags := detail.GetFlags()
	raw := sendmuxRawMessage{
		ID: content.GetID(), ThreadID: nilString(content.GetThreadID()), From: from,
		To: mailboxAddressStrings(participants.GetTo()), CC: mailboxAddressStrings(participants.GetCc()),
		BCC: mailboxAddressStrings(participants.GetBcc()), ReplyTo: mailboxAddressStrings(participants.GetReplyTo()),
		Subject: nilString(content.GetSubject()), Text: textBody, HTML: htmlBody,
		ReceivedAt: receivedAt, SentAt: sentAt, Seen: flags.GetSeen(),
		Keywords: append([]string(nil), detail.GetKeywords()...), InReplyTo: inReplyTo,
		References: references, RFCMessageIDs: messageIDs,
	}
	for _, attachment := range content.GetAttachments() {
		downloadURL, _ := attachment.GetDownloadURL().Get()
		size, _ := attachment.GetSizeBytes().Get()
		raw.Attachments = append(raw.Attachments, sendmuxRawAttachment{
			ID: attachment.GetID(), Filename: nilString(attachment.GetFilename()),
			ContentType: attachment.GetContentType(), SizeBytes: int64(size), DownloadURL: downloadURL,
		})
	}
	return raw
}

func mailboxAddresses(addresses []string) []mailbox.MailboxAddress {
	result := make([]mailbox.MailboxAddress, 0, len(addresses))
	for _, address := range addresses {
		name := mailbox.NilString{}
		name.SetToNull()
		result = append(result, mailbox.MailboxAddress{Email: address, Name: name})
	}
	return result
}

func mailboxAddressStrings(addresses []mailbox.MailboxAddress) []string {
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, strings.ToLower(address.GetEmail()))
	}
	return result
}

func nilString(value mailbox.NilString) string {
	result, _ := value.Get()
	return result
}

func parseSendmuxTime(value mailbox.NilString) time.Time {
	raw, ok := value.Get()
	if !ok || raw == "" {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339Nano, raw)
	return parsed
}

type sendmuxAPIErrorResponse interface {
	APIError() *core.APIError
}

func sendmuxUnexpectedResponse(operation string, response any) error {
	if details := sendmuxValidationDetails(response); len(details) > 0 {
		parts := make([]string, 0, len(details))
		for _, detail := range details {
			field := strings.TrimSpace(detail.GetField())
			message := strings.TrimSpace(detail.GetMessage())
			if field == "" {
				parts = append(parts, message)
			} else {
				parts = append(parts, field+": "+message)
			}
		}
		return fmt.Errorf("Sendmux %s validation failed: %s", operation, strings.Join(parts, "; "))
	}
	if apiError, ok := response.(sendmuxAPIErrorResponse); ok {
		return apiError.APIError()
	}
	return fmt.Errorf("Sendmux %s returned unexpected response %T", operation, response)
}

func sendmuxValidationDetails(response any) []mailbox.ApiErrorDetail {
	var apiError *mailbox.ApiError
	switch response := response.(type) {
	case *mailbox.MailboxSendMessageBadRequest:
		apiError = (*mailbox.ApiError)(response)
	case *mailbox.MailboxSendMessageUnprocessableEntity:
		apiError = (*mailbox.ApiError)(response)
	default:
		return nil
	}
	errorBody := apiError.GetError()
	return errorBody.GetErrors()
}
