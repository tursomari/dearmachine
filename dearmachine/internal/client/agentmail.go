package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
	"golang.org/x/net/html"
)

const pageSize = 100

var _ Transport = (*Mailbox)(nil)
var _ PairAuthorizer = (*Mailbox)(nil)

// Mailbox calls AgentMail REST endpoints directly through the Go SDK.
type Mailbox struct {
	client             agentmail.Client
	inboxID            string
	pollMu             sync.Mutex
	recoveryAfter      time.Time
	attachmentMu       sync.RWMutex
	attachmentMessages map[string]string
	authMu             sync.Mutex
	authenticated      map[string]string
	authHTTPClient     *http.Client
	authLookupTXT      func(context.Context, string) ([]string, error)
}

func NewMailbox(client agentmail.Client, inboxID string) (*Mailbox, error) {
	if strings.TrimSpace(inboxID) == "" {
		return nil, fmt.Errorf("inbox ID is required")
	}
	return &Mailbox{
		client:             client,
		inboxID:            inboxID,
		attachmentMessages: make(map[string]string),
	}, nil
}

// NewAgentMailTransport constructs the production AgentMail adapter.
func NewAgentMailTransport(inboxID string) (*Mailbox, error) {
	credential, err := loadAgentMailCredential()
	if err != nil {
		return nil, err
	}
	return NewMailbox(agentmail.NewClient(option.WithAPIKey(credential)), inboxID)
}

// AuthorizePair ensures AgentMail will accept mail from the paired
// correspondent and permit both replies and outbound delivery to that
// correspondent. AgentMail's reply endpoint enforces the send allow list too.
// It is idempotent so an interrupted creation can be retried safely.
func (m *Mailbox) AuthorizePair(ctx context.Context, email string) error {
	email, err := canonicalPairAddress(email)
	if err != nil {
		return fmt.Errorf("authorize AgentMail pair: %w", err)
	}
	for _, direction := range []string{"receive", "reply", "send"} {
		if err := m.ensurePairAllowEntry(ctx, direction, email); err != nil {
			return err
		}
	}
	return nil
}

func (m *Mailbox) ensurePairAllowEntry(ctx context.Context, direction, email string) error {
	getParams := agentmail.InboxListGetParams{
		InboxID:   m.inboxID,
		Direction: agentmail.InboxListGetParamsDirection(direction),
		Type:      agentmail.InboxListGetParamsTypeAllow,
	}
	if _, err := m.client.Inboxes.Lists.Get(ctx, email, getParams); err == nil {
		return nil
	} else if !agentMailStatus(err, http.StatusNotFound) {
		return fmt.Errorf("inspect AgentMail %s allow entry for %s: %w", direction, email, err)
	}
	_, err := m.client.Inboxes.Lists.New(
		ctx,
		agentmail.InboxListNewParamsTypeAllow,
		agentmail.InboxListNewParams{
			InboxID:   m.inboxID,
			Direction: agentmail.InboxListNewParamsDirection(direction),
			Entry:     email,
		},
	)
	if err == nil {
		return nil
	}
	// A concurrent retry may win the create race. Accept conflict only after
	// proving the exact entry now exists.
	if agentMailStatus(err, http.StatusConflict) {
		if _, verifyErr := m.client.Inboxes.Lists.Get(ctx, email, getParams); verifyErr == nil {
			return nil
		}
	}
	return fmt.Errorf("authorize AgentMail %s for %s: %w", direction, email, err)
}

