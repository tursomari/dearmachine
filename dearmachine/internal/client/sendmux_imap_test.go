package client

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// Script the wire protocol, including servers whose HEADER search index has
// not caught up. No credentials, network or real email are used in this suite.
func sendmuxIMAPFixture(t *testing.T, mode string, raw []byte, id string) (net.Conn, <-chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		server.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprint(server, "* OK [CAPABILITY IMAP4rev1] fixture\r\n")
		scanner := bufio.NewScanner(server)
		folder := ""
		selects := 0
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.SplitN(line, " ", 2)
			if len(parts) != 2 {
				return
			}
			tag, command := parts[0], parts[1]
			switch {
			case strings.HasPrefix(command, "CAPABILITY"):
				fmt.Fprint(server, "* CAPABILITY IMAP4rev1\r\n")
			case strings.HasPrefix(command, "LOGIN "):
				if !strings.Contains(command, "machine@receiver.test") || !strings.Contains(command, "fixture-credential") {
					t.Error("wrong mailbox login")
				}
				if mode == "login-error" {
					fmt.Fprintf(server, "%s NO private-provider-detail\r\n", tag)
					continue
				}
			case strings.HasPrefix(command, "LIST "):
				fmt.Fprint(server, "* LIST () \"/\" \"Drafts\"\r\n* LIST () \"/\" \"INBOX\"\r\n")
			case strings.HasPrefix(command, "EXAMINE "):
				folder = strings.Trim(strings.TrimPrefix(command, "EXAMINE "), "\"")
				count := 1
				if folder == "Drafts" {
					count = 0
				}
				if mode == "duplicate" && folder == "INBOX" {
					count = 2
				}
				if mode == "scan-limit" {
					count = maxSendmuxIMAPMessages + 1
				}
				validity := 1
				selects++
				if mode == "changed-validity" && selects > 2 {
					validity = 2
				}
				fmt.Fprintf(server, "* %d EXISTS\r\n* OK [UIDVALIDITY %d] fixture\r\n", count, validity)
			case strings.HasPrefix(command, "FETCH "), strings.HasPrefix(command, "UID FETCH "):
				if !strings.Contains(command, "BODY.PEEK[") || folder != "INBOX" {
					t.Error("fetch was not read-only or crossed folders")
					return
				}
				body, responseSection := raw, ""
				uid := 42
				size := len(raw)
				isHeader := strings.Contains(command, "HEADER.FIELDS")
				if isHeader {
					body = []byte("Message-ID: \t" + id + "\r\n\r\n")
					responseSection = "HEADER.FIELDS (MESSAGE-ID)"
					if mode == "not-found" {
						body = []byte("Message-ID: <other@sender.test>\r\n\r\n")
					}
					if mode == "oversize" {
						size = maxAuthenticationMessageBytes + 1
					}
				} else {
					if mode == "wrong-uid" {
						uid++
					}
					if mode == "wrong-id" {
						body = bytes.ReplaceAll(raw, []byte(id), []byte("<changed@sender.test>"))
						size = len(body)
					}
					if mode == "wrong-size" {
						size++
					}
				}
				if mode == "oversize-literal" || mode == "cancelled" {
					fmt.Fprintf(server, "* 1 FETCH (UID 42 RFC822.SIZE %d BODY[%s] {%d}\r\n", size, responseSection, maxAuthenticationMessageBytes+1)
					// Never send the huge literal. The client must terminate
					// without trying to allocate it or waiting for its bytes.
					var p [1]byte
					server.Read(p[:])
					return
				}
				fmt.Fprintf(server, "* 1 FETCH (UID %d RFC822.SIZE %d BODY[%s] {%d}\r\n%s)\r\n", uid, size, responseSection, len(body), body)
				if mode == "duplicate" && isHeader {
					fmt.Fprintf(server, "* 2 FETCH (UID 43 RFC822.SIZE %d BODY[%s] {%d}\r\n%s)\r\n", size, responseSection, len(body), body)
				}
			default:
				t.Errorf("unexpected IMAP command: %s", strings.Fields(command)[0])
				return
			}
			fmt.Fprintf(server, "%s OK fixture\r\n", tag)
		}
	}()
	return client, done
}

func TestSendmuxIMAPReadOnlyOriginalBytesAndFailClosedBinding(t *testing.T) {
	for _, mode := range []string{"valid", "login-error", "not-found", "duplicate", "scan-limit", "changed-validity", "oversize", "oversize-literal", "wrong-uid", "wrong-id", "wrong-size", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			message := signedFixtureMessage()
			raw, lookup := signedMailFixture(t, message, "sender.test", nil)
			conn, done := sendmuxIMAPFixture(t, mode, raw, message.MessageID)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			data, err := fetchSendmuxIMAP(ctx, conn, "machine@receiver.test", "fixture-credential", message.MessageID)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("fetch %s: %v", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-provider-detail") {
				t.Fatal("leaked server reply")
			}
			if mode == "valid" {
				if !bytes.Equal(raw, data) {
					t.Fatal("raw evidence changed")
				}
				if err := verifySignedMessage(ctx, data, message, lookup); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("IMAP connection did not close")
			}
		})
	}
}
