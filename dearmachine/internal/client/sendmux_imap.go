package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	sendmuxIMAPAddress        = "mail.sendmux.ai:993"
	maxSendmuxIMAPFolders     = 64
	maxSendmuxIMAPMessages    = 10000
	maxSendmuxIMAPHeaderBytes = 64 << 10
)

func newSendmuxIMAPFetcher(credential string) sendmuxRawFetcher {
	return func(ctx context.Context, address, messageID string) ([]byte, error) {
		// The mailbox credential goes only to Sendmux's fixed, verified TLS
		// endpoint, never a URL or hostname obtained from message metadata.
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
		conn, err := dialer.DialContext(ctx, "tcp", sendmuxIMAPAddress)
		if err != nil {
			return nil, errors.New("connect Sendmux IMAP failed")
		}
		return fetchSendmuxIMAP(ctx, conn, address, credential, messageID)
	}
}

// Bound the entire session as well as individual streamed literals. Never
// enable IMAP debug logging: it includes both credentials and private mail.
type sendmuxIMAPConn struct {
	net.Conn
	remaining int64
}

func (conn *sendmuxIMAPConn) Read(p []byte) (int, error) {
	if conn.remaining <= 0 {
		return 0, errors.New("IMAP evidence byte limit exceeded")
	}
	if int64(len(p)) > conn.remaining {
		p = p[:conn.remaining]
	}
	n, err := conn.Conn.Read(p)
	conn.remaining -= int64(n)
	return n, err
}

func fetchSendmuxIMAP(ctx context.Context, conn net.Conn, address, credential, messageID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c := imapclient.New(&sendmuxIMAPConn{Conn: conn, remaining: 2 * maxAuthenticationMessageBytes}, nil)
	defer c.Close()
	if err := c.Login(address, credential).Wait(); err != nil {
		return nil, errors.New("Sendmux IMAP login failed")
	}
	var folders []string
	list := c.List("", "*", nil)
	for folder := list.Next(); folder != nil; folder = list.Next() {
		if len(folders) >= maxSendmuxIMAPFolders {
			conn.Close()
			list.Close()
			return nil, errors.New("Sendmux IMAP folder limit exceeded")
		}
		if !slices.Contains(folder.Attrs, imap.MailboxAttrNoSelect) && !slices.Contains(folders, folder.Mailbox) {
			folders = append(folders, folder.Mailbox)
		}
	}
	if err := list.Close(); err != nil {
		return nil, errors.New("list Sendmux IMAP folders failed")
	}
	type match struct {
		folder   string
		uid      imap.UID
		validity uint32
		size     int64
	}
	var matches []match
	total := uint32(0)
	header := &imap.FetchItemBodySection{Peek: true, Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}}
	for _, folder := range folders {
		status, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return nil, errors.New("examine Sendmux IMAP folder failed")
		}
		if status.UIDValidity == 0 || status.NumMessages > maxSendmuxIMAPMessages-total {
			return nil, errors.New("Sendmux IMAP evidence scan limit exceeded")
		}
		total += status.NumMessages
		// Direct header fetches work even when the provider's HEADER search
		// index hasn't found a just-delivered message. Only the selected header
		// is downloaded for unrelated mail; no bodies and no Seen mutations.
		for first := uint32(1); first <= status.NumMessages; first += sendmuxPageSize {
			last := min(first+sendmuxPageSize-1, status.NumMessages)
			set := imap.SeqSet{{Start: first, Stop: last}}
			items, err := fetchSendmuxSections(c, conn, set, header, maxSendmuxIMAPHeaderBytes, int(last-first+1))
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				parsed, err := mail.ReadMessage(bytes.NewReader(item.body))
				if err != nil || len(parsed.Header["Message-Id"]) != 1 {
					continue
				}
				if strings.TrimSpace(parsed.Header.Get("Message-Id")) == messageID {
					matches = append(matches, match{folder, item.uid, status.UIDValidity, item.size})
					if len(matches) > 1 {
						return nil, ErrMessageUnauthenticated
					}
				}
			}
		}
	}
	if len(matches) != 1 {
		return nil, ErrMessageUnauthenticated
	}
	found := matches[0]
	if found.uid == 0 || found.size <= 0 || found.size > maxAuthenticationMessageBytes {
		return nil, ErrMessageUnauthenticated
	}
	status, err := c.Select(found.folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, errors.New("reopen Sendmux IMAP folder failed")
	}
	if status.UIDValidity != found.validity {
		return nil, ErrMessageUnauthenticated
	}
	items, err := fetchSendmuxSections(c, conn, imap.UIDSetNum(found.uid), &imap.FetchItemBodySection{Peek: true}, maxAuthenticationMessageBytes, 1)
	if err != nil {
		return nil, err
	}
	if len(items) != 1 || items[0].uid != found.uid || int64(len(items[0].body)) != found.size || items[0].size != found.size {
		return nil, ErrMessageUnauthenticated
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(items[0].body))
	if err != nil || len(parsed.Header["Message-Id"]) != 1 || strings.TrimSpace(parsed.Header.Get("Message-Id")) != messageID {
		return nil, ErrMessageUnauthenticated
	}
	return items[0].body, nil
}

type sendmuxIMAPItem struct {
	uid  imap.UID
	size int64
	body []byte
}

// Consume literals as streams, checking the advertised size before reading.
// On failure close the socket before draining the command to avoid hanging on
// a malicious literal or leaving the decoder goroutine blocked on a channel.
func fetchSendmuxSections(c *imapclient.Client, conn net.Conn, set imap.NumSet, section *imap.FetchItemBodySection, limit int64, count int) (items []sendmuxIMAPItem, err error) {
	cmd := c.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}})
	defer func() {
		if err != nil {
			conn.Close()
		}
		if closeErr := cmd.Close(); err == nil && closeErr != nil {
			err = errors.New("fetch Sendmux IMAP evidence failed")
		}
	}()
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		if len(items) >= count {
			return nil, ErrMessageUnauthenticated
		}
		var item sendmuxIMAPItem
		for data := msg.Next(); data != nil; data = msg.Next() {
			switch data := data.(type) {
			case imapclient.FetchItemDataUID:
				item.uid = data.UID
			case imapclient.FetchItemDataRFC822Size:
				item.size = data.Size
			case imapclient.FetchItemDataBodySection:
				if item.body != nil || !data.MatchCommand(section) || data.Literal == nil || data.Literal.Size() > limit {
					return nil, ErrMessageUnauthenticated
				}
				item.body, err = io.ReadAll(io.LimitReader(data.Literal, limit+1))
				if err != nil || int64(len(item.body)) > limit {
					return nil, ErrMessageUnauthenticated
				}
			default:
				return nil, ErrMessageUnauthenticated
			}
		}
		if item.uid == 0 || len(item.body) == 0 {
			return nil, ErrMessageUnauthenticated
		}
		items = append(items, item)
	}
	return items, nil
}