func agentMailStatus(err error, status int) bool {
	var apiErr *agentmail.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

// ProvisionAgentMailInbox creates a provider-owned inbox with a randomized
// address. The caller is responsible for persisting the returned identity.
func ProvisionAgentMailInbox(ctx context.Context) (Inbox, error) {
	credential, err := loadAgentMailCredential()
	if err != nil {
		return Inbox{}, err
	}
	api := agentmail.NewClient(option.WithAPIKey(credential))
	created, err := api.Inboxes.New(ctx, agentmail.InboxNewParams{CreateInbox: agentmail.CreateInboxParam{}})
	if err != nil {
		return Inbox{}, fmt.Errorf("create AgentMail inbox: %w", err)
	}
	return Inbox{Transport: "agentmail", ProviderID: created.InboxID, Address: created.Email}, nil
}

func InspectAgentMailInbox(ctx context.Context, selection string) (Inbox, error) {
	credential, err := loadAgentMailCredential()
	if err != nil {
		return Inbox{}, err
	}
	api := agentmail.NewClient(option.WithAPIKey(credential))
	pageToken := ""
	var matches []agentmail.Inbox
	for {
		params := agentmail.InboxListParams{Limit: agentmail.Int(100)}
		if pageToken != "" {
			params.PageToken = agentmail.String(pageToken)
		}
		page, err := api.Inboxes.List(ctx, params)
		if err != nil {
			return Inbox{}, fmt.Errorf("list AgentMail inboxes: %w", err)
		}
		for _, inbox := range page.Inboxes {
			if strings.EqualFold(strings.TrimSpace(selection), inbox.InboxID) || strings.EqualFold(strings.TrimSpace(selection), inbox.Email) {
				matches = append(matches, inbox)
			}
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}
	if len(matches) == 0 {
		return Inbox{}, fmt.Errorf("AgentMail inbox %q was not found", selection)
	}
	if len(matches) > 1 {
		return Inbox{}, fmt.Errorf("AgentMail inbox %q is ambiguous", selection)
	}
	return Inbox{Transport: "agentmail", ProviderID: matches[0].InboxID, Address: matches[0].Email}, nil
}

func loadAgentMailCredential() (string, error) {
	if credential := strings.TrimSpace(os.Getenv("AGENTMAIL_API_KEY")); credential != "" {
		return credential, nil
	}
	credentialPath := strings.TrimSpace(os.Getenv("AGENTMAIL_API_KEY_FILE"))
	usingDefault := credentialPath == ""
	if usingDefault {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("AGENTMAIL_API_KEY or AGENTMAIL_API_KEY_FILE is required: resolve default credential path: %w", err)
		}
		credentialPath = filepath.Join(home, ".config", "dearmachine", "agentmail-api-key")
	}
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		if usingDefault && errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("AGENTMAIL_API_KEY or AGENTMAIL_API_KEY_FILE is required (optional default: $HOME/.config/dearmachine/agentmail-api-key)")
		}
		return "", fmt.Errorf("read AGENTMAIL_API_KEY_FILE: %w", err)
	}
	credential := strings.TrimRight(string(contents), "\r\n")
	if strings.TrimSpace(credential) == "" {
		return "", fmt.Errorf("AGENTMAIL_API_KEY_FILE is empty")
	}
	if strings.ContainsAny(credential, "\r\n") {
		return "", fmt.Errorf("AGENTMAIL_API_KEY_FILE must contain exactly one line")
	}
	return credential, nil
}

func (m *Mailbox) pollTarget() string {
	return m.inboxID
}

func (m *Mailbox) Poll(ctx context.Context) ([]Message, error) {
	m.pollMu.Lock()
	defer m.pollMu.Unlock()

	unread, err := m.listMessageSummaries(ctx, agentmail.InboxMessageListParams{
		IncludeUnauthenticated: agentmail.Bool(true),
		Ascending:              agentmail.Bool(true),
		Labels:                 []string{"unread"},
		Limit:                  agentmail.Int(pageSize),
	})
	if err != nil {
		return nil, err
	}
	recoveryParams := agentmail.InboxMessageListParams{
		IncludeUnauthenticated: agentmail.Bool(true),
		Ascending:              agentmail.Bool(true),
		Limit:                  agentmail.Int(pageSize),
	}
	if !m.recoveryAfter.IsZero() {
		// Keep a small overlap because providers can assign equal timestamps or
		// make a just-delivered message visible after a later one.
		recoveryParams.After = agentmail.Time(m.recoveryAfter.Add(-time.Minute))
	}
	recovery, err := m.listMessageSummaries(ctx, recoveryParams)
	if err != nil {
		return nil, err
	}

	nextRecoveryAfter := m.recoveryAfter
	byID := make(map[string]bool, len(unread)+len(recovery))
	for _, summary := range unread {
		byID[summary.MessageID] = true
	}
	for _, summary := range recovery {
		if messageTimeFromAgentMailSummary(summary).After(nextRecoveryAfter) {
			nextRecoveryAfter = messageTimeFromAgentMailSummary(summary)
		}
		if containsFold(summary.Labels, "unread") {
			if _, listedUnread := byID[summary.MessageID]; !listedUnread {
				byID[summary.MessageID] = false
			}
		}
	}

	messages := make([]Message, 0, len(byID))
	for messageID, listedUnread := range byID {
		message, err := m.client.Inboxes.Messages.Get(
			ctx,
			messageID,
			agentmail.InboxMessageGetParams{InboxID: m.inboxID},
		)
		if err != nil {
			return nil, fmt.Errorf("get AgentMail message %s: %w", messageID, err)
		}
		if !listedUnread && !containsFold(message.Labels, "unread") {
			continue
		}
		messages = append(messages, m.normalize(*message))
	}
	m.recoveryAfter = nextRecoveryAfter
	return finalizePolledMessages(messages), nil
}

