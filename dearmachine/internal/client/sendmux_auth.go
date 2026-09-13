package client

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

type sendmuxRawFetcher func(context.Context, string, string) ([]byte, error)

func sendmuxRFCMessageID(source sendmuxRawMessage) string {
	if len(source.RFCMessageIDs) != 1 {
		return ""
	}
	id := strings.TrimSpace(source.RFCMessageIDs[0])
	if len(id) < 3 || !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") || strings.ContainsAny(id, "\r\n\t ") {
		return ""
	}
	return id
}

// REST owns the opaque record ID, thread and MIME mapping; IMAP supplies the
// original signed bytes. Bind both views before accepting any inbound work.
func (transport *SendmuxTransport) AuthenticateMessage(ctx context.Context, message Message) error {
	if transport.authRaw == nil {
		return ErrSenderAttributionUnsupported
	}
	if message.MessageID == "" || message.RFCMessageID == "" || containsFold(message.Labels, "unauthenticated") {
		return ErrMessageUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	inbox, err := transport.mailbox(ctx)
	if err != nil {
		return errors.New("resolve Sendmux authentication inbox failed")
	}
	if message.Delivery.InboxID != inbox.ID || message.Delivery.Recipient != inbox.Email || !visibleRecipient(message, inbox.Email) {
		return ErrMessageUnauthenticated
	}
	fingerprint := messageFingerprint(message)
	transport.authMu.Lock()
	previous := transport.authenticated[message.MessageID]
	transport.authMu.Unlock()
	if previous != "" {
		if previous != fingerprint {
			return ErrMessageUnauthenticated
		}
		return nil
	}
	source, err := transport.rawMessage(ctx, message.MessageID)
	if err != nil {
		return errors.New("get Sendmux authentication record failed")
	}
	if source.ID != message.MessageID || sendmuxRFCMessageID(source) != message.RFCMessageID ||
		messageFingerprint(transport.normalize(source)) != fingerprint {
		return ErrMessageUnauthenticated
	}
	raw, err := transport.authRaw(ctx, inbox.Email, message.RFCMessageID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrMessageUnauthenticated) {
			return ErrMessageUnauthenticated
		}
		// Never include server replies, credentials or message data in errors.
		return errors.New("get Sendmux IMAP authentication evidence failed")
	}
	expected := message
	expected.MessageID = message.RFCMessageID
	lookup := transport.authLookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	if err := verifySignedMessage(ctx, raw, expected, lookup); err != nil {
		return err
	}
	transport.authMu.Lock()
	if transport.authenticated == nil || len(transport.authenticated) >= 2048 {
		transport.authenticated = make(map[string]string)
	}
	transport.authenticated[message.MessageID] = fingerprint
	transport.authMu.Unlock()
	return nil
}