func (m *Mailbox) listMessageSummaries(
	ctx context.Context,
	params agentmail.InboxMessageListParams,
) ([]agentmail.InboxMessageListResponseMessage, error) {
	var summaries []agentmail.InboxMessageListResponseMessage
	pageToken := ""
	for {
		if pageToken != "" {
			params.PageToken = agentmail.String(pageToken)
		}
		page, err := m.client.Inboxes.Messages.List(ctx, m.inboxID, params)
		if err != nil {
			return nil, fmt.Errorf("list AgentMail messages: %w", err)
		}
		summaries = append(summaries, page.Messages...)
		pageToken = page.NextPageToken
		if pageToken == "" {
			return summaries, nil
		}
	}
}

func messageTimeFromAgentMailSummary(message agentmail.InboxMessageListResponseMessage) time.Time {
	if !message.Timestamp.IsZero() {
		return message.Timestamp
	}
	return message.CreatedAt
}

func (m *Mailbox) Thread(ctx context.Context, threadID string) ([]Message, error) {
	thread, err := m.client.Inboxes.Threads.Get(
		ctx,
		threadID,
		agentmail.InboxThreadGetParams{InboxID: m.inboxID},
	)
	if err != nil {
		return nil, fmt.Errorf("get AgentMail thread %s: %w", threadID, err)
	}
	messages := make([]Message, 0, len(thread.Messages))
	for _, message := range thread.Messages {
		messages = append(messages, m.normalize(message))
	}
	return messages, nil
}

func (m *Mailbox) Message(ctx context.Context, messageID string) (Message, error) {
	message, err := m.client.Inboxes.Messages.Get(
		ctx,
		messageID,
		agentmail.InboxMessageGetParams{InboxID: m.inboxID},
	)
	if err != nil {
		return Message{}, fmt.Errorf(
			"get AgentMail message %s: %w",
			messageID,
			err,
		)
	}
	return m.normalize(*message), nil
}

func (m *Mailbox) Reply(
	ctx context.Context,
	messageID string,
	payload ReplyPayload,
	idempotencyKey string,
) (string, error) {
	params := agentmail.InboxMessageReplyParams{
		InboxID:  m.inboxID,
		Text:     agentmail.String(payload.Text),
		ReplyAll: agentmail.Bool(false),
	}
	if len(payload.To) > 0 {
		params.To = agentmail.AddressesUnionParam{OfStringArray: append([]string(nil), payload.To...)}
		params.Cc = agentmail.AddressesUnionParam{OfStringArray: append([]string(nil), payload.CC...)}
		params.Bcc = agentmail.AddressesUnionParam{OfStringArray: append([]string(nil), payload.BCC...)}
	}
	if payload.HTML != "" {
		params.HTML = agentmail.String(payload.HTML)
	}
	if len(payload.Files) > 0 {
		params.Attachments = make([]agentmail.SendAttachmentParam, 0, len(payload.Files))
		for _, file := range payload.Files {
			params.Attachments = append(params.Attachments, agentmail.SendAttachmentParam{
				Content:            agentmail.String(base64.StdEncoding.EncodeToString(file.Contents)),
				ContentType:        agentmail.String(file.ContentType),
				Filename:           agentmail.String(file.Filename),
				ContentDisposition: agentmail.AttachmentContentDispositionAttachment,
			})
		}
	}
	receipt, err := m.client.Inboxes.Messages.Reply(
		ctx,
		messageID,
		params,
		option.WithHeader("Idempotency-Key", idempotencyKey),
	)
	if err != nil {
		return "", fmt.Errorf("reply to AgentMail message %s: %w", messageID, err)
	}
	if receipt.MessageID == "" {
		return "", fmt.Errorf("reply to AgentMail message %s returned no receipt", messageID)
	}
	return receipt.MessageID, nil
}

func (m *Mailbox) ReplyReceipt(
	ctx context.Context,
	message Message,
	recipient string,
) (string, bool, error) {
	messages, err := m.Thread(ctx, message.ThreadID)
	if err != nil {
		return "", false, err
	}
	for _, candidate := range messages {
		if candidate.MessageID == message.MessageID {
			continue
		}
		if candidate.InReplyTo == message.MessageID &&
			containsFold(candidate.Labels, "sent") &&
			containsMessageAddress(candidate.To, replyReceiptRecipient(message, recipient)) {
			return candidate.MessageID, true, nil
		}
	}
	return "", false, nil
}

func (m *Mailbox) FetchAttachment(
	ctx context.Context,
	attachmentID string,
	maxBytes int64,
) ([]byte, error) {
	m.attachmentMu.RLock()
	messageID, ok := m.attachmentMessages[attachmentID]
	m.attachmentMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("fetch AgentMail attachment %s: message ID is unavailable", attachmentID)
	}

	attachment, err := m.client.Inboxes.Messages.GetAttachment(
		ctx,
		attachmentID,
		agentmail.InboxMessageGetAttachmentParams{
			InboxID:   m.inboxID,
			MessageID: messageID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("get AgentMail attachment %s: %w", attachmentID, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.DownloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare AgentMail attachment %s download: %w", attachmentID, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download AgentMail attachment %s: %w", attachmentID, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf(
			"download AgentMail attachment %s: unexpected status %s",
			attachmentID,
			response.Status,
		)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read AgentMail attachment %s: %w", attachmentID, err)
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf(
			"download AgentMail attachment %s: %w",
			attachmentID,
			ErrAttachmentTooLarge,
		)
	}
	return contents, nil
}

func (m *Mailbox) MarkProcessed(ctx context.Context, messageID string) error {
	_, err := m.SetMessageRead(ctx, messageID, true)
	return err
}

func (m *Mailbox) SetMessageRead(ctx context.Context, messageID string, read bool) (bool, error) {
	add, remove := "read", "unread"
	if !read {
		add, remove = remove, add
	}
	_, err := m.client.Inboxes.Messages.Update(
		ctx,
		messageID,
		agentmail.InboxMessageUpdateParams{
			InboxID: m.inboxID,
			UpdateMessage: agentmail.UpdateMessageParam{
				AddLabels: agentmail.UpdateMessageAddLabelsUnionParam{
					OfString: agentmail.String(add),
				},
				RemoveLabels: agentmail.UpdateMessageRemoveLabelsUnionParam{
					OfString: agentmail.String(remove),
				},
			},
		},
	)
	if err != nil {
		return true, fmt.Errorf("mark AgentMail message %s processed: %w", messageID, err)
	}
	return true, nil
}

func htmlToText(raw string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(raw))
	var rendered strings.Builder
	var skipped []string
	var links []string
	quoteDepth := 0
	lineStart := true
	writeBreak := func() {
		rendered.WriteByte('\n')
		lineStart = true
	}
	writeText := func(value string) {
		for len(value) > 0 {
			newline := strings.IndexByte(value, '\n')
			segment := value
			if newline >= 0 {
				segment = value[:newline]
			}
			if lineStart && quoteDepth > 0 && strings.TrimSpace(segment) != "" {
				rendered.WriteString(strings.Repeat(">", quoteDepth))
				rendered.WriteByte(' ')
			}
			rendered.WriteString(segment)
			if newline < 0 {
				if strings.TrimSpace(segment) != "" {
					lineStart = false
				}
				break
			}
			writeBreak()
			value = value[newline+1:]
		}
	}

	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break
		}

		token := tokenizer.Token()
		tag := strings.ToLower(token.Data)
		switch tokenType {
		case html.TextToken:
			if len(skipped) == 0 {
				writeText(token.Data)
			}
		case html.StartTagToken:
			if isSkippedHTMLTag(tag) {
				skipped = append(skipped, tag)
				continue
			}
			if len(skipped) > 0 {
				continue
			}
			if isHTMLLineBreak(tag) {
				writeBreak()
			}
			if tag == "blockquote" {
				quoteDepth++
			}
			if tag == "a" {
				links = append(links, safeHTMLLink(token.Attr))
			}
		case html.SelfClosingTagToken:
			if len(skipped) == 0 && isHTMLLineBreak(tag) {
				writeBreak()
			}
		case html.EndTagToken:
			if len(skipped) > 0 {
				for i := len(skipped) - 1; i >= 0; i-- {
					if skipped[i] == tag {
						skipped = skipped[:i]
						break
					}
				}
				continue
			}
			if tag == "a" && len(links) > 0 {
				href := links[len(links)-1]
				links = links[:len(links)-1]
				if href != "" {
					writeText(" (" + href + ")")
				}
			}
			if tag == "blockquote" && quoteDepth > 0 {
				quoteDepth--
			}
			if isHTMLLineBreak(tag) {
				writeBreak()
			}
		}
	}

	lines := strings.Split(rendered.String(), "\n")
	cleaned := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if len(cleaned) > 0 && !blank {
				cleaned = append(cleaned, "")
				blank = true
			}
			continue
		}
		cleaned = append(cleaned, line)
		blank = false
	}
	for len(cleaned) > 0 && cleaned[len(cleaned)-1] == "" {
		cleaned = cleaned[:len(cleaned)-1]
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func isSkippedHTMLTag(tag string) bool {
	switch tag {
	case "head", "script", "style":
		return true
	default:
		return false
	}
}

func isHTMLLineBreak(tag string) bool {
	switch tag {
	case "br", "p", "div", "li", "blockquote", "pre", "tr", "table", "hr",
		"h1", "h2", "h3", "h4", "h5", "h6":
		return true
	default:
		return false
	}
}

func safeHTMLLink(attributes []html.Attribute) string {
	for _, attribute := range attributes {
		if strings.EqualFold(attribute.Key, "href") {
			href := strings.TrimSpace(attribute.Val)
			lower := strings.ToLower(href)
			if strings.HasPrefix(lower, "http://") ||
				strings.HasPrefix(lower, "https://") ||
				strings.HasPrefix(lower, "mailto:") {
				return href
			}
			return ""
		}
	}
	return ""
}

func (m *Mailbox) normalize(message agentmail.Message) Message {
	body := message.ExtractedText
	if strings.TrimSpace(body) == "" {
		body = message.Text
	}
	if strings.TrimSpace(body) == "" {
		body = htmlToText(message.ExtractedHTML)
	}
	if strings.TrimSpace(body) == "" {
		body = htmlToText(message.HTML)
	}
	if strings.TrimSpace(body) == "" {
		body = message.Preview
	}
	rawBody := message.Text
	if strings.TrimSpace(rawBody) == "" {
		rawBody = htmlToText(message.HTML)
	}
	if strings.TrimSpace(rawBody) == "" {
		rawBody = body
	}
	body, bodyReferences := stripConversationFooters(body)
	conversationReferences := mergeConversationReferences(
		bodyReferences,
		conversationReferencesInBodies(
			message.Text,
			htmlToText(message.HTML),
			message.ExtractedText,
			htmlToText(message.ExtractedHTML),
		),
	)
	attachments := make([]AttachmentRef, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		attachments = append(attachments, AttachmentRef{
			AttachmentID: attachment.AttachmentID,
			Filename:     attachment.Filename,
			ContentType:  attachment.ContentType,
			SizeBytes:    attachment.Size,
		})
	}
	normalized := Message{
		MessageID: message.MessageID,
		ThreadID:  message.ThreadID,
		From:      message.From,
		To:        append([]string(nil), message.To...),
		CC:        append([]string(nil), message.Cc...),
		BCC:       append([]string(nil), message.Bcc...),
		Delivery: normalizeMessageDelivery(
			m.inboxID, agentMailInboxAddress(message.InboxID), message.To, message.Cc, message.Bcc, "",
			messageReadStateFromLabels(message.Labels),
		),
		Timestamp:              message.Timestamp,
		CreatedAt:              message.CreatedAt,
		Subject:                message.Subject,
		Body:                   body,
		RawBody:                rawBody,
		InReplyTo:              message.InReplyTo,
		References:             append([]string(nil), message.References...),
		ConversationReferences: conversationReferences,
		Labels:                 append([]string(nil), message.Labels...),
		Attachments:            attachments,
	}
	if len(attachments) > 0 {
		m.attachmentMu.Lock()
		for _, attachment := range attachments {
			m.attachmentMessages[attachment.AttachmentID] = message.MessageID
		}
		m.attachmentMu.Unlock()
	}
	return normalized
}

func agentMailInboxAddress(inboxID string) string {
	if strings.Contains(inboxID, "@") {
		return inboxID
	}
	return ""
}

func messageReadStateFromLabels(labels []string) MessageReadState {
	switch {
	case containsFold(labels, "unread"):
		return MessageReadStateUnread
	case containsFold(labels, "read"):
		return MessageReadStateRead
	default:
		return MessageReadStateUnknown
	}
}
